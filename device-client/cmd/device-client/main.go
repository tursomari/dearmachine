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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dearmachine/dearmachine/internal/deviceclient"
	"github.com/dearmachine/dearmachine/internal/entrypoint"
	"github.com/dearmachine/dearmachine/internal/synctrigger"

	backendcatalog "github.com/dearmachine/dearmachine/internal/backends"
)

func main() {
	err := run(os.Args[1:], os.Getenv, defaultDependencies())
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return
	}
	log.Printf("device client: %v", err)
	os.Exit(1)
}

type config struct {
	inboxID                string
	dbPath                 string
	projectDir             string
	model                  string
	mctBinary              string
	deviceConfig           string
	managerPath            string
	entryPointRepo         string
	entryPointPrompt       string
	concurrency            int
	maintenanceMinTurns    int
	maintenanceMinTurnsSet bool
	pollInterval           time.Duration
	pidfile                string
	once                   bool
	verbose                bool
}

type application interface {
	Run(context.Context) error
	RunOnce(context.Context) error
}

type dependencies struct {
	openStore    func(string) (*deviceclient.Store, error)
	newTransport func(string) (deviceclient.Transport, error)
	newRunner    func(string, string, string) (*deviceclient.MCTRunner, error)
	newApp       func(
		deviceclient.Transport,
		*deviceclient.Store,
		*deviceclient.MCTRunner,
		*synctrigger.Orchestrator,
		int,
		time.Duration,
		*log.Logger,
		bool,
		string,
	) (application, error)
	newLogger     func() *log.Logger
	notifyContext func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)
	flagOutput    io.Writer
	stdin         io.Reader
	stdout        io.Writer
	lookPath      func(string) (string, error)
	userHomeDir   func() (string, error)
}

func defaultDependencies() dependencies {
	return dependencies{
		openStore: deviceclient.OpenStore,
		newTransport: func(inboxID string) (deviceclient.Transport, error) {
			return deviceclient.NewAgentMailTransport(inboxID)
		},
		newRunner: deviceclient.NewMCTRunner,
		newApp: func(
			transport deviceclient.Transport,
			store *deviceclient.Store,
			runner *deviceclient.MCTRunner,
			syncOrchestrator *synctrigger.Orchestrator,
			concurrency int,
			pollInterval time.Duration,
			logger *log.Logger,
			verbose bool,
			pidfile string,
		) (application, error) {
			return deviceclient.New(
				transport,
				store,
				runner,
				syncOrchestrator,
				concurrency,
				pollInterval,
				logger,
				verbose,
				pidfile,
			)
		},
		newLogger: func() *log.Logger {
			return log.New(os.Stderr, "device-client: ", log.LstdFlags)
		},
		notifyContext: signal.NotifyContext,
		flagOutput:    os.Stderr,
		stdin:         os.Stdin,
		stdout:        os.Stdout,
		lookPath:      exec.LookPath,
		userHomeDir:   os.UserHomeDir,
	}
}

func parseConfig(args []string, output io.Writer) (config, error) {
	flags := flag.NewFlagSet("device-client", flag.ContinueOnError)
	flags.SetOutput(output)
	var cfg config
	flags.StringVar(&cfg.inboxID, "inbox-id", "", "AgentMail inbox ID")
	flags.StringVar(
		&cfg.dbPath,
		"db",
		"",
		"SQLite state database path (default: ~/.dearmachine/state/device-client.db)",
	)
	flags.StringVar(
		&cfg.projectDir,
		"project",
		".",
		"mct-agent session working directory; independent of --entry-point-repo",
	)
	flags.StringVar(
		&cfg.model,
		"model",
		"",
		"optional mct-agent model alias; project default when omitted",
	)
	flags.StringVar(
		&cfg.mctBinary,
		"mct-agent",
		"mct-agent",
		"path to the mct-agent executable",
	)
	flags.StringVar(
		&cfg.deviceConfig,
		"config",
		"",
		"device configuration path (default: ~/.dearmachine/config/device-client.toml)",
	)
	flags.StringVar(
		&cfg.managerPath,
		"agent-manager",
		"",
		"agent-manager executable (default: ~/.dearmachine/agent-manager/agent-manager)",
	)
	flags.StringVar(
		&cfg.entryPointRepo,
		"entry-point-repo",
		"~/.dearmachine/entrypoint/main",
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
		60*time.Second,
		"delay after each completed AgentMail poll",
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
	flags.StringVar(&cfg.pidfile, "pidfile", "", "path to write the Device Client process ID")
	flags.BoolVar(&cfg.once, "once", false, "poll once, process available messages, and exit")
	flags.BoolVar(&cfg.verbose, "verbose", false, "log every AgentMail poll cycle")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	flags.Visit(func(setFlag *flag.Flag) {
		if setFlag.Name == "maintenance-min-turns" {
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
	if len(args) > 0 && args[0] == "init" {
		return runInit(args[1:], deps)
	}
	if len(args) > 0 && args[0] == "setup-agents" {
		return runSetupAgents(args[1:], deps)
	}
	if len(args) > 0 && args[0] == "inbox" {
		return runInbox(args[1:], getenv, deps)
	}
	flagOutput := deps.flagOutput
	if flagOutput == nil {
		flagOutput = io.Discard
	}
	cfg, err := parseConfig(args, flagOutput)
	if err != nil {
		return err
	}
	if cfg.inboxID == "" {
		return fmt.Errorf("--inbox-id is required")
	}
	if getenv("AGENTMAIL_API_KEY") == "" {
		return fmt.Errorf("AGENTMAIL_API_KEY is required")
	}
	backends, managerPath, customBackends, err := loadAgentManagedConfig(cfg, deps)
	if err != nil {
		return err
	}
	dbPath := cfg.dbPath
	if strings.TrimSpace(dbPath) == "" {
		dbPath, err = deviceclient.DefaultDeviceDatabasePath(deps.userHomeDir)
		if err != nil {
			return err
		}
	}

	store, err := deps.openStore(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	transport, err := deps.newTransport(cfg.inboxID)
	if err != nil {
		return err
	}
	runner, err := deps.newRunner(cfg.mctBinary, cfg.projectDir, cfg.model)
	if err != nil {
		return err
	}
	if err := runner.ConfigureAgentManaged(backends, managerPath); err != nil {
		return err
	}
	logger := deps.newLogger()
	orchestrator, err := buildOrchestrator(cfg, deps, logger, backends, managerPath, customBackends)
	if err != nil {
		return err
	}
	if orchestrator != nil && !cfg.maintenanceMinTurnsSet {
		environmentValue := strings.TrimSpace(getenv("DEARMACHINE_MAINTENANCE_MIN_TURNS"))
		if environmentValue != "" {
			cfg.maintenanceMinTurns, err = strconv.Atoi(environmentValue)
			if err != nil {
				return fmt.Errorf("DEARMACHINE_MAINTENANCE_MIN_TURNS must be an integer: %w", err)
			}
			if cfg.maintenanceMinTurns < 0 {
				return fmt.Errorf("--maintenance-min-turns must not be negative")
			}
			orchestrator.MaintenanceMinTurns = cfg.maintenanceMinTurns
		}
	}
	if orchestrator != nil && orchestrator.MaintenanceMinTurns > 0 {
		orchestrator.TurnCounter = store.CountProcessedSince
	}
	app, err := deps.newApp(
		transport,
		store,
		runner,
		orchestrator,
		cfg.concurrency,
		cfg.pollInterval,
		logger,
		cfg.verbose,
		cfg.pidfile,
	)
	if err != nil {
		return err
	}

	ctx, stop := deps.notifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	if cfg.once {
		return app.RunOnce(ctx)
	}
	return app.Run(ctx)
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
		"~/.dearmachine/entrypoint/main",
		"entry-point repository to initialize",
	)
	mctBinary := flags.String("mct-agent", "mct-agent", "path to the mct-agent executable")
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
	result, err := entrypoint.Initialize(context.Background(), entrypoint.Options{
		RepoPath:    resolvedRepo,
		MCTBinary:   *mctBinary,
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
		"Initialized entry point: %s\nSkeleton commit: %s\nDearMachine commit: %s\nProject store: %s\n",
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

func loadAgentManagedConfig(cfg config, deps dependencies) ([]string, string, []backendcatalog.Backend, error) {
	configPath := cfg.deviceConfig
	var err error
	if configPath == "" {
		configPath, err = deviceclient.DefaultDeviceConfigPath(deps.userHomeDir)
		if err != nil {
			return nil, "", nil, err
		}
	}
	managerPath := cfg.managerPath
	if managerPath == "" {
		managerPath, err = deviceclient.DefaultAgentManagerPath(deps.userHomeDir)
		if err != nil {
			return nil, "", nil, err
		}
	}
	managerPath, err = filepath.Abs(managerPath)
	if err != nil {
		return nil, "", nil, fmt.Errorf("resolve agent-manager path: %w", err)
	}
	customBackendCatalog, err := deviceclient.LoadCustomBackendsFromManager(managerPath)
	if err != nil {
		return nil, "", nil, err
	}
	deviceConfig, err := deviceclient.LoadDeviceConfigWithCustom(configPath, customBackendCatalog...)
	if err != nil {
		return nil, "", nil, err
	}
	return append([]string(nil), deviceConfig.Backends...), managerPath, customBackendCatalog, nil
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
		MCTBinary:           cfg.mctBinary,
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
		path, err := deviceclient.DefaultDeviceConfigPath(deps.userHomeDir)
		if err != nil {
			return err
		}
		*configPath = path
	}
	_, err := deviceclient.SetupAgents(
		deps.stdin,
		deps.stdout,
		filepath.Clean(*configPath),
		preferred,
		deps.lookPath,
	)
	return err
}
