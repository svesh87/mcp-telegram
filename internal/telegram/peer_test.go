package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
)

// The identifiers in the access lists are the ones a person copies out of a Telegram
// client. If this mapping drifts, an access list silently stops matching the chat it was
// written for, so it is pinned here.
func TestChatIdentifiersMatchWhatAClientShows(t *testing.T) {
	cases := []struct {
		name string
		got  int64
		want int64
		kind string
	}{
		{"a person", UserChatID(424242), 424242, ChatKindUser},
		{"a small group", GroupChatID(4242), -4242, ChatKindGroup},
		{"a supergroup or channel", ChannelChatID(1111111111), -1001111111111, ChatKindChannel},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.want {
				t.Errorf("identifier is %d, want %d", c.got, c.want)
			}
			if kind := ChatKindOf(c.got); kind != c.kind {
				t.Errorf("kind of %d is %q, want %q", c.got, kind, c.kind)
			}
		})
	}
}

func TestChatIDOfAPeer(t *testing.T) {
	cases := []struct {
		peer    tg.PeerClass
		want    int64
		wantErr bool
	}{
		{&tg.PeerUser{UserID: 424242}, 424242, false},
		{&tg.PeerChat{ChatID: 4242}, -4242, false},
		{&tg.PeerChannel{ChannelID: 1111111111}, -1001111111111, false},
		{&tg.PeerUser{}, 0, false},
	}

	for _, c := range cases {
		got, err := ChatIDOf(c.peer)
		if (err != nil) != c.wantErr {
			t.Fatalf("ChatIDOf(%T) error is %v", c.peer, err)
		}
		if got != c.want {
			t.Errorf("ChatIDOf(%T) is %d, want %d", c.peer, got, c.want)
		}
	}

	if _, err := ChatIDOf(nil); err == nil {
		t.Error("an unsupported peer type was accepted")
	}
}

func TestChatKindOfSomethingThatIsNoChat(t *testing.T) {
	if kind := ChatKindOf(0); kind != "unknown" {
		t.Errorf("kind of 0 is %q", kind)
	}
}

// The cache is what makes an identifier from a list usable: MTProto needs the access hash
// that came with the peer, and that hash only arrives with the dialog list.
func TestPeerCacheFillsFromEntityLists(t *testing.T) {
	cache := newPeerCache()

	cache.addUsers([]tg.UserClass{
		&tg.User{ID: 42, AccessHash: 777, FirstName: "Anna", Username: "annap"},
		&tg.UserEmpty{ID: 43},
	})
	cache.addChats([]tg.ChatClass{
		&tg.Chat{ID: 100, Title: "small group"},
		&tg.Channel{ID: 200, AccessHash: 888, Title: "accounting", Megagroup: true},
		&tg.Channel{ID: 300, AccessHash: 999, Title: "announcements"},
		&tg.ChatEmpty{ID: 400},
	})

	peer, ok := cache.peer(UserChatID(42))
	if !ok {
		t.Fatal("the person is not in the cache")
	}
	user, ok := peer.(*tg.InputPeerUser)
	if !ok || user.UserID != 42 || user.AccessHash != 777 {
		t.Errorf("the person resolved to %+v", peer)
	}

	chat, ok := cache.chat(UserChatID(42))
	if !ok || chat.Title != "Anna" || chat.Username != "annap" || chat.Kind != ChatKindUser {
		t.Errorf("the person is described as %+v", chat)
	}

	if _, ok := cache.peer(UserChatID(43)); ok {
		t.Error("an empty user ended up in the cache")
	}

	if peer, ok := cache.peer(GroupChatID(100)); !ok {
		t.Error("the small group is not in the cache")
	} else if _, ok := peer.(*tg.InputPeerChat); !ok {
		t.Errorf("the small group resolved to %T", peer)
	}

	// A supergroup is a channel to MTProto but a group to a person reading the answer.
	if chat, ok := cache.chat(ChannelChatID(200)); !ok || chat.Kind != ChatKindGroup {
		t.Errorf("the supergroup is described as %+v", chat)
	}
	if chat, ok := cache.chat(ChannelChatID(300)); !ok || chat.Kind != ChatKindChannel {
		t.Errorf("the channel is described as %+v", chat)
	}

	if _, ok := cache.peer(GroupChatID(400)); ok {
		t.Error("an empty chat ended up in the cache")
	}

	if _, ok := cache.peer(ChannelChatID(555)); ok {
		t.Error("the cache answered for a chat it never saw")
	}
}
