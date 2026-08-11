// Command mcp-telegram serves Telegram to an MCP client as two separate identities,
// each with its own read and write lists.
//
// The lists live in the server's configuration, and a caller holds no Telegram
// credentials of its own. That is the point of the whole thing: reaching a chat means
// going through this server, and this server checks the lists first.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/svesh87/mcp-telegram/internal/access"
	"github.com/svesh87/mcp-telegram/internal/config"
	"github.com/svesh87/mcp-telegram/internal/telegram"
	"github.com/svesh87/mcp-telegram/internal/tools"
	"github.com/svesh87/mcp-telegram/internal/transport"
)

// version is set at build time. A build without it says so rather than claiming a number.
var version = "dev"

// startupTimeout bounds the wait for the Telegram connection at startup. Serving tools
// that cannot work is worse than exiting: a container that exits gets restarted, and the
// reason ends up in its log.
const startupTimeout = 2 * time.Minute

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mcp-telegram: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	// The login command is separate because it needs a person: the code arrives in
	// Telegram, and nobody but the account owner can type it back.
	if len(args) > 0 && args[0] == "login" {
		return login(args[1:])
	}

	flags := flag.NewFlagSet("mcp-telegram", flag.ContinueOnError)
	var (
		identities  = flags.String("identities", "", "which identities to run: user, bot, or user,bot")
		allowWrite  = flags.Bool("allow-write", false, "register the writing tools (off by default)")
		transportID = flags.String("transport", config.TransportStdio,
			"transport: "+config.TransportStdio+" or "+config.TransportHTTP)
		address     = flags.String("address", "127.0.0.1:8815", "address to listen on with "+config.TransportHTTP)
		sessionDir  = flags.String("session-dir", "", "directory holding the MTProto session of the user identity")
		downloadDir = flags.String("download-dir", "", "directory for downloaded files; without it the download tools are not offered")
		showVersion = flags.Bool("version", false, "print the version and exit")
	)

	if err := flags.Parse(args); err != nil {
		return err
	}

	if *showVersion {
		fmt.Println("mcp-telegram " + version)
		return nil
	}

	cfg, err := config.Load(config.Flags{
		Identities:  *identities,
		AllowWrite:  *allowWrite,
		Transport:   *transportID,
		Address:     *address,
		SessionDir:  *sessionDir,
		DownloadDir: *downloadDir,
	}, config.OSEnv)
	if err != nil {
		return err
	}

	return serve(cfg)
}

func serve(cfg *config.Config) error {
	// Signals are handled here so that a stop closes the Telegram connection and lets the
	// session file be written out, rather than leaving it as it was mid-flight.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	checker := access.New(cfg.Lists)

	opts := tools.Options{
		Checker:     checker,
		Identities:  cfg.Identities,
		AllowWrite:  cfg.AllowWrite,
		DownloadDir: cfg.DownloadDir,
	}

	if cfg.DownloadDir != "" {
		if err := os.MkdirAll(cfg.DownloadDir, 0o700); err != nil {
			return fmt.Errorf("download directory %s: %w", cfg.DownloadDir, err)
		}
	}

	finished := make(chan error, 1)

	if cfg.Uses(access.User) {
		user, err := telegram.NewUser(telegram.UserOptions{
			APIID:      cfg.APIID,
			APIHash:    cfg.APIHash,
			SessionDir: cfg.SessionDir,
		})
		if err != nil {
			return err
		}

		go func() { finished <- user.Run(ctx) }()

		ready, cancel := context.WithTimeout(ctx, startupTimeout)
		defer cancel()
		if err := user.Wait(ready); err != nil {
			return fmt.Errorf("connecting as the user identity: %w", err)
		}

		opts.UserRead = user
		if cfg.AllowWrite {
			opts.UserWrite = user
		}
	}

	if cfg.Uses(access.Bot) {
		bot, err := telegram.NewBot(telegram.BotOptions{Token: cfg.BotToken})
		if err != nil {
			return err
		}

		opts.BotRead = bot
		if cfg.AllowWrite {
			opts.BotWrite = bot
		}
	}

	mcpServer := server.NewMCPServer("mcp-telegram", version, server.WithToolCapabilities(true))
	if err := tools.Register(mcpServer, opts); err != nil {
		return err
	}

	served := make(chan error, 1)
	go func() {
		switch cfg.Transport {
		case config.TransportHTTP:
			served <- transport.ServeHTTP(mcpServer, cfg.Address, cfg.AuthToken)
		default:
			served <- transport.ServeStdio(mcpServer)
		}
	}()

	select {
	case err := <-served:
		return err
	case err := <-finished:
		// The Telegram connection dropped for good. Serving on would answer every tool
		// with the same failure.
		if err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("the Telegram connection stopped: %w", err)
		}
		return nil
	case <-ctx.Done():
		return nil
	}
}

// login authorises the session file. It shares the credentials and the session directory
// with the server, and nothing else.
func login(args []string) error {
	flags := flag.NewFlagSet("mcp-telegram login", flag.ContinueOnError)
	sessionDir := flags.String("session-dir", "", "directory to save the MTProto session in")

	if err := flags.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(config.Flags{
		Identities: "user",
		Transport:  config.TransportStdio,
		SessionDir: *sessionDir,
	}, config.OSEnv)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return telegram.Login(ctx, telegram.UserOptions{
		APIID:      cfg.APIID,
		APIHash:    cfg.APIHash,
		SessionDir: cfg.SessionDir,
	}, os.Stdin, os.Stdout)
}
