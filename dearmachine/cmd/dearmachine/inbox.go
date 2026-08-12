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
)

func runInbox(args []string, getenv func(string) string, deps dependencies) error {
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
		return runInboxSkip(args[1:], getenv, deps)
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

func runInboxSkip(args []string, getenv func(string) string, deps dependencies) error {
	output := outputOrDiscard(deps.flagOutput)
	stdout := outputOrDiscard(deps.stdout)
	flags := flag.NewFlagSet("inbox skip", flag.ContinueOnError)
	flags.SetOutput(output)
	current := flags.Bool("current", false, "skip the exact snapshot of currently unread messages")
	inboxID := flags.String("inbox-id", "", "AgentMail inbox ID")
	dbPath := flags.String("db", "", "SQLite state database path")
	pidfile := flags.String("pidfile", "", "DearMachine Client PID file to check")
	projectDir := flags.String("project", ".", "mct-agent project for abandoned-session cleanup")
	mctBinary := flags.String("mct-agent", "mct-agent", "path to the mct-agent executable")
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
	if getenv("AGENTMAIL_API_KEY") == "" {
		return fmt.Errorf("AGENTMAIL_API_KEY is required")
	}
	resolvedDB, resolvedPID, err := resolveInboxStatePaths(*dbPath, *pidfile, deps)
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
			_, err := fmt.Fprintln(stdout, "No currently unread messages to skip. AgentMail unchanged.")
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

	store, err := deps.openStore(resolvedDB)
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
		"Skipped %d message(s) locally. AgentMail unchanged.\n",
		len(refs),
	); err != nil {
		return err
	}

	var cleanupErrors []error
	var runner *client.MCTRunner
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
				runner, err = client.NewMCTRunner(*mctBinary, resolvedProject, "")
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
	projectDir := flags.String("project", ".", "mct-agent project containing the session")
	mctBinary := flags.String("mct-agent", "mct-agent", "path to the mct-agent executable")
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

	resolvedDB, resolvedPID, err := resolveInboxStatePaths(*dbPath, *pidfile, deps)
	if err != nil {
		return err
	}
	if err := ensureDeviceClientStopped(resolvedPID); err != nil {
		return err
	}
	resolvedProject, err := resolvePath(*projectDir, deps.userHomeDir)
	if err != nil {
		return fmt.Errorf("resolve mct project: %w", err)
	}

	store, err := deps.openStore(resolvedDB)
	if err != nil {
		return err
	}
	defer store.Close()
	plan, err := store.PrepareAbandon(messageID)
	if err != nil {
		return err
	}
	runner, err := deps.newRunner(*mctBinary, resolvedProject, "")
	if err != nil {
		return fmt.Errorf("prepare mct session abandonment: %w", err)
	}
	ctx := context.Background()
	if err := store.CommitAbandon(plan, *reason); err != nil {
		return err
	}

	var cleanupErrors []error
	if err := client.RemoveRecoveryResult(plan.SessionID, plan.MessageID); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if err := runner.DeleteSession(ctx, plan.SessionID); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf(
			"message %s remains abandoned, but clean partial session %s: %w",
			plan.MessageID,
			plan.SessionID,
			err,
		))
	}
	if _, err := fmt.Fprintf(
		stdout,
		"Abandoned message %s locally at committed sequence %d. AgentMail unchanged.\n",
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
	resolvedDB, resolvedPID, err := resolveInboxStatePaths(*dbPath, *pidfile, deps)
	if err != nil {
		return err
	}
	if err := ensureDeviceClientStopped(resolvedPID); err != nil {
		return err
	}
	store, err := deps.openStore(resolvedDB)
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
	resolvedDB, _, err := resolveInboxStatePaths(*dbPath, "", deps)
	if err != nil {
		return err
	}
	store, err := deps.openStore(resolvedDB)
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

func resolveInboxStatePaths(dbPath, pidfile string, deps dependencies) (string, string, error) {
	var err error
	if strings.TrimSpace(dbPath) == "" {
		dbPath, err = client.DefaultDeviceDatabasePath(deps.userHomeDir)
		if err != nil {
			return "", "", err
		}
	} else {
		dbPath, err = resolvePath(dbPath, deps.userHomeDir)
		if err != nil {
			return "", "", err
		}
	}
	if strings.TrimSpace(pidfile) == "" {
		home, err := deps.userHomeDir()
		if err != nil {
			return "", "", fmt.Errorf("resolve default PID file: %w", err)
		}
		pidfile = filepath.Join(home, ".dearmachine", "run", "dearmachine.pid")
	} else {
		pidfile, err = resolvePath(pidfile, deps.userHomeDir)
		if err != nil {
			return "", "", err
		}
	}
	return filepath.Clean(dbPath), filepath.Clean(pidfile), nil
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

AgentMail is never modified by these commands.
Use "dearmachine inbox <command> --help" for command help.
`)
	return err
}

func inboxSkipHelp(output io.Writer) error {
	_, err := fmt.Fprint(outputOrDiscard(output), `Usage:
  dearmachine inbox skip --current --inbox-id <id> [flags]
  dearmachine inbox skip --inbox-id <id> [flags] <message-id>...

Records an exact local skip decision without changing AgentMail. The DearMachine Client must be stopped. --current snapshots messages that are unread now;
messages arriving later remain eligible.

Flags:
  --current          Select all messages unread at this instant
  --inbox-id <id>    AgentMail inbox ID (required)
  --db <path>        SQLite state database (default: normal DearMachine Client DB)
  --pidfile <path>   PID file to check (default: normal DearMachine Client PID file)
  --project <path>   mct-agent project for abandoned-session cleanup
  --mct-agent <path> mct-agent executable
  --reason <text>    Local audit reason
`)
	return err
}

func inboxAbandonHelp(output io.Writer) error {
	_, err := fmt.Fprint(outputOrDiscard(output), `Usage:
  dearmachine inbox abandon [flags] <message-id>

Force-skips one running follow-up in an established thread without changing
AgentMail. DearMachine Client must be stopped. Every established follow-up receives
a clean pre-run session checkpoint. This command atomically remaps the thread
to that checkpoint, records the message as locally skipped, and removes the
partial source session. Use ordinary inbox skip for messages that have not
started.

Flags:
  --db <path>        SQLite state database (default: normal DearMachine Client DB)
  --pidfile <path>   PID file to check (default: normal DearMachine Client PID file)
  --project <path>   mct-agent project containing the session
  --mct-agent <path> mct-agent executable
  --reason <text>    Local audit reason
`)
	return err
}

func inboxUnskipHelp(output io.Writer) error {
	_, err := fmt.Fprint(outputOrDiscard(output), `Usage:
  dearmachine inbox unskip [--db <path>] [--pidfile <path>] <message-id>...

Removes local skip decisions. AgentMail is not changed.

Flags:
  --db <path>       SQLite state database (default: normal DearMachine Client DB)
  --pidfile <path>  PID file to check (default: normal DearMachine Client PID file)
`)
	return err
}

func inboxSkippedHelp(output io.Writer) error {
	_, err := fmt.Fprint(outputOrDiscard(output), `Usage:
  dearmachine inbox skipped [--db <path>] [--json]

Lists local skip decisions. AgentMail is not queried or changed.

Flags:
  --db <path>  SQLite state database (default: normal DearMachine Client DB)
  --json       Output a JSON array
`)
	return err
}
