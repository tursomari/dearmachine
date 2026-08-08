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

	"github.com/dearmachine/dearmachine/internal/synctrigger"
)

type App struct {
	transport        Transport
	store            *Store
	runner           *MCTRunner
	syncOrchestrator *synctrigger.Orchestrator
	pollInterval     time.Duration
	logger           *log.Logger
	verbose          bool
	pidfile          string
	processed        int
	threads          map[string]struct{}
}

func New(
	transport Transport,
	store *Store,
	runner *MCTRunner,
	syncOrchestrator *synctrigger.Orchestrator,
	pollInterval time.Duration,
	logger *log.Logger,
	verbose bool,
	pidfile string,
) (*App, error) {
	if transport == nil {
		return nil, fmt.Errorf("transport is required")
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
		transport:        transport,
		store:            store,
		runner:           runner,
		syncOrchestrator: syncOrchestrator,
		pollInterval:     pollInterval,
		logger:           logger,
		verbose:          verbose,
		pidfile:          pidfile,
		threads:          make(map[string]struct{}),
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
		a.pollTarget(),
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
		if a.syncOrchestrator != nil {
			if err := a.syncOrchestrator.OrchestrateSync(ctx); err != nil {
				a.logger.Printf("sync trigger: %v", err)
			}
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

	messages, err := a.transport.Poll(ctx)
	if err != nil {
		return err
	}
	if a.verbose {
		a.logger.Printf("poll: %d unread messages", len(messages))
	}
	for _, message := range messages {
		skipped, err := a.store.IsSkipped(message.MessageID)
		if err != nil {
			return err
		}
		if skipped {
			if a.verbose {
				a.logger.Printf(
					"poll: locally skipped message=%s thread=%s",
					message.MessageID,
					message.ThreadID,
				)
			}
			continue
		}
		seen, err := a.store.Seen(message.MessageID)
		if err != nil {
			return err
		}
		if seen {
			if err := a.transport.MarkProcessed(ctx, message.MessageID); err != nil {
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
		message, err := a.transport.Message(ctx, pending.MessageID)
		if err != nil {
			return err
		}
		if err := a.processPending(ctx, message, pending, true); err != nil {
			return fmt.Errorf("recover message %s: %w", pending.MessageID, err)
		}
	}
	return nil
}

func (a *App) processMessage(ctx context.Context, message Message) error {
	pending, existed, err := a.store.BeginMessage(message.MessageID, message.ThreadID)
	if err != nil {
		return err
	}
	return a.processPending(ctx, message, pending, existed)
}

func (a *App) processPending(
	ctx context.Context,
	message Message,
	pending PendingMessage,
	recovering bool,
) error {
	if recovering {
		outboundMessageID, found, err := a.transport.ReplyReceipt(ctx, message)
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
			if err := a.transport.MarkProcessed(ctx, message.MessageID); err != nil {
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

	prompt := formatPrompt(message, pending.Session)
	finalPath := recoveryResultPath(pending.Session.SessionID, message.MessageID)
	var result RunResult
	var err error
	switch pending.State {
	case messageReceived:
		checkpointSessionID := ""
		if !pending.Session.IsNew {
			checkpointSessionID, err = a.runner.ForkSession(ctx, pending.Session.SessionID)
			if err != nil {
				return fmt.Errorf("checkpoint committed mct session before follow-up: %w", err)
			}
		}
		if err := a.store.MarkRunningWithCheckpoint(
			message.MessageID,
			prompt,
			checkpointSessionID,
		); err != nil {
			if checkpointSessionID != "" {
				cleanupErr := a.runner.DeleteSession(ctx, checkpointSessionID)
				if cleanupErr != nil {
					cleanupErr = fmt.Errorf(
						"clean unused mct checkpoint %s: %w",
						checkpointSessionID,
						cleanupErr,
					)
				}
				return errors.Join(err, cleanupErr)
			}
			return err
		}
		pending.CheckpointSessionID = checkpointSessionID
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
	outboundMessageID, err := a.transport.Reply(
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
	if err := a.transport.MarkProcessed(ctx, message.MessageID); err != nil {
		return err
	}
	if pending.CheckpointSessionID != "" {
		if err := a.runner.DeleteSession(ctx, pending.CheckpointSessionID); err != nil {
			a.logger.Printf(
				"checkpoint cleanup failed message=%s session=%s checkpoint=%s error=%v",
				message.MessageID,
				pending.Session.SessionID,
				pending.CheckpointSessionID,
				err,
			)
		}
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
	message Message,
	session Session,
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

	prompt.WriteString("\n")
	prompt.WriteString(messageBody(message))
	prompt.WriteString("\n\n[End of email]")
	return prompt.String()
}

func (a *App) pollTarget() string {
	if target, ok := a.transport.(interface{ pollTarget() string }); ok {
		return target.pollTarget()
	}
	return "email"
}

func idempotencyKey(sessionID, messageID string) string {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + messageID))
	return fmt.Sprintf("dearmachine-%x", sum[:])
}
