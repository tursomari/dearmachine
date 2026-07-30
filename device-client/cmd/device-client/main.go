package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	agentmail "github.com/agentmail-to/agentmail-go"
	"github.com/dearmachine/device-client-spike/internal/deviceclient"
)

func main() {
	if err := run(); err != nil {
		log.Printf("device client: %v", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		inboxID = flag.String("inbox-id", "", "AgentMail inbox ID")
		dbPath  = flag.String(
			"db",
			"device-client.db",
			"SQLite state database path",
		)
		projectDir = flag.String(
			"project",
			".",
			"project directory used as the mct-agent working directory",
		)
		model = flag.String(
			"model",
			"",
			"optional mct-agent model alias; project default when omitted",
		)
		mctBinary = flag.String(
			"mct-agent",
			"mct-agent",
			"path to the mct-agent executable",
		)
		pollInterval = flag.Duration(
			"poll-interval",
			60*time.Second,
			"delay after each completed AgentMail poll",
		)
		once    = flag.Bool("once", false, "poll once, process available messages, and exit")
		verbose = flag.Bool("verbose", false, "log every AgentMail poll cycle")
	)
	flag.Parse()

	if *inboxID == "" {
		return fmt.Errorf("--inbox-id is required")
	}
	if os.Getenv("AGENTMAIL_API_KEY") == "" {
		return fmt.Errorf("AGENTMAIL_API_KEY is required")
	}

	store, err := deviceclient.OpenStore(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	mailbox, err := deviceclient.NewMailbox(agentmail.NewClient(), *inboxID)
	if err != nil {
		return err
	}
	runner, err := deviceclient.NewMCTRunner(*mctBinary, *projectDir, *model)
	if err != nil {
		return err
	}
	logger := log.New(os.Stderr, "device-client: ", log.LstdFlags)
	app, err := deviceclient.New(
		mailbox,
		store,
		runner,
		*pollInterval,
		logger,
		*verbose,
	)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	if *once {
		if err := runner.Sync(ctx); err != nil {
			return err
		}
		return app.ProcessOnce(ctx)
	}
	return app.Run(ctx)
}
