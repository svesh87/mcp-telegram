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
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"golang.org/x/term"
)

// Attempts allowed for the two things a person types by hand. Both are refused by
// Telegram without ending the attempt, so asking again is cheaper than starting over.
const (
	codeAttempts     = 3
	passwordAttempts = 3
)

// Login authorises the session file by asking for the phone number and the code Telegram
// sends.
//
// It is a separate command rather than a step of starting the server: the code arrives in
// the account owner's Telegram, so only they can complete it, and a server that waits for
// a human at startup is a server that hangs. Run it once, and the session file carries the
// authorisation from then on.
func Login(ctx context.Context, opts UserOptions, in io.Reader, out io.Writer) error {
	if opts.SessionDir == "" {
		return errors.New("session directory is required: the authorisation has to be saved somewhere")
	}

	if err := os.MkdirAll(opts.SessionDir, 0o700); err != nil {
		return fmt.Errorf("session directory %s: %w", opts.SessionDir, err)
	}

	err := login(ctx, opts, in, out)

	// AUTH_RESTART means the attempt before this one died half way through, and Telegram
	// will refuse every new code until the temporary key it was tied to is gone. That key
	// lives in the session file, and an unauthorised session file is worth nothing, so it
	// is set aside and the whole thing runs once more.
	if err != nil && tgerr.Is(err, "AUTH_RESTART") {
		fmt.Fprintln(out, "Telegram wants the login started over: the previous attempt did not finish.")

		moved, resetErr := resetSession(opts.SessionDir)
		if resetErr != nil {
			return fmt.Errorf("%w (and the session file could not be moved aside: %v)", err, resetErr)
		}
		fmt.Fprintf(out, "the old session file was moved to %s, trying again\n", filepath.Base(moved))

		return login(ctx, opts, in, out)
	}

	return err
}

func login(ctx context.Context, opts UserOptions, in io.Reader, out io.Writer) error {
	client := telegram.NewClient(opts.APIID, opts.APIHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: filepath.Join(opts.SessionDir, SessionFile)},
		NoUpdates:      true,
	})

	return client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("checking authorization: %w", err)
		}

		// Running the command on an authorised session is a no-op rather than a second
		// login: a session that works must not be traded for a new one by accident.
		if status.Authorized {
			fmt.Fprintf(out, "this session is already authorized as %s (id %d)\n",
				userName(status.User), status.User.ID)
			return nil
		}

		prompt := &terminalAuth{in: bufio.NewReader(in), out: out}
		if err := signIn(ctx, client.Auth(), prompt, out); err != nil {
			return err
		}

		self, err := client.Self(ctx)
		if err != nil {
			return fmt.Errorf("reading the signed in account: %w", err)
		}

		fmt.Fprintf(out, "signed in as %s (id %d, username %q)\n",
			userName(self), self.ID, self.Username)
		fmt.Fprintf(out, "session saved to %s\n", filepath.Join(opts.SessionDir, SessionFile))

		return nil
	})
}

// authClient is the part of gotd's auth client this flow uses. Declared as an interface so
// the retries can be tested without Telegram; they are the whole point of not using the
// ready-made flow.
type authClient interface {
	SendCode(ctx context.Context, phone string, options auth.SendCodeOptions) (tg.AuthSentCodeClass, error)
	SignIn(ctx context.Context, phone, code, codeHash string) (*tg.AuthAuthorization, error)
	Password(ctx context.Context, password string) (*tg.AuthAuthorization, error)
}

// signIn walks the sign-in by hand instead of using gotd's ready-made flow.
//
// The flow gives up on a mistyped code or a mistyped two-factor password, and Telegram
// then answers the next attempt with AUTH_RESTART, so one slip costs a new code and a new
// round. Both are things a person types from memory; here each is asked again.
func signIn(ctx context.Context, client authClient, prompt *terminalAuth, out io.Writer) error {
	phone, err := prompt.Phone(ctx)
	if err != nil {
		return err
	}

	sent, err := client.SendCode(ctx, phone, auth.SendCodeOptions{})
	if err != nil {
		return fmt.Errorf("send code: %w", err)
	}

	code, ok := sent.(*tg.AuthSentCode)
	if !ok {
		// auth.sentCodeSuccess: Telegram accepted a future auth token and asked for
		// nothing. Nothing left to type.
		fmt.Fprintln(out, "Telegram accepted the login without a code.")
		return nil
	}

	for attempt := 1; ; attempt++ {
		typed, err := prompt.Code(ctx, code)
		if err != nil {
			return err
		}

		_, err = client.SignIn(ctx, phone, typed, code.PhoneCodeHash)

		switch {
		case err == nil:
			return nil

		case errors.Is(err, auth.ErrPasswordAuthNeeded):
			return password(ctx, client, prompt, out)

		case tgerr.Is(err, "PHONE_CODE_INVALID") && attempt < codeAttempts:
			fmt.Fprintln(out, "that code was refused, try again.")
			continue

		case tgerr.Is(err, "PHONE_CODE_EXPIRED"):
			return errors.New("the code has expired: run the login again and Telegram will send a new one")

		default:
			var signUp *auth.SignUpRequired
			if errors.As(err, &signUp) {
				return errors.New("this phone number has no Telegram account, " +
					"and this command does not create one")
			}
			return fmt.Errorf("sign in: %w", err)
		}
	}
}

// password asks for the two-factor password until it fits or the attempts run out.
func password(ctx context.Context, client authClient, prompt *terminalAuth, out io.Writer) error {
	for attempt := 1; ; attempt++ {
		typed, err := prompt.Password(ctx)
		if err != nil {
			return err
		}

		_, err = client.Password(ctx, typed)

		switch {
		case err == nil:
			return nil

		case errors.Is(err, auth.ErrPasswordInvalid) || tgerr.Is(err, "PASSWORD_HASH_INVALID"):
			if attempt >= passwordAttempts {
				return errors.New("the two-factor password was refused and the attempts are used up: " +
					"run the login again")
			}
			fmt.Fprintf(out, "that password was refused, attempts left: %d\n", passwordAttempts-attempt)

		default:
			return fmt.Errorf("password: %w", err)
		}
	}
}

// resetSession moves an unauthorised session file aside and returns where it went. It is
// never deleted: the caller has just failed to log in, and a file nobody can read is still
// better than a file nobody can find.
func resetSession(dir string) (string, error) {
	path := filepath.Join(dir, SessionFile)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	moved := fmt.Sprintf("%s.replaced-%d", path, time.Now().Unix())
	if err := os.Rename(path, moved); err != nil {
		return "", err
	}

	return moved, nil
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

// Password is read without echo. The two-factor password is a secret like any other, and a
// terminal keeps a copy of what it prints.
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
