package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
)

// Telegram answers a send with a batch of updates, and the identifier of the new message
// is somewhere inside it. A caller that wants to quote the message back needs it.
func TestSentMessageID(t *testing.T) {
	cases := []struct {
		name    string
		updates tg.UpdatesClass
		want    int
	}{
		{
			"a private chat",
			&tg.UpdateShortSentMessage{ID: 77},
			77,
		},
		{
			"a group",
			&tg.Updates{Updates: []tg.UpdateClass{
				&tg.UpdateMessageID{ID: 78, RandomID: 1},
			}},
			78,
		},
		{
			"a new message in a group",
			&tg.Updates{Updates: []tg.UpdateClass{
				&tg.UpdateNewMessage{Message: &tg.Message{ID: 79}},
			}},
			79,
		},
		{
			"a channel post",
			&tg.UpdatesCombined{Updates: []tg.UpdateClass{
				&tg.UpdateNewChannelMessage{Message: &tg.Message{ID: 80}},
			}},
			80,
		},
		{
			"updates that say nothing about it",
			&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateUserTyping{UserID: 42}}},
			0,
		},
		{
			"an update batch this server has no reader for",
			&tg.UpdatesTooLong{},
			0,
		},
		{
			"a deleted message where the identifier should be",
			&tg.Updates{Updates: []tg.UpdateClass{
				&tg.UpdateNewMessage{Message: &tg.MessageEmpty{ID: 81}},
			}},
			0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SentMessageID(c.updates); got != c.want {
				t.Errorf("SentMessageID is %d, want %d", got, c.want)
			}
		})
	}
}
