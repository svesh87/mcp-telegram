package access

import (
	"strings"
	"testing"
)

// The lists used across the tests. Identifiers are invented: negative values look
// like Telegram supergroups, positive ones like private chats, and none of them
// belong to a real chat.
var testLists = Lists{
	UserRead:  []int64{-1001, -1002},
	UserWrite: []int64{-1003},
	BotRead:   []int64{-2001},
	BotWrite:  []int64{-2002},
}

// The whole point of four lists: every identity and mode pair is decided separately,
// and a chat in one list grants nothing in another.
func TestMatrix(t *testing.T) {
	checker := New(testLists)

	cases := []struct {
		name     string
		identity Identity
		mode     Mode
		chat     int64
		want     bool
	}{
		{"user reads its read list", User, Read, -1001, true},
		{"user reads its second read entry", User, Read, -1002, true},
		{"user cannot write to a read-only chat", User, Write, -1001, false},
		{"user writes to its write list", User, Write, -1003, true},
		{"write implies read", User, Read, -1003, true},
		{"bot reads its own list", Bot, Read, -2001, true},
		{"bot cannot write to its read-only chat", Bot, Write, -2001, false},
		{"bot writes to its write list", Bot, Write, -2002, true},
		{"bot cannot read the user's chats", Bot, Read, -1001, false},
		{"user cannot read the bot's chats", User, Read, -2001, false},
		{"unknown chat is closed for the user", User, Read, -9999, false},
		{"unknown chat is closed for the bot", Bot, Read, -9999, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := checker.Allowed(c.identity, c.mode, c.chat); got != c.want {
				t.Errorf("Allowed(%s, %s, %d) = %v, want %v", c.identity, c.mode, c.chat, got, c.want)
			}
		})
	}
}

// A refusal has to say which of the two situations it is, or the caller cannot tell a
// missing chat from a chat it may only read.
func TestCheckExplainsRefusals(t *testing.T) {
	checker := New(testLists)

	if err := checker.Check(User, Read, -1001); err != nil {
		t.Errorf("allowed operation returned %v", err)
	}

	err := checker.Check(User, Write, -1001)
	if err == nil {
		t.Fatal("writing to a read-only chat was allowed")
	}
	if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("refusal does not mention that the chat is read-only: %v", err)
	}

	err = checker.Check(User, Read, -9999)
	if err == nil {
		t.Fatal("reading a chat outside every list was allowed")
	}
	if !strings.Contains(err.Error(), "not in any") {
		t.Errorf("refusal does not mention that the chat is in no list: %v", err)
	}
}

func TestChatsListsWhatIsAllowed(t *testing.T) {
	checker := New(testLists)

	// Sorted, and with the write entry present because write implies read.
	got := checker.Chats(User, Read)
	want := []int64{-1003, -1002, -1001}
	if len(got) != len(want) {
		t.Fatalf("Chats(user, read) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Chats(user, read) = %v, want %v", got, want)
		}
	}
}

func TestEmptyIdentityIsDetected(t *testing.T) {
	checker := New(Lists{UserRead: []int64{-1001}})

	if checker.Empty(User) {
		t.Error("user has a read list but reads as empty")
	}
	if !checker.Empty(Bot) {
		t.Error("bot has no lists at all but does not read as empty")
	}
}

func TestParseList(t *testing.T) {
	cases := []struct {
		raw     string
		want    []int64
		wantErr bool
	}{
		{"", nil, false},
		{"-1001", []int64{-1001}, false},
		{"-1001,-1002", []int64{-1001, -1002}, false},
		{" -1001 , -1002 ", []int64{-1001, -1002}, false},
		{"-1001,,-1002", []int64{-1001, -1002}, false},
		{"123456789", []int64{123456789}, false},
		// Names are refused on purpose: a chat title is renamed by whoever owns the
		// chat, so a list of names is a list somebody else can widen.
		{"@some_group", nil, true},
		{"-1001,@some_group", nil, true},
		{"not-a-number", nil, true},
	}

	for _, c := range cases {
		got, err := ParseList(c.raw)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseList(%q) accepted an invalid list", c.raw)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseList(%q) returned %v", c.raw, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("ParseList(%q) = %v, want %v", c.raw, got, c.want)
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("ParseList(%q) = %v, want %v", c.raw, got, c.want)
				break
			}
		}
	}
}

// The message for a rejected name must explain why, since "expected a number" alone
// looks like a bug in the caller's formatting.
func TestParseListRefusesNamesLoudly(t *testing.T) {
	_, err := ParseList("@some_group")
	if err == nil {
		t.Fatal("a chat name was accepted")
	}
	if !strings.Contains(err.Error(), "names") {
		t.Errorf("error does not explain that names are refused: %v", err)
	}
}
