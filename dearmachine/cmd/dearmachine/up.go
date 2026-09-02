package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"path/filepath"
	"strings"
	"time"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/entrypoint"
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
	foreground    bool
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
	cfg, err := parseUpRunConfig(runArgs, deps)
	if err != nil {
		return err
	}
	runtimePath, err := client.DefaultRuntimeConfigPath(deps.userHomeDir)
	if err != nil {
		return err
	}
	persisted, found, err := client.LoadRuntimeConfig(runtimePath)
	if err != nil {
		return err
	}
	if found {
		cfg, err = mergeRuntimeConfig(cfg, persisted)
		if err != nil {
			return err
		}
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
		for _, existing := range registry.Pairs {
			if strings.EqualFold(existing.UserEmail, request.email) {
				return fmt.Errorf("pair email %s is already registered as %s", request.email, existing.ID)
			}
		}
		if err := initializeSelectedEntryPoint(context.Background(), cfg, deps, output); err != nil {
			return err
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
		persisted, err := runtimeConfigFrom(cfg, deps.userHomeDir)
		if err != nil {
			return err
		}
		if err := client.SaveRuntimeConfig(runtimePath, persisted); err != nil {
			return err
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
	if command.foreground || cfg.once {
		return runPairStates(cfg, getenv, deps, states)
	}
	if err := requireDaemonStopped(deps.userHomeDir); err != nil {
		return err
	}
	logPath, err := client.DefaultDaemonLogPath(deps.userHomeDir)
	if err != nil {
		return err
	}
	childArgs := []string{"up", "--foreground"}
	for _, selector := range command.pairSelectors {
		childArgs = append(childArgs, "--pair", selector)
	}
	childArgs = append(childArgs, runArgs...)
	starter := deps.startBackground
	if starter == nil {
		starter = startBackground
	}
	pid, err := starter(childArgs, logPath)
	if err != nil {
		return err
	}
	lockPath, err := client.DefaultDaemonLockPath(deps.userHomeDir)
	if err != nil {
		return err
	}
	waitReady := deps.waitDaemonReady
	if waitReady == nil {
		waitReady = client.WaitDaemonReady
	}
	if err := waitReady(lockPath, pid, daemonStartupTimeout); err != nil {
		return fmt.Errorf("background client did not become ready; inspect %s: %w", logPath, err)
	}
	_, err = fmt.Fprintf(output, "Started DearMachine in the background (PID %d). Log: %s\n", pid, logPath)
	return err
}

func parseUpRunConfig(args []string, deps dependencies) (config, error) {
	output := deps.flagOutput
	if output == nil {
		output = io.Discard
	}
	return parseConfig(args, output)
}

func mergeRuntimeConfig(cfg config, persisted client.RuntimeConfig) (config, error) {
	if !cfg.setFlags["project"] {
		cfg.projectDir = persisted.Project
	}
	if !cfg.setFlags["model"] {
		cfg.model = persisted.Model
	}
	if !cfg.setFlags["agent-bin"] {
		cfg.agentBinary = persisted.AgentBinary
	}
	if !cfg.setFlags["config"] {
		cfg.deviceConfig = persisted.DeviceConfig
	}
	if !cfg.setFlags["agent-manager"] {
		cfg.managerPath = persisted.ManagerPath
	}
	if !cfg.setFlags["entry-point-repo"] {
		cfg.entryPointRepo = persisted.EntryPointRepo
	}
	if !cfg.setFlags["entry-point-prompt"] {
		cfg.entryPointPrompt = persisted.EntryPointPrompt
	}
	if !cfg.setFlags["poll-interval"] {
		parsed, err := time.ParseDuration(persisted.PollInterval)
		if err != nil {
			return config{}, fmt.Errorf("parse persisted poll interval: %w", err)
		}
		cfg.pollInterval = parsed
	}
	if !cfg.setFlags["concurrency"] {
		cfg.concurrency = persisted.Concurrency
	}
	if !cfg.setFlags["maintenance-min-turns"] {
		cfg.maintenanceMinTurns = persisted.MaintenanceMinTurns
		cfg.maintenanceMinTurnsSet = persisted.MaintenanceMinTurnsSet
	}
	if !cfg.setFlags["magnifica-humanitas"] {
		cfg.magnificaHumanitas = persisted.MagnificaHumanitas
	}
	if !cfg.setFlags["verbose"] {
		cfg.verbose = persisted.Verbose
	}
	return cfg, nil
}

func runtimeConfigFrom(cfg config, userHomeDir func() (string, error)) (client.RuntimeConfig, error) {
	project, err := resolvePath(cfg.projectDir, userHomeDir)
	if err != nil {
		return client.RuntimeConfig{}, err
	}
	entryPointRepo := strings.TrimSpace(cfg.entryPointRepo)
	if entryPointRepo != "" {
		entryPointRepo, err = resolvePath(entryPointRepo, userHomeDir)
		if err != nil {
			return client.RuntimeConfig{}, err
		}
	}
	entryPointPrompt := strings.TrimSpace(cfg.entryPointPrompt)
	if entryPointPrompt != "" {
		entryPointPrompt, err = resolvePath(entryPointPrompt, userHomeDir)
		if err != nil {
			return client.RuntimeConfig{}, err
		}
	}
	deviceConfig := strings.TrimSpace(cfg.deviceConfig)
	if deviceConfig != "" {
		deviceConfig, err = resolvePath(deviceConfig, userHomeDir)
		if err != nil {
			return client.RuntimeConfig{}, err
		}
	}
	managerPath := strings.TrimSpace(cfg.managerPath)
	if managerPath != "" {
		managerPath, err = resolvePath(managerPath, userHomeDir)
		if err != nil {
			return client.RuntimeConfig{}, err
		}
	}
	agentBinary := strings.TrimSpace(cfg.agentBinary)
	if strings.ContainsRune(agentBinary, filepath.Separator) {
		agentBinary, err = resolvePath(agentBinary, userHomeDir)
		if err != nil {
			return client.RuntimeConfig{}, err
		}
	}
	return client.RuntimeConfig{
		Version:                client.RuntimeConfigVersion,
		Project:                project,
		Model:                  cfg.model,
		AgentBinary:            agentBinary,
		DeviceConfig:           deviceConfig,
		ManagerPath:            managerPath,
		EntryPointRepo:         entryPointRepo,
		EntryPointPrompt:       entryPointPrompt,
		PollInterval:           cfg.pollInterval.String(),
		Concurrency:            cfg.concurrency,
		MaintenanceMinTurns:    cfg.maintenanceMinTurns,
		MaintenanceMinTurnsSet: cfg.maintenanceMinTurnsSet,
		MagnificaHumanitas:     cfg.magnificaHumanitas,
		Verbose:                cfg.verbose,
	}, nil
}

func initializeSelectedEntryPoint(ctx context.Context, cfg config, deps dependencies, output io.Writer) error {
	repoPath, err := resolvePath(cfg.entryPointRepo, deps.userHomeDir)
	if err != nil {
		return err
	}
	initialize := deps.initializeEntryPoint
	if initialize == nil {
		initialize = entrypoint.Initialize
	}
	result, err := initialize(ctx, entrypoint.Options{
		RepoPath:    repoPath,
		AgentBinary: cfg.agentBinary,
	})
	if err != nil {
		return fmt.Errorf("initialize selected entry point: %w", err)
	}
	if result.AlreadyInitialized {
		return nil
	}
	_, err = fmt.Fprintf(output, "Initialized entry point: %s\n", result.RepoPath)
	return err
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
		case arg == "--foreground":
			command.foreground = true
		case arg == "--new-inbox":
			command.newInbox = true
		case arg == "--list":
			return upCommand{}, nil, errors.New("flag provided but not defined: --list; use `dearmachine status`")
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

Plain "up" starts every registered pair and every referenced inbox in one
background client using the runtime settings recorded by pair creation.
--foreground keeps that client attached for service managers and containers.
--pair is repeatable and narrows only this invocation; it never changes global
state. --create is the sole creation path. Sharing an inbox is always intentional
and requires --inbox. Pair creation asks the selected transport to authorize the correspondent before publishing local pair state. It also initializes the selected entry point when absent and leaves existing Git repositories unchanged.
Creation requires the daemon to be down.
`)
	return err
}

func inputOrEmpty(input io.Reader) io.Reader {
	if input == nil {
		return strings.NewReader("")
	}
	return input
}
