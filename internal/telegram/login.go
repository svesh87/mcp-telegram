package telegram

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"golang.org/x/term"
)

// Login authorises the session file by asking for the phone number and the code
// Telegram sends.
//
// It is a separate command rather than a step of starting the server: the code arrives
// in the account owner's Telegram, so only they can complete it, and a server that
// waits for a human at startup is a server that hangs. Run it once, and the session
// file carries the authorisation from then on.
func Login(ctx context.Context, opts UserOptions, in io.Reader, out io.Writer) error {
	if opts.SessionDir == "" {
		return errors.New("session directory is required: the authorisation has to be saved somewhere")
	}

	if err := os.MkdirAll(opts.SessionDir, 0o700); err != nil {
		return fmt.Errorf("session directory %s: %w", opts.SessionDir, err)
	}

	client := telegram.NewClient(opts.APIID, opts.APIHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: filepath.Join(opts.SessionDir, SessionFile)},
		NoUpdates:      true,
	})

	return client.Run(ctx, func(ctx context.Context) error {
		flow := auth.NewFlow(&terminalAuth{in: bufio.NewReader(in), out: out}, auth.SendCodeOptions{})
		if err := client.Auth().IfNecessary(ctx, flow); err != nil {
			return fmt.Errorf("signing in: %w", err)
		}

		self, err := client.Self(ctx)
		if err != nil {
			return fmt.Errorf("reading the signed in account: %w", err)
		}

		fmt.Fprintf(out, "signed in as %s (id %d, username %q)\n", userName(self), self.ID, self.Username)
		fmt.Fprintf(out, "session saved to %s\n", filepath.Join(opts.SessionDir, SessionFile))

		return nil
	})
}

// terminalAuth asks the operator for what only they have.
type terminalAuth struct {
	in  *bufio.Reader
	out io.Writer
}

func (t *terminalAuth) Phone(_ context.Context) (string, error) {
	return t.ask("phone number, with the country code: ")
}

func (t *terminalAuth) Code(_ context.Context, sentCode *tg.AuthSentCode) (string, error) {
	fmt.Fprintf(t.out, "Telegram sent a code (%s)\n", codeKind(sentCode.Type))
	return t.ask("code: ")
}

// Password is read without echo. The two-factor password is a secret like any other,
// and a terminal keeps a copy of what it prints.
func (t *terminalAuth) Password(_ context.Context) (string, error) {
	fmt.Fprint(t.out, "two-factor password (not shown): ")

	if term.IsTerminal(int(os.Stdin.Fd())) {
		typed, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(t.out)
		if err != nil {
			return "", err
		}
		return string(typed), nil
	}

	return t.ask("")
}

func (t *terminalAuth) AcceptTermsOfService(_ context.Context, tos tg.HelpTermsOfService) error {
	fmt.Fprintln(t.out, "Telegram asks to accept its terms of service:")
	fmt.Fprintln(t.out, tos.Text)
	return errors.New("terms of service have to be accepted in a Telegram client first")
}

// SignUp refuses. This server signs an existing account in; it does not create one.
func (t *terminalAuth) SignUp(_ context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("this phone number has no Telegram account, " +
		"and this command does not create one")
}

func (t *terminalAuth) ask(prompt string) (string, error) {
	if prompt != "" {
		fmt.Fprint(t.out, prompt)
	}

	line, err := t.in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}

	answer := strings.TrimSpace(line)
	if answer == "" {
		return "", errors.New("nothing entered")
	}

	return answer, nil
}

// codeKind says where to look for the code, because Telegram picks the channel.
func codeKind(kind tg.AuthSentCodeTypeClass) string {
	switch kind.(type) {
	case *tg.AuthSentCodeTypeApp:
		return "in the Telegram app"
	case *tg.AuthSentCodeTypeSMS:
		return "by SMS"
	case *tg.AuthSentCodeTypeCall:
		return "by phone call"
	case *tg.AuthSentCodeTypeFlashCall, *tg.AuthSentCodeTypeMissedCall:
		return "as the last digits of a calling number"
	case *tg.AuthSentCodeTypeEmailCode, *tg.AuthSentCodeTypeSetUpEmailRequired:
		return "by email"
	case *tg.AuthSentCodeTypeFragmentSMS:
		return "through Fragment"
	default:
		return "somewhere Telegram chose"
	}
}
