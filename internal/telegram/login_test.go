package telegram

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func terminal(input string) (*terminalAuth, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return &terminalAuth{in: bufio.NewReader(strings.NewReader(input)), out: out}, out
}

// fakeAuth answers like Telegram, badly, on purpose: the point of this flow is what happens
// when a person mistypes.
type fakeAuth struct {
	sent tg.AuthSentCodeClass
	// sendErr is returned by SendCode.
	sendErr error
	// code that finally works, and how the wrong ones are refused.
	code          string
	codeErr       error
	needsPassword bool
	// password that finally works.
	password string
	signUp   bool

	codes     []string
	passwords []string
}

func (f *fakeAuth) SendCode(context.Context, string, auth.SendCodeOptions) (tg.AuthSentCodeClass, error) {
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	if f.sent != nil {
		return f.sent, nil
	}
	return &tg.AuthSentCode{Type: &tg.AuthSentCodeTypeApp{}, PhoneCodeHash: "hash"}, nil
}

func (f *fakeAuth) SignIn(_ context.Context, _, code, _ string) (*tg.AuthAuthorization, error) {
	f.codes = append(f.codes, code)

	if f.signUp {
		return nil, &auth.SignUpRequired{}
	}
	if code != f.code {
		if f.codeErr != nil {
			return nil, f.codeErr
		}
		return nil, tgerr.New(400, "PHONE_CODE_INVALID")
	}
	if f.needsPassword {
		return nil, auth.ErrPasswordAuthNeeded
	}
	return &tg.AuthAuthorization{}, nil
}

func (f *fakeAuth) Password(_ context.Context, password string) (*tg.AuthAuthorization, error) {
	f.passwords = append(f.passwords, password)

	if password != f.password {
		return nil, auth.ErrPasswordInvalid
	}
	return &tg.AuthAuthorization{}, nil
}

func TestSignInStraightThrough(t *testing.T) {
	client := &fakeAuth{code: "12345"}
	prompt, out := terminal("+37400000000\n12345\n")

	if err := signIn(context.Background(), client, prompt, out); err != nil {
		t.Fatalf("signIn: %v", err)
	}

	// Where the code went matters: the operator has to know which app to look in.
	if !strings.Contains(out.String(), "in the Telegram app") {
		t.Errorf("the prompt does not say where the code went: %q", out.String())
	}
}

// A mistyped code used to cost a whole new round, because the ready-made flow gave up and
// Telegram then answered AUTH_RESTART.
func TestSignInAsksForTheCodeAgain(t *testing.T) {
	client := &fakeAuth{code: "12345"}
	prompt, out := terminal("+37400000000\n11111\n12345\n")

	if err := signIn(context.Background(), client, prompt, out); err != nil {
		t.Fatalf("signIn: %v", err)
	}

	if len(client.codes) != 2 {
		t.Errorf("%d codes were tried, want two", len(client.codes))
	}
	if !strings.Contains(out.String(), "that code was refused") {
		t.Errorf("nothing was said about the wrong code: %q", out.String())
	}
}

func TestSignInGivesUpOnTheCodeEventually(t *testing.T) {
	client := &fakeAuth{code: "12345"}
	prompt, out := terminal("+37400000000\n1\n2\n3\n4\n")

	if err := signIn(context.Background(), client, prompt, out); err == nil {
		t.Fatal("a wrong code was accepted")
	}

	if len(client.codes) != codeAttempts {
		t.Errorf("%d code attempts, want %d", len(client.codes), codeAttempts)
	}
}

// The case that actually happened: the cloud password was mistyped. It must cost one
// attempt, not the whole login.
func TestSignInAsksForThePasswordAgain(t *testing.T) {
	client := &fakeAuth{code: "12345", needsPassword: true, password: "right one"}
	prompt, out := terminal("+37400000000\n12345\nwrong one\nright one\n")

	if err := signIn(context.Background(), client, prompt, out); err != nil {
		t.Fatalf("signIn: %v", err)
	}

	if len(client.passwords) != 2 {
		t.Errorf("%d passwords were tried, want two", len(client.passwords))
	}
	if !strings.Contains(out.String(), "that password was refused") {
		t.Errorf("nothing was said about the wrong password: %q", out.String())
	}
	// The password itself must not reach the output: a terminal keeps what it prints.
	if strings.Contains(out.String(), "right one") || strings.Contains(out.String(), "wrong one") {
		t.Errorf("the password leaked into the output: %q", out.String())
	}
}

func TestSignInGivesUpOnThePasswordEventually(t *testing.T) {
	client := &fakeAuth{code: "12345", needsPassword: true, password: "right one"}
	prompt, out := terminal("+37400000000\n12345\na\nb\nc\nd\n")

	err := signIn(context.Background(), client, prompt, out)
	if err == nil {
		t.Fatal("a wrong password was accepted")
	}
	if !strings.Contains(err.Error(), "attempts are used up") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
	if len(client.passwords) != passwordAttempts {
		t.Errorf("%d password attempts, want %d", len(client.passwords), passwordAttempts)
	}
}

func TestSignInOnAnExpiredCode(t *testing.T) {
	client := &fakeAuth{code: "12345", codeErr: tgerr.New(400, "PHONE_CODE_EXPIRED")}
	prompt, out := terminal("+37400000000\n11111\n")

	err := signIn(context.Background(), client, prompt, out)
	if err == nil {
		t.Fatal("an expired code was accepted")
	}
	if !strings.Contains(err.Error(), "again") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
}

// This command signs an existing account in. Creating an account on someone's phone number
// is not something a server should be able to do.
func TestSignInNeverSignsUp(t *testing.T) {
	client := &fakeAuth{code: "12345", signUp: true}
	prompt, out := terminal("+37400000000\n12345\n")

	err := signIn(context.Background(), client, prompt, out)
	if err == nil {
		t.Fatal("signing up was allowed")
	}
	if !strings.Contains(err.Error(), "does not create one") {
		t.Errorf("the refusal is worded unhelpfully: %v", err)
	}
}

// AUTH_RESTART is Telegram saying the previous attempt is still in the way. It has to reach
// the caller as itself, because that is what triggers setting the session aside.
func TestSignInPassesAuthRestartOn(t *testing.T) {
	client := &fakeAuth{sendErr: tgerr.New(500, "AUTH_RESTART")}
	prompt, out := terminal("+37400000000\n")

	err := signIn(context.Background(), client, prompt, out)
	if !tgerr.Is(err, "AUTH_RESTART") {
		t.Errorf("the error lost its type: %v", err)
	}
}

func TestSignInWithoutACodeAtAll(t *testing.T) {
	client := &fakeAuth{sent: &tg.AuthSentCodeSuccess{}}
	prompt, out := terminal("+37400000000\n")

	if err := signIn(context.Background(), client, prompt, out); err != nil {
		t.Fatalf("signIn: %v", err)
	}
	if !strings.Contains(out.String(), "without a code") {
		t.Errorf("the output does not explain that no code is coming: %q", out.String())
	}
}

func TestSignInStopsWhenNothingIsEntered(t *testing.T) {
	client := &fakeAuth{code: "12345"}

	prompt, out := terminal("\n")
	if err := signIn(context.Background(), client, prompt, out); err == nil {
		t.Error("an empty phone number was accepted")
	}

	prompt, out = terminal("+37400000000\n")
	if err := signIn(context.Background(), client, prompt, out); err == nil {
		t.Error("an empty code was accepted")
	}
}

// An unauthorised session file is worth nothing, but it is still moved aside rather than
// deleted: a file nobody can read beats a file nobody can find.
func TestResetSessionMovesTheFileAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, SessionFile)
	if err := os.WriteFile(path, []byte("a key Telegram will not take any more"), 0o600); err != nil {
		t.Fatalf("preparing the file: %v", err)
	}

	moved, err := resetSession(dir)
	if err != nil {
		t.Fatalf("resetSession: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the session file is still in place")
	}
	if _, err := os.Stat(moved); err != nil {
		t.Errorf("the moved file is not there: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(moved), SessionFile) {
		t.Errorf("moved to %q, the name should start with the old one", moved)
	}
}

func TestResetSessionWhenThereIsNothingToReset(t *testing.T) {
	moved, err := resetSession(t.TempDir())
	if err != nil || moved != "" {
		t.Errorf("resetSession on an empty directory gave (%q, %v)", moved, err)
	}
}

func TestLoginNeedsASessionDirectory(t *testing.T) {
	err := Login(context.Background(), UserOptions{APIID: 1, APIHash: "hash"},
		strings.NewReader(""), &bytes.Buffer{})
	if err == nil {
		t.Error("a login was started with nowhere to save the session")
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
			t.Errorf("codeKind(%T) = %q, want %q", c.kind, got, c.want)
		}
	}
}

func TestPasswordErrorFromTelegramIsNotSwallowed(t *testing.T) {
	client := &failingPassword{}
	prompt, out := terminal("a password\n")

	err := password(context.Background(), client, prompt, out)
	if err == nil || !strings.Contains(err.Error(), "network") {
		t.Errorf("the error from Telegram was swallowed: %v", err)
	}
}

type failingPassword struct{ fakeAuth }

func (f *failingPassword) Password(context.Context, string) (*tg.AuthAuthorization, error) {
	return nil, errors.New("the network went away")
}
