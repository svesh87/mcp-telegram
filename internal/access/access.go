// Package access decides which chats a caller may read and write.
//
// The server's own configuration is the only place this is decided. Callers never
// hold Telegram credentials, so they cannot reach a chat by going around it.
package access

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Identity separates the two ways this server can act. Rights are held per identity
// because "my account" and "the bot" are different actors with different reach: the
// bot sees only what it was added to, the account sees everything its user does.
type Identity int

const (
	User Identity = iota
	Bot
)

func (i Identity) String() string {
	switch i {
	case User:
		return "user"
	case Bot:
		return "bot"
	default:
		return "unknown"
	}
}

// Mode is the kind of operation a tool performs.
type Mode int

const (
	Read Mode = iota
	Write
)

func (m Mode) String() string {
	switch m {
	case Read:
		return "read"
	case Write:
		return "write"
	default:
		return "unknown"
	}
}

// Lists holds the four access lists, one per identity and mode. Read access is
// implied by write access: a list that may be written to may also be read, since
// writing blind to a chat is not a thing anyone wants.
type Lists struct {
	UserRead  []int64
	UserWrite []int64
	BotRead   []int64
	BotWrite  []int64
}

// Checker answers whether an identity may act on a chat.
type Checker struct {
	allowed map[Identity]map[Mode]map[int64]bool
}

// New builds a checker from the four lists.
func New(lists Lists) *Checker {
	c := &Checker{allowed: map[Identity]map[Mode]map[int64]bool{
		User: {Read: {}, Write: {}},
		Bot:  {Read: {}, Write: {}},
	}}

	add := func(identity Identity, mode Mode, chats []int64) {
		for _, chat := range chats {
			c.allowed[identity][mode][chat] = true
			// Write implies read.
			if mode == Write {
				c.allowed[identity][Read][chat] = true
			}
		}
	}

	add(User, Read, lists.UserRead)
	add(User, Write, lists.UserWrite)
	add(Bot, Read, lists.BotRead)
	add(Bot, Write, lists.BotWrite)

	return c
}

// Allowed reports whether the identity may perform mode on chat.
func (c *Checker) Allowed(identity Identity, mode Mode, chat int64) bool {
	return c.allowed[identity][mode][chat]
}

// Check returns nil when the operation is allowed, and an error a caller can act on
// otherwise. A chat missing from every list is refused explicitly rather than
// answered with an empty result, so nobody mistakes "not allowed" for "nothing
// there".
func (c *Checker) Check(identity Identity, mode Mode, chat int64) error {
	if c.Allowed(identity, mode, chat) {
		return nil
	}

	if mode == Write && c.Allowed(identity, Read, chat) {
		return fmt.Errorf("chat %d is read-only for the %s identity: add it to the %s write list to allow this",
			chat, identity, identity)
	}

	return fmt.Errorf("chat %d is not in any %s access list: add it to a list to allow this", chat, identity)
}

// Chats lists the chats an identity may act on, sorted, for tools that answer
// "what can I see" without touching Telegram.
func (c *Checker) Chats(identity Identity, mode Mode) []int64 {
	chats := make([]int64, 0, len(c.allowed[identity][mode]))
	for chat := range c.allowed[identity][mode] {
		chats = append(chats, chat)
	}
	sort.Slice(chats, func(i, j int) bool { return chats[i] < chats[j] })
	return chats
}

// Empty reports whether an identity has no rights at all, which means it should not
// be offered to callers.
func (c *Checker) Empty(identity Identity) bool {
	return len(c.allowed[identity][Read]) == 0 && len(c.allowed[identity][Write]) == 0
}

// ParseList reads a comma-separated list of chat identifiers.
//
// Identifiers, not names: a chat title is renamed by whoever owns the chat, and an
// access list that can be widened by someone else renaming their group is not an
// access list. Whitespace and empty entries are tolerated because these lists are
// hand-edited.
func ParseList(raw string) ([]int64, error) {
	var chats []int64

	for _, field := range strings.Split(raw, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}

		chat, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a chat identifier: expected a number, "+
				"and names are deliberately not accepted", field)
		}

		chats = append(chats, chat)
	}

	return chats, nil
}
