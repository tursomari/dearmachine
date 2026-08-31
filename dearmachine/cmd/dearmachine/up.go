package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"strings"

	"github.com/dearmachine/dearmachine/internal/client"
)

type stringListFlag []string

func (values *stringListFlag) add(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("--pair requires an email address or UUID")
	}
	*values = append(*values, value)
	return nil
}

type upCommand struct {
	create        bool
	list          bool
	help          bool
	newInbox      bool
	email         string
	inbox         string
	transport     string
	pairSelectors stringListFlag
}

func runUp(args []string, getenv func(string) string, deps dependencies) error {
	command, runArgs, err := parseUpArgs(args)
	if err != nil {
		return err
	}
	if command.help {
		return upHelp(deps.stdout)
	}
	registryPath, err := client.DefaultPairRegistryPath(deps.userHomeDir)
	if err != nil {
		return err
	}
	registry, err := client.LoadPairRegistry(registryPath)
	if err != nil {
		return err
	}
	output := outputOrDiscard(deps.stdout)
	if command.list {
		return printRegistry(output, registry)
	}
	if err := validateUpRunArgs(runArgs, deps); err != nil {
		return err
	}
	if command.create {
		if len(command.pairSelectors) != 0 {
			return errors.New("--pair cannot be combined with --create")
		}
		request, err := collectCreateRequest(command, deps.stdin, output, inputIsInteractive(deps))
		if err != nil {
			return err
		}
		if err := requireDaemonStopped(deps.userHomeDir); err != nil {
			return fmt.Errorf("create pair: %w", err)
		}
		inbox, err := resolveCreateInbox(context.Background(), request, registry, deps)
		if err != nil {
			return err
		}
		if deps.authorizePair == nil {
			return fmt.Errorf("transport %q cannot authorize pair creation", inbox.Transport)
		}
		if err := deps.authorizePair(
			context.Background(), inbox.Transport, inbox.ProviderID, request.email,
		); err != nil {
			return fmt.Errorf("authorize %s pair: %w", inbox.Transport, err)
		}
		pair, err := client.CreatePair(deps.userHomeDir, client.Pair{UserEmail: request.email, InboxID: inbox.ID})
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(output, "Created pair %s (%s) on inbox %s (%s).\n", pair.UserEmail, pair.ID, inbox.Address, inbox.ID)
	}
	states, err := client.ResolvePairStates(deps.userHomeDir, command.pairSelectors)
	if err != nil {
		return err
	}
	return runPairStates(runArgs, getenv, deps, states)
}

func validateUpRunArgs(args []string, deps dependencies) error {
	output := deps.flagOutput
	if output == nil {
		output = io.Discard
	}
	cfg, err := parseConfig(args, output)
	if err != nil {
		return err
	}
	if cfg.inboxID != "" || cfg.dbPath != "" || cfg.allowSet {
		return errors.New("--inbox-id, --db, and --allow are direct diagnostic flags and cannot be used with `dearmachine up`")
	}
	if strings.TrimSpace(cfg.pidfile) != "" {
		lockPath, err := client.DefaultDaemonLockPath(deps.userHomeDir)
		if err != nil {
			return err
		}
		requested, err := resolvePath(cfg.pidfile, deps.userHomeDir)
		if err != nil {
			return err
		}
		if requested != lockPath {
			return fmt.Errorf("--pidfile must be the daemon lock path %s", lockPath)
		}
	}
	return nil
}

func parseUpArgs(args []string) (upCommand, []string, error) {
	var command upCommand
	runArgs := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := args[index]
		value := func(name string) (string, error) {
			index++
			if index == len(args) || strings.TrimSpace(args[index]) == "" {
				return "", fmt.Errorf("%s requires a value", name)
			}
			return args[index], nil
		}
		switch {
		case arg == "--create":
			command.create = true
		case arg == "--new-inbox":
			command.newInbox = true
		case arg == "--list":
			command.list = true
		case arg == "--help" || arg == "-h":
			command.help = true
		case arg == "--new" || strings.HasPrefix(arg, "--new="):
			return upCommand{}, nil, errors.New("flag provided but not defined: --new")
		case arg == "--switch" || strings.HasPrefix(arg, "--switch="):
			return upCommand{}, nil, errors.New("flag provided but not defined: --switch")
		case arg == "--pair":
			selected, err := value("--pair")
			if err != nil {
				return upCommand{}, nil, err
			}
			if err := command.pairSelectors.add(selected); err != nil {
				return upCommand{}, nil, err
			}
		case strings.HasPrefix(arg, "--pair="):
			if err := command.pairSelectors.add(strings.TrimPrefix(arg, "--pair=")); err != nil {
				return upCommand{}, nil, err
			}
		case arg == "--email" || arg == "--inbox" || arg == "--transport":
			selected, err := value(arg)
			if err != nil {
				return upCommand{}, nil, err
			}
			switch arg {
			case "--email":
				command.email = selected
			case "--inbox":
				command.inbox = selected
			case "--transport":
				command.transport = selected
			}
		case strings.HasPrefix(arg, "--email="):
			command.email = strings.TrimPrefix(arg, "--email=")
		case strings.HasPrefix(arg, "--inbox="):
			command.inbox = strings.TrimPrefix(arg, "--inbox=")
		case strings.HasPrefix(arg, "--transport="):
			command.transport = strings.TrimPrefix(arg, "--transport=")
		default:
			runArgs = append(runArgs, arg)
		}
	}
	if command.list && (command.create || command.newInbox || command.email != "" || command.inbox != "" || command.transport != "" || len(command.pairSelectors) != 0) {
		return upCommand{}, nil, errors.New("--list cannot be combined with creation or selection flags")
	}
	if !command.create && (command.newInbox || command.email != "" || command.inbox != "" || command.transport != "") {
		return upCommand{}, nil, errors.New("--email, --inbox, --new-inbox, and --transport require --create")
	}
	if command.newInbox && strings.TrimSpace(command.inbox) != "" {
		return upCommand{}, nil, errors.New("choose exactly one of --new-inbox or --inbox")
	}
	return command, runArgs, nil
}

type createRequest struct {
	email     string
	newInbox  bool
	inbox     string
	transport string
}

func collectCreateRequest(command upCommand, input io.Reader, output io.Writer, interactive bool) (createRequest, error) {
	request := createRequest{
		email: strings.TrimSpace(command.email), newInbox: command.newInbox,
		inbox: strings.TrimSpace(command.inbox), transport: strings.ToLower(strings.TrimSpace(command.transport)),
	}
	reader := bufio.NewReader(inputOrEmpty(input))
	prompt := func(label string, target *string) error {
		if strings.TrimSpace(*target) != "" {
			return nil
		}
		if !interactive {
			return fmt.Errorf("%s is required in non-interactive mode", label)
		}
		if _, err := fmt.Fprintf(output, "%s: ", label); err != nil {
			return err
		}
		value, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		*target = strings.TrimSpace(value)
		if *target == "" {
			return fmt.Errorf("%s is required", label)
		}
		return nil
	}
	if _, err := fmt.Fprintln(output, "Create a DearMachine pair:"); err != nil {
		return createRequest{}, err
	}
	if err := prompt("User email", &request.email); err != nil {
		return createRequest{}, err
	}
	parsedEmail, err := mail.ParseAddress(request.email)
	if err != nil || strings.TrimSpace(parsedEmail.Address) == "" {
		return createRequest{}, errors.New("user email must be an RFC 5322 address")
	}
	request.email = strings.ToLower(strings.TrimSpace(parsedEmail.Address))
	if !request.newInbox && request.inbox == "" {
		if !interactive {
			return createRequest{}, errors.New("choose exactly one of --new-inbox or --inbox")
		}
		choice := ""
		if err := prompt("Existing inbox UUID/address, or 'new'", &choice); err != nil {
			return createRequest{}, err
		}
		if strings.EqualFold(choice, "new") {
			request.newInbox = true
		} else {
			request.inbox = choice
		}
	}
	if request.newInbox {
		if err := prompt("Transport", &request.transport); err != nil {
			return createRequest{}, err
		}
	}
	if request.newInbox == (request.inbox != "") {
		return createRequest{}, errors.New("choose exactly one of --new-inbox or --inbox")
	}
	return request, nil
}

func resolveCreateInbox(ctx context.Context, request createRequest, registry client.PairRegistry, deps dependencies) (client.Inbox, error) {
	if request.inbox != "" {
		registered, err := client.ResolveInbox(registry, request.inbox)
		if err == nil {
			if request.transport != "" && request.transport != registered.Transport {
				return client.Inbox{}, fmt.Errorf("inbox %q uses transport %q, not %q", request.inbox, registered.Transport, request.transport)
			}
			return registered, nil
		}
		if !errors.Is(err, client.ErrUnknownInbox) {
			return client.Inbox{}, err
		}
		if request.transport == "" {
			return client.Inbox{}, fmt.Errorf("%w; add --transport to adopt an exact provider inbox", err)
		}
		if deps.inspectInbox == nil {
			return client.Inbox{}, fmt.Errorf("transport %q cannot inspect an existing inbox", request.transport)
		}
		inspected, inspectErr := deps.inspectInbox(ctx, request.transport, request.inbox)
		if inspectErr != nil {
			return client.Inbox{}, fmt.Errorf("inspect %s inbox %q: %w", request.transport, request.inbox, inspectErr)
		}
		inspected.Transport = request.transport
		return client.RegisterInbox(deps.userHomeDir, inspected)
	}
	if deps.provisionInbox == nil {
		return client.Inbox{}, fmt.Errorf("transport %q does not support inbox provisioning; use --inbox to share or adopt an existing inbox", request.transport)
	}
	provisioned, err := deps.provisionInbox(ctx, request.transport)
	if err != nil {
		return client.Inbox{}, fmt.Errorf("provision %s inbox: %w", request.transport, err)
	}
	provisioned.Transport = request.transport
	return client.RegisterInbox(deps.userHomeDir, provisioned)
}

func printRegistry(output io.Writer, registry client.PairRegistry) error {
	if len(registry.Pairs) == 0 {
		_, err := fmt.Fprintln(output, "No pairs are registered.")
		return err
	}
	inboxes := make(map[string]client.Inbox, len(registry.Inboxes))
	for _, inbox := range registry.Inboxes {
		inboxes[inbox.ID] = inbox
	}
	for _, pair := range registry.Pairs {
		inbox := inboxes[pair.InboxID]
		if _, err := fmt.Fprintf(output, "%s\t%s\t%s\t%s\n", pair.ID, pair.UserEmail, inbox.Address, inbox.Transport); err != nil {
			return err
		}
	}
	return nil
}

func upHelp(output io.Writer) error {
	_, err := fmt.Fprint(outputOrDiscard(output), `Usage:
  dearmachine up [--pair <email-or-uuid> ...] [run flags]
  dearmachine up --create --email <address> (--new-inbox --transport <id> | --inbox <selector>) [run flags]
  dearmachine up --list

Plain "up" starts every registered pair and every referenced inbox in one
daemon. --pair is repeatable and narrows only this invocation; it never changes
global state. --create is the sole creation path. Sharing an inbox is always
intentional and requires --inbox. Pair creation asks the selected transport to
authorize the correspondent before publishing local pair state and requires the
daemon to be down.
`)
	return err
}

func inputOrEmpty(input io.Reader) io.Reader {
	if input == nil {
		return strings.NewReader("")
	}
	return input
}
