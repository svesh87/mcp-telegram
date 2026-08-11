// Package tools exposes the Telegram identities as MCP tools.
//
// Every tool that touches a chat checks the access lists first, and the lists come from
// the server's own configuration. A caller holds no Telegram credentials, so there is no
// way around this check: refusing here is refusing outright.
package tools

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/svesh87/mcp-telegram/internal/access"
	"github.com/svesh87/mcp-telegram/internal/telegram"
)

// UserReader is the read side of the account identity.
type UserReader interface {
	Chats(ctx context.Context) ([]telegram.Chat, error)
	ChatInfo(ctx context.Context, chatID int64) (telegram.Chat, error)
	History(ctx context.Context, chatID int64, opts telegram.HistoryOptions) ([]telegram.Message, error)
	Search(ctx context.Context, chatID int64, text string, limit int) ([]telegram.Message, error)
	Download(ctx context.Context, chatID int64, messageID int, dir string) (telegram.SavedFile, error)
}

// UserWriter is the write side of the account identity.
type UserWriter interface {
	SendMessage(ctx context.Context, chatID int64, text string, replyTo int) (telegram.Sent, error)
	SendFile(ctx context.Context, chatID int64, path, caption string) (telegram.Sent, error)
}

// BotReader is the read side of the bot identity.
type BotReader interface {
	Me(ctx context.Context) (telegram.Chat, error)
	ChatInfo(ctx context.Context, chatID int64) (telegram.Chat, error)
	Updates(ctx context.Context, offset, limit int) ([]telegram.Message, error)
	Download(ctx context.Context, fileID, dir string) (telegram.SavedFile, error)
}

// BotWriter is the write side of the bot identity.
type BotWriter interface {
	SendMessage(ctx context.Context, chatID int64, text string, replyTo int) (telegram.Sent, error)
	SendFile(ctx context.Context, chatID int64, path, caption string) (telegram.Sent, error)
}

// Options is what the tools need to work.
type Options struct {
	Checker *access.Checker
	// Identities that are enabled. A tool for an identity the server does not run is
	// not registered at all, rather than registered and failing: an agent that can see
	// the tool will call it.
	Identities []access.Identity
	// AllowWrite gates every writing tool. Off by default, and the flag that turns it on
	// still cannot widen the lists.
	AllowWrite bool
	// DownloadDir is where downloaded files land. Empty means the download tools are not
	// offered, because this server does not choose a place on disk on its own.
	DownloadDir string

	UserRead  UserReader
	UserWrite UserWriter
	BotRead   BotReader
	BotWrite  BotWriter
}

// registry carries the options into the handlers.
type registry struct{ opts Options }

// Register adds the tools this configuration allows.
func Register(srv *server.MCPServer, opts Options) error {
	if opts.Checker == nil {
		return errors.New("access checker is required")
	}

	r := &registry{opts: opts}

	srv.AddTool(accessListsTool(), r.accessLists)

	if opts.uses(access.User) {
		if opts.UserRead == nil {
			return errors.New("the user identity is enabled but no client was given")
		}
		r.registerUser(srv)
	}

	if opts.uses(access.Bot) {
		if opts.BotRead == nil {
			return errors.New("the bot identity is enabled but no client was given")
		}
		r.registerBot(srv)
	}

	return nil
}

func (o Options) uses(identity access.Identity) bool {
	for _, enabled := range o.Identities {
		if enabled == identity {
			return true
		}
	}
	return false
}

// writingAllowed says whether writing tools should be offered for an identity: the flag
// has to be on and the identity has to have somewhere to write.
func (o Options) writingAllowed(identity access.Identity) bool {
	return o.AllowWrite && len(o.Checker.Chats(identity, access.Write)) > 0
}

func accessListsTool() mcp.Tool {
	return mcp.NewTool("telegram_access_lists",
		mcp.WithDescription("Report what this server may do: which identities it runs, "+
			"which chats each of them may read and write, and whether writing is enabled at all. "+
			"Answers from configuration without calling Telegram, so it is the cheapest way to find "+
			"out whether a chat is reachable before trying."),
		mcp.WithReadOnlyHintAnnotation(true),
	)
}

// accessLists answers from the configuration alone. A caller that knows what is allowed
// stops guessing chat identifiers, and a refusal stops looking like a fault.
func (r *registry) accessLists(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	type chatRef struct {
		ID   int64  `json:"id"`
		Kind string `json:"kind"`
	}
	type identityLists struct {
		Read  []chatRef `json:"read"`
		Write []chatRef `json:"write"`
	}

	refs := func(identity access.Identity, mode access.Mode) []chatRef {
		chats := r.opts.Checker.Chats(identity, mode)
		list := make([]chatRef, 0, len(chats))
		for _, chat := range chats {
			list = append(list, chatRef{ID: chat, Kind: telegram.ChatKindOf(chat)})
		}
		return list
	}

	payload := struct {
		Identities   []string                 `json:"identities"`
		WriteEnabled bool                     `json:"write_enabled"`
		Lists        map[string]identityLists `json:"lists"`
		DownloadDir  string                   `json:"download_dir,omitempty"`
	}{
		WriteEnabled: r.opts.AllowWrite,
		Lists:        map[string]identityLists{},
		DownloadDir:  r.opts.DownloadDir,
	}

	for _, identity := range r.opts.Identities {
		payload.Identities = append(payload.Identities, identity.String())
		payload.Lists[identity.String()] = identityLists{
			Read:  refs(identity, access.Read),
			Write: refs(identity, access.Write),
		}
	}

	return mcp.NewToolResultJSON(payload)
}

// chatIDArg reads a chat identifier.
//
// It comes as a string because these identifiers are long negative numbers copied out of
// a Telegram client, and a string survives that trip unchanged.
func chatIDArg(req mcp.CallToolRequest) (int64, error) {
	raw, err := req.RequireString("chat_id")
	if err != nil {
		return 0, err
	}

	raw = strings.TrimSpace(raw)
	chatID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("chat_id %q is not a number: chat identifiers are numbers, "+
			"and names are not accepted", raw)
	}

	if chatID == 0 {
		return 0, errors.New("chat_id 0 is not a chat")
	}

	return chatID, nil
}

// sinceArg reads an optional lower bound on message dates.
func sinceArg(req mcp.CallToolRequest) (time.Time, error) {
	raw := strings.TrimSpace(req.GetString("since", ""))
	if raw == "" {
		return time.Time{}, nil
	}

	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if at, err := time.Parse(layout, raw); err == nil {
			return at.UTC(), nil
		}
	}

	return time.Time{}, fmt.Errorf("since %q is not a date: use 2006-01-02 or a full RFC 3339 timestamp", raw)
}

// downloadDir answers where a download may write, or why it may not.
func (r *registry) downloadDir() (string, error) {
	if r.opts.DownloadDir == "" {
		return "", errors.New("this server was started without --download-dir, " +
			"so it has nowhere to save files")
	}

	return r.opts.DownloadDir, nil
}

func toolError(err error) *mcp.CallToolResult {
	return mcp.NewToolResultError(err.Error())
}
