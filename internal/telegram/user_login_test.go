package telegram

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

// webClient is testClient built the way NewUser builds it with the web login on.
func webClient() *UserClient {
	client := testClient()
	client.loginNeeded = make(chan struct{})
	client.signedIn = make(chan struct{})
	return client
}

func statusOf(status *auth.Status, err error) func(context.Context) (*auth.Status, error) {
	return func(context.Context) (*auth.Status, error) { return status, err }
}

// runStart runs start in the background and returns where its result lands.
func runStart(client *UserClient, status *auth.Status) (context.CancelFunc, chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- client.start(ctx, statusOf(status, nil)) }()
	return cancel, result
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestNewUserWebLoginIsOptIn(t *testing.T) {
	plain, err := NewUser(UserOptions{APIID: 1, APIHash: "hash", SessionDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewUser: %v", err)
	}
	if plain.loginNeeded != nil || plain.signedIn != nil || plain.WebLogin() != nil {
		t.Error("the web login is on without being asked for")
	}

	web, err := NewUser(UserOptions{APIID: 1, APIHash: "hash", SessionDir: t.TempDir(), WebLogin: true})
	if err != nil {
		t.Fatalf("NewUser: %v", err)
	}
	if web.WebLogin() == nil {
		t.Error("the web login is off although asked for")
	}
}

// The behaviour every existing deployment depends on: without the web login an
// unauthorised session stops the client, and the server with it.
func TestStartWithoutWebLoginRefusesAnUnauthorisedSession(t *testing.T) {
	client := testClient()

	err := client.start(context.Background(), statusOf(&auth.Status{Authorized: false}, nil))
	if !errors.Is(err, ErrNotAuthorized) {
		t.Errorf("start on an unauthorised session = %v, want ErrNotAuthorized", err)
	}
}

func TestStartOnAnAuthorisedSession(t *testing.T) {
	client := testClient()
	cancel, result := runStart(client, &auth.Status{Authorized: true, User: &tg.User{ID: 42, FirstName: "Ann"}})

	waitFor(t, client.ready, "ready")
	if client.selfID != 42 {
		t.Errorf("selfID = %d, want 42", client.selfID)
	}
	if account := client.Account(); !account.SignedIn || account.Name != "Ann" {
		t.Errorf("Account = %+v", account)
	}

	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Errorf("start ended with %v", err)
	}
}

func TestStartFailures(t *testing.T) {
	client := testClient()
	if err := client.start(context.Background(), statusOf(nil, errors.New("no network"))); err == nil {
		t.Error("a failed status check was ignored")
	}

	client = testClient()
	client.refresh = func(context.Context) error { return errors.New("no dialogs") }
	if err := client.start(context.Background(), statusOf(&auth.Status{Authorized: true}, nil)); err == nil {
		t.Error("a failed dialog list was ignored")
	}
}

// With the web login the client stays connected on an unauthorised session, says so to
// tools at once, and carries on as soon as the page signs it in: no restart.
func TestStartWaitsForTheWebLogin(t *testing.T) {
	client := webClient()

	if account := client.Account(); account.CanSignIn || account.Problem == "" {
		t.Errorf("before connecting, Account = %+v", account)
	}
	if _, err := client.authForLogin(); err == nil {
		t.Error("a sign-in was offered before the connection was up")
	}

	cancel, result := runStart(client, &auth.Status{Authorized: false})
	defer cancel()

	waitFor(t, client.loginNeeded, "loginNeeded")

	if err := client.Wait(context.Background()); !errors.Is(err, ErrLoginPending) {
		t.Errorf("Wait while waiting for a person = %v, want ErrLoginPending", err)
	}
	if account := client.Account(); !account.CanSignIn || account.SignedIn {
		t.Errorf("while waiting, Account = %+v", account)
	}

	client.markSignedIn(&tg.User{ID: 5, FirstName: "Bob", Username: "bob"})
	client.markSignedIn(&tg.User{ID: 5}) // a second report changes nothing and does not panic

	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := client.Wait(ctx); err != nil {
		t.Fatalf("Wait after the sign-in: %v", err)
	}
	if client.selfID != 5 {
		t.Errorf("selfID = %d, want 5", client.selfID)
	}
	if account := client.Account(); !account.SignedIn || account.Username != "bob" {
		t.Errorf("after the sign-in, Account = %+v", account)
	}
	if _, err := client.authForLogin(); err == nil {
		t.Error("a second sign-in was offered on a signed in session")
	}

	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Errorf("start ended with %v", err)
	}
}

func TestStartWaitingForTheWebLoginStopsWithTheContext(t *testing.T) {
	client := webClient()
	cancel, result := runStart(client, &auth.Status{Authorized: false})

	waitFor(t, client.loginNeeded, "loginNeeded")
	cancel()

	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Errorf("start ended with %v", err)
	}
}

func TestAccountWhenTheClientStopped(t *testing.T) {
	client := webClient()
	client.err = errors.New("the connection broke")
	close(client.done)

	account := client.Account()
	if account.CanSignIn || account.Problem != "the connection broke" {
		t.Errorf("Account = %+v", account)
	}
	if _, err := client.authForLogin(); err == nil {
		t.Error("a sign-in was offered on a stopped client")
	}
}

func TestAccountSignedInWithoutAName(t *testing.T) {
	client := webClient()
	client.markSignedIn(nil)

	if account := client.Account(); !account.SignedIn {
		t.Errorf("Account = %+v", account)
	}
}

func TestWaitSignedInButStoppedBeforeReady(t *testing.T) {
	client := webClient()
	close(client.loginNeeded)
	client.markSignedIn(nil)
	client.err = errors.New("dialogs failed")
	close(client.done)

	if err := client.Wait(context.Background()); err == nil {
		t.Error("Wait succeeded on a client that stopped")
	}
}

func TestWaitSignedInContextEnds(t *testing.T) {
	client := webClient()
	close(client.loginNeeded)
	client.markSignedIn(nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Wait(ctx); err == nil {
		t.Error("Wait ignored its context")
	}
}

func TestWebLoginFromAWaitingClient(t *testing.T) {
	client := webClient()
	close(client.loginNeeded)

	login := client.WebLogin()
	if !login.Account().CanSignIn {
		t.Errorf("Account = %+v", login.Account())
	}

	// The auth client handed out is the running client's own; there is no client here, so
	// only the refusal paths are checked without one.
	login.client = func() (authClient, error) { return &fakeAuth{code: "1"}, nil }
	step, err := login.Start(context.Background(), testPhone)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := login.Code(context.Background(), step.Flow, "1"); err != nil {
		t.Fatalf("Code: %v", err)
	}

	waitFor(t, client.signedIn, "signedIn")
}
