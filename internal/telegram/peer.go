package telegram

import (
	"fmt"
	"sync"

	"github.com/gotd/td/constant"
	"github.com/gotd/td/tg"
)

// Chat is what a caller may know about a chat without reading it.
type Chat struct {
	ID       int64  `json:"id"`
	Kind     string `json:"kind"`
	Title    string `json:"title,omitempty"`
	Username string `json:"username,omitempty"`
}

// Chat kinds.
const (
	ChatKindUser    = "user"
	ChatKindGroup   = "group"
	ChatKindChannel = "channel"
)

// peerCache maps the chat identifiers used in the access lists to the input peers
// MTProto needs.
//
// The identifiers in the lists are the ones a person can actually copy out of a
// Telegram client: negative for groups and channels, positive for people. MTProto
// wants the plain identifier plus an access hash instead, and the hash is only handed
// out with the peer, which is why this cache is filled from the dialog list rather
// than computed.
type peerCache struct {
	mu    sync.RWMutex
	peers map[int64]tg.InputPeerClass
	chats map[int64]Chat
}

func newPeerCache() *peerCache {
	return &peerCache{
		peers: map[int64]tg.InputPeerClass{},
		chats: map[int64]Chat{},
	}
}

func (c *peerCache) put(id int64, peer tg.InputPeerClass, chat Chat) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.peers[id] = peer
	c.chats[id] = chat
}

func (c *peerCache) peer(id int64) (tg.InputPeerClass, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	peer, ok := c.peers[id]
	return peer, ok
}

func (c *peerCache) chat(id int64) (Chat, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	chat, ok := c.chats[id]
	return chat, ok
}

// addUsers and addChats fill the cache from any response that carries entity lists.
func (c *peerCache) addUsers(users []tg.UserClass) {
	for _, raw := range users {
		user, ok := raw.(*tg.User)
		if !ok {
			continue
		}

		c.put(UserChatID(user.ID),
			&tg.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash},
			Chat{
				ID:       UserChatID(user.ID),
				Kind:     ChatKindUser,
				Title:    userName(user),
				Username: user.Username,
			})
	}
}

func (c *peerCache) addChats(chats []tg.ChatClass) {
	for _, raw := range chats {
		switch chat := raw.(type) {
		case *tg.Chat:
			c.put(GroupChatID(chat.ID),
				&tg.InputPeerChat{ChatID: chat.ID},
				Chat{ID: GroupChatID(chat.ID), Kind: ChatKindGroup, Title: chat.Title})
		case *tg.Channel:
			// A supergroup and a broadcast channel are the same kind of peer to MTProto and
			// differ only by this flag, which is the one place the difference is visible.
			kind := ChatKindChannel
			if chat.Megagroup {
				kind = ChatKindGroup
			}

			c.put(ChannelChatID(chat.ID),
				&tg.InputPeerChannel{ChannelID: chat.ID, AccessHash: chat.AccessHash},
				Chat{
					ID:       ChannelChatID(chat.ID),
					Kind:     kind,
					Title:    chat.Title,
					Username: chat.Username,
				})
		}
	}
}

// UserChatID, GroupChatID and ChannelChatID build the identifiers used in the access
// lists out of the plain MTProto ones. The arithmetic is Telegram's, so it comes from
// their constants rather than from a literal written here.
func UserChatID(id int64) int64 {
	var peer constant.TDLibPeerID
	peer.User(id)
	return int64(peer)
}

func GroupChatID(id int64) int64 {
	var peer constant.TDLibPeerID
	peer.Chat(id)
	return int64(peer)
}

func ChannelChatID(id int64) int64 {
	var peer constant.TDLibPeerID
	peer.Channel(id)
	return int64(peer)
}

// ChatIDOf reads the access-list identifier off a peer reference.
func ChatIDOf(peer tg.PeerClass) (int64, error) {
	switch p := peer.(type) {
	case *tg.PeerUser:
		return UserChatID(p.UserID), nil
	case *tg.PeerChat:
		return GroupChatID(p.ChatID), nil
	case *tg.PeerChannel:
		return ChannelChatID(p.ChannelID), nil
	default:
		return 0, fmt.Errorf("unsupported peer type %T", peer)
	}
}

// ChatKindOf says what an access-list identifier refers to, without asking Telegram.
//
// A supergroup and a broadcast channel are numbered the same way, so both come back as
// a channel here. Telling them apart takes the chat itself, which the chat tools do.
func ChatKindOf(id int64) string {
	peer := constant.TDLibPeerID(id)

	switch {
	case peer.IsUser():
		return ChatKindUser
	case peer.IsChat():
		return ChatKindGroup
	case peer.IsChannel():
		return ChatKindChannel
	default:
		return "unknown"
	}
}
