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

	agentmail "github.com/agentmail-to/agentmail-go"
	"github.com/dearmachine/dearmachine/internal/deviceclient"
	"github.com/dearmachine/dearmachine/internal/synctrigger"
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
	inboxID          string
	dbPath           string
	projectDir       string
	model            string
	mctBinary        string
	deviceConfig     string
	managerPath      string
	entryPointRepo   string
	entryPointPrompt string
	pollInterval     time.Duration
	pidfile          string
	once             bool
	verbose          bool
}

type application interface {
	Run(context.Context) error
	RunOnce(context.Context) error
}

type dependencies struct {
	openStore  func(string) (*deviceclient.Store, error)
	newMailbox func(agentmail.Client, string) (*deviceclient.Mailbox, error)
	newRunner  func(string, string, string) (*deviceclient.MCTRunner, error)
	newApp     func(
		*deviceclient.Mailbox,
		*deviceclient.Store,
		*deviceclient.MCTRunner,
		*synctrigger.Orchestrator,
		time.Duration,
		*log.Logger,
		bool,
		string,
	) (application, error)
	newClient     func() agentmail.Client
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
		openStore:  deviceclient.OpenStore,
		newMailbox: deviceclient.NewMailbox,
		newRunner:  deviceclient.NewMCTRunner,
		newApp: func(
			mailbox *deviceclient.Mailbox,
			store *deviceclient.Store,
			runner *deviceclient.MCTRunner,
			syncOrchestrator *synctrigger.Orchestrator,
			pollInterval time.Duration,
			logger *log.Logger,
			verbose bool,
			pidfile string,
		) (application, error) {
			return deviceclient.New(
				mailbox,
				store,
				runner,
				syncOrchestrator,
				pollInterval,
				logger,
				verbose,
				pidfile,
			)
		},
		newClient: func() agentmail.Client { return agentmail.NewClient() },
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
	flags.StringVar(&cfg.dbPath, "db", "device-client.db", "SQLite state database path")
	flags.StringVar(
		&cfg.projectDir,
		"project",
		".",
		"project directory used as the mct-agent working directory",
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
		"entry-point repo path (default: ~/.dearmachine/entrypoint/main)",
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
	flags.StringVar(&cfg.pidfile, "pidfile", "", "path to write the Device Client process ID")
	flags.BoolVar(&cfg.once, "once", false, "poll once, process available messages, and exit")
	flags.BoolVar(&cfg.verbose, "verbose", false, "log every AgentMail poll cycle")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	if flags.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	return cfg, nil
}

func run(args []string, getenv func(string) string, deps dependencies) error {
	if len(args) > 0 && args[0] == "setup-agents" {
		return runSetupAgents(args[1:], deps)
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
	backends, managerPath, err := loadAgentManagedConfig(cfg, deps)
	if err != nil {
		return err
	}

	store, err := deps.openStore(cfg.dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	mailbox, err := deps.newMailbox(deps.newClient(), cfg.inboxID)
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
	orchestrator, err := buildOrchestrator(cfg, deps, logger)
	if err != nil {
		return err
	}
	app, err := deps.newApp(
		mailbox,
		store,
		runner,
		orchestrator,
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

func loadAgentManagedConfig(cfg config, deps dependencies) ([]string, string, error) {
	configPath := cfg.deviceConfig
	var err error
	if configPath == "" {
		configPath, err = deviceclient.DefaultDeviceConfigPath(deps.userHomeDir)
		if err != nil {
			return nil, "", err
		}
	}
	deviceConfig, err := deviceclient.LoadDeviceConfig(configPath)
	if err != nil {
		return nil, "", err
	}
	managerPath := cfg.managerPath
	if managerPath == "" {
		managerPath, err = deviceclient.DefaultAgentManagerPath(deps.userHomeDir)
		if err != nil {
			return nil, "", err
		}
	}
	managerPath, err = filepath.Abs(managerPath)
	if err != nil {
		return nil, "", fmt.Errorf("resolve agent-manager path: %w", err)
	}
	return append([]string(nil), deviceConfig.Backends...), managerPath, nil
}

func buildOrchestrator(cfg config, deps dependencies, logger *log.Logger) (*synctrigger.Orchestrator, error) {
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
		RepoPath:           resolvedRepo,
		MCTBinary:          cfg.mctBinary,
		PromptTemplatePath: resolvedPrompt,
		Logger:             logger,
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
