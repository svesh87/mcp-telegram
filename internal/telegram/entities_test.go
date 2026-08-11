package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
)

// fakeEntities stands in for gotd's entity storage, which the pagination helpers hand
// out with every page.
type fakeEntities struct {
	users    map[int64]*tg.User
	chats    map[int64]*tg.Chat
	channels map[int64]*tg.Channel
}

func (f fakeEntities) Users() map[int64]*tg.User       { return f.users }
func (f fakeEntities) Chats() map[int64]*tg.Chat       { return f.chats }
func (f fakeEntities) Channels() map[int64]*tg.Channel { return f.channels }

func TestEntitiesOfAPage(t *testing.T) {
	source := fakeEntities{
		users:    map[int64]*tg.User{42: {ID: 42, FirstName: "Anna"}},
		chats:    map[int64]*tg.Chat{100: {ID: 100, Title: "small group"}},
		channels: map[int64]*tg.Channel{200: {ID: 200, Title: "accounting"}},
	}

	entities := entitiesOf(source)

	if got := entities.Author(&tg.PeerUser{UserID: 42}); got.Name != "Anna" {
		t.Errorf("the person is %+v", got)
	}
	if got := entities.Author(&tg.PeerChat{ChatID: 100}); got.Name != "small group" {
		t.Errorf("the group is %+v", got)
	}
	if got := entities.Author(&tg.PeerChannel{ChannelID: 200}); got.Name != "accounting" {
		t.Errorf("the channel is %+v", got)
	}
}

func TestFlatteningEntityMapsBackIntoLists(t *testing.T) {
	users := usersOf(map[int64]*tg.User{42: {ID: 42}, 43: {ID: 43}})
	if len(users) != 2 {
		t.Errorf("got %d users", len(users))
	}

	chats := chatsOf(
		map[int64]*tg.Chat{100: {ID: 100}},
		map[int64]*tg.Channel{200: {ID: 200}, 300: {ID: 300}},
	)
	if len(chats) != 3 {
		t.Errorf("got %d chats", len(chats))
	}
}
