package deviceclient

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentmail "github.com/agentmail-to/agentmail-go"
)

type App struct {
	mailbox      *Mailbox
	store        *Store
	runner       *MCTRunner
	pollInterval time.Duration
	logger       *log.Logger
	verbose      bool
	pidfile      string
	processed    int
	threads      map[string]struct{}
}

func New(
	mailbox *Mailbox,
	store *Store,
	runner *MCTRunner,
	pollInterval time.Duration,
	logger *log.Logger,
	verbose bool,
	pidfile string,
) (*App, error) {
	if mailbox == nil {
		return nil, fmt.Errorf("mailbox is required")
	}
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if runner == nil {
		return nil, fmt.Errorf("mct runner is required")
	}
	if pollInterval <= 0 {
		return nil, fmt.Errorf("poll interval must be positive")
	}
	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}
	return &App{
		mailbox:      mailbox,
		store:        store,
		runner:       runner,
		pollInterval: pollInterval,
		logger:       logger,
		verbose:      verbose,
		pidfile:      pidfile,
		threads:      make(map[string]struct{}),
	}, nil
}

func (a *App) Run(ctx context.Context) (runErr error) {
	cleanup, err := a.start(ctx)
	if err != nil {
		return err
	}
	defer func() {
		runErr = errors.Join(runErr, cleanup())
	}()
	a.logger.Printf(
		"Device Client started. Polling %s every %s. Project: %s.",
		a.mailbox.inboxID,
		a.pollInterval,
		a.runner.projectDir,
	)

	for {
		if err := a.ProcessOnce(ctx); err != nil {
			if ctx.Err() != nil {
				a.logShutdown()
				return nil
			}
			return err
		}

		timer := time.NewTimer(a.pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			a.logShutdown()
			return nil
		case <-timer.C:
		}
	}
}

func (a *App) RunOnce(ctx context.Context) (runErr error) {
	cleanup, err := a.start(ctx)
	if err != nil {
		return err
	}
	defer func() {
		runErr = errors.Join(runErr, cleanup())
	}()
	return a.ProcessOnce(ctx)
}

func (a *App) start(ctx context.Context) (func() error, error) {
	if err := a.runner.Sync(ctx); err != nil {
		return nil, err
	}
	cleanup, err := createPIDFile(a.pidfile)
	if err != nil {
		return nil, err
	}
	return cleanup, nil
}

func (a *App) ProcessOnce(ctx context.Context) error {
	if err := a.recoverPending(ctx); err != nil {
		return err
	}

	messages, err := a.mailbox.Poll(ctx)
	if err != nil {
		return err
	}
	if a.verbose {
		a.logger.Printf("poll: %d unread messages", len(messages))
	}
	for _, message := range messages {
		seen, err := a.store.Seen(message.MessageID)
		if err != nil {
			return err
		}
		if seen {
			if err := a.mailbox.MarkProcessed(ctx, message.MessageID); err != nil {
				return err
			}
			continue
		}
		if err := a.processMessage(ctx, message); err != nil {
			return fmt.Errorf("process message %s: %w", message.MessageID, err)
		}
	}
	return nil
}

func (a *App) recoverPending(ctx context.Context) error {
	pendingMessages, err := a.store.Pending()
	if err != nil {
		return err
	}
	for _, pending := range pendingMessages {
		message, err := a.mailbox.Message(ctx, pending.MessageID)
		if err != nil {
			return err
		}
		if err := a.processPending(ctx, message, pending, true); err != nil {
			return fmt.Errorf("recover message %s: %w", pending.MessageID, err)
		}
	}
	return nil
}

func (a *App) processMessage(ctx context.Context, message agentmail.Message) error {
	pending, existed, err := a.store.BeginMessage(message.MessageID, message.ThreadID)
	if err != nil {
		return err
	}
	return a.processPending(ctx, message, pending, existed)
}

func (a *App) processPending(
	ctx context.Context,
	message agentmail.Message,
	pending PendingMessage,
	recovering bool,
) error {
	if recovering {
		outboundMessageID, found, err := a.mailbox.ReplyReceipt(ctx, message)
		if err != nil {
			return err
		}
		if found {
			if err := a.store.Complete(
				message.MessageID,
				sessionStatus(pending.ResultKind),
				outboundMessageID,
			); err != nil {
				return err
			}
			a.recordProcessed(message.ThreadID)
			if err := a.mailbox.MarkProcessed(ctx, message.MessageID); err != nil {
				return err
			}
			a.logger.Printf(
				"recovered receipt message=%s thread=%s session=%s outbound=%s",
				message.MessageID,
				message.ThreadID,
				pending.Session.SessionID,
				outboundMessageID,
			)
			return nil
		}
	}

	var history []agentmail.Message
	var err error
	if !pending.Session.IsNew {
		history, err = a.mailbox.Thread(ctx, message.ThreadID)
		if err != nil {
			return err
		}
	}

	prompt := formatPrompt(message, pending.Session, history)
	finalPath := recoveryResultPath(pending.Session.SessionID, message.MessageID)
	var result RunResult
	switch pending.State {
	case messageReceived:
		if err := a.store.MarkRunning(message.MessageID, prompt); err != nil {
			return err
		}
		result, err = a.runner.Run(ctx, pending.Session, prompt, finalPath)
	case messageRunning:
		result, err = a.runner.Recover(
			ctx,
			pending.Session,
			pending.Prompt,
			finalPath,
		)
	case messageResultReady:
		result = RunResult{
			Kind: pending.ResultKind,
			Text: pending.ResultText,
		}
	default:
		return fmt.Errorf("unsupported pending message state %q", pending.State)
	}
	if err != nil {
		return err
	}
	if pending.State != messageResultReady {
		if err := a.store.StoreResult(message.MessageID, result); err != nil {
			return err
		}
		if err := os.Remove(finalPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove durable mct result: %w", err)
		}
	}

	key := idempotencyKey(pending.Session.SessionID, message.MessageID)
	outboundMessageID, err := a.mailbox.Reply(
		ctx,
		message.MessageID,
		result.Text,
		key,
	)
	if err != nil {
		return err
	}

	if err := a.store.Complete(
		message.MessageID,
		sessionStatus(result.Kind),
		outboundMessageID,
	); err != nil {
		return err
	}
	a.recordProcessed(message.ThreadID)
	if err := os.Remove(finalPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove durable mct result: %w", err)
	}
	if err := a.mailbox.MarkProcessed(ctx, message.MessageID); err != nil {
		return err
	}

	a.logger.Printf(
		"processed message=%s thread=%s session=%s result=%s",
		message.MessageID,
		message.ThreadID,
		pending.Session.SessionID,
		result.Kind,
	)
	return nil
}

func (a *App) recordProcessed(threadID string) {
	a.processed++
	a.threads[threadID] = struct{}{}
}

func (a *App) logShutdown() {
	a.logger.Printf(
		"Device Client shutting down. Processed %d messages across %d threads.",
		a.processed,
		len(a.threads),
	)
}

func sessionStatus(kind ResultKind) string {
	if kind == ResultQuestion {
		return "suspended_user_input"
	}
	return "completed"
}

func recoveryResultPath(sessionID, messageID string) string {
	return filepath.Join(
		os.TempDir(),
		"device-client-mct-results",
		idempotencyKey(sessionID, messageID)+".md",
	)
}

func formatPrompt(
	message agentmail.Message,
	session Session,
	history []agentmail.Message,
) string {
	var prompt strings.Builder
	fmt.Fprintf(
		&prompt,
		"[DearMachine: email from %s received at %s]\n",
		message.From,
		messageTime(message).UTC().Format(time.RFC3339),
	)
	fmt.Fprintf(
		&prompt,
		"[Thread: %s | Session: %s | Sequence: %d]\n",
		message.ThreadID,
		session.SessionID,
		session.Sequence,
	)

	contextMessages := withoutMessage(history, message.MessageID)
	if len(contextMessages) > 0 {
		prompt.WriteString("[Previous messages in this thread:]\n")
		for _, previous := range contextMessages {
			role := "Agent"
			if strings.EqualFold(previous.From, message.From) {
				role = "User"
			}
			fmt.Fprintf(&prompt, "[%s:] %s\n\n", role, messageBody(previous))
		}
	}

	prompt.WriteString("\n")
	prompt.WriteString(messageBody(message))
	prompt.WriteString("\n\n[End of email]")
	return prompt.String()
}

func withoutMessage(messages []agentmail.Message, messageID string) []agentmail.Message {
	result := make([]agentmail.Message, 0, len(messages))
	for _, message := range messages {
		if message.MessageID != messageID {
			result = append(result, message)
		}
	}
	const contextLimit = 10
	if len(result) > contextLimit {
		result = result[len(result)-contextLimit:]
	}
	return result
}

func idempotencyKey(sessionID, messageID string) string {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + messageID))
	return fmt.Sprintf("dearmachine-%x", sum[:])
}
