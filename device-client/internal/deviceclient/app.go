package deviceclient

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
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
}

func New(
	mailbox *Mailbox,
	store *Store,
	runner *MCTRunner,
	pollInterval time.Duration,
	logger *log.Logger,
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
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	if err := a.runner.Sync(ctx); err != nil {
		return err
	}

	for {
		if err := a.ProcessOnce(ctx); err != nil {
			if ctx.Err() != nil {
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
			return nil
		case <-timer.C:
		}
	}
}

func (a *App) ProcessOnce(ctx context.Context) error {
	messages, err := a.mailbox.Poll(ctx)
	if err != nil {
		return err
	}
	for _, message := range messages {
		seen, err := a.store.Seen(message.MessageID)
		if err != nil {
			return err
		}
		if seen {
			continue
		}
		if err := a.processMessage(ctx, message); err != nil {
			return fmt.Errorf("process message %s: %w", message.MessageID, err)
		}
	}
	return nil
}

func (a *App) processMessage(ctx context.Context, message agentmail.Message) error {
	session, err := a.store.ReserveSession(message.ThreadID)
	if err != nil {
		return err
	}

	var history []agentmail.Message
	if !session.IsNew {
		history, err = a.mailbox.Thread(ctx, message.ThreadID)
		if err != nil {
			return err
		}
	}

	prompt := formatPrompt(message, session, history)
	result, err := a.runner.Run(ctx, session, prompt)
	if err != nil {
		return err
	}

	key := idempotencyKey(session.SessionID, message.MessageID)
	if err := a.mailbox.Reply(ctx, message.MessageID, result.Text, key); err != nil {
		return err
	}

	status := "completed"
	if result.Kind == ResultQuestion {
		status = "suspended_user_input"
	}
	if err := a.store.Complete(message.MessageID, message.ThreadID, status); err != nil {
		return err
	}
	if err := a.mailbox.MarkProcessed(ctx, message.MessageID); err != nil {
		return err
	}

	a.logger.Printf(
		"processed message=%s thread=%s session=%s result=%s",
		message.MessageID,
		message.ThreadID,
		session.SessionID,
		result.Kind,
	)
	return nil
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
