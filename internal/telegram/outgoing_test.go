package telegram

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOutgoingFileFromDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invoice.pdf")
	if err := os.WriteFile(path, []byte("%PDF"), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	content, name, err := OutgoingFile{Path: path}.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if string(content) != "%PDF" || name != "invoice.pdf" {
		t.Errorf("the file came out as %q named %q", content, name)
	}

	// An explicit name wins over the one on disk.
	_, name, err = OutgoingFile{Path: path, Name: "January.pdf"}.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if name != "January.pdf" {
		t.Errorf("the name is %q", name)
	}
}

func TestOutgoingFileFromContent(t *testing.T) {
	content, name, err := OutgoingFile{Name: "../invoice.pdf", Content: []byte("%PDF")}.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if string(content) != "%PDF" {
		t.Errorf("the content is %q", content)
	}
	// The name came from a caller, so it is stripped the same way an incoming one is.
	if name != "invoice.pdf" {
		t.Errorf("the name is %q", name)
	}
}

func TestOutgoingFileRefusals(t *testing.T) {
	cases := map[string]OutgoingFile{
		"nothing at all":           {},
		"content with no name":     {Content: []byte("%PDF")},
		"a path and content":       {Path: "/tmp/a.pdf", Content: []byte("%PDF")},
		"a path that is not there": {Path: filepath.Join(t.TempDir(), "nope")},
		"a directory":              {Path: t.TempDir()},
	}

	for name, file := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := file.Bytes(); err == nil {
				t.Error("it was accepted")
			}
		})
	}
}
