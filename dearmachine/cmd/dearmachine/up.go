package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dearmachine/dearmachine/internal/hostos"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/entrypoint"
	"github.com/dearmachine/dearmachine/internal/machtianiconfig"
	"github.com/dearmachine/dearmachine/internal/supervisor"
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
	resume        bool
	foreground    bool
	help          bool
	newInbox      bool
	email         string
	inbox         string
	transport     string
	pairSelectors stringListFlag
}

func runUp(args []string, getenv func(string) string, deps dependencies) error {
	if len(args) == 1 && args[0] == "--bootstrap" {
		return runBootstrap(getenv, deps)
	}
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
	if found || len(registry.Pairs) > 0 {
		home, err := deps.userHomeDir()
		if err != nil {
			return err
		}
		if err := machtianiconfig.Migrate(context.Background(), cfg.agentBinary, home); err != nil {
			return err
		}
	}
	if command.create {
		if len(command.pairSelectors) != 0 {
			return errors.New("--pair cannot be combined with --create")
		}
		transactionPath, err := createTransactionPath(deps.userHomeDir)
		if err != nil {
			return err
		}
		transaction, foundTransaction, err := loadCreateTransaction(transactionPath)
		if err != nil {
			return err
		}
		if foundTransaction && !command.resume {
			return fmt.Errorf("pair creation is incomplete; retry the same command with --resume")
		}
		var request createRequest
		if foundTransaction {
			request, err = resumeCreateRequest(command, transaction)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(output, "Resuming incomplete Dear Machine setup.")
		} else {
			request, err = collectCreateRequest(command, deps.stdin, output, inputIsInteractive(deps))
			if err != nil {
				return err
			}
			transaction = createTransaction{Version: createTransactionVersion, Phase: createPhaseRequested, Request: newCreateSelection(request)}
			if err := saveCreateTransaction(transactionPath, transaction); err != nil {
				return err
			}
		}
		if err := requireDaemonStopped(deps.userHomeDir); err != nil {
			return fmt.Errorf("create pair: %w", err)
		}
		existingPair, pairAlreadyPublished := pairForEmail(registry, request.email)
		if pairAlreadyPublished && phaseBeforeCreate(transaction.Phase, createPhasePairCreated) {
			if transaction.Inbox.ID == "" || existingPair.InboxID != transaction.Inbox.ID {
				return fmt.Errorf("pair email %s is already registered as %s", request.email, existingPair.ID)
			}
			transaction.PairID = existingPair.ID
			transaction.Phase = createPhasePairCreated
			if err := saveCreateTransaction(transactionPath, transaction); err != nil {
				return err
			}
		}
		if request.transport != transaction.Request.Transport {
			previous := transaction.Request.Transport
			transaction.Request = newCreateSelection(request)
			if err := saveCreateTransaction(transactionPath, transaction); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(output, "Changed inbox provider from %s to %s. Completed local setup is retained; previous provider resources are left unchanged.\n", previous, request.transport)
		}
		if phaseBeforeCreate(transaction.Phase, createPhaseEntryPointInitialized) {
			if err := initializeSelectedEntryPoint(context.Background(), cfg, deps, output, command.resume); err != nil {
				return err
			}
			transaction.Phase = createPhaseEntryPointInitialized
			if err := saveCreateTransaction(transactionPath, transaction); err != nil {
				return err
			}
		}
		inbox := transaction.Inbox.clientInbox()
		if phaseBeforeCreate(transaction.Phase, createPhaseInboxResolved) {
			inbox, err = resolveCreateInbox(context.Background(), request, registry, deps)
			if err != nil {
				return err
			}
			transaction.Inbox = newCreateInbox(inbox)
			transaction.Phase = createPhaseInboxResolved
			if err := saveCreateTransaction(transactionPath, transaction); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(output, "Inbox ready: %s (%s).\n", inbox.Address, inbox.Transport)
		}
		if phaseBeforeCreate(transaction.Phase, createPhasePairAuthorized) {
			if deps.authorizePair == nil {
				return fmt.Errorf("transport %q cannot authorize pair creation", inbox.Transport)
			}
			guestPath, err := client.DefaultGuestDatabasePath(deps.userHomeDir)
			if err != nil {
				return err
			}
			guests, err := client.OpenGuestStore(guestPath)
			if err != nil {
				return err
			}
			err = guests.AuthorizePair(context.Background(), inbox, request.email, func() error {
				return deps.authorizePair(context.Background(), inbox.Transport, inbox.ProviderID, request.email)
			})
			closeErr := guests.Close()
			if err != nil || closeErr != nil {
				return fmt.Errorf("authorize %s pair: %w", inbox.Transport, errors.Join(err, closeErr))
			}
			transaction.Phase = createPhasePairAuthorized
			if err := saveCreateTransaction(transactionPath, transaction); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(output, "Authorized the sender for this Dear Machine pair.")
		}
		if phaseBeforeCreate(transaction.Phase, createPhaseRuntimeConfigured) {
			persisted, err := runtimeConfigFrom(cfg, deps.userHomeDir)
			if err != nil {
				return err
			}
			if err := client.SaveRuntimeConfig(runtimePath, persisted); err != nil {
				return err
			}
			transaction.Phase = createPhaseRuntimeConfigured
			if err := saveCreateTransaction(transactionPath, transaction); err != nil {
				return err
			}
		}
		pair := existingPair
		if phaseBeforeCreate(transaction.Phase, createPhasePairCreated) {
			pair, err = client.CreatePair(deps.userHomeDir, client.Pair{UserEmail: request.email, InboxID: inbox.ID})
			if err != nil {
				return err
			}
			transaction.PairID = pair.ID
			transaction.Phase = createPhasePairCreated
			if err := saveCreateTransaction(transactionPath, transaction); err != nil {
				return err
			}
		}
		_, _ = fmt.Fprintf(output, "Created pair %s (%s) on inbox %s (%s).\n", pair.UserEmail, pair.ID, inbox.Address, inbox.ID)
		if err := os.Remove(transactionPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("finish pair creation transaction: %w", err)
		}
	}
	states, err := client.ResolvePairStates(deps.userHomeDir, command.pairSelectors)
	if err != nil {
		return err
	}
	if command.foreground || cfg.once {
		return runPairStates(cfg, getenv, deps, states)
	}
	lock, err := client.DefaultDaemonLockPath(deps.userHomeDir)
	if err != nil {
		return err
	}
	present, err := supervisor.HasRecord(stateFromLock(lock))
	if err != nil {
		return err
	}
	if !present {
		if err := requireDaemonStopped(deps.userHomeDir); err != nil {
			return err
		}
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
	if !cfg.setFlags["minimal-footer"] {
		cfg.minimalFooter = persisted.MinimalFooter
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
		MinimalFooter:          cfg.minimalFooter,
		Verbose:                cfg.verbose,
	}, nil
}

func initializeSelectedEntryPoint(ctx context.Context, cfg config, deps dependencies, output io.Writer, resume bool) error {
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
		Resume:      resume,
		Progress: func(message string) {
			_, _ = fmt.Fprintf(output, "Entry point: %s.\n", message)
		},
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
		case arg == "--resume":
			command.resume = true
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
	if !command.create && (command.resume || command.newInbox || command.email != "" || command.inbox != "" || command.transport != "") {
		return upCommand{}, nil, errors.New("--resume, --email, --inbox, --new-inbox, and --transport require --create")
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
	if _, err := fmt.Fprintln(output, "Pair UUID\tAuthorized sender\tDear Machine inbox\tTransport"); err != nil {
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
  dearmachine up --create [--resume] --email <address> (--new-inbox --transport <id> | --inbox <selector>) [run flags]

Plain "up" starts every registered pair and every referenced inbox in one
background client using the runtime settings recorded by pair creation.
--foreground keeps that client attached for service managers and containers.
--pair is repeatable and narrows only this invocation; it never changes global
state. --create is the sole creation path. Sharing an inbox is always intentional
and requires --inbox. With --resume, an explicit --transport change is allowed for a new inbox only before an inbox or pairing is recorded. Completed local setup is retained and previous provider resources are left unchanged.
Pair creation asks the selected transport to authorize the correspondent before publishing local pair state. It also initializes the selected entry point when absent and leaves existing Git repositories unchanged.
Creation requires the daemon to be down.
`)
	return err
}

const createTransactionVersion = 1

type createPhase string

const (
	createPhaseRequested             createPhase = "requested"
	createPhaseEntryPointInitialized createPhase = "entry-point-initialized"
	createPhaseInboxResolved         createPhase = "inbox-resolved"
	createPhasePairAuthorized        createPhase = "pair-authorized"
	createPhaseRuntimeConfigured     createPhase = "runtime-configured"
	createPhasePairCreated           createPhase = "pair-created"
)

type createSelection struct {
	Email     string `json:"email"`
	NewInbox  bool   `json:"new_inbox"`
	Inbox     string `json:"inbox,omitempty"`
	Transport string `json:"transport,omitempty"`
}

func newCreateSelection(request createRequest) createSelection {
	return createSelection{Email: request.email, NewInbox: request.newInbox, Inbox: request.inbox, Transport: request.transport}
}

func (selection createSelection) createRequest() createRequest {
	return createRequest{email: selection.Email, newInbox: selection.NewInbox, inbox: selection.Inbox, transport: selection.Transport}
}

type createInbox struct {
	ID         string `json:"id"`
	Transport  string `json:"transport"`
	ProviderID string `json:"provider_id"`
	Address    string `json:"address"`
}

func newCreateInbox(inbox client.Inbox) createInbox {
	return createInbox{ID: inbox.ID, Transport: inbox.Transport, ProviderID: inbox.ProviderID, Address: inbox.Address}
}

func (inbox createInbox) clientInbox() client.Inbox {
	return client.Inbox{ID: inbox.ID, Transport: inbox.Transport, ProviderID: inbox.ProviderID, Address: inbox.Address}
}

type createTransaction struct {
	Version int             `json:"version"`
	Phase   createPhase     `json:"phase"`
	Request createSelection `json:"request"`
	Inbox   createInbox     `json:"inbox,omitempty"`
	PairID  string          `json:"pair_id,omitempty"`
}

func createTransactionPath(userHomeDir func() (string, error)) (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("user home directory is empty")
	}
	return filepath.Join(home, ".dearmachine", "state", "create-transaction.json"), nil
}

func loadCreateTransaction(path string) (createTransaction, bool, error) {
	metadata, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return createTransaction{}, false, nil
	}
	if err != nil {
		return createTransaction{}, false, fmt.Errorf("inspect pair creation transaction: %w", err)
	}
	if !metadata.Mode().IsRegular() || metadata.Mode()&os.ModeSymlink != 0 || !hostos.Private(path, metadata, 0o077) {
		return createTransaction{}, false, errors.New("pair creation transaction must be a private regular file owned by the current user")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return createTransaction{}, false, fmt.Errorf("read pair creation transaction: %w", err)
	}
	var transaction createTransaction
	if err := json.Unmarshal(data, &transaction); err != nil {
		return createTransaction{}, false, fmt.Errorf("parse pair creation transaction: %w", err)
	}
	if transaction.Version != createTransactionVersion || !knownCreatePhase(transaction.Phase) {
		return createTransaction{}, false, fmt.Errorf("unsupported pair creation transaction in %s", path)
	}
	return transaction, true, nil
}

func saveCreateTransaction(path string, transaction createTransaction) error {
	data, err := json.MarshalIndent(transaction, "", "  ")
	if err != nil {
		return fmt.Errorf("encode pair creation transaction: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create pair creation state directory: %w", err)
	}
	if err := hostos.Protect(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("secure pair creation state directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".create-*.tmp")
	if err != nil {
		return fmt.Errorf("create pair creation transaction: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := hostos.Protect(temporary.Name(), 0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish pair creation transaction: %w", err)
	}
	return nil
}

func phaseBeforeCreate(current, target createPhase) bool {
	order := map[createPhase]int{
		createPhaseRequested: 1, createPhaseEntryPointInitialized: 2, createPhaseInboxResolved: 3,
		createPhasePairAuthorized: 4, createPhaseRuntimeConfigured: 5, createPhasePairCreated: 6,
	}
	return order[current] < order[target]
}

func knownCreatePhase(phase createPhase) bool {
	return !phaseBeforeCreate(phase, createPhaseRequested) && !phaseBeforeCreate(createPhasePairCreated, phase)
}

// A failed provisioning request must not pin a new installation to an
// unavailable provider. Only an explicit transport change before a durable
// inbox or pair exists may revise the selection; all other resume checks hold.
func resumeCreateRequest(command upCommand, transaction createTransaction) (createRequest, error) {
	request := transaction.Request.createRequest()
	selected := strings.ToLower(strings.TrimSpace(command.transport))
	if selected != "" && selected != request.transport {
		if !request.newInbox || request.inbox != "" ||
			!phaseBeforeCreate(transaction.Phase, createPhaseInboxResolved) ||
			transaction.Inbox != (createInbox{}) || transaction.PairID != "" {
			return createRequest{}, errors.New("cannot change --transport for this incomplete setup: an inbox or pairing is already recorded or an existing inbox was selected; resume with the original selection and preserve that inbox")
		}
		request.transport = selected
	}
	if err := validateResumeSelection(command, request); err != nil {
		return createRequest{}, err
	}
	return request, nil
}

func validateResumeSelection(command upCommand, request createRequest) error {
	checks := []struct{ label, supplied, recorded string }{
		{"email", strings.ToLower(strings.TrimSpace(command.email)), request.email},
		{"inbox", strings.TrimSpace(command.inbox), request.inbox},
		{"transport", strings.ToLower(strings.TrimSpace(command.transport)), request.transport},
	}
	for _, check := range checks {
		if check.supplied != "" && check.supplied != check.recorded {
			return fmt.Errorf("--%s does not match the incomplete pair creation; resume with the original selection", check.label)
		}
	}
	if command.newInbox && !request.newInbox {
		return errors.New("--new-inbox does not match the incomplete pair creation")
	}
	return nil
}

func pairForEmail(registry client.PairRegistry, email string) (client.Pair, bool) {
	for _, pair := range registry.Pairs {
		if strings.EqualFold(pair.UserEmail, email) {
			return pair, true
		}
	}
	return client.Pair{}, false
}

func inputOrEmpty(input io.Reader) io.Reader {
	if input == nil {
		return strings.NewReader("")
	}
	return input
}
