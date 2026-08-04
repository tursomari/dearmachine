package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dearmachine/dearmachine/internal/agentmanager"
	backendcatalog "github.com/dearmachine/dearmachine/internal/backends"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "agent-manager:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, _ io.Writer) error {
	root, err := agentmanager.DefaultRoot()
	if err != nil {
		return err
	}
	manager := agentmanager.New(root)
	if len(args) == 2 && args[0] == "_supervise" {
		return manager.Supervise(context.Background(), args[1])
	}
	if len(args) == 0 {
		return helpError("command is required", "agent-manager --help")
	}
	if args[0] == "help" {
		return writeHelp(stdout, args[1:]...)
	}
	if isHelp(args[0]) {
		if len(args) != 1 {
			return helpError("top-level help takes no arguments", "agent-manager --help")
		}
		return writeHelp(stdout)
	}
	switch args[0] {
	case "backend":
		return runBackend(manager, args[1:], stdout)
	case "ticket":
		return runTicket(manager, args[1:], stdout)
	case "worker":
		return runWorker(manager, args[1:], stdout)
	default:
		return helpError(fmt.Sprintf("unknown command %q", args[0]), "agent-manager --help")
	}
}

func runTicket(manager *agentmanager.Manager, args []string, output io.Writer) error {
	if len(args) == 0 {
		return helpError("ticket command is required", "agent-manager ticket --help")
	}
	if args[0] == "help" {
		return writeHelp(output, append([]string{"ticket"}, args[1:]...)...)
	}
	if isHelp(args[0]) {
		if len(args) != 1 {
			return helpError("ticket help takes no arguments", "agent-manager ticket --help")
		}
		return writeHelp(output, "ticket")
	}
	switch args[0] {
	case "send":
		if len(args) == 2 && isHelp(args[1]) {
			return writeHelp(output, "ticket", "send")
		}
		flags := flag.NewFlagSet("ticket send", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		backend := flags.String("backend", "", "approved backend ID")
		request := flags.String("file", "", "work request file")
		cwd := flags.String("cwd", "", "worker working directory")
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return writeHelp(output, "ticket", "send")
			}
			return helpError(err.Error(), "agent-manager ticket send --help")
		}
		if flags.NArg() != 0 || strings.TrimSpace(*backend) == "" ||
			strings.TrimSpace(*request) == "" || strings.TrimSpace(*cwd) == "" {
			return helpError(
				"ticket send requires --backend <name>, --file <path>, and --cwd <project-dir>",
				"agent-manager ticket send --help",
			)
		}
		if err := configureApprovedBackends(manager); err != nil {
			return err
		}
		id, err := manager.Send(*backend, *request, *cwd)
		if err != nil {
			return err
		}
		fmt.Fprintln(output, id)
		return nil
	case "status":
		if len(args) == 2 && isHelp(args[1]) {
			return writeHelp(output, "ticket", "status")
		}
		if len(args) != 2 {
			return helpError("ticket status requires <ticket-id>", "agent-manager ticket status --help")
		}
		meta, err := manager.Status(args[1])
		if err != nil {
			return err
		}
		printMeta(output, meta)
		return nil
	case "view":
		if len(args) == 2 && isHelp(args[1]) {
			return writeHelp(output, "ticket", "view")
		}
		if len(args) != 2 {
			return helpError("ticket view requires <ticket-id>", "agent-manager ticket view --help")
		}
		return manager.View(args[1], output)
	case "cancel":
		if len(args) == 2 && isHelp(args[1]) {
			return writeHelp(output, "ticket", "cancel")
		}
		if len(args) != 2 {
			return helpError("ticket cancel requires <ticket-id> or --all", "agent-manager ticket cancel --help")
		}
		if args[1] == "--all" {
			return manager.CancelAll()
		}
		return manager.Cancel(args[1])
	default:
		return helpError(fmt.Sprintf("unknown ticket command %q", args[0]), "agent-manager ticket --help")
	}
}

func runBackend(manager *agentmanager.Manager, args []string, output io.Writer) error {
	if len(args) == 0 {
		return helpError("backend command is required", "agent-manager backend --help")
	}
	if args[0] == "help" {
		return writeHelp(output, append([]string{"backend"}, args[1:]...)...)
	}
	if isHelp(args[0]) {
		if len(args) != 1 {
			return helpError("backend help takes no arguments", "agent-manager backend --help")
		}
		return writeHelp(output, "backend")
	}
	switch args[0] {
	case "list":
		if len(args) == 2 && isHelp(args[1]) {
			return writeHelp(output, "backend", "list")
		}
		if len(args) != 1 {
			return helpError("backend list takes no arguments", "agent-manager backend list --help")
		}
		if err := configureApprovedBackends(manager); err != nil {
			return err
		}
		for _, backend := range manager.BackendList() {
			fmt.Fprintln(output, backend)
		}
		return nil
	case "health":
		if len(args) == 2 && isHelp(args[1]) {
			return writeHelp(output, "backend", "health")
		}
		if len(args) != 2 {
			return helpError("backend health requires <name>", "agent-manager backend health --help")
		}
		if err := configureApprovedBackends(manager); err != nil {
			return err
		}
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve health-check working directory: %w", err)
		}
		result, healthErr := manager.BackendHealth(context.Background(), args[1], cwd)
		fmt.Fprintf(output, "backend=%s\nprobe=%q\nreply=%q\n", result.Backend, result.Probe, result.Reply)
		if healthErr != nil {
			fmt.Fprintf(output, "result=fail reason=%s\n", result.Reason)
			return healthErr
		}
		fmt.Fprintln(output, "result=ok")
		return nil
	default:
		return helpError(fmt.Sprintf("unknown backend command %q", args[0]), "agent-manager backend --help")
	}
}

func configureApprovedBackends(manager *agentmanager.Manager) error {
	approved, err := backendcatalog.Decode(os.Getenv(backendcatalog.EnvironmentVariable))
	if err != nil {
		return err
	}
	if err := manager.SetApprovedBackends(approved); err != nil {
		return fmt.Errorf("configure approved backends: %w", err)
	}
	return nil
}

func runWorker(manager *agentmanager.Manager, args []string, output io.Writer) error {
	if len(args) > 0 && args[0] == "help" {
		return writeHelp(output, append([]string{"worker"}, args[1:]...)...)
	}
	if len(args) == 1 && isHelp(args[0]) {
		return writeHelp(output, "worker")
	}
	if len(args) == 3 && args[1] == "status" && isHelp(args[2]) {
		return writeHelp(output, "worker", "status")
	}
	if len(args) != 2 || args[1] != "status" {
		return helpError("worker command requires <name> status", "agent-manager worker --help")
	}
	metas, err := manager.List(args[0])
	if err != nil {
		return err
	}
	for _, meta := range metas {
		printMeta(output, meta)
	}
	return nil
}

func isHelp(arg string) bool {
	return arg == "help" || arg == "-h" || arg == "--help"
}

func helpError(message, command string) error {
	return fmt.Errorf("%s\nRun %q for usage.", message, command)
}

func writeHelp(output io.Writer, topic ...string) error {
	key := strings.Join(topic, " ")
	help, ok := helpMenus[key]
	if !ok {
		return helpError(fmt.Sprintf("unknown help topic %q", key), nearestHelpCommand(topic))
	}
	fmt.Fprint(output, help)
	return nil
}

func nearestHelpCommand(topic []string) string {
	for length := len(topic) - 1; length > 0; length-- {
		key := strings.Join(topic[:length], " ")
		if _, ok := helpMenus[key]; ok {
			return "agent-manager " + key + " --help"
		}
	}
	return "agent-manager --help"
}

var helpMenus = map[string]string{
	"": `Usage:
  agent-manager <command> [arguments]

Commands:
  backend   Inspect and probe approved backends
  ticket    Send and manage supervised work tickets
  worker    Inspect tickets for a worker

Run "agent-manager help <command>" for command help.
Run "agent-manager help <command> <subcommand>" for subcommand help.
`,
	"backend": `Usage:
  agent-manager backend <command> [arguments]

Commands:
  list            Print approved backends in priority order
  health <name>   Functionally probe an approved backend

Run "agent-manager backend <command> --help" for subcommand help.
`,
	"backend list": `Usage:
  agent-manager backend list

Print approved backends, one per line, in priority order.
`,
	"backend health": `Usage:
  agent-manager backend health <name>

Ask an approved backend to perform a transient file-write probe in the current directory.
The probe file is removed before the command returns.
`,
	"ticket": `Usage:
  agent-manager ticket <command> [arguments]

Commands:
  send      Start a supervised work ticket
  status    Show a ticket's current status
  view      Show a ticket's request and result
  cancel    Cancel one ticket or all tickets

Run "agent-manager ticket <command> --help" for subcommand help.
`,
	"ticket send": `Usage:
  agent-manager ticket send --backend <name> --file <path> --cwd <project-dir>

Options:
  --backend <name>       Approved backend ID
  --file <path>          Work-request file
  --cwd <project-dir>    Worker working directory
`,
	"ticket status": `Usage:
  agent-manager ticket status <ticket-id>

Show the current status and runtime metadata for one ticket.
`,
	"ticket view": `Usage:
  agent-manager ticket view <ticket-id>

Show the open request and any available ticket result.
`,
	"ticket cancel": `Usage:
  agent-manager ticket cancel <ticket-id>
  agent-manager ticket cancel --all

Cancel one running ticket or every running ticket.
`,
	"worker": `Usage:
  agent-manager worker <name> status

Show tickets associated with one worker.
Run "agent-manager help worker status" for status help.
`,
	"worker status": `Usage:
  agent-manager worker <name> status

Show tickets associated with one worker.
`,
}

func printMeta(output io.Writer, meta agentmanager.Meta) {
	fmt.Fprintf(output, "status=%s ticket-id=%s worker=%s pid=%d session-id=%s\n", meta.Status, meta.TicketID, meta.Worker, meta.PID, meta.NativeSession)
}
