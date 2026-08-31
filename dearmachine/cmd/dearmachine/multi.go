package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/synctrigger"
	"github.com/dearmachine/dearmachine/internal/transports"
)

func requireDaemonStopped(userHomeDir func() (string, error)) error {
	path, err := client.DefaultDaemonLockPath(userHomeDir)
	if err != nil {
		return err
	}
	unlock, err := client.CreateDaemonLock(path)
	if err != nil {
		return err
	}
	return unlock()
}

func runPairStates(args []string, getenv func(string) string, deps dependencies, states []client.PairState) error {
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
	backends, managerPath, customBackends, responseTier, err := loadAgentManagedConfig(cfg, deps)
	if err != nil {
		return err
	}
	logger := deps.newLogger()
	newRawTransport := deps.newRawTransport
	if newRawTransport == nil {
		newRawTransport = transports.NewRaw
	}
	byInbox := make(map[string][]client.PairState)
	inboxOrder := make([]string, 0)
	for _, state := range states {
		if _, found := byInbox[state.Inbox.ID]; !found {
			inboxOrder = append(inboxOrder, state.Inbox.ID)
		}
		byInbox[state.Inbox.ID] = append(byInbox[state.Inbox.ID], state)
	}
	endpoints := make(map[string]client.Transport, len(states))
	for _, inboxID := range inboxOrder {
		group := byInbox[inboxID]
		inbox := group[0].Inbox
		raw, err := newRawTransport(inbox.Transport, inbox.ProviderID)
		if err != nil {
			return fmt.Errorf("open %s inbox %s: %w", inbox.Transport, inbox.ProviderID, err)
		}
		pairs := make([]client.Pair, 0, len(group))
		for _, state := range group {
			pairs = append(pairs, state.Pair)
		}
		router, err := client.NewInboxRouter(raw, inbox, pairs, cfg.pollInterval)
		if err != nil {
			return err
		}
		for _, state := range group {
			endpoint, err := router.Endpoint(state.Pair.ID)
			if err != nil {
				return err
			}
			endpoints[state.Pair.ID] = endpoint
		}
	}

	applications := make([]application, 0, len(states))
	stores := make([]*client.Store, 0, len(states))
	defer func() {
		for _, store := range stores {
			_ = store.Close()
		}
	}()
	for index, state := range states {
		openPairStore := deps.openPairStore
		if openPairStore == nil {
			openPairStore = client.OpenPairStore
		}
		store, err := openPairStore(state.Path, state.Pair)
		if err != nil {
			return fmt.Errorf("open pair %s database: %w", state.Pair.ID, err)
		}
		stores = append(stores, store)
		runner, err := deps.newRunner(cfg.agentBinary, cfg.projectDir, cfg.model)
		if err != nil {
			return err
		}
		runner.SetMagnificaHumanitas(cfg.magnificaHumanitas)
		if err := runner.ConfigureAgentManaged(backends, managerPath, customBackends); err != nil {
			return err
		}
		var orchestrator = (*synctrigger.Orchestrator)(nil)
		if index == 0 {
			orchestrator, err = buildOrchestrator(cfg, deps, logger, backends, managerPath, customBackends)
			if err != nil {
				return err
			}
			if orchestrator != nil && !cfg.maintenanceMinTurnsSet {
				environmentValue := strings.TrimSpace(getenv("DEARMACHINE_MAINTENANCE_MIN_TURNS"))
				if environmentValue != "" {
					cfg.maintenanceMinTurns, err = strconv.Atoi(environmentValue)
					if err != nil || cfg.maintenanceMinTurns < 0 {
						return fmt.Errorf("DEARMACHINE_MAINTENANCE_MIN_TURNS must be a non-negative integer")
					}
					orchestrator.MaintenanceMinTurns = cfg.maintenanceMinTurns
				}
			}
			if orchestrator != nil && orchestrator.MaintenanceMinTurns > 0 {
				orchestrator.TurnCounter = store.CountProcessedSince
			}
		}
		app, err := deps.newApp(endpoints[state.Pair.ID], store, runner, orchestrator, cfg.concurrency, cfg.pollInterval, logger, cfg.verbose, "", responseTier)
		if err != nil {
			return err
		}
		applications = append(applications, app)
	}
	lockPath, err := client.DefaultDaemonLockPath(deps.userHomeDir)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.pidfile) != "" {
		requested, err := resolvePath(cfg.pidfile, deps.userHomeDir)
		if err != nil {
			return err
		}
		if requested != lockPath {
			return fmt.Errorf("--pidfile must be the daemon lock path %s", lockPath)
		}
	}
	newDaemon := deps.newPairDaemon
	if newDaemon == nil {
		newDaemon = newApplicationGroup
	}
	daemon, err := newDaemon(applications, cfg.concurrency, lockPath)
	if err != nil {
		return err
	}
	ctx, stop := deps.notifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cfg.once {
		return daemon.RunOnce(ctx)
	}
	return daemon.Run(ctx)
}

// applicationGroup keeps dependency-injected unit tests lightweight. The
// production dependency replaces it with client.MultiDaemon.
type applicationGroup struct{ applications []application }

func newApplicationGroup(applications []application, _ int, _ string) (application, error) {
	return &applicationGroup{applications: applications}, nil
}

func (group *applicationGroup) Run(ctx context.Context) error {
	for _, app := range group.applications {
		if err := app.Run(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (group *applicationGroup) RunOnce(ctx context.Context) error {
	for _, app := range group.applications {
		if err := app.RunOnce(ctx); err != nil {
			return err
		}
	}
	return nil
}
