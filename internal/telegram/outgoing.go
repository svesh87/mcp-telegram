package telegram

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// AlbumLimit is Telegram's cap on one media group. A caller with more files sends more
// than one album.
const AlbumLimit = 10

// OutgoingFile is a file on its way out, given either as a path on the machine running
// this server or as bytes the caller carried in.
//
// Both exist for a reason. A path is what an operator has at hand; bytes are what a
// script has, and a script does not have to hand its documents to a container to send
// them.
type OutgoingFile struct {
	// Name is the file name Telegram will show. Required with Content, optional with
	// Path.
	Name string
	// Path is a file on the machine running this server.
	Path string
	// Content is the file itself.
	Content []byte
}

// Bytes returns the content and the name to send it under.
func (f OutgoingFile) Bytes() ([]byte, string, error) {
	if err := f.validate(); err != nil {
		return nil, "", err
	}

	if f.Path == "" {
		return f.Content, SafeName(f.Name), nil
	}

	info, err := os.Stat(f.Path)
	if err != nil {
		return nil, "", fmt.Errorf("reading %s: %w", f.Path, err)
	}
	if info.IsDir() {
		return nil, "", fmt.Errorf("%s is a directory", f.Path)
	}

	content, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, "", fmt.Errorf("reading %s: %w", f.Path, err)
	}

	name := f.Name
	if name == "" {
		name = filepath.Base(f.Path)
	}

	return content, SafeName(name), nil
}

func (f OutgoingFile) validate() error {
	switch {
	case f.Path != "" && f.Content != nil:
		return errors.New("a file is given both as a path and as content: pick one")
	case f.Path == "" && f.Content == nil:
		return errors.New("a file needs either a path or content")
	case f.Path == "" && f.Name == "":
		return errors.New("a file given as content needs a name")
	default:
		return nil
	}
}
