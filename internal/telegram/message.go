// Package telegram reaches Telegram as two separate identities: a user account over
// MTProto and a bot over the Bot API. Both answer in the same message shape, so a
// caller reading one chat does not have to know which identity fetched it.
package telegram

import (
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tg"
)

// Message is one message, with every field a caller has asked for so far: who wrote
// it, when, what it says, what hangs off it, and what it answers. The previous server
// returned text only, which meant every real question needed a second pass over the
// same history.
type Message struct {
	ID               int          `json:"id"`
	ChatID           int64        `json:"chat_id"`
	Date             time.Time    `json:"date"`
	Edited           *time.Time   `json:"edited,omitempty"`
	Author           Author       `json:"author"`
	Text             string       `json:"text,omitempty"`
	ReplyToMessageID int          `json:"reply_to_message_id,omitempty"`
	Attachments      []Attachment `json:"attachments,omitempty"`
	Outgoing         bool         `json:"outgoing,omitempty"`
	// Service holds the kind of a service message ("someone joined the group"), which
	// carries no text but does carry meaning when reading a history in order.
	Service string `json:"service,omitempty"`
	// UpdateID is set only on messages the bot read from its update queue. The caller
	// needs it to ask for the next batch, since the queue is acknowledged by offset.
	UpdateID int `json:"update_id,omitempty"`
}

// Author is the sender. Channel posts and anonymous group admins have no user behind
// them, so Kind says what the identifier means instead of leaving a bare number.
type Author struct {
	ID       int64  `json:"id,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Name     string `json:"name,omitempty"`
	Username string `json:"username,omitempty"`
	Bot      bool   `json:"bot,omitempty"`
}

// Attachment describes a file on a message without downloading it. Names and sizes are
// enough to decide whether the file is worth fetching, and that decision belongs to
// the caller.
type Attachment struct {
	Kind     string `json:"kind"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Size     int64  `json:"size,omitempty"`
	// FileID identifies the file for a download. On MTProto it is the document or photo
	// identifier; on the Bot API it is the opaque file_id.
	FileID string `json:"file_id,omitempty"`
}

// Attachment kinds. These are the caller's vocabulary, not Telegram's: MTProto and the
// Bot API describe the same photo in different words.
const (
	KindPhoto     = "photo"
	KindDocument  = "document"
	KindVideo     = "video"
	KindAudio     = "audio"
	KindVoice     = "voice"
	KindSticker   = "sticker"
	KindAnimation = "animation"
	KindWebPage   = "web_page"
	KindGeo       = "geo"
	KindContact   = "contact"
	KindPoll      = "poll"
	KindOther     = "other"
)

// Entities resolves the peer identifiers inside a message. MTProto sends senders in a
// separate list beside the messages, so a message on its own cannot name its author.
type Entities struct {
	users    map[int64]*tg.User
	chats    map[int64]*tg.Chat
	channels map[int64]*tg.Channel
}

// NewEntities indexes the users and chats that came with a response.
func NewEntities(users []tg.UserClass, chats []tg.ChatClass) *Entities {
	e := &Entities{
		users:    map[int64]*tg.User{},
		chats:    map[int64]*tg.Chat{},
		channels: map[int64]*tg.Channel{},
	}

	for _, raw := range users {
		if user, ok := raw.(*tg.User); ok {
			e.users[user.ID] = user
		}
	}

	for _, raw := range chats {
		switch chat := raw.(type) {
		case *tg.Chat:
			e.chats[chat.ID] = chat
		case *tg.Channel:
			e.channels[chat.ID] = chat
		}
	}

	return e
}

// entitiesOf adapts gotd's own entity storage, which the pagination helpers hand out
// with every page.
func entitiesOf(source peerEntities) *Entities {
	return &Entities{
		users:    source.Users(),
		chats:    source.Chats(),
		channels: source.Channels(),
	}
}

// peerEntities is the part of gotd's entity storage this package reads. Declared here
// rather than imported as a type so the conversion stays testable without building one
// of theirs.
type peerEntities interface {
	Users() map[int64]*tg.User
	Chats() map[int64]*tg.Chat
	Channels() map[int64]*tg.Channel
}

// usersOf and chatsOf flatten the entity maps back into the lists the cache fills from.
func usersOf(users map[int64]*tg.User) []tg.UserClass {
	list := make([]tg.UserClass, 0, len(users))
	for _, user := range users {
		list = append(list, user)
	}
	return list
}

func chatsOf(chats map[int64]*tg.Chat, channels map[int64]*tg.Channel) []tg.ChatClass {
	list := make([]tg.ChatClass, 0, len(chats)+len(channels))
	for _, chat := range chats {
		list = append(list, chat)
	}
	for _, channel := range channels {
		list = append(list, channel)
	}
	return list
}

// Author names a peer as far as the entity lists allow. An unknown peer still gets its
// identifier and kind, because a message with an unnamed author is more useful than a
// failed read.
func (e *Entities) Author(peer tg.PeerClass) Author {
	switch p := peer.(type) {
	case *tg.PeerUser:
		author := Author{ID: p.UserID, Kind: "user"}
		if user, ok := e.users[p.UserID]; ok {
			author.Name = userName(user)
			author.Username = user.Username
			author.Bot = user.Bot
		}
		return author
	case *tg.PeerChat:
		author := Author{ID: p.ChatID, Kind: "chat"}
		if chat, ok := e.chats[p.ChatID]; ok {
			author.Name = chat.Title
		}
		return author
	case *tg.PeerChannel:
		author := Author{ID: p.ChannelID, Kind: "channel"}
		if channel, ok := e.channels[p.ChannelID]; ok {
			author.Name = channel.Title
			author.Username = channel.Username
		}
		return author
	default:
		return Author{}
	}
}

func userName(user *tg.User) string {
	name := strings.TrimSpace(user.FirstName + " " + user.LastName)
	if name != "" {
		return name
	}

	return user.Username
}

// ConvertMessages maps a history response into the caller's shape. Messages Telegram
// reports as deleted (messageEmpty) are dropped: they carry an identifier and nothing
// else.
func ConvertMessages(raw []tg.MessageClass, chatID int64, entities *Entities) []Message {
	messages := make([]Message, 0, len(raw))

	for _, item := range raw {
		message, ok := ConvertMessage(item, chatID, entities)
		if !ok {
			continue
		}
		messages = append(messages, message)
	}

	return messages
}

// ConvertMessage maps one message. The second result is false for messages with no
// content at all.
func ConvertMessage(raw tg.MessageClass, chatID int64, entities *Entities) (Message, bool) {
	switch m := raw.(type) {
	case *tg.Message:
		message := Message{
			ID:       m.ID,
			ChatID:   chatID,
			Date:     time.Unix(int64(m.Date), 0).UTC(),
			Text:     m.Message,
			Outgoing: m.Out,
		}

		if m.FromID != nil {
			message.Author = entities.Author(m.FromID)
		} else {
			// A channel post and a message in a private chat both leave from_id out: the
			// sender is the chat itself.
			message.Author = entities.Author(m.PeerID)
		}

		if m.EditDate != 0 {
			at := time.Unix(int64(m.EditDate), 0).UTC()
			message.Edited = &at
		}

		if header, ok := m.ReplyTo.(*tg.MessageReplyHeader); ok {
			message.ReplyToMessageID = header.ReplyToMsgID
		}

		if m.Media != nil {
			if attachment, ok := ConvertMedia(m.Media); ok {
				message.Attachments = append(message.Attachments, attachment)
			}
		}

		return message, true

	case *tg.MessageService:
		message := Message{
			ID:      m.ID,
			ChatID:  chatID,
			Date:    time.Unix(int64(m.Date), 0).UTC(),
			Service: serviceName(m.Action),
		}

		if m.FromID != nil {
			message.Author = entities.Author(m.FromID)
		}

		return message, true

	default:
		return Message{}, false
	}
}

// ConvertMedia describes an attachment. Media Telegram sends as unsupported, or as a
// type this server has no name for, still comes back as "other" rather than vanishing:
// a message that says "here is the invoice" with no visible attachment reads as a bug.
func ConvertMedia(media tg.MessageMediaClass) (Attachment, bool) {
	switch m := media.(type) {
	case *tg.MessageMediaPhoto:
		attachment := Attachment{Kind: KindPhoto}
		if photo, ok := m.Photo.(*tg.Photo); ok {
			attachment.FileID = fmt.Sprint(photo.ID)
		}
		return attachment, true

	case *tg.MessageMediaDocument:
		attachment := Attachment{Kind: KindDocument}

		doc, ok := m.Document.(*tg.Document)
		if !ok {
			return attachment, true
		}

		attachment.FileID = fmt.Sprint(doc.ID)
		attachment.MimeType = doc.MimeType
		attachment.Size = doc.Size
		attachment.Kind = documentKind(doc)
		attachment.FileName = documentFileName(doc)

		return attachment, true

	case *tg.MessageMediaWebPage:
		return Attachment{Kind: KindWebPage}, true
	case *tg.MessageMediaGeo, *tg.MessageMediaGeoLive, *tg.MessageMediaVenue:
		return Attachment{Kind: KindGeo}, true
	case *tg.MessageMediaContact:
		return Attachment{Kind: KindContact}, true
	case *tg.MessageMediaPoll:
		return Attachment{Kind: KindPoll}, true
	case *tg.MessageMediaEmpty:
		return Attachment{}, false
	default:
		return Attachment{Kind: KindOther}, true
	}
}

// documentKind reads the kind off the document attributes. Telegram calls a voice
// message, a video, a sticker and a spreadsheet all documents, and the difference is
// in the attributes.
func documentKind(doc *tg.Document) string {
	for _, attribute := range doc.Attributes {
		switch a := attribute.(type) {
		case *tg.DocumentAttributeSticker:
			return KindSticker
		case *tg.DocumentAttributeAnimated:
			return KindAnimation
		case *tg.DocumentAttributeVideo:
			return KindVideo
		case *tg.DocumentAttributeAudio:
			if a.Voice {
				return KindVoice
			}
			return KindAudio
		}
	}

	return KindDocument
}

func documentFileName(doc *tg.Document) string {
	for _, attribute := range doc.Attributes {
		if name, ok := attribute.(*tg.DocumentAttributeFilename); ok {
			return name.FileName
		}
	}

	return ""
}

// serviceName turns a service action into words. The schema name is trimmed to its
// meaning ("messageActionChatAddUser" becomes "chat_add_user") rather than passed
// through, so the output does not read as a leaked internal type.
func serviceName(action tg.MessageActionClass) string {
	name := strings.TrimPrefix(action.TypeName(), "messageAction")
	if name == "" {
		return "unknown"
	}

	var out strings.Builder
	for i, symbol := range name {
		if symbol >= 'A' && symbol <= 'Z' {
			if i > 0 {
				out.WriteByte('_')
			}
			out.WriteRune(symbol + ('a' - 'A'))
			continue
		}
		out.WriteRune(symbol)
	}

	return out.String()
}
