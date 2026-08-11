package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/svesh87/mcp-telegram/internal/access"
	"github.com/svesh87/mcp-telegram/internal/telegram"
)

// Chat identifiers made up for these tests. Negative for a group, positive for a person,
// as Telegram numbers them.
const (
	readableChat = -1001111111111
	writableChat = -1002222222222
	forbidden    = -1003333333333
	botChat      = 424242
)

type fakeUser struct {
	chats    []telegram.Chat
	messages []telegram.Message
	saved    telegram.SavedFile
	sent     []telegram.Sent
	err      error

	historyOpts telegram.HistoryOptions
	searchText  string
	searchLimit int
	sentText    string
	sentPath    string
}

func (f *fakeUser) Chats(context.Context) ([]telegram.Chat, error) {
	return f.chats, f.err
}

func (f *fakeUser) ChatInfo(_ context.Context, chatID int64) (telegram.Chat, error) {
	if f.err != nil {
		return telegram.Chat{}, f.err
	}
	return telegram.Chat{ID: chatID, Kind: telegram.ChatKindGroup, Title: "accounting"}, nil
}

func (f *fakeUser) History(_ context.Context, _ int64, opts telegram.HistoryOptions) ([]telegram.Message, error) {
	f.historyOpts = opts
	return f.messages, f.err
}

func (f *fakeUser) Search(_ context.Context, _ int64, text string, limit int) ([]telegram.Message, error) {
	f.searchText, f.searchLimit = text, limit
	return f.messages, f.err
}

func (f *fakeUser) Download(_ context.Context, _ int64, _ int, _ string) (telegram.SavedFile, error) {
	return f.saved, f.err
}

func (f *fakeUser) SendMessage(_ context.Context, chatID int64, text string, _ int) (telegram.Sent, error) {
	f.sentText = text
	sent := telegram.Sent{ChatID: chatID, MessageID: 7}
	f.sent = append(f.sent, sent)
	return sent, f.err
}

func (f *fakeUser) SendFile(_ context.Context, chatID int64, path, _ string) (telegram.Sent, error) {
	f.sentPath = path
	sent := telegram.Sent{ChatID: chatID, MessageID: 8}
	f.sent = append(f.sent, sent)
	return sent, f.err
}

type fakeBot struct {
	updates []telegram.Message
	sent    []telegram.Sent
	err     error
}

func (f *fakeBot) Me(context.Context) (telegram.Chat, error) {
	return telegram.Chat{ID: 555, Kind: telegram.ChatKindUser, Title: "helper bot"}, f.err
}

func (f *fakeBot) ChatInfo(_ context.Context, chatID int64) (telegram.Chat, error) {
	return telegram.Chat{ID: chatID, Kind: telegram.ChatKindUser}, f.err
}

func (f *fakeBot) Updates(context.Context, int, int) ([]telegram.Message, error) {
	return f.updates, f.err
}

func (f *fakeBot) Download(context.Context, string, string) (telegram.SavedFile, error) {
	return telegram.SavedFile{Path: "/downloads/file.pdf"}, f.err
}

func (f *fakeBot) SendMessage(_ context.Context, chatID int64, _ string, _ int) (telegram.Sent, error) {
	sent := telegram.Sent{ChatID: chatID, MessageID: 9}
	f.sent = append(f.sent, sent)
	return sent, f.err
}

func (f *fakeBot) SendFile(_ context.Context, chatID int64, _, _ string) (telegram.Sent, error) {
	sent := telegram.Sent{ChatID: chatID, MessageID: 10}
	f.sent = append(f.sent, sent)
	return sent, f.err
}

func testLists() access.Lists {
	return access.Lists{
		UserRead:  []int64{readableChat},
		UserWrite: []int64{writableChat},
		BotRead:   []int64{botChat},
		BotWrite:  []int64{botChat},
	}
}

func testOptions(lists access.Lists) Options {
	return Options{
		Checker:    access.New(lists),
		Identities: []access.Identity{access.User, access.Bot},
		UserRead:   &fakeUser{},
		UserWrite:  &fakeUser{},
		BotRead:    &fakeBot{},
		BotWrite:   &fakeBot{},
	}
}

func request(arguments map[string]any) mcp.CallToolRequest {
	return mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: arguments}}
}

func payloadOf(t *testing.T, result *mcp.CallToolResult, target any) {
	t.Helper()

	if result.IsError {
		t.Fatalf("tool answered with an error: %s", textOf(t, result))
	}

	if err := json.Unmarshal([]byte(textOf(t, result)), target); err != nil {
		t.Fatalf("the answer is not JSON: %v (%s)", err, textOf(t, result))
	}
}

func textOf(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()

	if len(result.Content) == 0 {
		t.Fatal("the answer carries no content")
	}

	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("the answer carries %T, not text", result.Content[0])
	}

	return text.Text
}

func registeredTools(t *testing.T, opts Options) []string {
	t.Helper()

	srv := server.NewMCPServer("mcp-telegram", "test", server.WithToolCapabilities(true))
	if err := Register(srv, opts); err != nil {
		t.Fatalf("Register: %v", err)
	}

	names := make([]string, 0, len(srv.ListTools()))
	for name := range srv.ListTools() {
		names = append(names, name)
	}
	sort.Strings(names)

	return names
}

// Writing is the whole reason this server exists in the shape it does. A server started
// without --allow-write must not even advertise a tool that writes: an agent calls what
// it can see.
func TestWritingToolsAreAbsentUnlessEnabled(t *testing.T) {
	names := registeredTools(t, testOptions(testLists()))

	for _, name := range names {
		if strings.Contains(name, "send") {
			t.Errorf("%s is registered on a read-only server", name)
		}
	}

	for _, expected := range []string{
		"telegram_access_lists",
		"telegram_user_chats",
		"telegram_user_chat_info",
		"telegram_user_history",
		"telegram_user_search",
		"telegram_bot_info",
		"telegram_bot_chat_info",
		"telegram_bot_updates",
	} {
		if !contains(names, expected) {
			t.Errorf("%s is missing from a read-only server (registered: %v)", expected, names)
		}
	}
}

func TestWritingToolsAppearWithTheFlag(t *testing.T) {
	opts := testOptions(testLists())
	opts.AllowWrite = true

	names := registeredTools(t, opts)

	for _, expected := range []string{
		"telegram_user_send_message",
		"telegram_user_send_file",
		"telegram_bot_send_message",
		"telegram_bot_send_file",
	} {
		if !contains(names, expected) {
			t.Errorf("%s is missing from a server started with writing enabled", expected)
		}
	}
}

// An identity with an empty write list gets no writing tools even with the flag on:
// there is nowhere for them to write, and a tool that always refuses is worse than no
// tool.
func TestWritingToolsFollowTheLists(t *testing.T) {
	lists := testLists()
	lists.UserWrite = nil

	opts := testOptions(lists)
	opts.AllowWrite = true

	names := registeredTools(t, opts)

	if contains(names, "telegram_user_send_message") {
		t.Error("the account got a writing tool with an empty write list")
	}
	if !contains(names, "telegram_bot_send_message") {
		t.Error("the bot lost its writing tool although its write list is not empty")
	}
}

// An identity the server does not run must not be advertised at all.
func TestToolsOfAnIdentityThatIsNotRunning(t *testing.T) {
	opts := testOptions(testLists())
	opts.Identities = []access.Identity{access.Bot}

	names := registeredTools(t, opts)

	for _, name := range names {
		if strings.HasPrefix(name, "telegram_user_") {
			t.Errorf("%s is registered although the user identity is off", name)
		}
	}
}

// Downloads write to disk, so the directory is configuration and not something a caller
// picks. Without it the tools are not offered.
func TestDownloadToolsNeedADirectory(t *testing.T) {
	names := registeredTools(t, testOptions(testLists()))
	if contains(names, "telegram_user_download") || contains(names, "telegram_bot_download") {
		t.Error("a download tool is offered although no directory was configured")
	}

	opts := testOptions(testLists())
	opts.DownloadDir = t.TempDir()

	names = registeredTools(t, opts)
	if !contains(names, "telegram_user_download") || !contains(names, "telegram_bot_download") {
		t.Errorf("a download tool is missing although a directory was configured: %v", names)
	}
}

func TestRegisterRefusesAnIdentityWithoutAClient(t *testing.T) {
	opts := testOptions(testLists())
	opts.UserRead = nil

	srv := server.NewMCPServer("mcp-telegram", "test")
	if err := Register(srv, opts); err == nil {
		t.Error("Register accepted the user identity with no client behind it")
	}

	if err := Register(srv, Options{}); err == nil {
		t.Error("Register accepted a configuration with no access checker")
	}
}

// The access lists are the answer to "what may I touch", and answering it without
// calling Telegram is what keeps a caller from probing chat identifiers.
func TestAccessListsAnswerFromConfiguration(t *testing.T) {
	opts := testOptions(testLists())
	opts.DownloadDir = "/downloads"
	r := &registry{opts: opts}

	result, err := r.accessLists(context.Background(), request(nil))
	if err != nil {
		t.Fatalf("accessLists: %v", err)
	}

	var payload struct {
		Identities   []string `json:"identities"`
		WriteEnabled bool     `json:"write_enabled"`
		DownloadDir  string   `json:"download_dir"`
		Lists        map[string]struct {
			Read []struct {
				ID   int64  `json:"id"`
				Kind string `json:"kind"`
			} `json:"read"`
			Write []struct {
				ID int64 `json:"id"`
			} `json:"write"`
		} `json:"lists"`
	}
	payloadOf(t, result, &payload)

	if payload.WriteEnabled {
		t.Error("write_enabled is true on a read-only server")
	}
	if payload.DownloadDir != "/downloads" {
		t.Errorf("download_dir is %q", payload.DownloadDir)
	}

	user := payload.Lists["user"]
	// The write list is readable too, so the read list holds both chats.
	if len(user.Read) != 2 {
		t.Errorf("the account reads %d chats, want 2", len(user.Read))
	}
	if len(user.Write) != 1 || user.Write[0].ID != writableChat {
		t.Errorf("the account write list is %v", user.Write)
	}
	// From an identifier alone a supergroup cannot be told from a broadcast channel, so
	// both are reported as a channel here.
	for _, chat := range user.Read {
		if chat.Kind != telegram.ChatKindChannel {
			t.Errorf("chat %d is reported as %q", chat.ID, chat.Kind)
		}
	}
}

func TestReadingRefusesAChatOutsideTheLists(t *testing.T) {
	r := &registry{opts: testOptions(testLists())}

	result, err := r.userHistory(context.Background(), request(map[string]any{
		"chat_id": "-1003333333333",
	}))
	if err != nil {
		t.Fatalf("userHistory: %v", err)
	}

	if !result.IsError {
		t.Fatal("a chat in no access list was read")
	}
	if !strings.Contains(textOf(t, result), "not in any user access list") {
		t.Errorf("the refusal does not say why: %s", textOf(t, result))
	}
}

// A chat that may be read but not written to is refused differently, because the fix is
// different: one is a wrong identifier, the other is a list to extend.
func TestWritingRefusesAReadOnlyChat(t *testing.T) {
	opts := testOptions(testLists())
	opts.AllowWrite = true
	r := &registry{opts: opts}

	result, err := r.userSendMessage(context.Background(), request(map[string]any{
		"chat_id": "-1001111111111",
		"text":    "hello",
	}))
	if err != nil {
		t.Fatalf("userSendMessage: %v", err)
	}

	if !result.IsError {
		t.Fatal("a read-only chat was written to")
	}
	if !strings.Contains(textOf(t, result), "read-only") {
		t.Errorf("the refusal does not say the chat is read-only: %s", textOf(t, result))
	}

	sender := opts.UserWrite.(*fakeUser)
	if len(sender.sent) != 0 {
		t.Errorf("the refusal still reached Telegram: %v", sender.sent)
	}
}

func TestWritingAnAllowedChat(t *testing.T) {
	opts := testOptions(testLists())
	opts.AllowWrite = true
	r := &registry{opts: opts}

	result, err := r.userSendMessage(context.Background(), request(map[string]any{
		"chat_id": "-1002222222222",
		"text":    "the invoice is attached",
	}))
	if err != nil {
		t.Fatalf("userSendMessage: %v", err)
	}

	var sent telegram.Sent
	payloadOf(t, result, &sent)

	if sent.ChatID != writableChat || sent.MessageID != 7 {
		t.Errorf("sent is %+v", sent)
	}
	if got := opts.UserWrite.(*fakeUser).sentText; got != "the invoice is attached" {
		t.Errorf("the text that went out is %q", got)
	}
}

func TestHistoryPassesItsBoundsThrough(t *testing.T) {
	opts := testOptions(testLists())
	r := &registry{opts: opts}

	_, err := r.userHistory(context.Background(), request(map[string]any{
		"chat_id":   "-1001111111111",
		"limit":     float64(25),
		"since":     "2026-01-31",
		"min_id":    float64(100),
		"offset_id": float64(900),
	}))
	if err != nil {
		t.Fatalf("userHistory: %v", err)
	}

	got := opts.UserRead.(*fakeUser).historyOpts
	if got.Limit != 25 || got.MinID != 100 || got.OffsetID != 900 {
		t.Errorf("bounds arrived as %+v", got)
	}
	if want := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC); !got.Since.Equal(want) {
		t.Errorf("since arrived as %s, want %s", got.Since, want)
	}
}

func TestHistoryRejectsANonsenseDate(t *testing.T) {
	r := &registry{opts: testOptions(testLists())}

	result, err := r.userHistory(context.Background(), request(map[string]any{
		"chat_id": "-1001111111111",
		"since":   "last tuesday",
	}))
	if err != nil {
		t.Fatalf("userHistory: %v", err)
	}

	if !result.IsError {
		t.Fatal("an unparsable date was accepted")
	}
}

// The chat list is filtered down to the access lists: a caller must not learn about
// chats it may not read, and an identifier in a list that the account cannot see is
// reported so that a typo in the list is visible.
func TestChatsAreFilteredAndMissingOnesReported(t *testing.T) {
	opts := testOptions(testLists())
	opts.UserRead = &fakeUser{chats: []telegram.Chat{
		{ID: readableChat, Kind: telegram.ChatKindGroup, Title: "accounting"},
		{ID: forbidden, Kind: telegram.ChatKindGroup, Title: "somebody else's chat"},
	}}
	r := &registry{opts: opts}

	result, err := r.userChats(context.Background(), request(nil))
	if err != nil {
		t.Fatalf("userChats: %v", err)
	}

	var payload struct {
		Chats []struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
			Write bool   `json:"write"`
		} `json:"chats"`
		NotVisible []int64 `json:"not_visible"`
	}
	payloadOf(t, result, &payload)

	if len(payload.Chats) != 1 || payload.Chats[0].ID != readableChat {
		t.Fatalf("the answer holds %+v", payload.Chats)
	}
	if payload.Chats[0].Write {
		t.Error("a read-only chat is reported as writable")
	}
	if len(payload.NotVisible) != 1 || payload.NotVisible[0] != writableChat {
		t.Errorf("not_visible is %v, want the chat that is in a list but not in the dialogs", payload.NotVisible)
	}
}

// Anyone can write to a bot, so its update queue is the one place where messages from
// chats in no list arrive by themselves.
func TestBotUpdatesDropWhatIsNotInTheList(t *testing.T) {
	opts := testOptions(testLists())
	opts.BotRead = &fakeBot{updates: []telegram.Message{
		{ID: 1, ChatID: botChat, Text: "from the owner", UpdateID: 10},
		{ID: 2, ChatID: forbidden, Text: "from a stranger", UpdateID: 11},
	}}
	r := &registry{opts: opts}

	result, err := r.botUpdates(context.Background(), request(nil))
	if err != nil {
		t.Fatalf("botUpdates: %v", err)
	}

	var payload struct {
		Count      int `json:"count"`
		Skipped    int `json:"skipped_not_in_access_list"`
		NextOffset int `json:"next_offset"`
		Messages   []struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
		} `json:"messages"`
	}
	payloadOf(t, result, &payload)

	if payload.Count != 1 || len(payload.Messages) != 1 {
		t.Fatalf("the answer holds %d messages", len(payload.Messages))
	}
	if payload.Messages[0].ChatID != botChat {
		t.Errorf("the answer holds chat %d", payload.Messages[0].ChatID)
	}
	if payload.Skipped != 1 {
		t.Errorf("skipped is %d, want the one message from a chat in no list", payload.Skipped)
	}
	// The offset acknowledges the filtered message too, or it comes back for ever.
	if payload.NextOffset != 12 {
		t.Errorf("next_offset is %d, want 12", payload.NextOffset)
	}
}

func TestChatIDArgument(t *testing.T) {
	cases := []struct {
		name    string
		value   any
		want    int64
		wantErr bool
	}{
		{"group", "-1001111111111", -1001111111111, false},
		{"person", "424242", 424242, false},
		{"spaces around it", "  -1001111111111  ", -1001111111111, false},
		{"a name", "@accounting", 0, true},
		{"zero", "0", 0, true},
		{"missing", nil, 0, true},
		{"a number instead of a string", float64(-100), 0, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			arguments := map[string]any{}
			if c.value != nil {
				arguments["chat_id"] = c.value
			}

			got, err := chatIDArg(request(arguments))
			if c.wantErr {
				if err == nil {
					t.Fatalf("chat_id %v was accepted as %d", c.value, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("chat_id %v: %v", c.value, err)
			}
			if got != c.want {
				t.Errorf("chat_id %v parsed as %d, want %d", c.value, got, c.want)
			}
		})
	}
}

func TestDownloadWithoutADirectory(t *testing.T) {
	r := &registry{opts: testOptions(testLists())}

	result, err := r.userDownload(context.Background(), request(map[string]any{
		"chat_id":    "-1001111111111",
		"message_id": float64(5),
	}))
	if err != nil {
		t.Fatalf("userDownload: %v", err)
	}

	if !result.IsError {
		t.Fatal("a download was attempted with no directory configured")
	}
	if !strings.Contains(textOf(t, result), "--download-dir") {
		t.Errorf("the refusal does not say what is missing: %s", textOf(t, result))
	}
}

func TestFailuresFromTelegramComeBackAsToolErrors(t *testing.T) {
	opts := testOptions(testLists())
	opts.UserRead = &fakeUser{err: errors.New("chat 1 is not among this account's dialogs")}
	r := &registry{opts: opts}

	result, err := r.userChatInfo(context.Background(), request(map[string]any{
		"chat_id": "-1001111111111",
	}))
	if err != nil {
		t.Fatalf("userChatInfo returned a protocol error instead of a tool error: %v", err)
	}
	if !result.IsError {
		t.Error("a failure from Telegram was reported as success")
	}
}

func TestSearchNeedsAQuery(t *testing.T) {
	r := &registry{opts: testOptions(testLists())}

	result, err := r.userSearch(context.Background(), request(map[string]any{
		"chat_id": "-1001111111111",
	}))
	if err != nil {
		t.Fatalf("userSearch: %v", err)
	}
	if !result.IsError {
		t.Error("a search with no query was accepted")
	}
}

// errNoConnection stands in for anything Telegram or the network can do to a call.
var errNoConnection = errors.New("no connection to Telegram")

func contains(names []string, name string) bool {
	for _, item := range names {
		if item == name {
			return true
		}
	}
	return false
}
