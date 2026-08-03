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
		return errors.New("usage: agent-manager ticket <send|status|view|cancel> ... | worker <name> <health|status>")
	}
	switch args[0] {
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
		if len(args) < 2 {
			return errors.New("worker is required")
		}
		flags := flag.NewFlagSet("ticket send", flag.ContinueOnError)
		flags.SetOutput(errorsOutput)
		request := flags.String("file", "", "work request file")
		cwd := flags.String("cwd", "", "worker working directory")
		if err := flags.Parse(args[2:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || strings.TrimSpace(*request) == "" || strings.TrimSpace(*cwd) == "" {
			return errors.New("ticket send requires --file <path> and --cwd <project-dir>")
		}
		id, err := manager.Send(args[1], *request, *cwd)
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

func runWorker(manager *agentmanager.Manager, args []string, output io.Writer) error {
	if len(args) != 2 {
		return errors.New("worker command requires <worker> <health|status>")
	}
	switch args[1] {
	case "health":
		path, err := manager.WorkerHealth(args[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "healthy worker=%s path=%s\n", args[0], path)
		return nil
	case "status":
		metas, err := manager.List(args[0])
		if err != nil {
			return err
		}
		for _, meta := range metas {
			printMeta(output, meta)
		}
		return nil
	default:
		return fmt.Errorf("unknown worker command %q", args[1])
	}
}

func printMeta(output io.Writer, meta agentmanager.Meta) {
	fmt.Fprintf(output, "status=%s ticket-id=%s worker=%s pid=%d session-id=%s\n", meta.Status, meta.TicketID, meta.Worker, meta.PID, meta.NativeSession)
}
