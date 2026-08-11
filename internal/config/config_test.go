package config

import (
	"strings"
	"testing"

	"github.com/svesh87/mcp-telegram/internal/access"
)

// envMap builds an Env over a map, so tests never touch the process environment.
func envMap(values map[string]string) Env {
	return func(key string) string { return values[key] }
}

// A working set of values: user identity, one read list, stdio transport.
func baseEnv() map[string]string {
	return map[string]string{
		EnvAPIID:    "12345",
		EnvAPIHash:  "0123456789abcdef0123456789abcdef",
		EnvUserRead: "-1001",
	}
}

func baseFlags() Flags {
	return Flags{
		Identities: "user",
		Transport:  TransportStdio,
		Address:    "127.0.0.1:8080",
		SessionDir: "/session",
	}
}

func TestLoadAcceptsAWorkingSet(t *testing.T) {
	cfg, err := Load(baseFlags(), envMap(baseEnv()))
	if err != nil {
		t.Fatalf("valid configuration rejected: %v", err)
	}

	if !cfg.Uses(access.User) || cfg.Uses(access.Bot) {
		t.Errorf("identities = %v, want user only", cfg.Identities)
	}
	if cfg.APIID != 12345 {
		t.Errorf("APIID = %d, want 12345", cfg.APIID)
	}
	if cfg.AllowWrite {
		t.Error("writing is enabled without --allow-write")
	}
}

// Three ways to run, and each is spelled out rather than guessed from which
// credentials happen to be present.
func TestIdentitySelection(t *testing.T) {
	cases := []struct {
		raw      string
		wantUser bool
		wantBot  bool
		wantErr  bool
	}{
		{"user", true, false, false},
		{"bot", false, true, false},
		{"user,bot", true, true, false},
		{"bot,user", true, true, false},
		{" user , bot ", true, true, false},
		{"user,user", true, false, false},
		{"", false, false, true},
		{"admin", false, false, true},
		{"user,admin", false, false, true},
	}

	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			env := baseEnv()
			env[EnvBotToken] = "123:abc"

			flags := baseFlags()
			flags.Identities = c.raw

			cfg, err := Load(flags, envMap(env))
			if c.wantErr {
				if err == nil {
					t.Fatalf("identities %q accepted", c.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("identities %q rejected: %v", c.raw, err)
			}
			if cfg.Uses(access.User) != c.wantUser || cfg.Uses(access.Bot) != c.wantBot {
				t.Errorf("identities %q gave user=%v bot=%v", c.raw, cfg.Uses(access.User), cfg.Uses(access.Bot))
			}
		})
	}
}

// Each identity needs its own credentials, and the failure has to name the missing
// variable: "authentication failed" three screens later is not a diagnosis.
func TestMissingCredentialsAreNamed(t *testing.T) {
	cases := []struct {
		name       string
		identities string
		drop       []string
		wantIn     string
	}{
		{"user without api id", "user", []string{EnvAPIID}, EnvAPIID},
		{"user without api hash", "user", []string{EnvAPIHash}, EnvAPIHash},
		{"bot without token", "bot", []string{EnvBotToken}, EnvBotToken},
		{"both, bot token missing", "user,bot", []string{EnvBotToken}, EnvBotToken},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := baseEnv()
			env[EnvBotToken] = "123:abc"
			for _, key := range c.drop {
				delete(env, key)
			}

			flags := baseFlags()
			flags.Identities = c.identities

			_, err := Load(flags, envMap(env))
			if err == nil {
				t.Fatal("configuration without credentials was accepted")
			}
			if !strings.Contains(err.Error(), c.wantIn) {
				t.Errorf("error does not name %s: %v", c.wantIn, err)
			}
		})
	}
}

// The user identity keeps a session that has to survive container restarts, so a
// missing session directory is a configuration error, not a detail to default.
func TestUserIdentityRequiresSessionDir(t *testing.T) {
	flags := baseFlags()
	flags.SessionDir = ""

	_, err := Load(flags, envMap(baseEnv()))
	if err == nil {
		t.Fatal("user identity without a session directory was accepted")
	}
	if !strings.Contains(err.Error(), "session") {
		t.Errorf("error does not mention the session: %v", err)
	}
}

// A bot-only server needs no phone login and therefore no session directory.
func TestBotOnlyNeedsNoSession(t *testing.T) {
	flags := baseFlags()
	flags.Identities = "bot"
	flags.SessionDir = ""

	env := baseEnv()
	env[EnvBotToken] = "123:abc"
	env[EnvBotRead] = "-2001"

	if _, err := Load(flags, envMap(env)); err != nil {
		t.Fatalf("bot-only configuration rejected: %v", err)
	}
}

func TestHTTPTransportRequiresToken(t *testing.T) {
	flags := baseFlags()
	flags.Transport = TransportHTTP

	_, err := Load(flags, envMap(baseEnv()))
	if err == nil {
		t.Fatal("HTTP transport without a token was accepted")
	}
	if !strings.Contains(err.Error(), EnvAuthToken) {
		t.Errorf("error does not name %s: %v", EnvAuthToken, err)
	}

	env := baseEnv()
	env[EnvAuthToken] = "local-token"
	if _, err := Load(flags, envMap(env)); err != nil {
		t.Fatalf("HTTP transport with a token rejected: %v", err)
	}
}

func TestUnknownTransportIsRefused(t *testing.T) {
	flags := baseFlags()
	flags.Transport = "websocket"

	_, err := Load(flags, envMap(baseEnv()))
	if err == nil {
		t.Fatal("unknown transport was accepted")
	}
	if !strings.Contains(err.Error(), "websocket") {
		t.Errorf("error does not quote the unknown value: %v", err)
	}
}

// Writing enabled with nowhere to write means every writing tool would refuse every
// chat. Saying so at startup beats letting the caller discover it per chat.
func TestAllowWriteNeedsAWriteList(t *testing.T) {
	flags := baseFlags()
	flags.AllowWrite = true

	_, err := Load(flags, envMap(baseEnv()))
	if err == nil {
		t.Fatal("--allow-write with empty write lists was accepted")
	}

	env := baseEnv()
	env[EnvUserWrite] = "-1003"
	if _, err := Load(flags, envMap(env)); err != nil {
		t.Fatalf("--allow-write with a write list rejected: %v", err)
	}
}

func TestBadChatListIsReportedWithItsVariable(t *testing.T) {
	env := baseEnv()
	env[EnvUserRead] = "@some_group"

	_, err := Load(baseFlags(), envMap(env))
	if err == nil {
		t.Fatal("a chat name in the list was accepted")
	}
	if !strings.Contains(err.Error(), EnvUserRead) {
		t.Errorf("error does not name the offending variable: %v", err)
	}
}

func TestAPIIDMustBeNumeric(t *testing.T) {
	env := baseEnv()
	env[EnvAPIID] = "not-a-number"

	_, err := Load(baseFlags(), envMap(env))
	if err == nil {
		t.Fatal("non-numeric api id was accepted")
	}
	if !strings.Contains(err.Error(), EnvAPIID) {
		t.Errorf("error does not name %s: %v", EnvAPIID, err)
	}
}
