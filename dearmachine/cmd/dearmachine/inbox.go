package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/transports"
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
	inboxID := flags.String("inbox-id", "", "mail transport inbox ID or address")
	transportID := flags.String("transport", "agentmail", "mail transport ID ("+strings.Join(transports.IDs(), ", ")+")")
	allowValue := flags.String("allow", "", "comma-separated paired RFC 5322 email addresses (required; or DEARMACHINE_ALLOW)")
	dbPath := flags.String("db", "", "SQLite state database path")
	pidfile := flags.String("pidfile", "", "DearMachine Client PID file to check")
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
	if strings.TrimSpace(*inboxID) == "" {
		return fmt.Errorf(
			"--inbox-id is required\nRun \"dearmachine inbox skip --help\" for usage",
		)
	}
	allowSet := false
	flags.Visit(func(setFlag *flag.Flag) {
		if setFlag.Name == "allow" {
			allowSet = true
		}
	})
	if allowSet && strings.TrimSpace(*allowValue) == "" {
		return fmt.Errorf("--allow must not be empty")
	}
	if deps.newTransport == nil {
		if !allowSet {
			*allowValue = os.Getenv("DEARMACHINE_ALLOW")
		}
		allow, err := transports.ParseAllowList(*allowValue)
		if err != nil {
			return fmt.Errorf("--allow or DEARMACHINE_ALLOW: %w", err)
		}
		selectedTransport := *transportID
		deps.newTransport = func(inboxID string) (client.Transport, error) {
			return transports.New(selectedTransport, inboxID, allow)
		}
	}
	resolvedDB, resolvedPID, pair, err := resolveInboxStatePaths(*dbPath, *pidfile, deps)
	if err != nil {
		return err
	}
	if err := ensureDeviceClientStopped(resolvedPID); err != nil {
		return err
	}

	transport, err := deps.newTransport(strings.TrimSpace(*inboxID))
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

	store, err := openInboxStore(resolvedDB, pair, deps)
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
	dbPath := flags.String("db", "", "SQLite state database path")
	pidfile := flags.String("pidfile", "", "DearMachine Client PID file to check")
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

	resolvedDB, resolvedPID, pair, err := resolveInboxStatePaths(*dbPath, *pidfile, deps)
	if err != nil {
		return err
	}
	if err := ensureDeviceClientStopped(resolvedPID); err != nil {
		return err
	}
	resolvedProject, err := resolvePath(*projectDir, deps.userHomeDir)
	if err != nil {
		return fmt.Errorf("resolve agent project: %w", err)
	}

	store, err := openInboxStore(resolvedDB, pair, deps)
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
	dbPath := flags.String("db", "", "SQLite state database path")
	pidfile := flags.String("pidfile", "", "DearMachine Client PID file to check")
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
	resolvedDB, resolvedPID, pair, err := resolveInboxStatePaths(*dbPath, *pidfile, deps)
	if err != nil {
		return err
	}
	if err := ensureDeviceClientStopped(resolvedPID); err != nil {
		return err
	}
	store, err := openInboxStore(resolvedDB, pair, deps)
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
	dbPath := flags.String("db", "", "SQLite state database path")
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
	resolvedDB, _, pair, err := resolveInboxStatePaths(*dbPath, "", deps)
	if err != nil {
		return err
	}
	store, err := openInboxStore(resolvedDB, pair, deps)
	if err != nil {
		return err
	}
	defer store.Close()
	messages, err := store.SkippedMessages()
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(messages)
	}
	if len(messages) == 0 {
		_, err := fmt.Fprintln(stdout, "No locally skipped messages.")
		return err
	}
	for _, message := range messages {
		if _, err := fmt.Fprintf(
			stdout,
			"%s\t%s\t%s\t%s\n",
			message.MessageID,
			message.ThreadID,
			message.SkippedAt,
			message.Reason,
		); err != nil {
			return err
		}
	}
	return nil
}

// resolveInboxStatePaths keeps an explicit --db override for low-level
// maintenance. Without one, exactly one registered pair must be resolvable.
func resolveInboxStatePaths(dbPath, pidfile string, deps dependencies) (string, string, *client.Pair, error) {
	var err error
	var pair *client.Pair
	if strings.TrimSpace(dbPath) == "" {
		state, resolveErr := client.ResolvePairState(deps.userHomeDir, "")
		if resolveErr != nil {
			return "", "", nil, resolveErr
		}
		dbPath = state.Path
		selected := state.Pair
		pair = &selected
	} else {
		dbPath, err = resolvePath(dbPath, deps.userHomeDir)
		if err != nil {
			return "", "", nil, err
		}
	}
	if strings.TrimSpace(pidfile) == "" {
		home, err := deps.userHomeDir()
		if err != nil {
			return "", "", nil, fmt.Errorf("resolve default PID file: %w", err)
		}
		pidfile = filepath.Join(home, ".dearmachine", "run", "dearmachine.pid")
	} else {
		pidfile, err = resolvePath(pidfile, deps.userHomeDir)
		if err != nil {
			return "", "", nil, err
		}
	}
	return filepath.Clean(dbPath), filepath.Clean(pidfile), pair, nil
}

func openInboxStore(path string, pair *client.Pair, deps dependencies) (*client.Store, error) {
	if pair == nil {
		return deps.openStore(path)
	}
	openPairStore := deps.openPairStore
	if openPairStore == nil {
		openPairStore = client.OpenPairStore
	}
	return openPairStore(path, *pair)
}

func ensureDeviceClientStopped(pidfile string) error {
	content, err := os.ReadFile(pidfile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read DearMachine Client PID file: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || pid <= 0 {
		return fmt.Errorf("invalid DearMachine Client PID file %s", pidfile)
	}
	err = syscall.Kill(pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return fmt.Errorf(
			"DearMachine Client PID %d is still running; stop it before changing local inbox state",
			pid,
		)
	}
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return fmt.Errorf("check DearMachine Client PID %d: %w", pid, err)
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
	_, err := fmt.Fprintf(outputOrDiscard(output), `Usage:
  dearmachine inbox skip --current --inbox-id <id> [flags]
  dearmachine inbox skip --inbox-id <id> [flags] <message-id>...

Records an exact local skip decision without changing the remote inbox. The DearMachine Client must be stopped. --current snapshots messages that are eligible now;
messages arriving later remain eligible.

Flags:
  --current          Select all messages eligible at this instant
  --transport <id>   Mail transport ID: %s (default: agentmail)
  --inbox-id <id>    Mail transport inbox ID or address (required)
  --db <path>        SQLite state database (default: normal DearMachine Client DB)
  --pidfile <path>   PID file to check (default: normal DearMachine Client PID file)
  --project <path>   machtiani project for abandoned-session cleanup
  --agent-bin <path> machtiani executable
  --reason <text>    Local audit reason
`, strings.Join(transports.IDs(), ", "))
	return err
}

func inboxAbandonHelp(output io.Writer) error {
	_, err := fmt.Fprint(outputOrDiscard(output), `Usage:
  dearmachine inbox abandon [flags] <message-id>

Force-skips one running follow-up in an established thread without changing
the remote inbox. DearMachine Client must be stopped. Every established
follow-up receives
a clean pre-run session checkpoint. This command atomically remaps the thread
to that checkpoint, records the message as locally skipped, and removes the
partial source session. Use ordinary inbox skip for messages that have not
started.

Flags:
  --db <path>        SQLite state database (default: normal DearMachine Client DB)
  --pidfile <path>   PID file to check (default: normal DearMachine Client PID file)
  --project <path>   machtiani project containing the session
  --agent-bin <path> machtiani executable
  --reason <text>    Local audit reason
`)
	return err
}

func inboxUnskipHelp(output io.Writer) error {
	_, err := fmt.Fprint(outputOrDiscard(output), `Usage:
  dearmachine inbox unskip [--db <path>] [--pidfile <path>] <message-id>...

Removes local skip decisions. The remote inbox is not changed.

Flags:
  --db <path>       SQLite state database (default: normal DearMachine Client DB)
  --pidfile <path>  PID file to check (default: normal DearMachine Client PID file)
`)
	return err
}

func inboxSkippedHelp(output io.Writer) error {
	_, err := fmt.Fprint(outputOrDiscard(output), `Usage:
  dearmachine inbox skipped [--db <path>] [--json]

Lists local skip decisions. The remote inbox is not queried or changed.

Flags:
  --db <path>  SQLite state database (default: normal DearMachine Client DB)
  --json       Output a JSON array
`)
	return err
}
