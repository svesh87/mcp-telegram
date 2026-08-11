// Package config turns the environment and command line into a validated
// configuration, and refuses to start on anything ambiguous.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/svesh87/mcp-telegram/internal/access"
)

// Transport names accepted by the --transport flag.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "streamable-http"
)

// Environment variables. Chat lists live here rather than in a file so they sit with
// the rest of the secrets in one pass entry, and so a public repository never has to
// carry an example that looks like a real list.
const (
	EnvAPIID    = "TELEGRAM_API_ID"
	EnvAPIHash  = "TELEGRAM_API_HASH"
	EnvBotToken = "TELEGRAM_BOT_TOKEN"

	EnvUserRead  = "TELEGRAM_USER_READ_CHATS"
	EnvUserWrite = "TELEGRAM_USER_WRITE_CHATS"
	EnvBotRead   = "TELEGRAM_BOT_READ_CHATS"
	EnvBotWrite  = "TELEGRAM_BOT_WRITE_CHATS"

	EnvAuthToken = "MCP_AUTH_TOKEN"
)

// Config is the validated configuration of one server process.
type Config struct {
	Identities  []access.Identity
	Lists       access.Lists
	APIID       int
	APIHash     string
	BotToken    string
	AllowWrite  bool
	Transport   string
	Address     string
	AuthToken   string
	SessionDir  string
	DownloadDir string
}

// Flags are the command line values Load needs. Kept separate from parsing so tests
// can build a configuration without touching the global flag set.
type Flags struct {
	Identities  string
	AllowWrite  bool
	Transport   string
	Address     string
	SessionDir  string
	DownloadDir string
}

// Env reads a variable. Tests substitute a map instead of touching the process
// environment.
type Env func(key string) string

// OSEnv reads the process environment.
func OSEnv(key string) string { return os.Getenv(key) }

// Load validates flags and environment together. Every failure is returned rather
// than logged, so the caller decides how loudly to die.
func Load(flags Flags, env Env) (*Config, error) {
	identities, err := parseIdentities(flags.Identities)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Identities:  identities,
		AllowWrite:  flags.AllowWrite,
		Transport:   flags.Transport,
		Address:     flags.Address,
		SessionDir:  flags.SessionDir,
		DownloadDir: flags.DownloadDir,
		BotToken:    env(EnvBotToken),
		APIHash:     env(EnvAPIHash),
		AuthToken:   env(EnvAuthToken),
	}

	if err := loadLists(cfg, env); err != nil {
		return nil, err
	}

	if err := loadCredentials(cfg, env); err != nil {
		return nil, err
	}

	if err := validateTransport(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// parseIdentities accepts "user", "bot" or both, in any order. Running both is the
// interesting case (one process, two actors, separate rights), but each alone is
// legitimate: a bot-only server needs no phone login at all.
func parseIdentities(raw string) ([]access.Identity, error) {
	seen := map[access.Identity]bool{}
	var identities []access.Identity

	for _, field := range strings.Split(raw, ",") {
		switch strings.TrimSpace(field) {
		case "user":
			if !seen[access.User] {
				seen[access.User] = true
				identities = append(identities, access.User)
			}
		case "bot":
			if !seen[access.Bot] {
				seen[access.Bot] = true
				identities = append(identities, access.Bot)
			}
		case "":
			continue
		default:
			return nil, fmt.Errorf("unknown identity %q: use user, bot, or both separated by a comma", field)
		}
	}

	if len(identities) == 0 {
		return nil, errors.New("no identities selected: use --identities=user, --identities=bot or --identities=user,bot")
	}

	return identities, nil
}

func loadLists(cfg *Config, env Env) error {
	var err error

	for _, list := range []struct {
		name   string
		target *[]int64
	}{
		{EnvUserRead, &cfg.Lists.UserRead},
		{EnvUserWrite, &cfg.Lists.UserWrite},
		{EnvBotRead, &cfg.Lists.BotRead},
		{EnvBotWrite, &cfg.Lists.BotWrite},
	} {
		*list.target, err = access.ParseList(env(list.name))
		if err != nil {
			return fmt.Errorf("%s: %w", list.name, err)
		}
	}

	// A server with write enabled but no write list would advertise writing tools
	// that refuse every chat. Better to say so at startup.
	if cfg.AllowWrite && len(cfg.Lists.UserWrite) == 0 && len(cfg.Lists.BotWrite) == 0 {
		return fmt.Errorf("--allow-write is set but both write lists are empty: fill %s or %s",
			EnvUserWrite, EnvBotWrite)
	}

	return nil
}

func loadCredentials(cfg *Config, env Env) error {
	for _, identity := range cfg.Identities {
		switch identity {
		case access.User:
			raw := env(EnvAPIID)
			if raw == "" || cfg.APIHash == "" {
				return fmt.Errorf("identity user needs %s and %s", EnvAPIID, EnvAPIHash)
			}

			id, err := strconv.Atoi(raw)
			if err != nil {
				return fmt.Errorf("%s must be a number, got %q", EnvAPIID, raw)
			}
			cfg.APIID = id

			if cfg.SessionDir == "" {
				return errors.New("identity user needs --session-dir: the MTProto session has to outlive the container")
			}
		case access.Bot:
			if cfg.BotToken == "" {
				return fmt.Errorf("identity bot needs %s", EnvBotToken)
			}
		}
	}

	return nil
}

func validateTransport(cfg *Config) error {
	switch cfg.Transport {
	case TransportStdio:
		return nil
	case TransportHTTP:
		// The port reaches Telegram with this server's rights, and on loopback any
		// local process can knock. Refusing to start beats serving quietly.
		if cfg.AuthToken == "" {
			return fmt.Errorf("%s is required with --transport=%s: refusing to serve without authentication",
				EnvAuthToken, TransportHTTP)
		}
		if cfg.Address == "" {
			return errors.New("--address is required with --transport=" + TransportHTTP)
		}
		return nil
	default:
		return fmt.Errorf("unknown transport %q: use %s or %s", cfg.Transport, TransportStdio, TransportHTTP)
	}
}

// Uses reports whether an identity is enabled.
func (c *Config) Uses(identity access.Identity) bool {
	for _, enabled := range c.Identities {
		if enabled == identity {
			return true
		}
	}
	return false
}
