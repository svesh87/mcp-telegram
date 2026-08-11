package tools

import (
	"context"
	"testing"

	"github.com/svesh87/mcp-telegram/internal/access"
	"github.com/svesh87/mcp-telegram/internal/telegram"
)

func TestBotInfo(t *testing.T) {
	r := &registry{opts: testOptions(testLists())}

	result, err := r.botInfo(context.Background(), request(nil))
	if err != nil {
		t.Fatalf("botInfo: %v", err)
	}

	var chat telegram.Chat
	payloadOf(t, result, &chat)

	if chat.Title != "helper bot" {
		t.Errorf("the bot is described as %+v", chat)
	}
}

func TestBotChatInfoFollowsTheList(t *testing.T) {
	r := &registry{opts: testOptions(testLists())}

	result, err := r.botChatInfo(context.Background(), request(map[string]any{"chat_id": "424242"}))
	if err != nil {
		t.Fatalf("botChatInfo: %v", err)
	}

	var chat telegram.Chat
	payloadOf(t, result, &chat)
	if chat.ID != botChat {
		t.Errorf("the chat is %+v", chat)
	}

	refused, err := r.botChatInfo(context.Background(), request(map[string]any{"chat_id": "-1003333333333"}))
	if err != nil {
		t.Fatalf("botChatInfo: %v", err)
	}
	if !refused.IsError {
		t.Error("the bot answered about a chat in no access list")
	}

	broken, err := r.botChatInfo(context.Background(), request(nil))
	if err != nil {
		t.Fatalf("botChatInfo: %v", err)
	}
	if !broken.IsError {
		t.Error("a call with no chat identifier was accepted")
	}
}

func TestBotWriting(t *testing.T) {
	opts := testOptions(testLists())
	opts.AllowWrite = true
	r := &registry{opts: opts}

	result, err := r.botSendMessage(context.Background(), request(map[string]any{
		"chat_id": "424242",
		"text":    "the documents are ready",
	}))
	if err != nil {
		t.Fatalf("botSendMessage: %v", err)
	}

	var sent telegram.Sent
	payloadOf(t, result, &sent)
	if sent.MessageID != 9 {
		t.Errorf("sent is %+v", sent)
	}

	fileResult, err := r.botSendFile(context.Background(), request(map[string]any{
		"chat_id": "424242",
		"path":    "/tmp/invoice.pdf",
	}))
	if err != nil {
		t.Fatalf("botSendFile: %v", err)
	}
	payloadOf(t, fileResult, &sent)
	if sent.MessageID != 10 {
		t.Errorf("sent is %+v", sent)
	}

	// A chat in no write list is refused before anything leaves the machine.
	refused, err := r.botSendMessage(context.Background(), request(map[string]any{
		"chat_id": "-1003333333333",
		"text":    "hello",
	}))
	if err != nil {
		t.Fatalf("botSendMessage: %v", err)
	}
	if !refused.IsError {
		t.Fatal("the bot wrote to a chat in no write list")
	}

	if sender := opts.BotWrite.(*fakeBot); len(sender.sent) != 2 {
		t.Errorf("%d messages went out, want the refused one to have stopped here", len(sender.sent))
	}
}

func TestBotWritingNeedsItsArguments(t *testing.T) {
	opts := testOptions(testLists())
	opts.AllowWrite = true
	r := &registry{opts: opts}

	noText, err := r.botSendMessage(context.Background(), request(map[string]any{"chat_id": "424242"}))
	if err != nil {
		t.Fatalf("botSendMessage: %v", err)
	}
	if !noText.IsError {
		t.Error("a message with no text was accepted")
	}

	noPath, err := r.botSendFile(context.Background(), request(map[string]any{"chat_id": "424242"}))
	if err != nil {
		t.Fatalf("botSendFile: %v", err)
	}
	if !noPath.IsError {
		t.Error("a file with no path was accepted")
	}

	noChat, err := r.botSendFile(context.Background(), request(map[string]any{"path": "/tmp/a.pdf"}))
	if err != nil {
		t.Fatalf("botSendFile: %v", err)
	}
	if !noChat.IsError {
		t.Error("a file with no chat was accepted")
	}
}

func TestBotDownload(t *testing.T) {
	opts := testOptions(testLists())
	opts.DownloadDir = t.TempDir()
	r := &registry{opts: opts}

	result, err := r.botDownload(context.Background(), request(map[string]any{"file_id": "abc"}))
	if err != nil {
		t.Fatalf("botDownload: %v", err)
	}

	var saved telegram.SavedFile
	payloadOf(t, result, &saved)
	if saved.Path == "" {
		t.Error("the answer does not say where the file went")
	}

	noID, err := r.botDownload(context.Background(), request(nil))
	if err != nil {
		t.Fatalf("botDownload: %v", err)
	}
	if !noID.IsError {
		t.Error("a download with no file identifier was accepted")
	}

	// And without a configured directory the tool refuses even with a valid identifier.
	bare := &registry{opts: testOptions(testLists())}
	refused, err := bare.botDownload(context.Background(), request(map[string]any{"file_id": "abc"}))
	if err != nil {
		t.Fatalf("botDownload: %v", err)
	}
	if !refused.IsError {
		t.Error("a download was attempted with no directory configured")
	}
}

// The bot queue can be empty, and an empty answer must not look like a filtered one.
func TestBotUpdatesWhenTheQueueIsEmpty(t *testing.T) {
	opts := testOptions(testLists())
	opts.BotRead = &fakeBot{}
	r := &registry{opts: opts}

	result, err := r.botUpdates(context.Background(), request(nil))
	if err != nil {
		t.Fatalf("botUpdates: %v", err)
	}

	var payload struct {
		Count      int `json:"count"`
		Skipped    int `json:"skipped_not_in_access_list"`
		NextOffset int `json:"next_offset"`
	}
	payloadOf(t, result, &payload)

	if payload.Count != 0 || payload.Skipped != 0 || payload.NextOffset != 0 {
		t.Errorf("an empty queue answered %+v", payload)
	}
}

func TestUserDownloadAndSendFile(t *testing.T) {
	opts := testOptions(testLists())
	opts.AllowWrite = true
	opts.DownloadDir = t.TempDir()
	opts.UserRead = &fakeUser{saved: telegram.SavedFile{Path: "/downloads/invoice.pdf", Size: 10}}
	r := &registry{opts: opts}

	result, err := r.userDownload(context.Background(), request(map[string]any{
		"chat_id":    "-1001111111111",
		"message_id": float64(512),
	}))
	if err != nil {
		t.Fatalf("userDownload: %v", err)
	}

	var saved telegram.SavedFile
	payloadOf(t, result, &saved)
	if saved.Path != "/downloads/invoice.pdf" {
		t.Errorf("the answer says %+v", saved)
	}

	noMessage, err := r.userDownload(context.Background(), request(map[string]any{
		"chat_id": "-1001111111111",
	}))
	if err != nil {
		t.Fatalf("userDownload: %v", err)
	}
	if !noMessage.IsError {
		t.Error("a download with no message identifier was accepted")
	}

	sentResult, err := r.userSendFile(context.Background(), request(map[string]any{
		"chat_id": "-1002222222222",
		"path":    "/tmp/invoice.pdf",
		"caption": "for January",
	}))
	if err != nil {
		t.Fatalf("userSendFile: %v", err)
	}

	var sent telegram.Sent
	payloadOf(t, sentResult, &sent)
	if sent.MessageID != 8 {
		t.Errorf("sent is %+v", sent)
	}
	if got := opts.UserWrite.(*fakeUser).sentPath; got != "/tmp/invoice.pdf" {
		t.Errorf("the path that went out is %q", got)
	}

	refused, err := r.userSendFile(context.Background(), request(map[string]any{
		"chat_id": "-1001111111111",
		"path":    "/tmp/invoice.pdf",
	}))
	if err != nil {
		t.Fatalf("userSendFile: %v", err)
	}
	if !refused.IsError {
		t.Error("a file went to a read-only chat")
	}

	noPath, err := r.userSendFile(context.Background(), request(map[string]any{
		"chat_id": "-1002222222222",
	}))
	if err != nil {
		t.Fatalf("userSendFile: %v", err)
	}
	if !noPath.IsError {
		t.Error("a file with no path was accepted")
	}
}

func TestUserSendMessageNeedsText(t *testing.T) {
	opts := testOptions(testLists())
	opts.AllowWrite = true
	r := &registry{opts: opts}

	result, err := r.userSendMessage(context.Background(), request(map[string]any{
		"chat_id": "-1002222222222",
	}))
	if err != nil {
		t.Fatalf("userSendMessage: %v", err)
	}
	if !result.IsError {
		t.Error("a message with no text was accepted")
	}

	noChat, err := r.userSendMessage(context.Background(), request(map[string]any{"text": "hello"}))
	if err != nil {
		t.Fatalf("userSendMessage: %v", err)
	}
	if !noChat.IsError {
		t.Error("a message with no chat was accepted")
	}
}

func TestSearchAndChatsPassFailuresOn(t *testing.T) {
	opts := testOptions(testLists())
	failing := &fakeUser{err: errNoConnection}
	opts.UserRead = failing
	r := &registry{opts: opts}

	for name, call := range map[string]func() (bool, error){
		"chats": func() (bool, error) {
			result, err := r.userChats(context.Background(), request(nil))
			return result.IsError, err
		},
		"search": func() (bool, error) {
			result, err := r.userSearch(context.Background(), request(map[string]any{
				"chat_id": "-1001111111111", "query": "invoice",
			}))
			return result.IsError, err
		},
		"history": func() (bool, error) {
			result, err := r.userHistory(context.Background(), request(map[string]any{
				"chat_id": "-1001111111111",
			}))
			return result.IsError, err
		},
		"download": func() (bool, error) {
			withDir := &registry{opts: opts}
			withDir.opts.DownloadDir = t.TempDir()
			result, err := withDir.userDownload(context.Background(), request(map[string]any{
				"chat_id": "-1001111111111", "message_id": float64(1),
			}))
			return result.IsError, err
		},
		"bot updates": func() (bool, error) {
			bot := &registry{opts: testOptions(testLists())}
			bot.opts.BotRead = &fakeBot{err: errNoConnection}
			result, err := bot.botUpdates(context.Background(), request(nil))
			return result.IsError, err
		},
	} {
		isError, err := call()
		if err != nil {
			t.Errorf("%s returned a protocol error: %v", name, err)
		}
		if !isError {
			t.Errorf("%s reported a broken connection as success", name)
		}
	}
}

// A search does reach Telegram, so the query and the limit have to arrive unchanged.
func TestSearchArgumentsArrive(t *testing.T) {
	opts := testOptions(testLists())
	r := &registry{opts: opts}

	if _, err := r.userSearch(context.Background(), request(map[string]any{
		"chat_id": "-1001111111111",
		"query":   "invoice",
		"limit":   float64(5),
	})); err != nil {
		t.Fatalf("userSearch: %v", err)
	}

	reader := opts.UserRead.(*fakeUser)
	if reader.searchText != "invoice" || reader.searchLimit != 5 {
		t.Errorf("the search arrived as %q with limit %d", reader.searchText, reader.searchLimit)
	}
}

func TestAccessListsOfABotOnlyServer(t *testing.T) {
	opts := testOptions(testLists())
	opts.Identities = []access.Identity{access.Bot}
	r := &registry{opts: opts}

	result, err := r.accessLists(context.Background(), request(nil))
	if err != nil {
		t.Fatalf("accessLists: %v", err)
	}

	var payload struct {
		Identities  []string       `json:"identities"`
		Lists       map[string]any `json:"lists"`
		DownloadDir string         `json:"download_dir"`
	}
	payloadOf(t, result, &payload)

	if len(payload.Identities) != 1 || payload.Identities[0] != "bot" {
		t.Errorf("identities are %v", payload.Identities)
	}
	if _, ok := payload.Lists["user"]; ok {
		t.Error("the account's lists are reported by a bot-only server")
	}
	if payload.DownloadDir != "" {
		t.Errorf("download_dir is %q on a server without one", payload.DownloadDir)
	}
}
