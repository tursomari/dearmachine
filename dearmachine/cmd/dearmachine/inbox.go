package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/dearmachine/dearmachine/internal/client"
)

func runInbox(args []string, deps dependencies) error {
	if len(args) == 0 {
		return inboxHelp(deps.flagOutput)
	}
	switch args[0] {
	case "help", "-h", "--help":
		if len(args) == 1 {
			return inboxHelp(deps.flagOutput)
		}
		return runInboxHelp(args[1:], deps.flagOutput)
	case "delivery":
		return runInboxDelivery(args[1:], deps)
	case "skip":
		return runInboxSkip(args[1:], deps)
	case "abandon":
		return runInboxAbandon(args[1:], deps)
	case "unskip":
		return runInboxUnskip(args[1:], deps)
	case "skipped":
		return runInboxSkipped(args[1:], deps)
	default:
		return fmt.Errorf(
			"unknown inbox command %q\nRun \"dearmachine inbox --help\" for usage",
			args[0],
		)
	}
}

func runInboxHelp(args []string, output io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("help requires an inbox subcommand")
	}
	switch args[0] {
	case "delivery":
		return inboxDeliveryHelp(output)
	case "skip":
		return inboxSkipHelp(output)
	case "abandon":
		return inboxAbandonHelp(output)
	case "unskip":
		return inboxUnskipHelp(output)
	case "skipped":
		return inboxSkippedHelp(output)
	default:
		return fmt.Errorf("unknown inbox command %q", args[0])
	}
}

func runInboxSkip(args []string, deps dependencies) error {
	output := outputOrDiscard(deps.flagOutput)
	stdout := outputOrDiscard(deps.stdout)
	flags := flag.NewFlagSet("inbox skip", flag.ContinueOnError)
	flags.SetOutput(output)
	current := flags.Bool("current", false, "skip the exact snapshot of currently unread messages")
	pairSelector := flags.String("pair", "", "registered pair email address or UUID (required)")
	projectDir := flags.String("project", ".", "machtiani project for abandoned-session cleanup")
	agentBinary := flags.String("agent-bin", "machtiani", "path to the machtiani executable")
	reason := flags.String("reason", "operator skipped", "local audit reason")
	flags.Usage = func() { _ = inboxSkipHelp(output) }
	if err := flags.Parse(args); err != nil {
		return err
	}
	messageIDs := flags.Args()
	if *current == (len(messageIDs) > 0) {
		return fmt.Errorf(
			"choose exactly one of --current or one or more message IDs\n" +
				"Run \"dearmachine inbox skip --help\" for usage",
		)
	}
	if strings.TrimSpace(*pairSelector) == "" {
		return fmt.Errorf(
			"--pair is required\nRun \"dearmachine inbox skip --help\" for usage",
		)
	}
	state, err := client.ResolvePairState(deps.userHomeDir, *pairSelector)
	if err != nil {
		return err
	}
	if err := ensureDeviceClientStopped(deps); err != nil {
		return err
	}

	newRawTransport := deps.newRawTransport
	if newRawTransport == nil {
		return errors.New("raw mail transport constructor is unavailable")
	}
	raw, err := newRawTransport(state.Inbox.Transport, state.Inbox.ProviderID)
	if err != nil {
		return err
	}
	router, err := client.NewInboxRouter(raw, state.Inbox, []client.Pair{state.Pair}, 1)
	if err != nil {
		return err
	}
	transport, err := router.Endpoint(state.Pair.ID)
	if err != nil {
		return err
	}
	ctx := context.Background()
	var refs []client.MessageRef
	if *current {
		messages, err := transport.Poll(ctx)
		if err != nil {
			return err
		}
		for _, message := range messages {
			refs = append(refs, client.MessageRef{
				MessageID: message.MessageID,
				ThreadID:  message.ThreadID,
			})
		}
		if len(refs) == 0 {
			_, err := fmt.Fprintln(stdout, "No currently eligible messages to skip. Remote inbox unchanged.")
			return err
		}
	} else {
		for _, messageID := range messageIDs {
			message, err := transport.Message(ctx, messageID)
			if err != nil {
				return err
			}
			refs = append(refs, client.MessageRef{
				MessageID: message.MessageID,
				ThreadID:  message.ThreadID,
			})
		}
	}

	store, err := openInboxStore(state, deps)
	if err != nil {
		return err
	}
	defer store.Close()
	abandoned, err := store.SkipMessages(refs, *reason)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(
		stdout,
		"Skipped %d message(s) locally. Remote inbox unchanged.\n",
		len(refs),
	); err != nil {
		return err
	}

	var cleanupErrors []error
	var runner *client.AgentRunner
	for _, message := range abandoned {
		if err := client.RemoveRecoveryResult(message.SessionID, message.MessageID); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
		if message.CleanupSession {
			if runner == nil {
				resolvedProject, err := resolvePath(*projectDir, deps.userHomeDir)
				if err != nil {
					cleanupErrors = append(cleanupErrors, fmt.Errorf("resolve cleanup project: %w", err))
					continue
				}
				runner, err = client.NewAgentRunner(*agentBinary, resolvedProject, "")
				if err != nil {
					cleanupErrors = append(cleanupErrors, fmt.Errorf("prepare session cleanup: %w", err))
					continue
				}
			}
			if err := runner.DeleteSession(ctx, message.SessionID); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf(
					"message %s remains skipped, but clean abandoned session %s: %w",
					message.MessageID,
					message.SessionID,
					err,
				))
			}
		}
	}
	return errors.Join(cleanupErrors...)
}

func runInboxAbandon(args []string, deps dependencies) error {
	output := outputOrDiscard(deps.flagOutput)
	stdout := outputOrDiscard(deps.stdout)
	flags := flag.NewFlagSet("inbox abandon", flag.ContinueOnError)
	flags.SetOutput(output)
	pairSelector := flags.String("pair", "", "registered pair email address or UUID (required)")
	projectDir := flags.String("project", ".", "machtiani project containing the session")
	agentBinary := flags.String("agent-bin", "machtiani", "path to the machtiani executable")
	reason := flags.String(
		"reason",
		"operator abandoned in-progress follow-up",
		"local audit reason",
	)
	flags.Usage = func() { _ = inboxAbandonHelp(output) }
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return fmt.Errorf(
			"exactly one pending message ID is required\n" +
				"Run \"dearmachine inbox abandon --help\" for usage",
		)
	}
	messageID := strings.TrimSpace(flags.Arg(0))
	if messageID == "" {
		return fmt.Errorf("message ID is required")
	}

	if strings.TrimSpace(*pairSelector) == "" {
		return errors.New("--pair is required")
	}
	state, err := client.ResolvePairState(deps.userHomeDir, *pairSelector)
	if err != nil {
		return err
	}
	if err := ensureDeviceClientStopped(deps); err != nil {
		return err
	}
	resolvedProject, err := resolvePath(*projectDir, deps.userHomeDir)
	if err != nil {
		return fmt.Errorf("resolve agent project: %w", err)
	}

	store, err := openInboxStore(state, deps)
	if err != nil {
		return err
	}
	defer store.Close()
	plan, err := store.PrepareAbandon(messageID)
	if err != nil {
		return err
	}
	runner, err := deps.newRunner(*agentBinary, resolvedProject, "")
	if err != nil {
		return fmt.Errorf("prepare agent session abandonment: %w", err)
	}
	ctx := context.Background()
	if err := store.CommitAbandon(plan, *reason); err != nil {
		return err
	}

	var cleanupErrors []error
	if err := client.RemoveRecoveryResult(plan.SessionID, plan.MessageID); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if err := client.RemoveStagingDirs(
		resolvedProject,
		client.TurnKey(plan.PendingSequence, plan.MessageID),
	); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf(
			"message %s remains abandoned, but clean attachment staging for sequence %d: %w",
			plan.MessageID,
			plan.PendingSequence,
			err,
		))
	}
	if err := runner.DeleteSession(ctx, plan.SessionID); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf(
			"message %s remains abandoned, but clean partial session %s: %w",
			plan.MessageID,
			plan.SessionID,
			err,
		))
	}
	if _, err := runner.ForkSession(
		ctx,
		plan.CheckpointSessionID,
		plan.SessionID,
	); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf(
			"message %s remains abandoned, but restore clean checkpoint %s onto canonical session %s: %w",
			plan.MessageID,
			plan.CheckpointSessionID,
			plan.SessionID,
			err,
		))
	}
	if err := runner.DeleteSession(ctx, plan.CheckpointSessionID); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf(
			"message %s remains abandoned, but clean checkpoint session %s: %w",
			plan.MessageID,
			plan.CheckpointSessionID,
			err,
		))
	}
	if _, err := fmt.Fprintf(
		stdout,
		"Abandoned message %s locally at committed sequence %d. Remote inbox unchanged.\n",
		plan.MessageID,
		plan.CommittedSequence,
	); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	return errors.Join(cleanupErrors...)
}

func runInboxUnskip(args []string, deps dependencies) error {
	output := outputOrDiscard(deps.flagOutput)
	stdout := outputOrDiscard(deps.stdout)
	flags := flag.NewFlagSet("inbox unskip", flag.ContinueOnError)
	flags.SetOutput(output)
	pairSelector := flags.String("pair", "", "registered pair email address or UUID (required)")
	flags.Usage = func() { _ = inboxUnskipHelp(output) }
	if err := flags.Parse(args); err != nil {
		return err
	}
	messageIDs := flags.Args()
	if len(messageIDs) == 0 {
		return fmt.Errorf(
			"at least one message ID is required\n" +
				"Run \"dearmachine inbox unskip --help\" for usage",
		)
	}
	if strings.TrimSpace(*pairSelector) == "" {
		return errors.New("--pair is required")
	}
	state, err := client.ResolvePairState(deps.userHomeDir, *pairSelector)
	if err != nil {
		return err
	}
	if err := ensureDeviceClientStopped(deps); err != nil {
		return err
	}
	store, err := openInboxStore(state, deps)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.UnskipMessages(messageIDs); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "Unskipped %d message(s).\n", len(messageIDs))
	return err
}

func runInboxSkipped(args []string, deps dependencies) error {
	output := outputOrDiscard(deps.flagOutput)
	stdout := outputOrDiscard(deps.stdout)
	flags := flag.NewFlagSet("inbox skipped", flag.ContinueOnError)
	flags.SetOutput(output)
	pairSelector := flags.String("pair", "", "optional registered pair email address or UUID")
	jsonOutput := flags.Bool("json", false, "output a JSON array")
	flags.Usage = func() { _ = inboxSkippedHelp(output) }
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf(
			"unexpected arguments: %v\nRun \"dearmachine inbox skipped --help\" for usage",
			flags.Args(),
		)
	}
	var selectors []string
	if strings.TrimSpace(*pairSelector) != "" {
		selectors = []string{*pairSelector}
	}
	states, err := client.ResolvePairStates(deps.userHomeDir, selectors)
	if err != nil {
		return err
	}
	type pairMessages struct {
		PairID    string                  `json:"pair_id"`
		UserEmail string                  `json:"user_email"`
		Messages  []client.SkippedMessage `json:"messages"`
	}
	groups := make([]pairMessages, 0, len(states))
	total := 0
	for _, state := range states {
		store, err := openInboxStore(state, deps)
		if err != nil {
			return err
		}
		messages, listErr := store.SkippedMessages()
		closeErr := store.Close()
		if err := errors.Join(listErr, closeErr); err != nil {
			return err
		}
		total += len(messages)
		groups = append(groups, pairMessages{PairID: state.Pair.ID, UserEmail: state.Pair.UserEmail, Messages: messages})
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(groups)
	}
	if total == 0 {
		_, err := fmt.Fprintln(stdout, "No locally skipped messages.")
		return err
	}
	for _, group := range groups {
		for _, message := range group.Messages {
			if _, err := fmt.Fprintf(
				stdout,
				"%s\t%s\t%s\t%s\t%s\t%s\n",
				group.PairID,
				group.UserEmail,
				message.MessageID,
				message.ThreadID,
				message.SkippedAt,
				message.Reason,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func openInboxStore(state client.PairState, deps dependencies) (*client.Store, error) {
	openPairStore := deps.openPairStore
	if openPairStore == nil {
		openPairStore = client.OpenPairStore
	}
	return openPairStore(state.Path, state.Pair)
}

func ensureDeviceClientStopped(deps dependencies) error {
	path, err := client.DefaultDaemonLockPath(deps.userHomeDir)
	if err != nil {
		return err
	}
	status := deps.daemonStatus
	if status == nil {
		status = client.DaemonStatus
	}
	pid, running, err := status(path)
	if err != nil {
		return err
	}
	if running {
		return fmt.Errorf(
			"DearMachine Client PID %d is still running; stop it before changing local inbox state",
			pid,
		)
	}
	return nil
}

func outputOrDiscard(output io.Writer) io.Writer {
	if output == nil {
		return io.Discard
	}
	return output
}

func inboxHelp(output io.Writer) error {
	_, err := fmt.Fprint(outputOrDiscard(output), `Usage:
  dearmachine inbox <command> [flags]

Commands:
  delivery  Inspect a Sendmux reply submission and delivery status
  skip      Locally suppress messages that have not started
  abandon   Force-skip one running follow-up using a clean session fork
  unskip    Make locally skipped messages eligible again
  skipped   List locally skipped messages

The configured mail transport is never modified by these commands.
Use "dearmachine inbox <command> --help" for command help.
`)
	return err
}

func inboxSkipHelp(output io.Writer) error {
	_, err := fmt.Fprint(outputOrDiscard(output), `Usage:
  dearmachine inbox skip --pair <email-or-uuid> --current [flags]
  dearmachine inbox skip --pair <email-or-uuid> [flags] <message-id>...

Records an exact local skip decision without changing the remote inbox. The DearMachine Client must be stopped. --current snapshots messages that are eligible now;
messages arriving later remain eligible.

Flags:
  --current          Select all messages eligible at this instant
  --pair <selector>  Registered pair email address or UUID (required)
  --project <path>   machtiani project for abandoned-session cleanup
  --agent-bin <path> machtiani executable
  --reason <text>    Local audit reason
`)
	return err
}

func inboxAbandonHelp(output io.Writer) error {
	_, err := fmt.Fprint(outputOrDiscard(output), `Usage:
  dearmachine inbox abandon --pair <email-or-uuid> [flags] <message-id>

Force-skips one running follow-up in an established thread without changing
the remote inbox. DearMachine Client must be stopped. Every established
follow-up receives
a clean pre-run session checkpoint. This command atomically remaps the thread
to that checkpoint, records the message as locally skipped, and removes the
partial source session. Use ordinary inbox skip for messages that have not
started.

Flags:
  --pair <selector>  Registered pair email address or UUID (required)
  --project <path>   machtiani project containing the session
  --agent-bin <path> machtiani executable
  --reason <text>    Local audit reason
`)
	return err
}

func inboxUnskipHelp(output io.Writer) error {
	_, err := fmt.Fprint(outputOrDiscard(output), `Usage:
  dearmachine inbox unskip --pair <email-or-uuid> <message-id>...

Removes local skip decisions. The remote inbox is not changed.

Flags:
  --pair <selector>  Registered pair email address or UUID (required)
`)
	return err
}

func inboxSkippedHelp(output io.Writer) error {
	_, err := fmt.Fprint(outputOrDiscard(output), `Usage:
  dearmachine inbox skipped [--pair <email-or-uuid>] [--json]

Lists local skip decisions. The remote inbox is not queried or changed.

Flags:
  --pair <selector>  Limit output to one registered pair
  --json             Output a JSON array grouped by pair
`)
	return err
}
