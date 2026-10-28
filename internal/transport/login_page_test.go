package transport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"

	"github.com/svesh87/mcp-telegram/internal/telegram"
)

const testPublicURL = "https://mcp.example.com/telegram"

// fakeLogin stands in for telegram.WebLogin and remembers what reached it.
type fakeLogin struct {
	account telegram.Account
	step    telegram.LoginStep
	err     error

	calls []string
	got   []string
}

func (f *fakeLogin) Account() telegram.Account { return f.account }

func (f *fakeLogin) Start(_ context.Context, phone string) (telegram.LoginStep, error) {
	f.calls = append(f.calls, "start")
	f.got = append(f.got, phone)
	return f.step, f.err
}

func (f *fakeLogin) Code(_ context.Context, flow, code string) (telegram.LoginStep, error) {
	f.calls = append(f.calls, "code")
	f.got = append(f.got, flow, code)
	return f.step, f.err
}

func (f *fakeLogin) Password(_ context.Context, flow, password string) (telegram.LoginStep, error) {
	f.calls = append(f.calls, "password")
	f.got = append(f.got, flow, password)
	return f.step, f.err
}

func webHandler(t *testing.T, login LoginService) http.Handler {
	t.Helper()

	mcpServer := server.NewMCPServer("mcp-telegram", "test", server.WithToolCapabilities(true))
	return NewWebHandler(mcpServer, testToken, &Web{PublicURL: testPublicURL, Login: login})
}

func withBearer(request *http.Request) *http.Request {
	request.Header.Set("Authorization", "Bearer "+testToken)
	return request
}

func post(form url.Values) *http.Request {
	request := httptest.NewRequest(http.MethodPost, LoginPath, strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	return withBearer(request)
}

func serveTo(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// The old handler, which is what the server serves without --public-url, has no login page
// and no root redirect.
func TestWithoutPublicURLThereIsNoLoginPage(t *testing.T) {
	for _, path := range []string{LoginPath, "/"} {
		recorder := serveTo(handlerUnderTest(t), withBearer(httptest.NewRequest(http.MethodGet, path, nil)))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s answered %d without --public-url, want 404", path, recorder.Code)
		}
	}
}

func TestLoginPageNeedsTheBearer(t *testing.T) {
	login := &fakeLogin{account: telegram.Account{CanSignIn: true}}

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, LoginPath, nil),
		httptest.NewRequest(http.MethodGet, LoginPath+"?key="+testToken, nil),
		httptest.NewRequest(http.MethodGet, "/", nil),
	} {
		recorder := serveTo(webHandler(t, login), request)
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s answered %d without the header", request.URL, recorder.Code)
		}
		if strings.Contains(recorder.Body.String(), "<form") {
			t.Errorf("%s showed a form to a stranger", request.URL)
		}
	}

	if len(login.calls) != 0 {
		t.Errorf("the sign-in was reached without the bearer: %v", login.calls)
	}
}

func TestRootRedirectsToTheLoginPage(t *testing.T) {
	handler := webHandler(t, &fakeLogin{})

	recorder := serveTo(handler, withBearer(httptest.NewRequest(http.MethodGet, "/", nil)))
	if recorder.Code != http.StatusFound {
		t.Fatalf("/ answered %d, want 302", recorder.Code)
	}
	if got := recorder.Header().Get("Location"); got != testPublicURL+LoginPath {
		t.Errorf("redirected to %q, want %q", got, testPublicURL+LoginPath)
	}

	// Only the exact root: anything else unknown stays unknown.
	recorder = serveTo(handler, withBearer(httptest.NewRequest(http.MethodGet, "/elsewhere", nil)))
	if recorder.Code != http.StatusNotFound {
		t.Errorf("/elsewhere answered %d, want 404", recorder.Code)
	}
}

func TestLoginPageShowsThePhoneFormAndKeepsTheTokenOut(t *testing.T) {
	recorder := serveTo(webHandler(t, &fakeLogin{account: telegram.Account{CanSignIn: true}}),
		withBearer(httptest.NewRequest(http.MethodGet, LoginPath, nil)))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d", recorder.Code)
	}

	body := recorder.Body.String()
	if !strings.Contains(body, `name="phone"`) {
		t.Error("no phone form")
	}
	if !strings.Contains(body, `action="`+testPublicURL+LoginPath+`"`) {
		t.Errorf("the form does not post to the public address: %s", body)
	}
	if strings.Contains(body, testToken) {
		t.Error("the bearer token is in the page")
	}
	if !strings.Contains(body, "Вход не выполнен") {
		t.Error("the page does not say the account is not signed in")
	}

	for header, want := range map[string]string{
		"Cache-Control":   "no-store",
		"Referrer-Policy": "no-referrer",
	} {
		if got := recorder.Header().Get(header); got != want {
			t.Errorf("%s is %q, want %q", header, got, want)
		}
	}
	if csp := recorder.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' https://mcp.example.com") {
		t.Errorf("Content-Security-Policy is %q", csp)
	}
}

func TestLoginPageWhenSignedIn(t *testing.T) {
	recorder := serveTo(webHandler(t, &fakeLogin{account: telegram.Account{
		SignedIn: true, Name: "Ann Lee", Username: "annlee",
	}}), withBearer(httptest.NewRequest(http.MethodGet, LoginPath, nil)))

	body := recorder.Body.String()
	if !strings.Contains(body, "Ann Lee") || !strings.Contains(body, "@annlee") {
		t.Errorf("the page does not name the account: %s", body)
	}
	if strings.Contains(body, `name="phone"`) {
		t.Error("a phone form is offered on a signed in session")
	}
}

func TestLoginPageWhenSigningInIsNotPossible(t *testing.T) {
	recorder := serveTo(webHandler(t, &fakeLogin{account: telegram.Account{Problem: "still connecting"}}),
		withBearer(httptest.NewRequest(http.MethodGet, LoginPath, nil)))

	body := recorder.Body.String()
	if !strings.Contains(body, "still connecting") || strings.Contains(body, "<form") {
		t.Errorf("the page: %s", body)
	}
}

// A bot-only server has no account to sign in.
func TestLoginPageWithoutTheUserIdentity(t *testing.T) {
	handler := webHandler(t, nil)

	recorder := serveTo(handler, withBearer(httptest.NewRequest(http.MethodGet, LoginPath, nil)))
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "<form") ||
		!strings.Contains(recorder.Body.String(), "не включён") {
		t.Errorf("status %d, body %s", recorder.Code, recorder.Body.String())
	}

	recorder = serveTo(handler, post(url.Values{"step": {"phone"}, "phone": {"+1"}}))
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "<form") {
		t.Errorf("POST without a user identity: status %d", recorder.Code)
	}
}

func TestLoginPageWalksTheSteps(t *testing.T) {
	login := &fakeLogin{
		account: telegram.Account{CanSignIn: true},
		step:    telegram.LoginStep{Stage: telegram.StageCode, Flow: "flow-1", CodeKind: "in the Telegram app"},
	}
	handler := webHandler(t, login)

	recorder := serveTo(handler, post(url.Values{"step": {"phone"}, "phone": {"+37400000000"}}))
	body := recorder.Body.String()
	if !strings.Contains(body, `name="code"`) || !strings.Contains(body, `value="flow-1"`) {
		t.Fatalf("no code form after the phone: %s", body)
	}
	// The phone number is not echoed back into the page.
	if strings.Contains(body, "+37400000000") {
		t.Error("the phone number is in the page")
	}

	login.step = telegram.LoginStep{Stage: telegram.StagePassword, Flow: "flow-1"}
	body = serveTo(handler, post(url.Values{"step": {"code"}, "flow": {"flow-1"}, "code": {"12345"}})).Body.String()
	if !strings.Contains(body, `name="password"`) || !strings.Contains(body, `type="password"`) {
		t.Fatalf("no password form after SESSION_PASSWORD_NEEDED: %s", body)
	}

	login.step = telegram.LoginStep{Stage: telegram.StageDone}
	login.account = telegram.Account{SignedIn: true, Name: "Ann"}
	body = serveTo(handler, post(url.Values{"step": {"password"}, "flow": {"flow-1"}, "password": {"secret"}})).Body.String()
	if !strings.Contains(body, "Готово") || !strings.Contains(body, "Перезапускать сервер не нужно") {
		t.Errorf("no confirmation after the password: %s", body)
	}
	if strings.Contains(body, "secret") || strings.Contains(body, "12345") {
		t.Error("the password or the code is in the page")
	}

	wantCalls := []string{"start", "code", "password"}
	if strings.Join(login.calls, ",") != strings.Join(wantCalls, ",") {
		t.Errorf("calls %v, want %v", login.calls, wantCalls)
	}
	wantGot := []string{"+37400000000", "flow-1", "12345", "flow-1", "secret"}
	if strings.Join(login.got, ",") != strings.Join(wantGot, ",") {
		t.Errorf("the forms passed %v", login.got)
	}
}

func TestLoginPageShowsNoticesAndErrors(t *testing.T) {
	login := &fakeLogin{
		account: telegram.Account{CanSignIn: true},
		step:    telegram.LoginStep{Stage: telegram.StageCode, Flow: "f", Notice: "that code was refused, attempts left: 2"},
	}
	handler := webHandler(t, login)

	body := serveTo(handler, post(url.Values{"step": {"code"}, "flow": {"f"}, "code": {"1"}})).Body.String()
	if !strings.Contains(body, "attempts left: 2") || !strings.Contains(body, `name="code"`) {
		t.Errorf("the notice or the form is missing: %s", body)
	}

	login.err = errors.New("the code has expired: start over")
	body = serveTo(handler, post(url.Values{"step": {"code"}, "flow": {"f"}, "code": {"1"}})).Body.String()
	if !strings.Contains(body, "the code has expired") || !strings.Contains(body, `name="phone"`) {
		t.Errorf("after an error the page should show it and start over: %s", body)
	}
}

func TestLoginPageRefusesCrossSiteForms(t *testing.T) {
	login := &fakeLogin{account: telegram.Account{CanSignIn: true}}
	handler := webHandler(t, login)

	request := post(url.Values{"step": {"phone"}, "phone": {"+1"}})
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	if recorder := serveTo(handler, request); recorder.Code != http.StatusForbidden {
		t.Errorf("a cross-site form answered %d, want 403", recorder.Code)
	}

	request = post(url.Values{"step": {"phone"}, "phone": {"+1"}})
	request.Header.Del("Sec-Fetch-Site")
	request.Header.Set("Origin", "https://evil.example.org")
	if recorder := serveTo(handler, request); recorder.Code != http.StatusForbidden {
		t.Errorf("a form from another origin answered %d, want 403", recorder.Code)
	}

	// Behind the gateway the Host header is not the public one; the public origin is trusted.
	request = post(url.Values{"step": {"phone"}, "phone": {"+1"}})
	request.Header.Del("Sec-Fetch-Site")
	request.Header.Set("Origin", "https://mcp.example.com")
	if recorder := serveTo(handler, request); recorder.Code != http.StatusOK {
		t.Errorf("a form from the public origin answered %d, want 200", recorder.Code)
	}

	if len(login.calls) != 1 {
		t.Errorf("calls %v, want only the one from the public origin", login.calls)
	}
}

func TestLoginPageRefusesOtherMethodsAndJunk(t *testing.T) {
	handler := webHandler(t, &fakeLogin{account: telegram.Account{CanSignIn: true}})

	recorder := serveTo(handler, withBearer(httptest.NewRequest(http.MethodDelete, LoginPath, nil)))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE answered %d", recorder.Code)
	}

	request := post(url.Values{"phone": {strings.Repeat("9", maxLoginForm+1)}})
	if recorder := serveTo(handler, request); recorder.Code != http.StatusBadRequest {
		t.Errorf("an oversized form answered %d", recorder.Code)
	}
}

// The login page changes nothing about the endpoints that were there before.
func TestWebHandlerKeepsMCPAndHealth(t *testing.T) {
	handler := webHandler(t, &fakeLogin{})

	recorder := serveTo(handler, httptest.NewRequest(http.MethodGet, HealthPath, nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "ok\n" {
		t.Errorf("health: %d %q", recorder.Code, recorder.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, MCPPath, initializeBody())
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if recorder := serveTo(handler, request); recorder.Code != http.StatusUnauthorized {
		t.Errorf("/mcp without the bearer answered %d", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodPost, MCPPath, initializeBody())
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if recorder := serveTo(handler, withBearer(request)); recorder.Code != http.StatusOK {
		t.Errorf("/mcp with the bearer answered %d", recorder.Code)
	}
}

func TestOrigin(t *testing.T) {
	if got := origin("https://mcp.example.com:8443/telegram"); got != "https://mcp.example.com:8443" {
		t.Errorf("origin = %q", got)
	}
	if got := origin("%zz"); got != "" {
		t.Errorf("origin of junk = %q", got)
	}
}
