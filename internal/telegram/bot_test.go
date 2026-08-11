package telegram

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testBotToken = "424242:not-a-real-token"

// botServer answers like the Bot API and records what it was asked.
type botServer struct {
	t        *testing.T
	handlers map[string]func(*http.Request) (any, bool, string)
	paths    []string
	files    map[string][]byte
}

func newBotServer(t *testing.T) (*botServer, *BotClient) {
	t.Helper()

	fake := &botServer{
		t:        t,
		handlers: map[string]func(*http.Request) (any, bool, string){},
		files:    map[string][]byte{},
	}

	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	client, err := NewBot(BotOptions{Token: testBotToken, BaseURL: server.URL, HTTP: server.Client()})
	if err != nil {
		t.Fatalf("NewBot: %v", err)
	}

	return fake, client
}

func (b *botServer) answer(method string, result any) {
	b.handlers[method] = func(*http.Request) (any, bool, string) { return result, true, "" }
}

func (b *botServer) refuse(method, description string) {
	b.handlers[method] = func(*http.Request) (any, bool, string) { return nil, false, description }
}

func (b *botServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.paths = append(b.paths, r.URL.Path)

	if content, ok := b.files[r.URL.Path]; ok {
		_, _ = w.Write(content)
		return
	}

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	method := parts[len(parts)-1]

	handler, ok := b.handlers[method]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":404,"description":"method not found"}`))
		return
	}

	result, success, description := handler(r)

	envelope := map[string]any{"ok": success}
	if success {
		envelope["result"] = result
	} else {
		envelope["description"] = description
		envelope["error_code"] = 400
		w.WriteHeader(http.StatusBadRequest)
	}

	if err := json.NewEncoder(w).Encode(envelope); err != nil {
		b.t.Fatalf("answering %s: %v", method, err)
	}
}

func TestBotNeedsAToken(t *testing.T) {
	if _, err := NewBot(BotOptions{}); err == nil {
		t.Error("a bot client was built without a token")
	}
}

func TestBotMe(t *testing.T) {
	fake, client := newBotServer(t)
	fake.answer("getMe", map[string]any{
		"id": 555, "is_bot": true, "first_name": "helper", "username": "helper_bot",
	})

	me, err := client.Me(context.Background())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}

	if me.ID != 555 || me.Title != "helper" || me.Username != "helper_bot" {
		t.Errorf("the bot is described as %+v", me)
	}
}

func TestBotChatInfo(t *testing.T) {
	fake, client := newBotServer(t)
	fake.answer("getChat", map[string]any{
		"id": 424242, "type": "private", "first_name": "Pavel",
	})

	chat, err := client.ChatInfo(context.Background(), 424242)
	if err != nil {
		t.Fatalf("ChatInfo: %v", err)
	}

	if chat.Kind != ChatKindUser || chat.Title != "Pavel" {
		t.Errorf("the chat is described as %+v", chat)
	}
}

func TestBotUpdatesCarryEveryKindOfMessage(t *testing.T) {
	fake, client := newBotServer(t)
	fake.answer("getUpdates", []map[string]any{
		{
			"update_id": 10,
			"message": map[string]any{
				"message_id": 1,
				"date":       1770000000,
				"chat":       map[string]any{"id": 424242, "type": "private", "first_name": "Pavel"},
				"from":       map[string]any{"id": 424242, "first_name": "Pavel"},
				"text":       "hello",
			},
		},
		{
			"update_id": 11,
			"edited_message": map[string]any{
				"message_id": 2,
				"date":       1770000000,
				"edit_date":  1770000100,
				"chat":       map[string]any{"id": 424242, "type": "private"},
				"caption":    "the invoice",
				"document": map[string]any{
					"file_id": "abc", "file_name": "invoice.pdf",
					"mime_type": "application/pdf", "file_size": 4096,
				},
			},
		},
		{
			"update_id":   12,
			"poll_answer": map[string]any{"poll_id": "1"},
		},
	})

	messages, err := client.Updates(context.Background(), 5, 10)
	if err != nil {
		t.Fatalf("Updates: %v", err)
	}

	if len(messages) != 2 {
		t.Fatalf("got %d messages, want the update without a message dropped", len(messages))
	}

	if messages[0].Text != "hello" || messages[0].UpdateID != 10 || messages[0].ChatID != 424242 {
		t.Errorf("the first message is %+v", messages[0])
	}
	if messages[0].Author.Name != "Pavel" {
		t.Errorf("the author is %+v", messages[0].Author)
	}

	// A caption stands in for the text: a file with a note on it is not a message with no
	// text.
	second := messages[1]
	if second.Text != "the invoice" {
		t.Errorf("the caption did not become the text: %+v", second)
	}
	if second.Edited == nil {
		t.Error("the edit date is missing")
	}
	if len(second.Attachments) != 1 || second.Attachments[0].FileName != "invoice.pdf" ||
		second.Attachments[0].FileID != "abc" || second.Attachments[0].Kind != KindDocument {
		t.Errorf("the attachment is %+v", second.Attachments)
	}
}

func TestBotSendMessage(t *testing.T) {
	fake, client := newBotServer(t)

	var body map[string]any
	fake.handlers["sendMessage"] = func(r *http.Request) (any, bool, string) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("reading the request: %v", err)
		}
		return map[string]any{"message_id": 77}, true, ""
	}

	sent, err := client.SendMessage(context.Background(), 424242, "done", 76)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if sent.MessageID != 77 || sent.ChatID != 424242 {
		t.Errorf("sent is %+v", sent)
	}
	if body["text"] != "done" {
		t.Errorf("the text that went out is %v", body["text"])
	}
	if body["reply_to_message_id"] != float64(76) {
		t.Errorf("the reply is %v", body["reply_to_message_id"])
	}
}

func TestBotRefusesAnEmptyMessage(t *testing.T) {
	_, client := newBotServer(t)

	if _, err := client.SendMessage(context.Background(), 424242, "", 0); err == nil {
		t.Error("an empty message was sent")
	}
}

func TestBotSendFile(t *testing.T) {
	fake, client := newBotServer(t)

	path := filepath.Join(t.TempDir(), "invoice.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.4 pretend"), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	var (
		gotName    string
		gotCaption string
		gotContent []byte
	)
	fake.handlers["sendDocument"] = func(r *http.Request) (any, bool, string) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("the request is not multipart: %v", err)
		}

		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("reading a part: %v", err)
			}

			content, _ := io.ReadAll(part)
			switch part.FormName() {
			case "document":
				gotName, gotContent = part.FileName(), content
			case "caption":
				gotCaption = string(content)
			}
		}

		return map[string]any{"message_id": 78}, true, ""
	}

	sent, err := client.SendFile(context.Background(), 424242, path, "for January")
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}

	if sent.MessageID != 78 {
		t.Errorf("sent is %+v", sent)
	}
	if gotName != "invoice.pdf" || string(gotContent) != "%PDF-1.4 pretend" {
		t.Errorf("the file arrived as %q with %q", gotName, gotContent)
	}
	if gotCaption != "for January" {
		t.Errorf("the caption arrived as %q", gotCaption)
	}
}

func TestBotSendFileOfSomethingThatIsNotThere(t *testing.T) {
	_, client := newBotServer(t)

	if _, err := client.SendFile(context.Background(), 424242, filepath.Join(t.TempDir(), "nope"), ""); err == nil {
		t.Error("a missing file was sent")
	}
}

func TestBotDownload(t *testing.T) {
	fake, client := newBotServer(t)
	fake.answer("getFile", map[string]any{"file_id": "abc", "file_path": "documents/invoice.pdf"})
	fake.files["/file/bot"+testBotToken+"/documents/invoice.pdf"] = []byte("%PDF-1.4 pretend")

	dir := t.TempDir()
	saved, err := client.Download(context.Background(), "abc", dir)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	if saved.Path != filepath.Join(dir, "invoice.pdf") {
		t.Errorf("the file landed at %q", saved.Path)
	}
	if saved.Size != int64(len("%PDF-1.4 pretend")) {
		t.Errorf("size is %d", saved.Size)
	}

	content, err := os.ReadFile(saved.Path)
	if err != nil {
		t.Fatalf("reading the saved file: %v", err)
	}
	if string(content) != "%PDF-1.4 pretend" {
		t.Errorf("the saved file holds %q", content)
	}
}

func TestBotDownloadWithoutAPath(t *testing.T) {
	fake, client := newBotServer(t)
	fake.answer("getFile", map[string]any{"file_id": "abc"})

	if _, err := client.Download(context.Background(), "abc", t.TempDir()); err == nil {
		t.Error("a file with no path was downloaded")
	}
}

// The token sits in the request path, so an error must never quote a URL: these errors
// end up in an agent's transcript.
func TestBotErrorsNeverCarryTheToken(t *testing.T) {
	fake, client := newBotServer(t)
	fake.refuse("getChat", "Bad Request: chat not found")

	_, err := client.ChatInfo(context.Background(), 1)
	if err == nil {
		t.Fatal("a refusal was reported as success")
	}
	if !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("the error does not say what Telegram said: %v", err)
	}
	if strings.Contains(err.Error(), testBotToken) {
		t.Errorf("the error carries the bot token: %v", err)
	}

	// The same for a transport failure, where the URL is the obvious thing to include.
	broken, err := NewBot(BotOptions{Token: testBotToken, BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewBot: %v", err)
	}
	if _, err := broken.Me(context.Background()); err == nil {
		t.Error("a call to nowhere succeeded")
	} else if strings.Contains(err.Error(), testBotToken) {
		t.Errorf("the error carries the bot token: %v", err)
	}
}

func TestBotHandlesAnAnswerThatIsNotWhatItClaims(t *testing.T) {
	fake, client := newBotServer(t)

	// A method the fake does not know answers 404 with a description.
	if _, err := client.Me(context.Background()); err == nil {
		t.Error("a 404 was reported as success")
	}

	// A result of the wrong shape.
	fake.answer("getMe", "not an object")
	if _, err := client.Me(context.Background()); err == nil {
		t.Error("a result of the wrong shape was accepted")
	}
}
