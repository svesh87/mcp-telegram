package telegram

import (
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

// A file name comes from whoever sent the file. Nothing in it may decide where on disk
// this server writes.
func TestSafeNameStopsANameFromChoosingAPath(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"invoice.pdf", "invoice.pdf"},
		{"../../etc/passwd", "passwd"},
		{"/etc/shadow", "shadow"},
		{"справка.pdf", "_______.pdf"},
		{"report 2026;rm -rf.txt", "report_2026_rm_-rf.txt"},
		{"", "file"},
		{"...", "file"},
		{"   spaced.txt   ", "spaced.txt"},
		{"..", "file"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SafeName(c.name)

			if got != c.want {
				t.Errorf("SafeName(%q) is %q, want %q", c.name, got, c.want)
			}
			if strings.ContainsAny(got, "/\\") {
				t.Errorf("SafeName(%q) kept a path separator: %q", c.name, got)
			}
		})
	}
}

func TestDownloadNameCarriesTheChatAndMessage(t *testing.T) {
	if got := DownloadName(-1001111111111, 512, "invoice.pdf"); got != "-1001111111111_512_invoice.pdf" {
		t.Errorf("name is %q", got)
	}
}

func TestFileLocationOfADocument(t *testing.T) {
	media := &tg.MessageMediaDocument{Document: &tg.Document{
		ID:            9,
		AccessHash:    777,
		FileReference: []byte{1, 2, 3},
		Size:          4096,
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: "invoice.pdf"},
		},
	}}

	location, name, size, err := FileLocation(media)
	if err != nil {
		t.Fatalf("FileLocation: %v", err)
	}

	document, ok := location.(*tg.InputDocumentFileLocation)
	if !ok {
		t.Fatalf("location is %T", location)
	}
	if document.ID != 9 || document.AccessHash != 777 || len(document.FileReference) != 3 {
		t.Errorf("location is %+v", document)
	}
	if name != "invoice.pdf" || size != 4096 {
		t.Errorf("name is %q and size is %d", name, size)
	}
}

// A photo has several sizes of the same image. A caller asking for the file wants the
// file, so the largest one is taken.
func TestFileLocationOfAPhotoTakesTheLargestSize(t *testing.T) {
	media := &tg.MessageMediaPhoto{Photo: &tg.Photo{
		ID:         7,
		AccessHash: 888,
		Sizes: []tg.PhotoSizeClass{
			&tg.PhotoSizeEmpty{Type: "a"},
			&tg.PhotoSize{Type: "m", W: 320, H: 320},
			&tg.PhotoSizeProgressive{Type: "y", W: 1280, H: 1280},
			&tg.PhotoSize{Type: "s", W: 100, H: 100},
		},
	}}

	location, _, _, err := FileLocation(media)
	if err != nil {
		t.Fatalf("FileLocation: %v", err)
	}

	photo, ok := location.(*tg.InputPhotoFileLocation)
	if !ok {
		t.Fatalf("location is %T", location)
	}
	if photo.ThumbSize != "y" {
		t.Errorf("size %q was chosen, want the largest one", photo.ThumbSize)
	}
}

func TestFileLocationRefusesWhatCannotBeDownloaded(t *testing.T) {
	cases := []tg.MessageMediaClass{
		&tg.MessageMediaPoll{},
		&tg.MessageMediaDocument{},
		&tg.MessageMediaPhoto{},
		&tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 1}},
	}

	for _, media := range cases {
		if _, _, _, err := FileLocation(media); err == nil {
			t.Errorf("%T was accepted as a downloadable file", media)
		}
	}
}

func TestReverseTurnsTelegramOrderIntoReadingOrder(t *testing.T) {
	messages := []Message{{ID: 3}, {ID: 2}, {ID: 1}}

	Reverse(messages)

	for i, want := range []int{1, 2, 3} {
		if messages[i].ID != want {
			t.Fatalf("order is %v", messages)
		}
	}

	Reverse(nil)
}

func TestExtensionFor(t *testing.T) {
	if got := ExtensionFor("application/pdf"); got != ".pdf" {
		t.Errorf("extension of a pdf is %q", got)
	}
	if got := ExtensionFor("application/x-nothing-at-all"); got != "" {
		t.Errorf("an unknown type produced %q", got)
	}
}
