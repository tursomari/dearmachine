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

func run(args []string, stdout, stderr io.Writer) error {
	root, err := agentmanager.DefaultRoot()
	if err != nil {
		return err
	}
	manager := agentmanager.New(root)
	if len(args) == 2 && args[0] == "_supervise" {
		return manager.Supervise(context.Background(), args[1])
	}
	if len(args) < 2 {
		return errors.New("usage: agent-manager backend <list|health> ... | ticket <send|status|view|cancel> ... | worker <name> status")
	}
	switch args[0] {
	case "backend":
		return runBackend(manager, args[1:], stdout)
	case "ticket":
		return runTicket(manager, args[1:], stdout, stderr)
	case "worker":
		return runWorker(manager, args[1:], stdout)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runTicket(manager *agentmanager.Manager, args []string, output, errorsOutput io.Writer) error {
	if len(args) == 0 {
		return errors.New("ticket command is required")
	}
	switch args[0] {
	case "send":
		flags := flag.NewFlagSet("ticket send", flag.ContinueOnError)
		flags.SetOutput(errorsOutput)
		backend := flags.String("backend", "", "approved backend ID")
		request := flags.String("file", "", "work request file")
		cwd := flags.String("cwd", "", "worker working directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || strings.TrimSpace(*backend) == "" ||
			strings.TrimSpace(*request) == "" || strings.TrimSpace(*cwd) == "" {
			return errors.New("ticket send requires --backend <name>, --file <path>, and --cwd <project-dir>")
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
		if len(args) != 2 {
			return errors.New("ticket status requires a ticket-id")
		}
		meta, err := manager.Status(args[1])
		if err != nil {
			return err
		}
		printMeta(output, meta)
		return nil
	case "view":
		if len(args) != 2 {
			return errors.New("ticket view requires a ticket-id")
		}
		return manager.View(args[1], output)
	case "cancel":
		if len(args) != 2 {
			return errors.New("ticket cancel requires a ticket-id or --all")
		}
		if args[1] == "--all" {
			return manager.CancelAll()
		}
		return manager.Cancel(args[1])
	default:
		return fmt.Errorf("unknown ticket command %q", args[0])
	}
}

func runBackend(manager *agentmanager.Manager, args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("backend command is required")
	}
	if err := configureApprovedBackends(manager); err != nil {
		return err
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("backend list takes no arguments")
		}
		for _, backend := range manager.BackendList() {
			fmt.Fprintln(output, backend)
		}
		return nil
	case "health":
		if len(args) != 2 {
			return errors.New("backend health requires a backend name")
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
		return fmt.Errorf("unknown backend command %q", args[0])
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
	if len(args) != 2 || args[1] != "status" {
		return errors.New("worker command requires <worker> status")
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

func printMeta(output io.Writer, meta agentmanager.Meta) {
	fmt.Fprintf(output, "status=%s ticket-id=%s worker=%s pid=%d session-id=%s\n", meta.Status, meta.TicketID, meta.Worker, meta.PID, meta.NativeSession)
}
