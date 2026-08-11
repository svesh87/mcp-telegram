package telegram

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
)

// Sent describes what was sent, so a caller can quote the identifier back.
type Sent struct {
	ChatID    int64 `json:"chat_id"`
	MessageID int   `json:"message_id,omitempty"`
}

// SendMessage sends text to a chat as the account.
//
// Whether this may be called at all is decided before it: the write tools are only
// registered when the server was started with writing enabled, and the chat is checked
// against the write list. This function performs, it does not authorise.
func (u *UserClient) SendMessage(ctx context.Context, chatID int64, text string, replyTo int) (Sent, error) {
	if err := u.Wait(ctx); err != nil {
		return Sent{}, err
	}

	if text == "" {
		return Sent{}, errors.New("refusing to send an empty message")
	}

	peer, err := u.resolve(ctx, chatID)
	if err != nil {
		return Sent{}, err
	}

	builder := message.NewSender(u.raw).To(peer).CloneBuilder()
	if replyTo > 0 {
		builder = builder.Reply(replyTo)
	}

	updates, err := builder.Text(ctx, text)
	if err != nil {
		return Sent{}, fmt.Errorf("sending a message to chat %d: %w", chatID, err)
	}

	return Sent{ChatID: chatID, MessageID: SentMessageID(updates)}, nil
}

// SendFile sends a local file to a chat as the account.
func (u *UserClient) SendFile(ctx context.Context, chatID int64, path, caption string) (Sent, error) {
	if err := u.Wait(ctx); err != nil {
		return Sent{}, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return Sent{}, fmt.Errorf("reading %s: %w", path, err)
	}
	if info.IsDir() {
		return Sent{}, fmt.Errorf("%s is a directory", path)
	}

	peer, err := u.resolve(ctx, chatID)
	if err != nil {
		return Sent{}, err
	}

	upload, err := uploader.NewUploader(u.raw).FromPath(ctx, path)
	if err != nil {
		return Sent{}, fmt.Errorf("uploading %s: %w", path, err)
	}

	name := filepath.Base(path)
	document := message.UploadedDocument(upload).Filename(name)
	if caption != "" {
		document = message.UploadedDocument(upload, styling.Plain(caption)).Filename(name)
	}

	updates, err := message.NewSender(u.raw).To(peer).Media(ctx, document)
	if err != nil {
		return Sent{}, fmt.Errorf("sending %s to chat %d: %w", path, chatID, err)
	}

	return Sent{ChatID: chatID, MessageID: SentMessageID(updates)}, nil
}

// SentMessageID digs the identifier of the new message out of the update batch
// Telegram answers a send with. Zero means the batch did not carry one, which is not an
// error: the message went out either way.
func SentMessageID(updates tg.UpdatesClass) int {
	switch u := updates.(type) {
	case *tg.UpdateShortSentMessage:
		return u.ID
	case *tg.Updates:
		return firstMessageID(u.Updates)
	case *tg.UpdatesCombined:
		return firstMessageID(u.Updates)
	default:
		return 0
	}
}

func firstMessageID(updates []tg.UpdateClass) int {
	for _, update := range updates {
		switch u := update.(type) {
		case *tg.UpdateMessageID:
			return u.ID
		case *tg.UpdateNewMessage:
			if message, ok := u.Message.(*tg.Message); ok {
				return message.ID
			}
		case *tg.UpdateNewChannelMessage:
			if message, ok := u.Message.(*tg.Message); ok {
				return message.ID
			}
		}
	}

	return 0
}
