package telegram

import (
	"strings"
	"time"
)

// The Bot API answers in JSON with its own vocabulary. Only the fields this server
// hands to callers are declared: an unknown field is ignored by the decoder, and
// declaring the whole schema would be a second copy of Telegram's documentation to keep
// in step.

type botUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

func (u botUser) name() string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name != "" {
		return name
	}
	return u.Username
}

func (u botUser) author() Author {
	return Author{
		ID:       u.ID,
		Kind:     "user",
		Name:     u.name(),
		Username: u.Username,
		Bot:      u.IsBot,
	}
}

type botChat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

func (c botChat) chat() Chat {
	title := c.Title
	if title == "" {
		title = strings.TrimSpace(c.FirstName + " " + c.LastName)
	}

	kind := ChatKindGroup
	switch c.Type {
	case "private":
		kind = ChatKindUser
	case "channel":
		kind = ChatKindChannel
	}

	return Chat{ID: c.ID, Kind: kind, Title: title, Username: c.Username}
}

type botFile struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
	FilePath string `json:"file_path"`
}

func (f botFile) attachment(kind string) Attachment {
	return Attachment{
		Kind:     kind,
		FileName: f.FileName,
		MimeType: f.MimeType,
		Size:     f.FileSize,
		FileID:   f.FileID,
	}
}

type botMessage struct {
	MessageID      int         `json:"message_id"`
	Date           int64       `json:"date"`
	EditDate       int64       `json:"edit_date"`
	Chat           botChat     `json:"chat"`
	From           *botUser    `json:"from"`
	SenderChat     *botChat    `json:"sender_chat"`
	Text           string      `json:"text"`
	Caption        string      `json:"caption"`
	ReplyToMessage *botMessage `json:"reply_to_message"`

	Document  *botFile  `json:"document"`
	Photo     []botFile `json:"photo"`
	Video     *botFile  `json:"video"`
	Audio     *botFile  `json:"audio"`
	Voice     *botFile  `json:"voice"`
	Animation *botFile  `json:"animation"`
	Sticker   *botFile  `json:"sticker"`
}

// convert maps a Bot API message into the shape the user identity returns, so a caller
// reading one chat as the bot and another as the account gets the same fields in the
// same places.
func (m *botMessage) convert() Message {
	message := Message{
		ID:     m.MessageID,
		ChatID: m.Chat.ID,
		Date:   time.Unix(m.Date, 0).UTC(),
		Text:   m.Text,
	}

	if message.Text == "" {
		message.Text = m.Caption
	}

	if m.EditDate != 0 {
		at := time.Unix(m.EditDate, 0).UTC()
		message.Edited = &at
	}

	switch {
	case m.From != nil:
		message.Author = m.From.author()
	case m.SenderChat != nil:
		chat := m.SenderChat.chat()
		message.Author = Author{ID: chat.ID, Kind: chat.Kind, Name: chat.Title, Username: chat.Username}
	}

	if m.ReplyToMessage != nil {
		message.ReplyToMessageID = m.ReplyToMessage.MessageID
	}

	message.Attachments = m.attachments()

	return message
}

// attachments describes what hangs off the message. Photos come as a list of sizes of
// one image, so the largest stands for the photo.
func (m *botMessage) attachments() []Attachment {
	var attachments []Attachment

	for _, file := range []struct {
		file *botFile
		kind string
	}{
		{m.Document, KindDocument},
		{m.Video, KindVideo},
		{m.Audio, KindAudio},
		{m.Voice, KindVoice},
		{m.Animation, KindAnimation},
		{m.Sticker, KindSticker},
	} {
		if file.file != nil {
			attachments = append(attachments, file.file.attachment(file.kind))
		}
	}

	if largest, ok := largestBotPhoto(m.Photo); ok {
		attachments = append(attachments, largest.attachment(KindPhoto))
	}

	return attachments
}

func largestBotPhoto(sizes []botFile) (botFile, bool) {
	var (
		best  botFile
		found bool
	)

	for _, size := range sizes {
		if !found || size.FileSize >= best.FileSize {
			best, found = size, true
		}
	}

	return best, found
}

type botUpdate struct {
	UpdateID      int         `json:"update_id"`
	Message       *botMessage `json:"message"`
	EditedMessage *botMessage `json:"edited_message"`
	ChannelPost   *botMessage `json:"channel_post"`
}

// message picks whichever kind of message this update carries. Updates that carry none
// (a poll answer, a reaction) are skipped by the caller.
func (u botUpdate) message() *botMessage {
	switch {
	case u.Message != nil:
		return u.Message
	case u.EditedMessage != nil:
		return u.EditedMessage
	case u.ChannelPost != nil:
		return u.ChannelPost
	default:
		return nil
	}
}
