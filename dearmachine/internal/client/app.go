package client

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dearmachine/dearmachine/internal/synctrigger"
)

type App struct {
	transport        Transport
	store            *Store
	runner           *AgentRunner
	syncOrchestrator *synctrigger.Orchestrator
	concurrency      int
	pollInterval     time.Duration
	logger           *log.Logger
	verbose          bool
	pidfile          string
	responseTier     ResponseTier
	statsMu          sync.Mutex
	processed        int
	threads          map[string]struct{}
}

func New(
	transport Transport,
	store *Store,
	runner *AgentRunner,
	syncOrchestrator *synctrigger.Orchestrator,
	concurrency int,
	pollInterval time.Duration,
	logger *log.Logger,
	verbose bool,
	pidfile string,
	responseTier ResponseTier,
) (*App, error) {
	if transport == nil {
		return nil, fmt.Errorf("transport is required")
	}
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if runner == nil {
		return nil, fmt.Errorf("agent runner is required")
	}
	if concurrency < 1 {
		return nil, fmt.Errorf("concurrency must be at least 1")
	}
	if pollInterval <= 0 {
		return nil, fmt.Errorf("poll interval must be positive")
	}
	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}
	if responseTier == "" {
		responseTier = TierPlain
	}
	if _, err := ParseResponseTier(string(responseTier)); err != nil {
		return nil, err
	}
	return &App{
		transport:        transport,
		store:            store,
		runner:           runner,
		syncOrchestrator: syncOrchestrator,
		concurrency:      concurrency,
		pollInterval:     pollInterval,
		logger:           logger,
		verbose:          verbose,
		pidfile:          pidfile,
		responseTier:     responseTier,
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
		"DearMachine Client started. Polling %s every %s. Project: %s.",
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
	work := newThreadWorkQueue()
	if err := a.recoverPending(ctx, work); err != nil {
		return err
	}
	if err := a.pollAndClaim(ctx, work); err != nil {
		return err
	}
	return a.dispatch(ctx, work)
}

func (a *App) pollAndClaim(ctx context.Context, work *threadWorkQueue) error {
	messages, err := a.transport.Poll(ctx)
	if err != nil {
		return err
	}
	if a.verbose {
		a.logger.Printf("poll: %d unread messages", len(messages))
	}
	for _, message := range messages {
		if containsFold(message.Labels, "sent") {
			if a.verbose {
				a.logger.Printf("poll: locally skipped own outbound message=%s thread=%s", message.MessageID, message.ThreadID)
			}
			if err := a.transport.MarkProcessed(ctx, message.MessageID); err != nil {
				a.logger.Printf("poll: clear own outbound message=%s thread=%s: %v", message.MessageID, message.ThreadID, err)
			}
			continue
		}
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
		pending, existed, err := a.store.BeginMessage(message.MessageID, message.ThreadID, a.responseTier)
		if errors.Is(err, errMessageSkipped) {
			if a.verbose {
				a.logger.Printf(
					"poll: locally skipped message=%s thread=%s during claim",
					message.MessageID,
					message.ThreadID,
				)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("claim message %s: %w", message.MessageID, err)
		}
		work.enqueue(messageWork{message: message, pending: pending, recovering: existed})
	}
	return nil
}

func (a *App) recoverPending(ctx context.Context, work *threadWorkQueue) error {
	pendingMessages, err := a.store.Pending()
	if err != nil {
		return err
	}
	grouped := make(map[string][]PendingMessage)
	for _, pending := range pendingMessages {
		grouped[pending.ThreadID] = append(grouped[pending.ThreadID], pending)
	}
	for threadID := range grouped {
		sort.SliceStable(grouped[threadID], func(left, right int) bool {
			return grouped[threadID][left].Session.Sequence <
				grouped[threadID][right].Session.Sequence
		})
	}
	groupIndexes := make(map[string]int, len(grouped))
	// Preserve the store's global recovery order while replacing each thread's
	// slots with its sequence-sorted items. The dispatcher admits only the first
	// eligible item from any one of these thread groups at a time.
	for _, slot := range pendingMessages {
		index := groupIndexes[slot.ThreadID]
		pending := grouped[slot.ThreadID][index]
		groupIndexes[slot.ThreadID] = index + 1
		message, err := a.transport.Message(ctx, pending.MessageID)
		if err != nil {
			return err
		}
		work.enqueue(messageWork{message: message, pending: pending, recovering: true})
	}
	return nil
}

func (a *App) processWork(ctx context.Context, work messageWork) error {
	pending, found, err := a.store.PendingByID(work.pending.MessageID)
	if err != nil {
		return err
	}
	if !found {
		skipped, err := a.store.IsSkipped(work.pending.MessageID)
		if err != nil {
			return err
		}
		if skipped {
			return nil
		}
		seen, err := a.store.Seen(work.pending.MessageID)
		if err != nil {
			return err
		}
		if seen {
			return a.transport.MarkProcessed(ctx, work.pending.MessageID)
		}
		return fmt.Errorf("pending message disappeared before dispatch")
	}
	return a.processPending(ctx, work.message, pending, work.recovering)
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
			if err := a.cleanupStagingDirs(pending); err != nil {
				a.logger.Printf(
					"staging cleanup failed message=%s thread=%s error=%v",
					message.MessageID,
					message.ThreadID,
					err,
				)
			}
			return nil
		}
	}

	prompt := formatPrompt(message, pending.Session)
	finalPath := recoveryResultPath(pending.Session.SessionID, message.MessageID)
	tc, err := a.turnContext(ctx, pending)
	if err != nil {
		return err
	}
	if pending.State == messageReceived {
		if err := a.resetTurnDirs(pending); err != nil {
			return err
		}
		tier := a.tierFor(pending)
		if tier != TierPlain && len(message.Attachments) > 0 {
			if _, _, err := StageInbox(
				ctx,
				a.transport,
				a.runner.projectDir,
				TurnKey(pending.Session.Sequence, message.MessageID),
				message.Attachments,
				DefaultAttachmentLimits(),
			); err != nil {
				return fmt.Errorf("stage inbound attachments: %w", err)
			}
		}
	}
	var result RunResult
	switch pending.State {
	case messageReceived:
		checkpointSessionID := ""
		if !pending.Session.IsNew {
			checkpointSessionID, err = a.runner.ForkSession(ctx, pending.Session.SessionID)
			if err != nil {
				return fmt.Errorf("checkpoint committed agent session before follow-up: %w", err)
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
						"clean unused agent checkpoint %s: %w",
						checkpointSessionID,
						cleanupErr,
					)
				}
				return errors.Join(err, cleanupErr)
			}
			return err
		}
		pending.CheckpointSessionID = checkpointSessionID
		result, err = a.runner.Run(ctx, pending.Session, prompt, finalPath, tc)
	case messageRunning:
		result, err = a.runner.Recover(
			ctx,
			pending.Session,
			pending.Prompt,
			finalPath,
			tc,
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
	outboundFiles, outboundManifest, manifestJSON, err := a.outboundForState(ctx, pending, tc)
	if err != nil {
		return err
	}
	if pending.State != messageResultReady {
		if err := a.store.StoreResultWithManifest(message.MessageID, result, manifestJSON); err != nil {
			return err
		}
		if err := os.Remove(finalPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove durable agent result: %w", err)
		}
	}

	payload := ReplyPayload{Text: result.Text}
	tier := a.tierFor(pending)
	if tier != TierPlain {
		payload.HTML = replyHTML(result.Text)
	}
	switch tier {
	case TierFormatted:
		payload.Files = outboundFiles
	case TierComplete:
		if outboundManifest != nil && len(outboundFiles) > 0 {
			archive, err := BuildArchive(*outboundManifest, outboundFiles)
			if err != nil {
				return fmt.Errorf("build attachment archive: %w", err)
			}
			payload.Files = []OutboundFile{{
				Filename:    AttachmentArchiveName(),
				ContentType: "application/zip",
				Contents:    archive,
			}}
		}
	}
	key := idempotencyKey(pending.Session.SessionID, message.MessageID)
	outboundMessageID, err := a.transport.Reply(ctx, message.MessageID, payload, key)
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
	if err := a.cleanupStagingDirs(pending); err != nil {
		a.logger.Printf(
			"staging cleanup failed message=%s thread=%s error=%v",
			message.MessageID,
			message.ThreadID,
			err,
		)
	}
	a.recordProcessed(message.ThreadID)
	if err := os.Remove(finalPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove durable agent result: %w", err)
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

func (a *App) turnContext(ctx context.Context, pending PendingMessage) (TurnContext, error) {
	turnKey := TurnKey(pending.Session.Sequence, pending.MessageID)
	dirs, err := stagePaths(a.runner.projectDir, turnKey)
	if err != nil {
		return TurnContext{}, fmt.Errorf("prepare attachment staging: %w", err)
	}
	return TurnContext{
		InboxPath:    filepath.Join(dirs.Inbox, InboundManifestName()),
		OutboxPath:   dirs.Outbox,
		ManifestPath: filepath.Join(dirs.Inbox, InboundManifestName()),
		Tier:         string(a.tierFor(pending)),
	}, nil
}

func (a *App) tierFor(pending PendingMessage) ResponseTier {
	tier := pending.Session.ResponseTier
	if tier == "" {
		tier = a.responseTier
	}
	if tier == "" {
		tier = TierPlain
	}
	return tier
}

func (a *App) resetTurnDirs(pending PendingMessage) error {
	turnKey := TurnKey(pending.Session.Sequence, pending.MessageID)
	inbox := filepath.Join(a.runner.projectDir, ".attachments-inbox", turnKey)
	outbox := filepath.Join(a.runner.projectDir, ".attachments-outbox", turnKey)
	if err := os.RemoveAll(inbox); err != nil {
		return fmt.Errorf("reset inbound staging: %w", err)
	}
	if err := os.RemoveAll(outbox); err != nil {
		return fmt.Errorf("reset outbound staging: %w", err)
	}
	_, err := stagePaths(a.runner.projectDir, turnKey)
	return err
}

func (a *App) outboundForState(
	ctx context.Context,
	pending PendingMessage,
	tc TurnContext,
) ([]OutboundFile, *OutboundManifest, string, error) {
	tier := a.tierFor(pending)
	if tier == TierPlain {
		return nil, nil, "", nil
	}
	switch pending.State {
	case messageReceived, messageRunning:
		files, manifest, err := CollectOutbox(
			ctx,
			StagingDirs{Outbox: tc.OutboxPath},
			DefaultAttachmentLimits(),
		)
		if err != nil {
			return nil, nil, "", fmt.Errorf("collect outbox: %w", err)
		}
		payload, err := json.Marshal(manifest)
		if err != nil {
			return nil, nil, "", fmt.Errorf("encode outbound manifest: %w", err)
		}
		return files, manifest, string(payload), nil
	case messageResultReady:
		if strings.TrimSpace(pending.ResultManifest) == "" {
			return nil, nil, "", nil
		}
		var manifest OutboundManifest
		if err := json.Unmarshal([]byte(pending.ResultManifest), &manifest); err != nil {
			return nil, nil, "", fmt.Errorf("parse persisted outbound manifest: %w", err)
		}
		files, err := VerifyAndLoadOutbox(ctx, StagingDirs{Outbox: tc.OutboxPath}, manifest)
		if err != nil {
			return nil, nil, "", fmt.Errorf("verify outbound files: %w", err)
		}
		return files, &manifest, pending.ResultManifest, nil
	default:
		return nil, nil, "", fmt.Errorf("unsupported pending message state %q", pending.State)
	}
}

func (a *App) cleanupStagingDirs(pending PendingMessage) error {
	turnKey := TurnKey(pending.Session.Sequence, pending.MessageID)
	inbox := filepath.Join(a.runner.projectDir, ".attachments-inbox", turnKey)
	outbox := filepath.Join(a.runner.projectDir, ".attachments-outbox", turnKey)
	var first error
	for _, path := range []string{inbox, outbox} {
		if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
			first = errors.Join(first, err)
		}
	}
	return first
}

func replyHTML(text string) string {
	return "<!DOCTYPE html>\n<html><body><p style=\"white-space: pre-wrap\">" +
		html.EscapeString(text) + "</p></body></html>\n"
}

func (a *App) recordProcessed(threadID string) {
	a.statsMu.Lock()
	defer a.statsMu.Unlock()
	a.processed++
	a.threads[threadID] = struct{}{}
}

func (a *App) logShutdown() {
	a.statsMu.Lock()
	processed := a.processed
	threads := len(a.threads)
	a.statsMu.Unlock()
	a.logger.Printf(
		"DearMachine Client shutting down. Processed %d messages across %d threads.",
		processed,
		threads,
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
		"dearmachine-machtiani-results",
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
		"[Dear Machine, email from %s received at %s]\n",
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
	tier := session.ResponseTier
	if tier == "" {
		tier = TierPlain
	}
	fmt.Fprintf(&prompt, "[Response tier: %s]\n", tier)
	if len(message.Attachments) > 0 {
		for _, ref := range message.Attachments {
			fmt.Fprintf(
				&prompt,
				"[Attachment: %s (%s, %d bytes)]\n",
				ref.Filename,
				ref.ContentType,
				ref.SizeBytes,
			)
		}
		if tier == TierPlain {
			fmt.Fprintln(&prompt, "[Attachment bodies are not staged for the plain response tier]")
		}
	}
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
