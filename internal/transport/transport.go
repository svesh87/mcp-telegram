// Package transport serves the MCP server over stdio or streamable HTTP.
package transport

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

// Paths the HTTP transport answers on. The MCP path is mcp-go's default; health sits
// beside it and needs no token, because the image is built FROM scratch and a
// container healthcheck has no shell to read one with.
const (
	MCPPath    = "/mcp"
	HealthPath = "/healthz"
)

// ServeStdio hands the server to the stdio transport.
func ServeStdio(mcpServer *server.MCPServer) error {
	return server.ServeStdio(mcpServer)
}

// ServeHTTP starts the streamable HTTP transport on address, behind bearer auth.
func ServeHTTP(mcpServer *server.MCPServer, address, token string) error {
	httpServer := &http.Server{
		Addr:    address,
		Handler: NewHandler(mcpServer, token),
		// Only the header deadline is set. Read and write deadlines would cut off the
		// long-lived SSE streams this transport is built on.
		ReadHeaderTimeout: 10 * time.Second,
	}

	return httpServer.ListenAndServe()
}

// NewHandler wires the MCP endpoint behind bearer auth and adds the health endpoint.
func NewHandler(mcpServer *server.MCPServer, token string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc(HealthPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})

	mux.Handle(MCPPath, RequireBearer(server.NewStreamableHTTPServer(mcpServer), token))

	return mux
}

// RequireBearer rejects everything that does not carry the expected token.
//
// This server reaches Telegram as its configured identities, so whatever reaches its
// port inherits that reach; on loopback that is any process running as the same
// account. Comparison is constant-time so a wrong token leaks nothing through
// response timing.
func RequireBearer(next http.Handler, token string) http.Handler {
	expected := []byte(token)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := BearerToken(r.Header.Get("Authorization"))
		if !ok || subtle.ConstantTimeCompare([]byte(presented), expected) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="mcp-telegram"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// BearerToken pulls the token out of an Authorization header value. The scheme is
// matched case-insensitively, as the HTTP spec requires.
func BearerToken(header string) (string, bool) {
	const scheme = "bearer "

	if len(header) <= len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}

	token := strings.TrimSpace(header[len(scheme):])
	if token == "" {
		return "", false
	}

	return token, true
}
