package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

const testPhone = "+37400000000"

// webLogin builds a sign-in over the fake Telegram, with a clock the test moves.
func webLogin(client *fakeAuth) (*WebLogin, *[]*tg.User, *time.Time) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var signed []*tg.User

	login := &WebLogin{
		client:   func() (authClient, error) { return client, nil },
		signedIn: func(user *tg.User) { signed = append(signed, user) },
		account:  func() Account { return Account{CanSignIn: true} },
		now:      func() time.Time { return now },
		// Seventeen bytes repeated, so consecutive sixteen-byte identifiers differ.
		random: strings.NewReader(strings.Repeat("0123456789abcdefg", 64)),
	}

	return login, &signed, &now
}

func start(t *testing.T, login *WebLogin) LoginStep {
	t.Helper()

	step, err := login.Start(context.Background(), " "+testPhone+" ")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if step.Stage != StageCode || step.Flow == "" {
		t.Fatalf("Start gave %+v, want the code stage with a flow", step)
	}
	return step
}

func TestWebLoginStraightThrough(t *testing.T) {
	client := &fakeAuth{code: "12345", user: &tg.User{ID: 7, FirstName: "Ann"}}
	login, signed, _ := webLogin(client)

	step := start(t, login)
	if step.CodeKind != "in the Telegram app" {
		t.Errorf("the page is not told where the code went: %q", step.CodeKind)
	}

	done, err := login.Code(context.Background(), step.Flow, " 12345 ")
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	if done.Stage != StageDone || done.Flow != "" {
		t.Errorf("Code gave %+v, want done and no flow", done)
	}

	if len(*signed) != 1 || (*signed)[0].ID != 7 {
		t.Errorf("the client was told %v, want the user with id 7", *signed)
	}
	// The phone number reached Telegram as typed, trimmed, and is gone with the attempt.
	if len(client.phones) != 1 || client.phones[0] != testPhone {
		t.Errorf("SignIn got the phone numbers %q", client.phones)
	}
	if login.flow != nil {
		t.Error("the finished attempt is still held, phone number and all")
	}
}

func TestWebLoginAsksForTheCodeAgain(t *testing.T) {
	client := &fakeAuth{code: "12345"}
	login, signed, _ := webLogin(client)
	step := start(t, login)

	again, err := login.Code(context.Background(), step.Flow, "11111")
	if err != nil {
		t.Fatalf("a wrong code ended the attempt: %v", err)
	}
	if again.Stage != StageCode || again.Flow != step.Flow || !strings.Contains(again.Notice, "attempts left: 2") {
		t.Errorf("after a wrong code: %+v", again)
	}

	// An empty form costs nothing.
	if empty, err := login.Code(context.Background(), step.Flow, " "); err != nil || empty.Notice == "" {
		t.Errorf("an empty code gave (%+v, %v)", empty, err)
	}

	if done, err := login.Code(context.Background(), step.Flow, "12345"); err != nil || done.Stage != StageDone {
		t.Fatalf("the right code after a wrong one: (%+v, %v)", done, err)
	}
	if len(client.codes) != 2 || len(*signed) != 1 {
		t.Errorf("%d codes reached Telegram and %d sign-ins were reported", len(client.codes), len(*signed))
	}
}

func TestWebLoginGivesUpOnTheCodeEventually(t *testing.T) {
	client := &fakeAuth{code: "12345"}
	login, signed, _ := webLogin(client)
	step := start(t, login)

	var err error
	for i := 0; i < codeAttempts && err == nil; i++ {
		_, err = login.Code(context.Background(), step.Flow, "1")
	}
	if err == nil || !strings.Contains(err.Error(), "used up") {
		t.Fatalf("after %d wrong codes: %v", codeAttempts, err)
	}
	if len(client.codes) != codeAttempts || len(*signed) != 0 {
		t.Errorf("%d codes tried, %d sign-ins", len(client.codes), len(*signed))
	}
	if _, err := login.Code(context.Background(), step.Flow, "12345"); err == nil {
		t.Error("the attempt went on after the codes ran out")
	}
}

func TestWebLoginWithTwoFactorPassword(t *testing.T) {
	client := &fakeAuth{code: "12345", needsPassword: true, password: "right one", user: &tg.User{ID: 9}}
	login, signed, _ := webLogin(client)
	step := start(t, login)

	next, err := login.Code(context.Background(), step.Flow, "12345")
	if err != nil || next.Stage != StagePassword || next.Flow != step.Flow {
		t.Fatalf("SESSION_PASSWORD_NEEDED did not lead to the password form: (%+v, %v)", next, err)
	}

	// A code form replayed now is out of step.
	if _, err := login.Code(context.Background(), step.Flow, "12345"); err == nil {
		t.Error("a code was taken at the password stage")
	}

	wrong, err := login.Password(context.Background(), step.Flow, "wrong one")
	if err != nil || wrong.Stage != StagePassword || !strings.Contains(wrong.Notice, "attempts left") {
		t.Fatalf("a wrong password: (%+v, %v)", wrong, err)
	}
	if strings.Contains(wrong.Notice, "wrong one") {
		t.Error("the password came back in the notice")
	}

	if empty, err := login.Password(context.Background(), step.Flow, ""); err != nil || empty.Notice == "" {
		t.Errorf("an empty password gave (%+v, %v)", empty, err)
	}

	done, err := login.Password(context.Background(), step.Flow, "right one")
	if err != nil || done.Stage != StageDone {
		t.Fatalf("the right password: (%+v, %v)", done, err)
	}
	if len(*signed) != 1 || (*signed)[0].ID != 9 {
		t.Errorf("signed in as %v", *signed)
	}
}

func TestWebLoginGivesUpOnThePasswordEventually(t *testing.T) {
	client := &fakeAuth{code: "12345", needsPassword: true, password: "right one"}
	login, _, _ := webLogin(client)
	step := start(t, login)
	if _, err := login.Code(context.Background(), step.Flow, "12345"); err != nil {
		t.Fatalf("Code: %v", err)
	}

	var err error
	for i := 0; i < passwordAttempts && err == nil; i++ {
		_, err = login.Password(context.Background(), step.Flow, "nope")
	}
	if err == nil || !strings.Contains(err.Error(), "used up") {
		t.Errorf("after %d wrong passwords: %v", passwordAttempts, err)
	}
	if login.flow != nil {
		t.Error("the spent attempt is still held")
	}
}

func TestWebLoginPasswordErrorFromTelegram(t *testing.T) {
	client := &failingPassword{fakeAuth{code: "12345", needsPassword: true}}
	login, _, _ := webLogin(&client.fakeAuth)
	login.client = func() (authClient, error) { return client, nil }

	step := start(t, login)
	if _, err := login.Code(context.Background(), step.Flow, "12345"); err != nil {
		t.Fatalf("Code: %v", err)
	}
	if _, err := login.Password(context.Background(), step.Flow, "x"); err == nil ||
		!strings.Contains(err.Error(), "network") {
		t.Errorf("the error from Telegram was swallowed: %v", err)
	}
}

// A form only works for the attempt it was made for, and only for a while.
func TestWebLoginFlowIsBoundAndShortLived(t *testing.T) {
	login, _, now := webLogin(&fakeAuth{code: "12345"})
	step := start(t, login)

	if _, err := login.Code(context.Background(), "someone else's flow", "12345"); err == nil {
		t.Error("a code was taken for a flow nobody started")
	}
	if _, err := login.Code(context.Background(), "", "12345"); err == nil {
		t.Error("a code was taken without a flow")
	}
	if _, err := login.Password(context.Background(), step.Flow, "x"); err == nil {
		t.Error("a password was taken at the code stage")
	}

	*now = now.Add(loginFlowTTL + time.Second)
	if _, err := login.Code(context.Background(), step.Flow, "12345"); err == nil ||
		!strings.Contains(err.Error(), "expired") {
		t.Errorf("an expired attempt went on: %v", err)
	}
	if login.flow != nil {
		t.Error("the expired attempt, phone number and all, is still held")
	}
}

func TestWebLoginNewAttemptReplacesTheOld(t *testing.T) {
	login, _, _ := webLogin(&fakeAuth{code: "12345"})
	first := start(t, login)
	second := start(t, login)

	if first.Flow == second.Flow {
		t.Fatal("two attempts got the same identifier")
	}
	if _, err := login.Code(context.Background(), first.Flow, "12345"); err == nil {
		t.Error("the replaced attempt still works")
	}
}

func TestWebLoginRefusals(t *testing.T) {
	t.Run("empty phone", func(t *testing.T) {
		login, _, _ := webLogin(&fakeAuth{})
		if _, err := login.Start(context.Background(), "  "); err == nil {
			t.Error("an empty phone number was sent")
		}
	})

	t.Run("not ready", func(t *testing.T) {
		login, _, _ := webLogin(&fakeAuth{})
		login.client = func() (authClient, error) { return nil, errors.New("still connecting") }
		if _, err := login.Start(context.Background(), testPhone); err == nil {
			t.Error("a sign-in started without a connection")
		}
	})

	t.Run("connection gone between forms", func(t *testing.T) {
		login, _, _ := webLogin(&fakeAuth{code: "12345"})
		step := start(t, login)
		login.client = func() (authClient, error) { return nil, errors.New("stopped") }
		if _, err := login.Code(context.Background(), step.Flow, "12345"); err == nil {
			t.Error("a code went nowhere and nobody said so")
		}
	})

	t.Run("expired code", func(t *testing.T) {
		login, _, _ := webLogin(&fakeAuth{code: "12345", codeErr: tgerr.New(400, "PHONE_CODE_EXPIRED")})
		step := start(t, login)
		if _, err := login.Code(context.Background(), step.Flow, "1"); err == nil ||
			!strings.Contains(err.Error(), "expired") {
			t.Errorf("an expired code: %v", err)
		}
	})

	t.Run("never signs up", func(t *testing.T) {
		login, signed, _ := webLogin(&fakeAuth{code: "12345", signUp: true})
		step := start(t, login)
		if _, err := login.Code(context.Background(), step.Flow, "12345"); err == nil ||
			!strings.Contains(err.Error(), "does not create one") {
			t.Errorf("signing up: %v", err)
		}
		if len(*signed) != 0 {
			t.Error("a sign-up was reported as a sign-in")
		}
	})

	t.Run("auth restart", func(t *testing.T) {
		login, _, _ := webLogin(&fakeAuth{sendErr: tgerr.New(500, "AUTH_RESTART")})
		_, err := login.Start(context.Background(), testPhone)
		if !tgerr.Is(err, "AUTH_RESTART") || !strings.Contains(err.Error(), "mcp-telegram login") {
			t.Errorf("AUTH_RESTART is not explained: %v", err)
		}
	})

	t.Run("other send error", func(t *testing.T) {
		login, _, _ := webLogin(&fakeAuth{sendErr: tgerr.New(400, "PHONE_NUMBER_INVALID")})
		_, err := login.Start(context.Background(), testPhone)
		if err == nil || strings.Contains(err.Error(), testPhone) {
			t.Errorf("a refused number: %v", err)
		}
	})

	t.Run("bad random source", func(t *testing.T) {
		login, _, _ := webLogin(&fakeAuth{})
		login.random = strings.NewReader("")
		if _, err := login.Start(context.Background(), testPhone); err == nil {
			t.Error("an attempt started without an identifier")
		}
	})
}

func TestWebLoginWithoutACodeAtAll(t *testing.T) {
	user := &tg.User{ID: 3}
	login, signed, _ := webLogin(&fakeAuth{sent: &tg.AuthSentCodeSuccess{
		Authorization: &tg.AuthAuthorization{User: user},
	}})

	step, err := login.Start(context.Background(), testPhone)
	if err != nil || step.Stage != StageDone {
		t.Fatalf("sentCodeSuccess: (%+v, %v)", step, err)
	}
	if len(*signed) != 1 || (*signed)[0] != user {
		t.Errorf("signed in as %v", *signed)
	}

	login, _, _ = webLogin(&fakeAuth{sent: &tg.AuthSentCodeSuccess{
		Authorization: &tg.AuthAuthorizationSignUpRequired{},
	}})
	if _, err := login.Start(context.Background(), testPhone); err == nil {
		t.Error("a sign-up was taken for a sign-in")
	}

	login, _, _ = webLogin(&fakeAuth{sent: &tg.AuthSentCodePaymentRequired{}})
	if _, err := login.Start(context.Background(), testPhone); err == nil {
		t.Error("an answer nobody understands was taken for success")
	}
}

// Telegram says "sent to the app" and nothing arrives: the page offers the other way it
// named, and the code then signs in against the new hash.
func TestWebLoginResendsTheCodeAnotherWay(t *testing.T) {
	client := &fakeAuth{
		code: "12345",
		sent: &tg.AuthSentCode{Type: &tg.AuthSentCodeTypeApp{}, PhoneCodeHash: "hash", NextType: &tg.AuthCodeTypeSMS{}},
		resent: &tg.AuthSentCode{Type: &tg.AuthSentCodeTypeSMS{}, PhoneCodeHash: "hash-sms",
			NextType: &tg.AuthCodeTypeCall{}},
	}
	login, signed, _ := webLogin(client)

	step := start(t, login)
	if step.NextKind != "by SMS" {
		t.Fatalf("the page is not told it can ask for an SMS: %+v", step)
	}

	again, err := login.Resend(context.Background(), step.Flow)
	if err != nil {
		t.Fatalf("Resend: %v", err)
	}
	if again.Stage != StageCode || again.Flow != step.Flow || again.CodeKind != "by SMS" || again.NextKind != "by phone call" {
		t.Errorf("after the resend: %+v", again)
	}
	if client.resends != 1 || client.resentHashes[0] != "hash" {
		t.Errorf("resend went out %d times with %v", client.resends, client.resentHashes)
	}

	if _, err := login.Code(context.Background(), again.Flow, "12345"); err != nil {
		t.Fatalf("Code after the resend: %v", err)
	}
	if len(*signed) != 1 {
		t.Error("the resent code did not sign in")
	}
}

func TestWebLoginResendRefusals(t *testing.T) {
	sent := &tg.AuthSentCode{Type: &tg.AuthSentCodeTypeApp{}, PhoneCodeHash: "hash", NextType: &tg.AuthCodeTypeSMS{}}

	// Nothing else offered: no call to Telegram, a notice, the attempt stays.
	plain := &fakeAuth{}
	login, _, _ := webLogin(plain)
	step := start(t, login)
	if step.NextKind != "" {
		t.Fatalf("NextKind %q without a next type", step.NextKind)
	}
	again, err := login.Resend(context.Background(), step.Flow)
	if err != nil || again.Notice == "" || again.Flow != step.Flow || plain.resends != 0 {
		t.Errorf("resend without a next type: %+v, %v, %d calls", again, err, plain.resends)
	}

	// Telegram has nothing left: a notice, the button goes away, the code still works.
	unavailable := &fakeAuth{sent: sent, resendErr: tgerr.New(400, "SEND_CODE_UNAVAILABLE")}
	login, _, _ = webLogin(unavailable)
	step = start(t, login)
	again, err = login.Resend(context.Background(), step.Flow)
	if err != nil || again.Notice == "" || again.NextKind != "" || again.Flow != step.Flow {
		t.Errorf("SEND_CODE_UNAVAILABLE: %+v, %v", again, err)
	}

	// Too soon: a notice with the wait, the attempt stays.
	flood := &fakeAuth{sent: sent, resendErr: tgerr.New(420, "FLOOD_WAIT_30")}
	login, _, _ = webLogin(flood)
	step = start(t, login)
	again, err = login.Resend(context.Background(), step.Flow)
	if err != nil || !strings.Contains(again.Notice, "30") || again.Flow != step.Flow {
		t.Errorf("FLOOD_WAIT: %+v, %v", again, err)
	}

	// Expired: the attempt is over.
	expired := &fakeAuth{sent: sent, resendErr: tgerr.New(400, "PHONE_CODE_EXPIRED")}
	login, _, _ = webLogin(expired)
	step = start(t, login)
	if _, err := login.Resend(context.Background(), step.Flow); err == nil {
		t.Error("an expired code was not reported")
	}
	if _, err := login.Code(context.Background(), step.Flow, "1"); err == nil {
		t.Error("the attempt outlived an expired code")
	}

	// A stale form gets nowhere.
	login, _, _ = webLogin(&fakeAuth{sent: sent})
	start(t, login)
	if _, err := login.Resend(context.Background(), "not-the-flow"); err == nil {
		t.Error("a resend from another attempt was accepted")
	}
}

func TestWebLoginAccount(t *testing.T) {
	login, _, _ := webLogin(&fakeAuth{})
	if !login.Account().CanSignIn {
		t.Error("Account is not passed through")
	}
}

// The refactored verdicts must read Telegram the way the login command always did.
func TestVerdicts(t *testing.T) {
	if !codeRefused(tgerr.New(400, "PHONE_CODE_INVALID")) || codeRefused(errors.New("x")) {
		t.Error("codeRefused")
	}
	if !codeExpired(tgerr.New(400, "PHONE_CODE_EXPIRED")) || codeExpired(errors.New("x")) {
		t.Error("codeExpired")
	}
	if !passwordRefused(auth.ErrPasswordInvalid) || !passwordRefused(tgerr.New(400, "PASSWORD_HASH_INVALID")) ||
		passwordRefused(errors.New("x")) {
		t.Error("passwordRefused")
	}
	if err := signInFailure(errors.New("boom")); !strings.HasPrefix(err.Error(), "sign in: ") {
		t.Errorf("signInFailure = %v", err)
	}
}
