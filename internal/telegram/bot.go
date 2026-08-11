package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// BotAPI is the public Bot API endpoint. It is a field rather than a constant use so
// tests can answer instead of Telegram.
const BotAPI = "https://api.telegram.org"

// botTimeout bounds a single Bot API call. Uploads of large files are the slowest thing
// here, and a call that hangs longer than this is a call nobody is waiting for any more.
const botTimeout = 5 * time.Minute

// BotOptions configures the bot identity.
type BotOptions struct {
	Token   string
	BaseURL string
	HTTP    *http.Client
}

// BotClient reaches Telegram as a bot over the Bot API.
//
// A bot is a different actor from the account, not a lesser one: it sees only the chats
// it was added to, but inside them it can write. That is why it carries its own two
// access lists.
type BotClient struct {
	token string
	base  string
	http  *http.Client
}

// NewBot builds the client. Nothing is called until a tool asks for something.
func NewBot(opts BotOptions) (*BotClient, error) {
	if opts.Token == "" {
		return nil, errors.New("bot token is required for the bot identity")
	}

	base := opts.BaseURL
	if base == "" {
		base = BotAPI
	}

	httpClient := opts.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: botTimeout}
	}

	return &BotClient{token: opts.Token, base: base, http: httpClient}, nil
}

// Me names the bot itself, which is the cheapest proof that the token works.
func (b *BotClient) Me(ctx context.Context) (Chat, error) {
	var result botUser
	if err := b.call(ctx, "getMe", nil, &result); err != nil {
		return Chat{}, err
	}

	return Chat{
		ID:       result.ID,
		Kind:     ChatKindUser,
		Title:    result.name(),
		Username: result.Username,
	}, nil
}

// ChatInfo names a chat the bot is in.
func (b *BotClient) ChatInfo(ctx context.Context, chatID int64) (Chat, error) {
	var result botChat
	if err := b.call(ctx, "getChat", map[string]any{"chat_id": chatID}, &result); err != nil {
		return Chat{}, err
	}

	return result.chat(), nil
}

// Updates reads messages sent to the bot.
//
// This is the whole of what a bot may read: the Bot API has no history, so a bot sees a
// message only while it sits in the update queue. Reading with an offset acknowledges
// everything before it, and Telegram then drops those updates for good, which is why
// the offset is the caller's to pass and not this server's to guess.
func (b *BotClient) Updates(ctx context.Context, offset, limit int) ([]Message, error) {
	payload := map[string]any{}
	if offset != 0 {
		payload["offset"] = offset
	}
	if limit > 0 {
		payload["limit"] = limit
	}

	var result []botUpdate
	if err := b.call(ctx, "getUpdates", payload, &result); err != nil {
		return nil, err
	}

	messages := make([]Message, 0, len(result))
	for _, update := range result {
		message := update.message()
		if message == nil {
			continue
		}

		converted := message.convert()
		converted.UpdateID = update.UpdateID
		messages = append(messages, converted)
	}

	return messages, nil
}

// SendMessage sends text as the bot.
func (b *BotClient) SendMessage(ctx context.Context, chatID int64, text string, replyTo int) (Sent, error) {
	if text == "" {
		return Sent{}, errors.New("refusing to send an empty message")
	}

	payload := map[string]any{"chat_id": chatID, "text": text}
	if replyTo > 0 {
		payload["reply_to_message_id"] = replyTo
	}

	var result botMessage
	if err := b.call(ctx, "sendMessage", payload, &result); err != nil {
		return Sent{}, err
	}

	return Sent{ChatID: chatID, MessageID: result.MessageID}, nil
}

// SendFile sends a local file as the bot.
func (b *BotClient) SendFile(ctx context.Context, chatID int64, path, caption string) (Sent, error) {
	file, err := os.Open(path)
	if err != nil {
		return Sent{}, fmt.Errorf("reading %s: %w", path, err)
	}
	defer file.Close()

	body := &bytes.Buffer{}
	form := multipart.NewWriter(body)

	fields := map[string]string{"chat_id": strconv.FormatInt(chatID, 10)}
	if caption != "" {
		fields["caption"] = caption
	}
	for name, value := range fields {
		if err := form.WriteField(name, value); err != nil {
			return Sent{}, fmt.Errorf("building the request: %w", err)
		}
	}

	part, err := form.CreateFormFile("document", filepath.Base(path))
	if err != nil {
		return Sent{}, fmt.Errorf("building the request: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return Sent{}, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := form.Close(); err != nil {
		return Sent{}, fmt.Errorf("building the request: %w", err)
	}

	var result botMessage
	if err := b.post(ctx, "sendDocument", form.FormDataContentType(), body, &result); err != nil {
		return Sent{}, err
	}

	return Sent{ChatID: chatID, MessageID: result.MessageID}, nil
}

// Download saves a file the bot can see. Files are addressed by the identifier that
// came with the message, because the Bot API has no other way back to them.
func (b *BotClient) Download(ctx context.Context, fileID, dir string) (SavedFile, error) {
	var file botFile
	if err := b.call(ctx, "getFile", map[string]any{"file_id": fileID}, &file); err != nil {
		return SavedFile{}, err
	}
	if file.FilePath == "" {
		return SavedFile{}, errors.New("Telegram gave no path for this file")
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, b.fileURL(file.FilePath), nil)
	if err != nil {
		return SavedFile{}, fmt.Errorf("building the download request: %w", err)
	}

	response, err := b.http.Do(request)
	if err != nil {
		// The URL carries the token, so only the failure is reported, never the request.
		return SavedFile{}, errors.New("downloading the file failed")
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return SavedFile{}, fmt.Errorf("downloading the file failed with status %d", response.StatusCode)
	}

	path := filepath.Join(dir, SafeName(filepath.Base(file.FilePath)))
	out, err := os.Create(path)
	if err != nil {
		return SavedFile{}, fmt.Errorf("creating %s: %w", path, err)
	}
	defer out.Close()

	written, err := io.Copy(out, response.Body)
	if err != nil {
		return SavedFile{}, fmt.Errorf("writing %s: %w", path, err)
	}

	return SavedFile{Path: path, Size: written}, nil
}

// call sends a JSON request and unpacks the result.
func (b *BotClient) call(ctx context.Context, method string, payload any, out any) error {
	body := &bytes.Buffer{}
	if payload != nil {
		if err := json.NewEncoder(body).Encode(payload); err != nil {
			return fmt.Errorf("building the %s request: %w", method, err)
		}
	}

	return b.post(ctx, method, "application/json", body, out)
}

// post is where every Bot API call ends up.
//
// The bot token sits in the request path, so no error here may quote a URL: an error
// with a URL in it ends up in an agent's transcript, and the token with it.
func (b *BotClient) post(ctx context.Context, method, contentType string, body io.Reader, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, b.methodURL(method), body)
	if err != nil {
		return fmt.Errorf("building the %s request: %w", method, err)
	}
	request.Header.Set("Content-Type", contentType)

	response, err := b.http.Do(request)
	if err != nil {
		return fmt.Errorf("calling %s failed: the Bot API did not answer", method)
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBotResponse))
	if err != nil {
		return fmt.Errorf("reading the %s answer: %w", method, err)
	}

	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		ErrorCode   int             `json:"error_code"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("the %s answer is not the JSON the Bot API documents (status %d)",
			method, response.StatusCode)
	}

	if !envelope.OK {
		if envelope.Description != "" {
			return fmt.Errorf("%s: %s (error %d)", method, envelope.Description, envelope.ErrorCode)
		}
		return fmt.Errorf("%s failed with status %d", method, response.StatusCode)
	}

	if out == nil {
		return nil
	}

	if err := json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("the %s result is not the shape this server expects: %w", method, err)
	}

	return nil
}

// maxBotResponse caps how much of an answer is read. Bot API answers are small; a
// gigabyte of them is a broken endpoint, not an answer.
const maxBotResponse = 32 << 20

func (b *BotClient) methodURL(method string) string {
	return b.base + "/bot" + url.PathEscape(b.token) + "/" + method
}

func (b *BotClient) fileURL(path string) string {
	return b.base + "/file/bot" + url.PathEscape(b.token) + "/" + path
}
