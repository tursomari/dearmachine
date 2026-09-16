package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/entrypoint"
	"github.com/dearmachine/dearmachine/internal/synctrigger"
	"github.com/dearmachine/dearmachine/internal/transports"
	"github.com/mattn/go-isatty"

	backendcatalog "github.com/dearmachine/dearmachine/internal/backends"
)

func main() {
	err := run(os.Args[1:], os.Getenv, defaultDependencies())
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return
	}
	var childExit *conciergeExitError
	if errors.As(err, &childExit) {
		os.Exit(childExit.code)
	}
	log.Printf("dearmachine: %v", err)
	os.Exit(1)
}

type config struct {
	projectDir             string
	model                  string
	agentBinary            string
	deviceConfig           string
	managerPath            string
	entryPointRepo         string
	entryPointPrompt       string
	concurrency            int
	maintenanceMinTurns    int
	maintenanceMinTurnsSet bool
	pollInterval           time.Duration
	once                   bool
	verbose                bool
	magnificaHumanitas     bool
	minimalFooter          bool
	setFlags               map[string]bool
}

const defaultEntryPointRepo = "~/.dearmachine/entrypoint/main"

type application interface {
	Run(context.Context) error
	RunOnce(context.Context) error
}

type dependencies struct {
	initializeEntryPoint func(context.Context, entrypoint.Options) (entrypoint.Result, error)
	openPairStore        func(string, client.Pair) (*client.Store, error)
	newRawTransport      func(string, string) (client.Transport, error)
	provisionInbox       func(context.Context, string) (client.Inbox, error)
	inspectInbox         func(context.Context, string, string) (client.Inbox, error)
	authorizePair        func(context.Context, string, string, string) error
	newRunner            func(string, string, string) (*client.AgentRunner, error)
	newApp               func(
		client.Transport,
		*client.Store,
		*client.AgentRunner,
		*synctrigger.Orchestrator,
		int,
		time.Duration,
		*log.Logger,
		bool,
		string,
		client.ResponseTier,
	) (application, error)
	newPairDaemon     func([]application, int, string) (application, error)
	newLogger         func() *log.Logger
	notifyContext     func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)
	flagOutput        io.Writer
	stdin             io.Reader
	stdout            io.Writer
	lookPath          func(string) (string, error)
	launchConcierge   func(string, []string, io.Reader, io.Writer, io.Writer) error
	execProcess       func(string, []string, []string) error
	userHomeDir       func() (string, error)
	isInteractive     func(io.Reader) bool
	outputInteractive func(io.Writer) bool
	startBackground   func([]string, string) (int, error)
	waitDaemonReady   func(string, int, time.Duration) error
	stopDaemon        func(string, time.Duration) error
	daemonStatus      func(string) (int, bool, error)
}

func defaultDependencies() dependencies {
	return dependencies{
		initializeEntryPoint: entrypoint.Initialize,
		openPairStore:        client.OpenPairStore,
		newRawTransport:      transports.NewRaw,
		provisionInbox:       transports.ProvisionInbox,
		inspectInbox:         transports.InspectInbox,
		authorizePair:        transports.AuthorizePair,
		newRunner:            client.NewAgentRunner,
		newApp: func(
			transport client.Transport,
			store *client.Store,
			runner *client.AgentRunner,
			syncOrchestrator *synctrigger.Orchestrator,
			concurrency int,
			pollInterval time.Duration,
			logger *log.Logger,
			verbose bool,
			pidfile string,
			responseTier client.ResponseTier,
		) (application, error) {
			return client.New(
				transport,
				store,
				runner,
				syncOrchestrator,
				concurrency,
				pollInterval,
				logger,
				verbose,
				pidfile,
				responseTier,
			)
		},
		newPairDaemon: func(applications []application, concurrency int, lockPath string) (application, error) {
			apps := make([]*client.App, 0, len(applications))
			for _, application := range applications {
				app, ok := application.(*client.App)
				if !ok {
					return nil, fmt.Errorf("pair application has unexpected type %T", application)
				}
				apps = append(apps, app)
			}
			return client.NewMultiDaemon(apps, concurrency, lockPath)
		},
		newLogger: func() *log.Logger {
			return log.New(os.Stderr, "dearmachine: ", log.LstdFlags)
		},
		notifyContext:   signal.NotifyContext,
		flagOutput:      os.Stderr,
		stdin:           os.Stdin,
		stdout:          os.Stdout,
		lookPath:        exec.LookPath,
		launchConcierge: launchConciergeForeground,
		execProcess:     syscall.Exec,
		userHomeDir:     os.UserHomeDir,
		isInteractive: func(input io.Reader) bool {
			file, ok := input.(*os.File)
			if !ok {
				return false
			}
			return isatty.IsTerminal(file.Fd())
		},
		outputInteractive: func(output io.Writer) bool { file, ok := output.(*os.File); return ok && isatty.IsTerminal(file.Fd()) },
		startBackground:   startBackground,
		waitDaemonReady:   client.WaitDaemonReady,
		stopDaemon:        stopManagedDaemon,
		daemonStatus:      managedDaemonStatus,
	}
}

func inputIsInteractive(deps dependencies) bool {
	if deps.isInteractive == nil {
		return false
	}
	return deps.isInteractive(deps.stdin)
}

func parseConfig(args []string, output io.Writer) (config, error) {
	flags := flag.NewFlagSet("dearmachine", flag.ContinueOnError)
	flags.SetOutput(output)
	var cfg config
	flags.StringVar(
		&cfg.projectDir,
		"project",
		".",
		"machtiani session working directory; independent of --entry-point-repo",
	)
	flags.StringVar(
		&cfg.model,
		"model",
		"",
		"optional machtiani model alias; project default when omitted",
	)
	flags.StringVar(
		&cfg.agentBinary,
		"agent-bin",
		"machtiani",
		"path to the machtiani executable",
	)
	flags.StringVar(
		&cfg.deviceConfig,
		"config",
		"",
		"device configuration path (default: ~/.dearmachine/config/dearmachine.toml)",
	)
	flags.StringVar(
		&cfg.managerPath,
		"agent-manager",
		"",
		"agent-manager executable (default: agent-manager from PATH)",
	)
	flags.StringVar(
		&cfg.entryPointRepo,
		"entry-point-repo",
		defaultEntryPointRepo,
		"repository used for entry-point documentation sync, not the session working directory",
	)
	flags.StringVar(
		&cfg.entryPointPrompt,
		"entry-point-prompt",
		"~/.dearmachine/entrypoint/main/documentation/update-prompt-template.md",
		"entry-point sync prompt template path (default: ~/.dearmachine/entrypoint/main/documentation/update-prompt-template.md)",
	)
	flags.DurationVar(
		&cfg.pollInterval,
		"poll-interval",
		10*time.Second,
		"delay after each completed mail transport poll",
	)
	flags.IntVar(
		&cfg.concurrency,
		"concurrency",
		3,
		"maximum number of email threads processed concurrently",
	)
	flags.IntVar(
		&cfg.maintenanceMinTurns,
		"maintenance-min-turns",
		20,
		"completed turns required before entry-point maintenance (0 disables the gate)",
	)
	flags.BoolVar(
		&cfg.magnificaHumanitas,
		"magnifica-humanitas",
		false,
		"pass --magnifica-humanitas to machtiani run and parse the selected quote",
	)
	flags.BoolVar(
		&cfg.minimalFooter,
		"minimal-footer",
		false,
		"keep only the session reference in reply footers",
	)
	flags.BoolVar(&cfg.once, "once", false, "poll once, process available messages, and exit")
	flags.BoolVar(&cfg.verbose, "verbose", false, "log every mail transport poll cycle")
	for _, arg := range args {
		if arg == "-magnifica-humanitas" || strings.HasPrefix(arg, "-magnifica-humanitas=") {
			return config{}, fmt.Errorf("flag provided but not defined: -magnifica-humanitas")
		}
		if arg == "-minimal-footer" || strings.HasPrefix(arg, "-minimal-footer=") {
			return config{}, fmt.Errorf("flag provided but not defined: -minimal-footer")
		}
	}
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	flags.Visit(func(setFlag *flag.Flag) {
		if cfg.setFlags == nil {
			cfg.setFlags = make(map[string]bool)
		}
		cfg.setFlags[setFlag.Name] = true
		switch setFlag.Name {
		case "maintenance-min-turns":
			cfg.maintenanceMinTurnsSet = true
		}
	})
	if flags.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if cfg.concurrency < 1 {
		return config{}, fmt.Errorf("--concurrency must be at least 1")
	}
	if cfg.maintenanceMinTurns < 0 {
		return config{}, fmt.Errorf("--maintenance-min-turns must not be negative")
	}
	return cfg, nil
}

func run(args []string, getenv func(string) string, deps dependencies) error {
	wantsHelp := false
	for _, arg := range args {
		wantsHelp = wantsHelp || arg == "--help" || arg == "-h"
	}
	if !wantsHelp && deps.userHomeDir != nil && len(args) > 0 && (args[0] == "up" || args[0] == "restart" || args[0] == "_supervise" || args[0] == "systemd" || args[0] == "persistence") {
		home, err := deps.userHomeDir()
		if err != nil {
			return err
		}
		if err := refuseDuringUninstall(home); err != nil {
			return err
		}
	}
	if len(args) > 0 && args[0] == "uninstall" {
		return runUninstall(args[1:], getenv, deps)
	}
	if len(args) > 0 && args[0] == "guest" {
		return runGuest(args[1:], deps)
	}
	if len(args) > 0 && args[0] == "update" {
		return runUpdate(args[1:], getenv, deps)
	}
	if len(args) > 0 && args[0] == "_update-control" {
		return runUpdateControl(args[1:], deps)
	}
	if len(args) > 0 && (args[0] == "systemd" || args[0] == "persistence") {
		return runSupervisionChoice(args[0], args[1:], deps)
	}
	if len(args) > 0 && args[0] == "_supervise" {
		return runSupervise(args[1:], deps)
	}
	if len(args) > 0 && args[0] == "restart" {
		return runRestart(args[1:], getenv, deps)
	}
	if len(args) > 0 && args[0] == "init" {
		return runInit(args[1:], deps)
	}
	if len(args) > 0 && args[0] == "setup-agents" {
		return runSetupAgents(args[1:], deps)
	}
	if len(args) > 0 && args[0] == "inbox" {
		return runInbox(args[1:], deps)
	}
	if len(args) > 0 && args[0] == "up" {
		return runUp(args[1:], getenv, deps)
	}
	if len(args) > 0 && args[0] == "status" {
		return runStatus(args[1:], deps)
	}
	if len(args) > 0 && args[0] == "down" {
		return runDown(args[1:], deps)
	}
	if len(args) == 0 {
		return runBare(getenv, deps)
	}
	if args[0] == "--help" || args[0] == "-h" {
		return globalHelp(deps.stdout)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func globalHelp(output io.Writer) error {
	output = outputOrDiscard(output)
	_, err := fmt.Fprintln(output, `Usage: dearmachine <command> [options]

Commands:
  uninstall     permanently remove software and private data after terminal confirmation
  update [--check | --recover] [--json] check or update the coordinated Nix installation
  up            create pairs or start registered pairs
  down          stop the background client and cancel retries
  restart       restart through the existing supervisor
  status [--details | --json] show runtime, crash recovery, startup configuration, and pairs
  inbox         maintain pair inbox state
  guest         allow, list or revoke thread-scoped guest access
  init          initialize the entry-point repository
  setup-agents  configure agent backends
  systemd on|off|status     explicitly choose service use (no reboot persistence)
  persistence on|off|status separately approve reboot startup AND loginctl enable-linger`)
	return err
}

func runInit(args []string, deps dependencies) error {
	output := deps.flagOutput
	if output == nil {
		output = io.Discard
	}
	stdout := deps.stdout
	if stdout == nil {
		stdout = io.Discard
	}
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(output)
	repoPath := flags.String(
		"entry-point-repo",
		defaultEntryPointRepo,
		"entry-point repository to initialize",
	)
	agentBinary := flags.String("agent-bin", "machtiani", "path to the machtiani executable")
	snapshotDir := flags.String(
		"snapshot-dir",
		"",
		"optional directory for internal README snapshots around each sync",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected init arguments: %v", flags.Args())
	}
	resolvedRepo, err := resolvePath(*repoPath, deps.userHomeDir)
	if err != nil {
		return err
	}
	resolvedSnapshots := ""
	if strings.TrimSpace(*snapshotDir) != "" {
		resolvedSnapshots, err = resolvePath(*snapshotDir, deps.userHomeDir)
		if err != nil {
			return err
		}
	}
	initialize := deps.initializeEntryPoint
	if initialize == nil {
		initialize = entrypoint.Initialize
	}
	result, err := initialize(context.Background(), entrypoint.Options{
		RepoPath:    resolvedRepo,
		AgentBinary: *agentBinary,
		SnapshotDir: resolvedSnapshots,
	})
	if err != nil {
		return err
	}
	if result.AlreadyInitialized {
		_, err = fmt.Fprintf(stdout, "Entry point already initialized; left unchanged: %s\n", result.RepoPath)
		return err
	}
	if _, err := fmt.Fprintf(
		stdout,
		"Initialized entry point: %s\nSkeleton commit: %s\nDear Machine, commit: %s\nProject store: %s\n",
		result.RepoPath,
		result.SkeletonCommit,
		result.DearMachineCommit,
		result.ProjectStore,
	); err != nil {
		return err
	}
	for _, snapshot := range result.Snapshots {
		if _, err := fmt.Fprintf(stdout, "Internal README snapshot: %s\n", snapshot); err != nil {
			return err
		}
	}
	return nil
}

func loadAgentManagedConfig(cfg config, deps dependencies) ([]string, string, []backendcatalog.Backend, client.ResponseTier, error) {
	configPath := cfg.deviceConfig
	var err error
	if configPath == "" {
		configPath, err = client.DefaultDeviceConfigPath(deps.userHomeDir)
		if err != nil {
			return nil, "", nil, "", err
		}
	}
	managerPath := cfg.managerPath
	if managerPath == "" {
		managerPath, err = deps.lookPath("agent-manager")
		if err != nil {
			return nil, "", nil, "", fmt.Errorf("find packaged agent-manager on PATH: %w", err)
		}
	}
	managerPath, err = filepath.Abs(managerPath)
	if err != nil {
		return nil, "", nil, "", fmt.Errorf("resolve agent-manager path: %w", err)
	}
	customBackendCatalog, err := client.LoadCustomBackendsFromManager(configPath)
	if err != nil {
		return nil, "", nil, "", err
	}
	deviceConfig, err := client.LoadDeviceConfigWithCustom(configPath, customBackendCatalog...)
	if err != nil {
		return nil, "", nil, "", err
	}
	return append([]string(nil), deviceConfig.Backends...), managerPath, customBackendCatalog, deviceConfig.ResponseTier, nil
}

func buildOrchestrator(
	cfg config,
	deps dependencies,
	logger *log.Logger,
	backends []string,
	managerPath string,
	customBackends []backendcatalog.Backend,
) (*synctrigger.Orchestrator, error) {
	entryPointRepo := strings.TrimSpace(cfg.entryPointRepo)
	if entryPointRepo == "" {
		return nil, nil
	}
	resolvedRepo, err := resolvePath(entryPointRepo, deps.userHomeDir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(resolvedRepo); err != nil {
		return nil, nil
	}
	resolvedPrompt, err := resolvePath(cfg.entryPointPrompt, deps.userHomeDir)
	if err != nil {
		return nil, err
	}
	return &synctrigger.Orchestrator{
		RepoPath:            resolvedRepo,
		AgentBinary:         cfg.agentBinary,
		AgentManagerPath:    managerPath,
		Backends:            append([]string(nil), backends...),
		CustomBackends:      append([]backendcatalog.Backend(nil), customBackends...),
		PromptTemplatePath:  resolvedPrompt,
		MaintenanceMinTurns: cfg.maintenanceMinTurns,
		Logger:              logger,
	}, nil
}

func resolvePath(path string, userHomeDir func() (string, error)) (string, error) {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		home, err := userHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		if strings.TrimSpace(home) == "" {
			return "", fmt.Errorf("home directory is empty")
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"+string(filepath.Separator)))
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	return resolved, nil
}

type repeatedStrings []string

func (values *repeatedStrings) String() string {
	return strings.Join(*values, ",")
}

func (values *repeatedStrings) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func runSetupAgents(args []string, deps dependencies) error {
	output := deps.flagOutput
	if output == nil {
		output = io.Discard
	}
	flags := flag.NewFlagSet("setup-agents", flag.ContinueOnError)
	flags.SetOutput(output)
	configPath := flags.String("config", "", "device configuration path")
	var preferred repeatedStrings
	flags.Var(&preferred, "backend", "approved backend ID in priority order (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected setup-agents arguments: %v", flags.Args())
	}
	if *configPath == "" {
		path, err := client.DefaultDeviceConfigPath(deps.userHomeDir)
		if err != nil {
			return err
		}
		*configPath = path
	}
	_, err := client.SetupAgents(
		deps.stdin,
		deps.stdout,
		filepath.Clean(*configPath),
		preferred,
		deps.lookPath,
	)
	return err
}
