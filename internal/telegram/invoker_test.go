package telegram

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

// fakeInvoker answers raw MTProto calls without a network.
//
// This is where the reading tools are actually exercised: pagination, ordering and the
// bounds all run through gotd's own iterators, which a hand-rolled stub would skip.
type fakeInvoker struct {
	t *testing.T

	// pages are handed out one per messages.getHistory call.
	pages [][]tg.MessageClass
	users []tg.UserClass
	chats []tg.ChatClass

	search   []tg.MessageClass
	dialogs  []tg.DialogClass
	sentText string
	fail     error

	historyOffsets []int
	calls          []string
}

func (f *fakeInvoker) Invoke(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	if f.fail != nil {
		return f.fail
	}

	switch request := input.(type) {
	case *tg.MessagesGetHistoryRequest:
		f.calls = append(f.calls, "getHistory")
		f.historyOffsets = append(f.historyOffsets, request.OffsetID)

		var page []tg.MessageClass
		if len(f.pages) > 0 {
			page, f.pages = f.pages[0], f.pages[1:]
		}

		return f.answerMessages(output, page)

	case *tg.MessagesSearchRequest:
		f.calls = append(f.calls, "search")
		return f.answerMessages(output, f.search)

	case *tg.MessagesGetDialogsRequest:
		f.calls = append(f.calls, "getDialogs")

		box, ok := output.(*tg.MessagesDialogsBox)
		if !ok {
			f.t.Fatalf("getDialogs wants %T", output)
		}
		box.Dialogs = &tg.MessagesDialogs{Dialogs: f.dialogs, Users: f.users, Chats: f.chats}

		return nil

	case *tg.MessagesSendMessageRequest:
		f.calls = append(f.calls, "sendMessage")
		f.sentText = request.Message

		box, ok := output.(*tg.UpdatesBox)
		if !ok {
			f.t.Fatalf("sendMessage wants %T", output)
		}
		box.Updates = &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateMessageID{ID: 777}}}

		return nil

	default:
		f.t.Fatalf("nothing here answers %T", input)
		return nil
	}
}

func (f *fakeInvoker) answerMessages(output bin.Decoder, messages []tg.MessageClass) error {
	box, ok := output.(*tg.MessagesMessagesBox)
	if !ok {
		f.t.Fatalf("a message request wants %T", output)
	}

	box.Messages = &tg.MessagesMessagesSlice{
		Count:    len(messages),
		Messages: messages,
		Users:    f.users,
		Chats:    f.chats,
	}

	return nil
}

// wired builds a client that talks to the fake invoker and already knows one chat.
func wired(t *testing.T, invoker *fakeInvoker) *UserClient {
	t.Helper()

	client := ready(testClient())
	client.raw = tg.NewClient(invoker)
	client.cache.addChats([]tg.ChatClass{
		&tg.Channel{ID: 1111111111, AccessHash: 7, Title: "accounting", Megagroup: true},
	})

	return client
}

func chatMessage(id int, at time.Time, text string) tg.MessageClass {
	return &tg.Message{
		ID:      id,
		Date:    int(at.Unix()),
		Message: text,
		PeerID:  &tg.PeerChannel{ChannelID: 1111111111},
		FromID:  &tg.PeerUser{UserID: 42},
	}
}

// The case this server was written for: a whole chat, read in order, across more than one
// page. Telegram signals the end of a history by answering with less than a full page, so
// the first page here is a full one.
func TestHistoryWalksEveryPage(t *testing.T) {
	january := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	full := make([]tg.MessageClass, 0, historyBatch)
	for i := 0; i < historyBatch; i++ {
		id := 200 - i
		full = append(full, chatMessage(id, january.AddDate(0, 0, id), "message"))
	}

	invoker := &fakeInvoker{
		t:     t,
		users: []tg.UserClass{&tg.User{ID: 42, FirstName: "Anna"}},
		pages: [][]tg.MessageClass{
			full,
			{chatMessage(100, january, "the oldest one")},
		},
	}

	messages, err := wired(t, invoker).History(context.Background(),
		ChannelChatID(1111111111), HistoryOptions{})
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	if len(messages) != historyBatch+1 {
		t.Fatalf("got %d messages, want the whole of both pages", len(messages))
	}
	// Oldest first, although Telegram sent them newest first, page by page.
	if messages[0].Text != "the oldest one" || messages[0].ID != 100 {
		t.Errorf("the first message is %+v", messages[0])
	}
	if messages[len(messages)-1].ID != 200 {
		t.Errorf("the last message is %+v", messages[len(messages)-1])
	}
	if messages[0].Author.Name != "Anna" {
		t.Errorf("the author is %+v", messages[0].Author)
	}

	// Each request continues below the oldest message of the previous page, which is what
	// makes this a full read rather than the same page for ever. The last request is the
	// one that finds nothing left.
	if len(invoker.historyOffsets) < 2 || invoker.historyOffsets[0] != 0 || invoker.historyOffsets[1] != 101 {
		t.Errorf("the requests asked for offsets %v", invoker.historyOffsets)
	}
}

func TestHistoryStartsWhereTheCallerSays(t *testing.T) {
	invoker := &fakeInvoker{t: t, pages: [][]tg.MessageClass{{}}}

	if _, err := wired(t, invoker).History(context.Background(),
		ChannelChatID(1111111111), HistoryOptions{OffsetID: 500}); err != nil {
		t.Fatalf("History: %v", err)
	}

	if len(invoker.historyOffsets) == 0 || invoker.historyOffsets[0] != 500 {
		t.Errorf("the first request asked for offset %v", invoker.historyOffsets)
	}
}

func TestHistoryOfAChatThatIsNotAllowedToBeResolved(t *testing.T) {
	invoker := &fakeInvoker{t: t, fail: errors.New("no connection")}
	client := ready(testClient())
	client.raw = tg.NewClient(invoker)

	if _, err := client.History(context.Background(), ChannelChatID(999), HistoryOptions{}); err == nil {
		t.Error("a history was read for a chat that could not be resolved")
	}
}

func TestHistoryReportsAFailedRead(t *testing.T) {
	invoker := &fakeInvoker{t: t}
	client := wired(t, invoker)
	invoker.fail = errors.New("no connection")

	if _, err := client.History(context.Background(), ChannelChatID(1111111111), HistoryOptions{}); err == nil {
		t.Error("a failed read was reported as an empty chat")
	}
}

func TestSearchInsideOneChat(t *testing.T) {
	january := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	invoker := &fakeInvoker{
		t:     t,
		users: []tg.UserClass{&tg.User{ID: 42, FirstName: "Anna"}},
		search: []tg.MessageClass{
			chatMessage(30, january.AddDate(0, 1, 0), "the second invoice"),
			chatMessage(20, january, "the first invoice"),
		},
	}

	messages, err := wired(t, invoker).Search(context.Background(),
		ChannelChatID(1111111111), "invoice", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(messages) != 2 || messages[0].ID != 20 {
		t.Fatalf("the matches are %v", ids(messages))
	}
}

func TestSearchLimitIsCapped(t *testing.T) {
	invoker := &fakeInvoker{t: t}

	// A limit of zero or a silly one both become the page size rather than reaching
	// Telegram as they are.
	for _, limit := range []int{0, -1, 1000} {
		if _, err := wired(t, invoker).Search(context.Background(),
			ChannelChatID(1111111111), "invoice", limit); err != nil {
			t.Fatalf("Search with limit %d: %v", limit, err)
		}
	}
}

func TestLoadDialogsFillsThePeerCache(t *testing.T) {
	invoker := &fakeInvoker{
		t:     t,
		users: []tg.UserClass{&tg.User{ID: 42, AccessHash: 7, FirstName: "Anna"}},
		chats: []tg.ChatClass{&tg.Chat{ID: 100, Title: "small group"}},
		dialogs: []tg.DialogClass{
			&tg.Dialog{Peer: &tg.PeerUser{UserID: 42}},
			&tg.Dialog{Peer: &tg.PeerChat{ChatID: 100}},
		},
	}

	client := ready(testClient())
	client.raw = tg.NewClient(invoker)

	if err := client.loadDialogs(context.Background()); err != nil {
		t.Fatalf("loadDialogs: %v", err)
	}

	if _, ok := client.cache.peer(UserChatID(42)); !ok {
		t.Error("the person from the dialog list is not in the cache")
	}
	if _, ok := client.cache.peer(GroupChatID(100)); !ok {
		t.Error("the group from the dialog list is not in the cache")
	}
}

func TestSendMessageAsTheAccount(t *testing.T) {
	invoker := &fakeInvoker{t: t}

	sent, err := wired(t, invoker).SendMessage(context.Background(),
		ChannelChatID(1111111111), "the invoice is attached", 511)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if sent.MessageID != 777 || sent.ChatID != ChannelChatID(1111111111) {
		t.Errorf("sent is %+v", sent)
	}
	if invoker.sentText != "the invoice is attached" {
		t.Errorf("the text that went out is %q", invoker.sentText)
	}
}

func TestSendMessageRefusesAnEmptyOne(t *testing.T) {
	invoker := &fakeInvoker{t: t}

	if _, err := wired(t, invoker).SendMessage(context.Background(),
		ChannelChatID(1111111111), "", 0); err == nil {
		t.Error("an empty message was sent")
	}
	if len(invoker.calls) != 0 {
		t.Errorf("the refusal still reached Telegram: %v", invoker.calls)
	}
}

func TestSendFileOfSomethingThatIsNotThere(t *testing.T) {
	invoker := &fakeInvoker{t: t}
	client := wired(t, invoker)

	if _, err := client.SendFile(context.Background(),
		ChannelChatID(1111111111), t.TempDir()+"/nope", ""); err == nil {
		t.Error("a missing file was sent")
	}

	// A directory is not a file either, and the failure has to come before the upload.
	if _, err := client.SendFile(context.Background(),
		ChannelChatID(1111111111), t.TempDir(), ""); err == nil {
		t.Error("a directory was sent")
	}
	if len(invoker.calls) != 0 {
		t.Errorf("the refusal still reached Telegram: %v", invoker.calls)
	}
}

// Every read waits for the connection first, so a client that never came up answers with
// the reason rather than with an empty result.
func TestReadsWaitForTheConnection(t *testing.T) {
	client := testClient()
	client.err = ErrNotAuthorized
	close(client.done)

	if _, err := client.History(context.Background(), 1, HistoryOptions{}); !errors.Is(err, ErrNotAuthorized) {
		t.Errorf("History answered %v", err)
	}
	if _, err := client.Search(context.Background(), 1, "x", 1); !errors.Is(err, ErrNotAuthorized) {
		t.Errorf("Search answered %v", err)
	}
	if _, err := client.Chats(context.Background()); !errors.Is(err, ErrNotAuthorized) {
		t.Errorf("Chats answered %v", err)
	}
	if _, err := client.ChatInfo(context.Background(), 1); !errors.Is(err, ErrNotAuthorized) {
		t.Errorf("ChatInfo answered %v", err)
	}
	if _, err := client.Download(context.Background(), 1, 1, t.TempDir()); !errors.Is(err, ErrNotAuthorized) {
		t.Errorf("Download answered %v", err)
	}
	if _, err := client.SendMessage(context.Background(), 1, "x", 0); !errors.Is(err, ErrNotAuthorized) {
		t.Errorf("SendMessage answered %v", err)
	}
	if _, err := client.SendFile(context.Background(), 1, "x", ""); !errors.Is(err, ErrNotAuthorized) {
		t.Errorf("SendFile answered %v", err)
	}
}
