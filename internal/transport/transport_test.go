package transport

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"
)

const testToken = "correct-horse-battery-staple"

func initializeBody() io.Reader {
	return strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize",` +
		`"params":{"protocolVersion":"2025-03-26","capabilities":{},` +
		`"clientInfo":{"name":"transport-test","version":"0"}}}`)
}

func handlerUnderTest(t *testing.T) http.Handler {
	t.Helper()

	mcpServer := server.NewMCPServer("mcp-telegram", "test", server.WithToolCapabilities(true))
	return NewHandler(mcpServer, testToken)
}

// This server can read and write Telegram as its configured identities, so anything
// short of the exact token must be turned away. On loopback the caller is any local
// process, not only the agent.
func TestMCPEndpointRejectsEverythingButTheToken(t *testing.T) {
	cases := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer wrong-token", http.StatusUnauthorized},
		{"empty token", "Bearer ", http.StatusUnauthorized},
		{"token without scheme", testToken, http.StatusUnauthorized},
		{"another scheme", "Basic " + testToken, http.StatusUnauthorized},
		{"token as prefix of the real one", "Bearer correct-horse", http.StatusUnauthorized},
		{"correct token", "Bearer " + testToken, http.StatusOK},
		{"lowercase scheme", "bearer " + testToken, http.StatusOK},
		{"uppercase scheme", "BEARER " + testToken, http.StatusOK},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, MCPPath, initializeBody())
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			if c.authHeader != "" {
				request.Header.Set("Authorization", c.authHeader)
			}

			recorder := httptest.NewRecorder()
			handlerUnderTest(t).ServeHTTP(recorder, request)

			if recorder.Code != c.wantStatus {
				t.Errorf("status %d, want %d (body: %s)", recorder.Code, c.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestUnauthorizedResponseAdvertisesBearer(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, MCPPath, initializeBody())
	recorder := httptest.NewRecorder()

	handlerUnderTest(t).ServeHTTP(recorder, request)

	if got := recorder.Header().Get("WWW-Authenticate"); got == "" {
		t.Error("401 came back without a WWW-Authenticate header")
	}
}

func TestHealthEndpointNeedsNoTokenAndSaysNothingElse(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, HealthPath, nil)
	recorder := httptest.NewRecorder()

	handlerUnderTest(t).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want %d", recorder.Code, http.StatusOK)
	}
	if body := recorder.Body.String(); body != "ok\n" {
		t.Errorf("health body is %q, want %q", body, "ok\n")
	}
}

func TestBearerTokenParsing(t *testing.T) {
	cases := []struct {
		header    string
		wantToken string
		wantOK    bool
	}{
		{"Bearer abc", "abc", true},
		{"bearer abc", "abc", true},
		{"Bearer   abc  ", "abc", true},
		{"Bearer", "", false},
		{"Bearer ", "", false},
		{"", "", false},
		{"Basic abc", "", false},
		{"abc", "", false},
	}

	for _, c := range cases {
		token, ok := BearerToken(c.header)
		if token != c.wantToken || ok != c.wantOK {
			t.Errorf("BearerToken(%q) = (%q, %v), want (%q, %v)", c.header, token, ok, c.wantToken, c.wantOK)
		}
	}
}
