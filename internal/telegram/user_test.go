package telegram

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

// A client built for these tests is connected to nothing. Everything below is the part
// of the user identity that decides things, rather than the part that talks.
func testClient() *UserClient {
	client := &UserClient{
		cache: newPeerCache(),
		ready: make(chan struct{}),
		done:  make(chan struct{}),
	}
	client.refresh = func(context.Context) error { return nil }

	return client
}

func ready(client *UserClient) *UserClient {
	close(client.ready)
	return client
}

func TestNewUserNeedsASessionDirectory(t *testing.T) {
	if _, err := NewUser(UserOptions{APIID: 1, APIHash: "hash"}); err == nil {
		t.Error("a client was built with nowhere to keep its session")
	}
}

func TestNewUserCreatesTheSessionDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "session")

	client, err := NewUser(UserOptions{APIID: 1, APIHash: "hash", SessionDir: dir})
	if err != nil {
		t.Fatalf("NewUser: %v", err)
	}
	if client.refresh == nil {
		t.Error("the dialog refresh was not wired up")
	}

	if _, err := filepath.Glob(dir); err != nil {
		t.Errorf("the session directory is not there: %v", err)
	}
}

// Wait is how every tool finds out whether it may talk to Telegram at all.
func TestWait(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		if err := ready(testClient()).Wait(context.Background()); err != nil {
			t.Errorf("Wait on a ready client: %v", err)
		}
	})

	t.Run("stopped with a reason", func(t *testing.T) {
		client := testClient()
		client.err = ErrNotAuthorized
		close(client.done)

		if err := client.Wait(context.Background()); !errors.Is(err, ErrNotAuthorized) {
			t.Errorf("Wait answered %v, want the reason the client stopped", err)
		}
	})

	t.Run("stopped without one", func(t *testing.T) {
		client := testClient()
		close(client.done)

		if err := client.Wait(context.Background()); err == nil {
			t.Error("Wait on a stopped client answered without an error")
		}
	})

	t.Run("the caller gave up", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if err := testClient().Wait(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("Wait answered %v, want the cancellation", err)
		}
	})
}

// An identifier that is not in the cache is looked up once more, because a chat can be
// joined after the server started. Two misses in a row are refused with an explanation.
func TestResolveRefreshesOnceOnAnUnknownChat(t *testing.T) {
	t.Run("already known", func(t *testing.T) {
		client := testClient()
		client.cache.addUsers([]tg.UserClass{&tg.User{ID: 42, AccessHash: 7}})
		refreshed := false
		client.refresh = func(context.Context) error { refreshed = true; return nil }

		if _, err := client.resolve(context.Background(), UserChatID(42)); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if refreshed {
			t.Error("the dialog list was reloaded for a chat that was already known")
		}
	})

	t.Run("found on the second look", func(t *testing.T) {
		client := testClient()
		client.refresh = func(context.Context) error {
			client.cache.addUsers([]tg.UserClass{&tg.User{ID: 42, AccessHash: 7}})
			return nil
		}

		peer, err := client.resolve(context.Background(), UserChatID(42))
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if _, ok := peer.(*tg.InputPeerUser); !ok {
			t.Errorf("resolved to %T", peer)
		}
	})

	t.Run("not there at all", func(t *testing.T) {
		client := testClient()

		_, err := client.resolve(context.Background(), ChannelChatID(1111111111))
		if err == nil {
			t.Fatal("an unknown chat resolved")
		}
		if !strings.Contains(err.Error(), "member") || !strings.Contains(err.Error(), ChatKindChannel) {
			t.Errorf("the refusal does not help: %v", err)
		}
	})

	t.Run("the dialog list could not be read", func(t *testing.T) {
		client := testClient()
		client.refresh = func(context.Context) error { return errors.New("no connection") }

		if _, err := client.resolve(context.Background(), UserChatID(42)); err == nil {
			t.Error("resolve succeeded although the dialog list could not be read")
		}
	})
}

func TestChatsAndChatInfoComeFromTheCache(t *testing.T) {
	client := ready(testClient())
	client.cache.addUsers([]tg.UserClass{&tg.User{ID: 42, AccessHash: 7, FirstName: "Anna"}})
	client.cache.addChats([]tg.ChatClass{&tg.Chat{ID: 100, Title: "small group"}})

	chats, err := client.Chats(context.Background())
	if err != nil {
		t.Fatalf("Chats: %v", err)
	}
	if len(chats) != 2 {
		t.Fatalf("got %d chats", len(chats))
	}
	// Sorted, so an answer does not shuffle between calls.
	if chats[0].ID > chats[1].ID {
		t.Errorf("the chats are not sorted: %+v", chats)
	}

	chat, err := client.ChatInfo(context.Background(), UserChatID(42))
	if err != nil {
		t.Fatalf("ChatInfo: %v", err)
	}
	if chat.Title != "Anna" {
		t.Errorf("the chat is %+v", chat)
	}

	if _, err := client.ChatInfo(context.Background(), ChannelChatID(999)); err == nil {
		t.Error("ChatInfo answered for a chat the account cannot see")
	}
}

func TestSearchNeedsAQuery(t *testing.T) {
	client := ready(testClient())

	if _, err := client.Search(context.Background(), UserChatID(42), "", 10); err == nil {
		t.Error("a search with no query was accepted")
	}
}

// The bounds decide when a history read stops. A year of one chat is the case this was
// written for, so every bound is checked separately.
func TestHistoryBounds(t *testing.T) {
	january := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	messages := []Message{
		{ID: 10, Date: january.AddDate(0, 3, 0)},
		{ID: 9, Date: january.AddDate(0, 2, 0)},
		{ID: 8, Date: january.AddDate(0, 1, 0)},
		{ID: 7, Date: january},
	}

	cases := []struct {
		name string
		opts HistoryOptions
		want []int
	}{
		{"everything", HistoryOptions{}, []int{7, 8, 9, 10}},
		{"a limit", HistoryOptions{Limit: 2}, []int{9, 10}},
		{"from a date", HistoryOptions{Since: january.AddDate(0, 2, 0)}, []int{9, 10}},
		{"from a message", HistoryOptions{MinID: 8}, []int{9, 10}},
		{"a limit that is never reached", HistoryOptions{Limit: 100}, []int{7, 8, 9, 10}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			collected := &history{opts: c.opts}

			for _, message := range messages {
				if collected.add(message) {
					break
				}
			}

			result := collected.result()
			if len(result) != len(c.want) {
				t.Fatalf("got %d messages, want %d", len(result), len(c.want))
			}
			// Reading order: oldest first, whatever order Telegram sent them in.
			for i, want := range c.want {
				if result[i].ID != want {
					t.Fatalf("the answer is %v, want %v", ids(result), c.want)
				}
			}
		})
	}
}

func ids(messages []Message) []int {
	out := make([]int, 0, len(messages))
	for _, message := range messages {
		out = append(out, message.ID)
	}
	return out
}

func TestSearchResult(t *testing.T) {
	result := &tg.MessagesMessagesSlice{
		Messages: []tg.MessageClass{
			&tg.Message{ID: 9, Date: 2, Message: "second", PeerID: &tg.PeerUser{UserID: 42}},
			&tg.Message{ID: 8, Date: 1, Message: "first", PeerID: &tg.PeerUser{UserID: 42}},
		},
		Users: []tg.UserClass{&tg.User{ID: 42, FirstName: "Anna"}},
	}

	messages, err := searchResult(result, UserChatID(42))
	if err != nil {
		t.Fatalf("searchResult: %v", err)
	}

	if len(messages) != 2 || messages[0].ID != 8 {
		t.Fatalf("the answer is %v", ids(messages))
	}
	if messages[0].Author.Name != "Anna" {
		t.Errorf("the author is %+v", messages[0].Author)
	}
}

// A "nothing changed" answer to a request that carried no hash is not an empty result,
// so it is reported rather than passed off as one.
func TestSearchResultOfSomethingUnreadable(t *testing.T) {
	if _, err := searchResult(&tg.MessagesMessagesNotModified{}, -100); err == nil {
		t.Error("an unreadable answer was accepted as an empty result")
	}
}

func TestDownloadTarget(t *testing.T) {
	message := &tg.Message{ID: 512, Date: 1}
	message.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{
		ID: 9, AccessHash: 7, Size: 4096,
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: "../invoice.pdf"},
		},
	}})

	location, path, size, err := downloadTarget(message, -1001111111111, 512, "/downloads")
	if err != nil {
		t.Fatalf("downloadTarget: %v", err)
	}

	if _, ok := location.(*tg.InputDocumentFileLocation); !ok {
		t.Errorf("location is %T", location)
	}
	if path != "/downloads/-1001111111111_512_invoice.pdf" {
		t.Errorf("the file would land at %q", path)
	}
	if size != 4096 {
		t.Errorf("size is %d", size)
	}
}

func TestDownloadTargetOfAMessageWithNoFile(t *testing.T) {
	cases := []struct {
		name string
		raw  tg.MessageClass
	}{
		{"plain text", &tg.Message{ID: 1, Date: 1}},
		{"a service message", &tg.MessageService{ID: 2, Date: 1, Action: &tg.MessageActionChatAddUser{}}},
		{"a poll", withMedia(&tg.MessageMediaPoll{})},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, _, err := downloadTarget(c.raw, -100, 1, "/downloads"); err == nil {
				t.Error("a message with no file was accepted")
			}
		})
	}
}

func withMedia(media tg.MessageMediaClass) *tg.Message {
	message := &tg.Message{ID: 3, Date: 1}
	message.SetMedia(media)
	return message
}
