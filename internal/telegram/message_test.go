package telegram

import (
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

func testEntities() *Entities {
	return NewEntities(
		[]tg.UserClass{
			&tg.User{ID: 42, FirstName: "Anna", LastName: "Petrova", Username: "annap"},
			&tg.User{ID: 43, FirstName: "helper", Bot: true, Username: "helper_bot"},
			&tg.User{ID: 44},
			&tg.UserEmpty{ID: 45},
		},
		[]tg.ChatClass{
			&tg.Chat{ID: 100, Title: "small group"},
			&tg.Channel{ID: 200, Title: "accounting", Username: "acc"},
			&tg.ChatEmpty{ID: 300},
		},
	)
}

func TestAuthorNaming(t *testing.T) {
	entities := testEntities()

	cases := []struct {
		name     string
		peer     tg.PeerClass
		wantID   int64
		wantKind string
		wantName string
		wantBot  bool
	}{
		{"a person", &tg.PeerUser{UserID: 42}, 42, "user", "Anna Petrova", false},
		{"a bot", &tg.PeerUser{UserID: 43}, 43, "user", "helper", true},
		{"a person with no name at all", &tg.PeerUser{UserID: 44}, 44, "user", "", false},
		{"someone not in the entity list", &tg.PeerUser{UserID: 999}, 999, "user", "", false},
		{"a small group", &tg.PeerChat{ChatID: 100}, 100, "chat", "small group", false},
		{"a channel", &tg.PeerChannel{ChannelID: 200}, 200, "channel", "accounting", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			author := entities.Author(c.peer)

			if author.ID != c.wantID || author.Kind != c.wantKind {
				t.Errorf("author is %+v", author)
			}
			if author.Name != c.wantName {
				t.Errorf("name is %q, want %q", author.Name, c.wantName)
			}
			if author.Bot != c.wantBot {
				t.Errorf("bot is %v, want %v", author.Bot, c.wantBot)
			}
		})
	}

	if author := entities.Author(&tg.PeerUser{}); author.ID != 0 {
		t.Errorf("an empty peer produced %+v", author)
	}
}

// A user with neither first nor last name falls back to the username, because a message
// from a blank author reads as a broken read.
func TestUserNameFallsBackToTheUsername(t *testing.T) {
	if got := userName(&tg.User{ID: 1, Username: "nameless"}); got != "nameless" {
		t.Errorf("name is %q, want the username", got)
	}
}

func TestConvertMessageCarriesEveryFieldACallerAsksFor(t *testing.T) {
	raw := &tg.Message{
		ID:      512,
		Date:    1770000000,
		Message: "the invoice for January is attached",
		Out:     true,
		PeerID:  &tg.PeerChannel{ChannelID: 200},
	}
	raw.SetFromID(&tg.PeerUser{UserID: 42})
	raw.SetEditDate(1770000100)

	reply := &tg.MessageReplyHeader{}
	reply.SetReplyToMsgID(511)
	raw.SetReplyTo(reply)

	raw.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{
		ID:       9,
		MimeType: "application/pdf",
		Size:     4096,
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: "invoice-january.pdf"},
		},
	}})

	message, ok := ConvertMessage(raw, ChannelChatID(200), testEntities())
	if !ok {
		t.Fatal("a message was dropped")
	}

	if message.ID != 512 {
		t.Errorf("id is %d", message.ID)
	}
	if message.ChatID != ChannelChatID(200) {
		t.Errorf("chat is %d", message.ChatID)
	}
	if want := time.Unix(1770000000, 0).UTC(); !message.Date.Equal(want) {
		t.Errorf("date is %s, want %s", message.Date, want)
	}
	if message.Edited == nil || !message.Edited.Equal(time.Unix(1770000100, 0).UTC()) {
		t.Errorf("edit date is %v", message.Edited)
	}
	if message.Author.Name != "Anna Petrova" {
		t.Errorf("author is %+v", message.Author)
	}
	if message.Text != "the invoice for January is attached" {
		t.Errorf("text is %q", message.Text)
	}
	if message.ReplyToMessageID != 511 {
		t.Errorf("reply is %d", message.ReplyToMessageID)
	}
	if !message.Outgoing {
		t.Error("an outgoing message is reported as incoming")
	}
	if len(message.Attachments) != 1 {
		t.Fatalf("attachments are %+v", message.Attachments)
	}

	attachment := message.Attachments[0]
	if attachment.FileName != "invoice-january.pdf" || attachment.MimeType != "application/pdf" ||
		attachment.Size != 4096 || attachment.Kind != KindDocument {
		t.Errorf("the attachment is %+v", attachment)
	}
}

// A message with no from_id is a private chat or a channel post: the chat itself is the
// author, and leaving the author blank would lose who wrote it.
func TestConvertMessageWithoutASenderUsesThePeer(t *testing.T) {
	raw := &tg.Message{ID: 1, Date: 1, Message: "hello", PeerID: &tg.PeerUser{UserID: 42}}

	message, ok := ConvertMessage(raw, UserChatID(42), testEntities())
	if !ok {
		t.Fatal("a message was dropped")
	}

	if message.Author.ID != 42 || message.Author.Name != "Anna Petrova" {
		t.Errorf("author is %+v", message.Author)
	}
}

func TestConvertMessagesSkipsDeletedOnes(t *testing.T) {
	raw := []tg.MessageClass{
		&tg.Message{ID: 1, Date: 1, Message: "still here"},
		&tg.MessageEmpty{ID: 2},
		&tg.MessageService{ID: 3, Date: 2, Action: &tg.MessageActionChatAddUser{}},
	}

	messages := ConvertMessages(raw, -100, testEntities())

	if len(messages) != 2 {
		t.Fatalf("got %d messages, want the deleted one dropped", len(messages))
	}
	if messages[1].Service != "chat_add_user" {
		t.Errorf("the service message is described as %q", messages[1].Service)
	}
	if messages[1].Text != "" {
		t.Errorf("a service message came back with text %q", messages[1].Text)
	}
}

func TestServiceMessageKeepsItsAuthor(t *testing.T) {
	raw := &tg.MessageService{ID: 3, Date: 2, Action: &tg.MessageActionChatJoinedByLink{}}
	raw.SetFromID(&tg.PeerUser{UserID: 42})

	message, ok := ConvertMessage(raw, -100, testEntities())
	if !ok {
		t.Fatal("a service message was dropped")
	}
	if message.Author.ID != 42 {
		t.Errorf("author is %+v", message.Author)
	}
	if message.Service != "chat_joined_by_link" {
		t.Errorf("service is %q", message.Service)
	}
}

func TestMediaKinds(t *testing.T) {
	cases := []struct {
		name     string
		media    tg.MessageMediaClass
		wantKind string
		wantOK   bool
	}{
		{"a photo", &tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 7}}, KindPhoto, true},
		{"a plain file", document(&tg.DocumentAttributeFilename{FileName: "a.pdf"}), KindDocument, true},
		{"a video", document(&tg.DocumentAttributeVideo{}), KindVideo, true},
		{"a voice message", document(&tg.DocumentAttributeAudio{Voice: true}), KindVoice, true},
		{"music", document(&tg.DocumentAttributeAudio{}), KindAudio, true},
		{"a sticker", document(&tg.DocumentAttributeSticker{}), KindSticker, true},
		{"an animation", document(&tg.DocumentAttributeAnimated{}), KindAnimation, true},
		{"a link preview", &tg.MessageMediaWebPage{}, KindWebPage, true},
		{"a location", &tg.MessageMediaGeo{}, KindGeo, true},
		{"a venue", &tg.MessageMediaVenue{}, KindGeo, true},
		{"a contact", &tg.MessageMediaContact{}, KindContact, true},
		{"a poll", &tg.MessageMediaPoll{}, KindPoll, true},
		{"nothing", &tg.MessageMediaEmpty{}, "", false},
		{"something this server has no name for", &tg.MessageMediaGame{}, KindOther, true},
		{"a photo that is gone", &tg.MessageMediaPhoto{}, KindPhoto, true},
		{"a document that is gone", &tg.MessageMediaDocument{}, KindDocument, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			attachment, ok := ConvertMedia(c.media)

			if ok != c.wantOK {
				t.Fatalf("ok is %v, want %v", ok, c.wantOK)
			}
			if ok && attachment.Kind != c.wantKind {
				t.Errorf("kind is %q, want %q", attachment.Kind, c.wantKind)
			}
		})
	}
}

func TestUnsupportedMessageTypeIsDropped(t *testing.T) {
	if _, ok := ConvertMessage(&tg.MessageEmpty{ID: 1}, -100, testEntities()); ok {
		t.Error("an empty message was converted")
	}
}

func document(attributes ...tg.DocumentAttributeClass) tg.MessageMediaClass {
	return &tg.MessageMediaDocument{Document: &tg.Document{ID: 1, Attributes: attributes}}
}
