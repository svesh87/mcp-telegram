package telegram

import (
	"errors"
	"fmt"
	"mime"
	"path/filepath"
	"strings"

	"github.com/gotd/td/tg"
)

// SavedFile is where a downloaded attachment landed.
type SavedFile struct {
	Path string `json:"path"`
	Size int64  `json:"size,omitempty"`
}

// FileLocation picks the download location out of a message's media, along with the
// name the sender gave the file and its size.
//
// Photos have no name and several sizes; the largest is taken, because a caller asking
// for the file wants the file and not a thumbnail.
func FileLocation(media tg.MessageMediaClass) (tg.InputFileLocationClass, string, int64, error) {
	switch m := media.(type) {
	case *tg.MessageMediaDocument:
		document, ok := m.Document.(*tg.Document)
		if !ok {
			return nil, "", 0, errors.New("the attached document is no longer available")
		}

		return &tg.InputDocumentFileLocation{
			ID:            document.ID,
			AccessHash:    document.AccessHash,
			FileReference: document.FileReference,
		}, documentFileName(document), document.Size, nil

	case *tg.MessageMediaPhoto:
		photo, ok := m.Photo.(*tg.Photo)
		if !ok {
			return nil, "", 0, errors.New("the attached photo is no longer available")
		}

		size, ok := largestPhotoSize(photo.Sizes)
		if !ok {
			return nil, "", 0, errors.New("the attached photo has no size to download")
		}

		return &tg.InputPhotoFileLocation{
			ID:            photo.ID,
			AccessHash:    photo.AccessHash,
			FileReference: photo.FileReference,
			ThumbSize:     size,
		}, "", 0, nil

	default:
		return nil, "", 0, fmt.Errorf("media of type %s cannot be downloaded as a file", media.TypeName())
	}
}

// largestPhotoSize picks the biggest available size by its declared dimensions.
func largestPhotoSize(sizes []tg.PhotoSizeClass) (string, bool) {
	var (
		best  string
		found bool
		area  int
	)

	for _, size := range sizes {
		switch s := size.(type) {
		case *tg.PhotoSize:
			if s.W*s.H >= area {
				area, best, found = s.W*s.H, s.Type, true
			}
		case *tg.PhotoSizeProgressive:
			if s.W*s.H >= area {
				area, best, found = s.W*s.H, s.Type, true
			}
		}
	}

	return best, found
}

// DownloadName builds the name a downloaded file is saved under. The chat and message
// identifiers go in front, so two files of the same name from different messages do not
// collide.
func DownloadName(chatID int64, messageID int, name string) string {
	return fmt.Sprintf("%d_%d_%s", chatID, messageID, SafeName(name))
}

// SafeName strips a name from Telegram down to something safe to write.
//
// The name was chosen by whoever sent the file, so it is cut to its last element and to
// the characters a file name may hold: a message from a chat must not be able to decide
// where on disk this server writes.
func SafeName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))

	var clean strings.Builder
	for _, symbol := range name {
		switch {
		case symbol >= 'a' && symbol <= 'z',
			symbol >= 'A' && symbol <= 'Z',
			symbol >= '0' && symbol <= '9',
			symbol == '.', symbol == '-', symbol == '_':
			clean.WriteRune(symbol)
		default:
			clean.WriteByte('_')
		}
	}

	// Dots and dashes are trimmed off the ends: a leading dot hides the file, and a
	// leading dash reads as an option to whatever picks the file up later. Underscores
	// stay, since they are what a replaced character became.
	name = strings.Trim(clean.String(), ".-")
	if name == "" {
		return "file"
	}

	return name
}

// ExtensionFor guesses a file extension from a MIME type, for files Telegram carries
// without a name.
func ExtensionFor(mimeType string) string {
	extensions, err := mime.ExtensionsByType(mimeType)
	if err != nil || len(extensions) == 0 {
		return ""
	}

	return extensions[0]
}

// Reverse flips a message slice in place, turning Telegram's newest-first order into
// reading order.
func Reverse(messages []Message) {
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
}
