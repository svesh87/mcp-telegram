package telegram

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

func terminal(input string) (*terminalAuth, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return &terminalAuth{in: bufio.NewReader(strings.NewReader(input)), out: out}, out
}

func TestLoginAsksForThePhoneAndTheCode(t *testing.T) {
	auth, out := terminal("+37400000000\n12345\n")

	phone, err := auth.Phone(context.Background())
	if err != nil {
		t.Fatalf("Phone: %v", err)
	}
	if phone != "+37400000000" {
		t.Errorf("the phone number arrived as %q", phone)
	}

	code, err := auth.Code(context.Background(), &tg.AuthSentCode{Type: &tg.AuthSentCodeTypeApp{}})
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	if code != "12345" {
		t.Errorf("the code arrived as %q", code)
	}

	// Where the code went matters: the operator has to know which app to look in.
	if !strings.Contains(out.String(), "in the Telegram app") {
		t.Errorf("the prompt does not say where the code went: %q", out.String())
	}
}

func TestLoginRefusesEmptyAnswers(t *testing.T) {
	auth, _ := terminal("\n")
	if _, err := auth.Phone(context.Background()); err == nil {
		t.Error("an empty phone number was accepted")
	}

	auth, _ = terminal("")
	if _, err := auth.Phone(context.Background()); err == nil {
		t.Error("no answer at all was accepted")
	}
}

// This command signs an existing account in. Creating an account is not something a
// server should be able to do on the owner's phone number.
func TestLoginNeverSignsUp(t *testing.T) {
	auth, _ := terminal("")

	if _, err := auth.SignUp(context.Background()); err == nil {
		t.Error("SignUp was allowed")
	}
}

func TestLoginSendsTermsOfServiceBackToAClient(t *testing.T) {
	auth, out := terminal("")

	err := auth.AcceptTermsOfService(context.Background(), tg.HelpTermsOfService{Text: "the terms"})
	if err == nil {
		t.Error("the terms of service were accepted on the owner's behalf")
	}
	if !strings.Contains(out.String(), "the terms") {
		t.Errorf("the terms were not shown: %q", out.String())
	}
}

func TestCodeKind(t *testing.T) {
	cases := []struct {
		kind tg.AuthSentCodeTypeClass
		want string
	}{
		{&tg.AuthSentCodeTypeApp{}, "in the Telegram app"},
		{&tg.AuthSentCodeTypeSMS{}, "by SMS"},
		{&tg.AuthSentCodeTypeCall{}, "by phone call"},
		{&tg.AuthSentCodeTypeFlashCall{}, "as the last digits of a calling number"},
		{&tg.AuthSentCodeTypeMissedCall{}, "as the last digits of a calling number"},
		{&tg.AuthSentCodeTypeEmailCode{}, "by email"},
		{&tg.AuthSentCodeTypeFragmentSMS{}, "through Fragment"},
		{&tg.AuthSentCodeTypeFirebaseSMS{}, "somewhere Telegram chose"},
	}

	for _, c := range cases {
		if got := codeKind(c.kind); got != c.want {
			t.Errorf("codeKind(%T) is %q, want %q", c.kind, got, c.want)
		}
	}
}

func TestLoginNeedsASessionDirectory(t *testing.T) {
	err := Login(context.Background(), UserOptions{APIID: 1, APIHash: "hash"},
		strings.NewReader(""), &bytes.Buffer{})
	if err == nil {
		t.Error("a login was attempted with nowhere to save the session")
	}
}
