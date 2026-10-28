package telegram

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// The web login is the same sign-in as the login command, split into the requests a
// browser makes: one for the phone number, one for the code, one for the two-factor
// password. It runs on the connection the server already holds, so the account works as
// soon as it is signed in, and no second client ever opens the session file.
//
// What a person types is never logged and never written anywhere. The phone number and
// the code hash live in memory for the length of one attempt, tied to a random flow
// identifier that has nothing to do with the bearer token; the password goes to Telegram
// and is dropped.

// loginFlowTTL bounds one attempt. A code from Telegram lives for minutes, not hours, and
// a phone number has no business sitting in memory longer than that.
const loginFlowTTL = 10 * time.Minute

// LoginStage names the form the login page shows next.
type LoginStage string

// Stages of the web login.
const (
	StagePhone    LoginStage = "phone"
	StageCode     LoginStage = "code"
	StagePassword LoginStage = "password"
	StageDone     LoginStage = "done"
)

// LoginStep is where an attempt stands after a request.
type LoginStep struct {
	Stage LoginStage
	// Flow identifies the attempt in the next form. Empty once the attempt is over.
	Flow string
	// CodeKind says where Telegram sent the code.
	CodeKind string
	// NextKind says where a resent code would go; empty when Telegram offers nothing else.
	NextKind string
	// Notice is a refusal that costs one attempt rather than the whole sign-in.
	Notice string
}

// Account is the state of the user identity in words the login page can show. It never
// carries the phone number.
type Account struct {
	SignedIn bool
	Name     string
	Username string
	// CanSignIn says the connection is up and waiting for a sign-in.
	CanSignIn bool
	// Problem says why signing in is not possible right now.
	Problem string
}

// webFlow is one attempt in progress.
type webFlow struct {
	id       string
	phone    string
	codeHash string
	codeKind string
	nextKind string
	stage    LoginStage
	expires  time.Time

	codeTries     int
	passwordTries int
}

// WebLogin walks a sign-in for the login page.
//
// There is one attempt at a time. Telegram ties a sent code to the connection's key, so two
// attempts in parallel would only invalidate each other; a new one replaces the old.
type WebLogin struct {
	// client hands out the auth client when a sign-in is possible, and says why not
	// otherwise.
	client func() (authClient, error)
	// signedIn is told who signed in.
	signedIn func(*tg.User)
	// account reports the identity's state.
	account func() Account

	now    func() time.Time
	random io.Reader

	// mu is held for the whole of a request, Telegram's answer included: two browser tabs
	// racing each other through one attempt would make a mess of the attempt counts.
	mu   sync.Mutex
	flow *webFlow
}

// WebLogin returns the sign-in for the login page, or nil when the client was built
// without one.
func (u *UserClient) WebLogin() *WebLogin {
	if u.loginNeeded == nil {
		return nil
	}

	return &WebLogin{
		client:   u.authForLogin,
		signedIn: u.markSignedIn,
		account:  u.Account,
		now:      time.Now,
		random:   rand.Reader,
	}
}

// Account says who the session belongs to, or what stands in the way of signing in.
func (u *UserClient) Account() Account {
	u.mu.Lock()
	self := u.self
	u.mu.Unlock()

	if self != nil {
		return Account{SignedIn: true, Name: userName(self), Username: self.Username}
	}

	select {
	case <-u.signedIn:
		// Signed in, though Telegram did not say as whom.
		return Account{SignedIn: true}
	case <-u.done:
		return Account{Problem: u.stopped().Error()}
	default:
	}

	select {
	case <-u.loginNeeded:
		return Account{CanSignIn: true}
	default:
		return Account{Problem: "still connecting to Telegram, reload the page in a moment"}
	}
}

func (u *UserClient) authForLogin() (authClient, error) {
	account := u.Account()
	switch {
	case account.SignedIn:
		return nil, errors.New("this session is already signed in")
	case !account.CanSignIn:
		return nil, errors.New(account.Problem)
	}

	return u.client.Auth(), nil
}

// markSignedIn lets Run carry on past the sign-in. Only the first report counts.
func (u *UserClient) markSignedIn(user *tg.User) {
	u.signOnce.Do(func() {
		u.setSelf(user)
		close(u.signedIn)
	})
}

// Account says who the session belongs to.
func (w *WebLogin) Account() Account { return w.account() }

// Start sends the code to the phone number.
func (w *WebLogin) Start(ctx context.Context, phone string) (LoginStep, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Whatever came before is over: a new code makes the old one useless anyway.
	w.flow = nil

	phone = strings.TrimSpace(phone)
	if phone == "" {
		return LoginStep{}, errors.New("enter the phone number, with the country code")
	}

	client, err := w.client()
	if err != nil {
		return LoginStep{}, err
	}

	sent, err := client.SendCode(ctx, phone, auth.SendCodeOptions{})
	if err != nil {
		return LoginStep{}, sendCodeFailure(err)
	}

	switch sent := sent.(type) {
	case *tg.AuthSentCode:
		id, err := w.newID()
		if err != nil {
			return LoginStep{}, err
		}

		w.flow = &webFlow{
			id:       id,
			phone:    phone,
			codeHash: sent.PhoneCodeHash,
			codeKind: codeKind(sent.Type),
			nextKind: nextCodeKind(sent.NextType),
			stage:    StageCode,
			expires:  w.now().Add(loginFlowTTL),
		}

		return w.step(""), nil

	case *tg.AuthSentCodeSuccess:
		// Telegram accepted a future auth token and asked for nothing.
		authorization, ok := sent.Authorization.(*tg.AuthAuthorization)
		if !ok {
			return LoginStep{}, signInFailure(&auth.SignUpRequired{})
		}
		return w.finish(authorization), nil

	default:
		return LoginStep{}, fmt.Errorf("send code: Telegram answered with %T", sent)
	}
}

// Code checks the code Telegram sent.
func (w *WebLogin) Code(ctx context.Context, flowID, code string) (LoginStep, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	flow, err := w.current(flowID, StageCode)
	if err != nil {
		return LoginStep{}, err
	}

	code = strings.TrimSpace(code)
	if code == "" {
		return w.step("enter the code"), nil
	}

	client, err := w.client()
	if err != nil {
		w.flow = nil
		return LoginStep{}, err
	}

	flow.codeTries++
	authorization, err := client.SignIn(ctx, flow.phone, code, flow.codeHash)

	switch {
	case err == nil:
		return w.finish(authorization), nil

	case errors.Is(err, auth.ErrPasswordAuthNeeded):
		flow.stage = StagePassword
		return w.step(""), nil

	case codeRefused(err) && flow.codeTries < codeAttempts:
		return w.step(fmt.Sprintf("that code was refused, attempts left: %d", codeAttempts-flow.codeTries)), nil

	case codeRefused(err):
		w.flow = nil
		return LoginStep{}, errors.New("the code was refused and the attempts are used up: start over")

	case codeExpired(err):
		w.flow = nil
		return LoginStep{}, errors.New("the code has expired: start over and Telegram will send a new one")

	default:
		w.flow = nil
		return LoginStep{}, signInFailure(err)
	}
}

// Resend asks Telegram to send the code the other way it offered (SMS, a call). Telegram
// answers "sent to the app" even when nothing arrives there, which is what this is for.
func (w *WebLogin) Resend(ctx context.Context, flowID string) (LoginStep, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	flow, err := w.current(flowID, StageCode)
	if err != nil {
		return LoginStep{}, err
	}

	if flow.nextKind == "" {
		return w.step("Telegram offers no other way to send the code"), nil
	}

	client, err := w.client()
	if err != nil {
		w.flow = nil
		return LoginStep{}, err
	}

	sent, err := client.ResendCode(ctx, flow.phone, flow.codeHash)
	if err != nil {
		var rpc *tgerr.Error
		switch {
		case codeExpired(err):
			w.flow = nil
			return LoginStep{}, errors.New("the code has expired: start over and Telegram will send a new one")
		case tgerr.Is(err, "SEND_CODE_UNAVAILABLE"):
			flow.nextKind = ""
			return w.step("Telegram has no other way left to send the code"), nil
		case errors.As(err, &rpc) && rpc.IsType("FLOOD_WAIT"):
			return w.step(fmt.Sprintf("Telegram asks to wait %d s before sending the code again", rpc.Argument)), nil
		default:
			w.flow = nil
			return LoginStep{}, fmt.Errorf("resend code: %w", err)
		}
	}

	resent, ok := sent.(*tg.AuthSentCode)
	if !ok {
		w.flow = nil
		return LoginStep{}, fmt.Errorf("resend code: Telegram answered with %T", sent)
	}

	flow.codeHash = resent.PhoneCodeHash
	flow.codeKind = codeKind(resent.Type)
	flow.nextKind = nextCodeKind(resent.NextType)
	flow.expires = w.now().Add(loginFlowTTL)

	return w.step(""), nil
}

// Password checks the two-factor password. It is passed on and forgotten.
func (w *WebLogin) Password(ctx context.Context, flowID, password string) (LoginStep, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	flow, err := w.current(flowID, StagePassword)
	if err != nil {
		return LoginStep{}, err
	}

	if password == "" {
		return w.step("enter the two-factor password"), nil
	}

	client, err := w.client()
	if err != nil {
		w.flow = nil
		return LoginStep{}, err
	}

	flow.passwordTries++
	authorization, err := client.Password(ctx, password)

	switch {
	case err == nil:
		return w.finish(authorization), nil

	case passwordRefused(err) && flow.passwordTries < passwordAttempts:
		return w.step(fmt.Sprintf("that password was refused, attempts left: %d",
			passwordAttempts-flow.passwordTries)), nil

	case passwordRefused(err):
		w.flow = nil
		return LoginStep{}, errors.New("the two-factor password was refused and the attempts are used up: " +
			"start over")

	default:
		w.flow = nil
		return LoginStep{}, fmt.Errorf("password: %w", err)
	}
}

// current finds the attempt a form belongs to. A form from an older attempt, an expired
// one or one at another stage gets nowhere: the page then starts over.
func (w *WebLogin) current(flowID string, stage LoginStage) (*webFlow, error) {
	flow := w.flow
	if flow == nil || subtle.ConstantTimeCompare([]byte(flow.id), []byte(flowID)) != 1 {
		return nil, errors.New("this sign-in attempt is no longer current: start over")
	}

	if w.now().After(flow.expires) {
		w.flow = nil
		return nil, errors.New("this sign-in attempt has expired: start over")
	}

	if flow.stage != stage {
		return nil, errors.New("this form is out of step with the sign-in: start over")
	}

	return flow, nil
}

// step describes the attempt in progress.
func (w *WebLogin) step(notice string) LoginStep {
	return LoginStep{Stage: w.flow.stage, Flow: w.flow.id, CodeKind: w.flow.codeKind, NextKind: w.flow.nextKind,
		Notice: notice}
}

// finish closes the attempt and hands the account to the client.
func (w *WebLogin) finish(authorization *tg.AuthAuthorization) LoginStep {
	w.flow = nil

	var user *tg.User
	if authorization != nil {
		user, _ = authorization.User.(*tg.User)
	}
	w.signedIn(user)

	return LoginStep{Stage: StageDone}
}

func (w *WebLogin) newID() (string, error) {
	raw := make([]byte, 16)
	if _, err := io.ReadFull(w.random, raw); err != nil {
		return "", fmt.Errorf("making a sign-in identifier: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// sendCodeFailure words a refusal to send the code. AUTH_RESTART cannot be fixed from
// here: the stale attempt is tied to the key of the connection this server holds, and only
// the login command can set that key aside.
func sendCodeFailure(err error) error {
	if tgerr.Is(err, "AUTH_RESTART") {
		return fmt.Errorf("Telegram wants the login started over because an earlier attempt did not finish: "+
			"stop the server and run \"mcp-telegram login\" once with the same --session-dir (%w)", err)
	}
	return fmt.Errorf("send code: %w", err)
}
