package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/entrypoint"
)

func TestUpWithoutRegistryRequiresCreate(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	err := run([]string{"up", "--once"}, func(string) string { return "" }, deps)
	if err == nil || !strings.Contains(err.Error(), "up --create") {
		t.Fatalf("plain up error = %v", err)
	}
}

func TestUpCreateNewInboxCreatesVersionTwoPairAndRunsIt(t *testing.T) {
	app := &fakeApplication{}
	deps := testDependencies(t, app)
	deps.isInteractive = func(io.Reader) bool { return false }
	var output strings.Builder
	deps.stdout = &output
	if err := run([]string{
		"up", "--create", "--email", " User@Example.test ", "--new-inbox", "--transport", "agentmail", "--once",
	}, func(string) string { return "" }, deps); err != nil {
		t.Fatalf("run up --create: %v", err)
	}
	state, err := client.ResolvePairState(deps.userHomeDir, "user@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if state.Pair.UserEmail != "user@example.test" || state.Inbox.ProviderID != "provisioned-inbox" || app.runOnceCount != 1 {
		t.Fatalf("created state/app = %+v/%d", state, app.runOnceCount)
	}
	db, err := sql.Open("sqlite3", state.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var pairID string
	var schema int
	if err := db.QueryRow("SELECT pair_id, schema_version FROM pair_meta").Scan(&pairID, &schema); err != nil || pairID != state.Pair.ID || schema != client.PairSchemaVersion {
		t.Fatalf("pair metadata = %q/%d, %v", pairID, schema, err)
	}
	if !strings.Contains(output.String(), state.Pair.ID) || !strings.Contains(output.String(), state.Inbox.ID) {
		t.Fatalf("creation output = %q", output.String())
	}
}

func TestUpCreateAutoInitializesDefaultEntryPointBeforeProviderMutation(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	deps.isInteractive = func(io.Reader) bool { return false }
	home, err := deps.userHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	wantRepo := filepath.Join(home, ".dearmachine", "entrypoint", "main")
	initialized := false
	deps.initializeEntryPoint = func(_ context.Context, options entrypoint.Options) (entrypoint.Result, error) {
		if options.RepoPath != wantRepo || options.AgentBinary != "/test/machtiani" {
			t.Fatalf("initialize options = %+v", options)
		}
		initialized = true
		return entrypoint.Result{RepoPath: wantRepo}, nil
	}
	deps.provisionInbox = func(_ context.Context, transport string) (client.Inbox, error) {
		if !initialized {
			t.Fatal("provider mutation happened before default entry-point initialization")
		}
		return client.Inbox{Transport: transport, ProviderID: "provisioned-inbox", Address: "machine@example.test"}, nil
	}
	var output strings.Builder
	deps.stdout = &output
	err = run([]string{
		"up", "--create", "--email", "user@example.test", "--new-inbox", "--transport", "agentmail",
		"--agent-bin", "/test/machtiani", "--once",
	}, func(string) string { return "" }, deps)
	if err != nil {
		t.Fatalf("run up --create: %v", err)
	}
	if !initialized || !strings.Contains(output.String(), "Initialized entry point") {
		t.Fatalf("automatic initialization = %v, output = %q", initialized, output.String())
	}
}

func TestUpCreateSelectedInitializationFailurePrecedesProviderMutation(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	deps.isInteractive = func(io.Reader) bool { return false }
	deps.initializeEntryPoint = func(context.Context, entrypoint.Options) (entrypoint.Result, error) {
		return entrypoint.Result{}, errors.New("injected bootstrap failure")
	}
	mutated := false
	deps.provisionInbox = func(context.Context, string) (client.Inbox, error) {
		mutated = true
		return client.Inbox{}, nil
	}
	err := run([]string{
		"up", "--create", "--email", "user@example.test", "--new-inbox", "--transport", "agentmail", "--once",
		"--entry-point-repo", filepath.Join(t.TempDir(), "selected-entrypoint"),
	}, func(string) string { return "" }, deps)
	if err == nil || !strings.Contains(err.Error(), "initialize selected entry point") ||
		!strings.Contains(err.Error(), "injected bootstrap failure") || mutated {
		t.Fatalf("initialization error = %v, provider mutated = %v", err, mutated)
	}
	registryPath, _ := client.DefaultPairRegistryPath(deps.userHomeDir)
	if _, statErr := os.Stat(registryPath); !os.IsNotExist(statErr) {
		t.Fatalf("failed initialization published registry: %v", statErr)
	}
}

func TestUpCreateLeavesExistingSelectedCustomEntryPointUntouched(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	deps.isInteractive = func(io.Reader) bool { return false }
	deps.initializeEntryPoint = nil
	repo := filepath.Join(t.TempDir(), "custom-entrypoint")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(repo, "README.md")
	if err := os.WriteFile(sentinel, []byte("existing entry point\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	deps.stdout = &output
	if err := run([]string{
		"up", "--create", "--email", "user@example.test", "--new-inbox", "--transport", "agentmail", "--once",
		"--entry-point-repo", repo,
	}, func(string) string { return "" }, deps); err != nil {
		t.Fatalf("run up --create: %v", err)
	}
	contents, err := os.ReadFile(sentinel)
	if err != nil || string(contents) != "existing entry point\n" {
		t.Fatalf("existing entry point changed: %q, %v", contents, err)
	}
	if strings.Contains(output.String(), "Initialized entry point") {
		t.Fatalf("existing initialization reported as new: %q", output.String())
	}
}

func TestUpCreateAutoInitializesSelectedCustomEntryPointBeforeProviderMutation(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	deps.isInteractive = func(io.Reader) bool { return false }
	customRepo := filepath.Join(t.TempDir(), "custom-entrypoint")
	initialized := false
	deps.initializeEntryPoint = func(_ context.Context, options entrypoint.Options) (entrypoint.Result, error) {
		if options.RepoPath != customRepo {
			t.Fatalf("selected initialization path = %q, want %q", options.RepoPath, customRepo)
		}
		initialized = true
		return entrypoint.Result{RepoPath: customRepo}, nil
	}
	deps.provisionInbox = func(_ context.Context, transport string) (client.Inbox, error) {
		if !initialized {
			t.Fatal("provider mutation happened before selected entry-point initialization")
		}
		return client.Inbox{Transport: transport, ProviderID: "provisioned-inbox", Address: "machine@example.test"}, nil
	}
	if err := run([]string{
		"up", "--create", "--email", "user@example.test", "--new-inbox", "--transport", "agentmail",
		"--entry-point-repo", customRepo, "--once",
	}, func(string) string { return "" }, deps); err != nil {
		t.Fatalf("run with custom entry point: %v", err)
	}
	if !initialized {
		t.Fatal("selected custom entry point was not initialized")
	}
}

func TestUpCreateRejectsUnsafeSelectedEntryPointBeforeProviderMutation(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	deps.isInteractive = func(io.Reader) bool { return false }
	deps.initializeEntryPoint = nil
	customRepo := t.TempDir()
	if err := os.WriteFile(filepath.Join(customRepo, "user-file.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mutated := false
	deps.provisionInbox = func(context.Context, string) (client.Inbox, error) {
		mutated = true
		return client.Inbox{}, nil
	}
	err := run([]string{
		"up", "--create", "--email", "user@example.test", "--new-inbox", "--transport", "agentmail",
		"--entry-point-repo", customRepo, "--once",
	}, func(string) string { return "" }, deps)
	if err == nil || !strings.Contains(err.Error(), "not a Git repository, and is not empty") || mutated {
		t.Fatalf("unsafe selected path error = %v, provider mutated = %v", err, mutated)
	}
}

func TestUpDefaultsToAllPairsAndPairFlagNarrowsWithoutMutation(t *testing.T) {
	app := &fakeApplication{}
	deps := testDependencies(t, app)
	first := makeUpTestPair(t, deps, "first")
	second := makeUpTestPair(t, deps, "second")
	path, _ := client.DefaultPairRegistryPath(deps.userHomeDir)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"up", "--once"}, func(string) string { return "" }, deps); err != nil {
		t.Fatalf("plain up: %v", err)
	}
	if app.runOnceCount != 2 {
		t.Fatalf("plain up ran %d apps, want 2", app.runOnceCount)
	}
	app.runOnceCount = 0
	if err := run([]string{"up", "--pair", second.UserEmail, "--once"}, func(string) string { return "" }, deps); err != nil {
		t.Fatalf("selected up: %v", err)
	}
	if app.runOnceCount != 1 || first.ID == second.ID {
		t.Fatalf("selected up ran %d apps", app.runOnceCount)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("selection mutated registry: %v", err)
	}
}

func TestUpCreateCanIntentionallyShareRegisteredInbox(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	first := makeUpTestPair(t, deps, "first")
	registryPath, _ := client.DefaultPairRegistryPath(deps.userHomeDir)
	registry, err := client.LoadPairRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := client.ResolveInbox(registry, registry.Inboxes[0].Address)
	if err != nil {
		t.Fatal(err)
	}
	deps.isInteractive = func(io.Reader) bool { return false }
	deps.provisionInbox = func(context.Context, string) (client.Inbox, error) {
		t.Fatal("shared inbox creation called provisioning")
		return client.Inbox{}, nil
	}
	authorized := false
	deps.authorizePair = func(_ context.Context, transport, providerID, email string) error {
		authorized = true
		if transport != inbox.Transport || providerID != inbox.ProviderID || email != "second@example.test" {
			t.Fatalf("authorize pair = %q/%q/%q", transport, providerID, email)
		}
		return nil
	}
	if err := run([]string{"up", "--create", "--email", "second@example.test", "--inbox", inbox.ID, "--once"}, func(string) string { return "" }, deps); err != nil {
		t.Fatalf("share inbox: %v", err)
	}
	states, err := client.ResolvePairStates(deps.userHomeDir, nil)
	if err != nil || !authorized || len(states) != 2 || states[0].Pair.ID != first.ID || states[1].Inbox.ID != inbox.ID {
		t.Fatalf("shared states = %+v, authorized=%v, %v", states, authorized, err)
	}
}

func TestUpCreateAuthorizationFailureDoesNotPublishPair(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	deps.isInteractive = func(io.Reader) bool { return false }
	inbox, err := client.RegisterInbox(deps.userHomeDir, client.Inbox{
		Transport: "agentmail", ProviderID: "provider-inbox", Address: "machine@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	deps.authorizePair = func(context.Context, string, string, string) error {
		return errors.New("injected policy failure")
	}
	err = run([]string{
		"up", "--create", "--email", "user@example.test", "--inbox", inbox.ID, "--once",
	}, func(string) string { return "" }, deps)
	if err == nil || !strings.Contains(err.Error(), "authorize agentmail pair") ||
		!strings.Contains(err.Error(), "injected policy failure") {
		t.Fatalf("authorization error = %v", err)
	}
	registryPath, _ := client.DefaultPairRegistryPath(deps.userHomeDir)
	registry, err := client.LoadPairRegistry(registryPath)
	if err != nil || len(registry.Pairs) != 0 {
		t.Fatalf("registry after failed authorization = %+v, %v", registry, err)
	}
}

func TestUpCreateRejectsDuplicateEmailBeforeProviderMutation(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	first := makeUpTestPair(t, deps, "duplicate")
	deps.isInteractive = func(io.Reader) bool { return false }
	mutated := false
	deps.provisionInbox = func(context.Context, string) (client.Inbox, error) {
		mutated = true
		return client.Inbox{}, nil
	}
	err := run([]string{
		"up", "--create", "--email", first.UserEmail, "--new-inbox", "--transport", "agentmail", "--once",
	}, func(string) string { return "" }, deps)
	if err == nil || !strings.Contains(err.Error(), "already registered") || mutated {
		t.Fatalf("duplicate creation = %v, provider mutated=%v", err, mutated)
	}
}

func TestUpCreateCanAdoptExactProviderInbox(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	deps.isInteractive = func(io.Reader) bool { return false }
	inspected := false
	deps.inspectInbox = func(_ context.Context, transport, selection string) (client.Inbox, error) {
		inspected = true
		if transport != "openmail" || selection != "provider-inbox" {
			t.Fatalf("inspect = %q/%q", transport, selection)
		}
		return client.Inbox{Transport: transport, ProviderID: "inb-exact", Address: "machine@openmail.test"}, nil
	}
	if err := run([]string{"up", "--create", "--email", "user@example.test", "--inbox", "provider-inbox", "--transport", "openmail", "--once"}, func(string) string { return "" }, deps); err != nil {
		t.Fatalf("adopt inbox: %v", err)
	}
	state, err := client.ResolvePairState(deps.userHomeDir, "user@example.test")
	if err != nil || !inspected || state.Inbox.ProviderID != "inb-exact" || state.Inbox.Transport != "openmail" {
		t.Fatalf("adopted state = %+v, inspected=%v, err=%v", state, inspected, err)
	}
}

func TestUpCreateChecksDaemonLockBeforeProvisioning(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	deps.isInteractive = func(io.Reader) bool { return false }
	path, err := client.DefaultDaemonLockPath(deps.userHomeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	called := false
	deps.provisionInbox = func(context.Context, string) (client.Inbox, error) {
		called = true
		return client.Inbox{}, nil
	}
	err = run([]string{"up", "--create", "--email", "user@example.test", "--new-inbox", "--transport", "agentmail", "--once"}, func(string) string { return "" }, deps)
	if err == nil || !strings.Contains(err.Error(), "already running") || called {
		t.Fatalf("locked creation = %v, provisioned=%v", err, called)
	}
}

func TestUpRemovedNewAndSwitchFlags(t *testing.T) {
	for _, args := range [][]string{{"--new"}, {"--switch", "anything"}} {
		if _, _, err := parseUpArgs(args); err == nil || !strings.Contains(err.Error(), "not defined") {
			t.Fatalf("parseUpArgs(%v) error = %v", args, err)
		}
	}
}

func TestUpCreateNonInteractiveRequiresCompleteIntent(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	deps.isInteractive = func(io.Reader) bool { return false }
	for _, args := range [][]string{
		{"up", "--create", "--email", "user@example.test"},
		{"up", "--create", "--new-inbox", "--transport", "agentmail"},
		{"up", "--create", "--email", "user@example.test", "--new-inbox"},
	} {
		if err := run(args, func(string) string { return "" }, deps); err == nil {
			t.Fatalf("run(%v) unexpectedly succeeded", args)
		}
	}
}

func TestUpCreateValidatesRunFlagsBeforeProviderMutation(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	deps.isInteractive = func(io.Reader) bool { return false }
	called := false
	deps.provisionInbox = func(context.Context, string) (client.Inbox, error) {
		called = true
		return client.Inbox{}, nil
	}
	err := run([]string{
		"up", "--create", "--email", "user@example.test", "--new-inbox", "--transport", "agentmail", "--db", "forbidden.db", "--once",
	}, func(string) string { return "" }, deps)
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined: -db") || called {
		t.Fatalf("preflight error = %v, provisioned=%v", err, called)
	}
	path, _ := client.DefaultPairRegistryPath(deps.userHomeDir)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("preflight created registry: %v", err)
	}
}

func TestPublicHelpDescribesAllPairsAndCreation(t *testing.T) {
	var output strings.Builder
	if err := run([]string{"up", "--help"}, func(string) string { return "" }, dependencies{stdout: &output}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"starts every registered pair", "--pair", "--create", "--new-inbox", "--inbox", "selected entry point when absent", "authorize the correspondent"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("help missing %q:\n%s", want, output.String())
		}
	}
	if err := run([]string{"pair", "--help"}, func(string) string { return "" }, dependencies{flagOutput: io.Discard}); err == nil {
		t.Fatal("pair subcommand unexpectedly succeeded")
	}
}

func makeUpTestPair(t *testing.T, deps dependencies, seed string) client.Pair {
	t.Helper()
	inbox, err := client.RegisterInbox(deps.userHomeDir, client.Inbox{
		Transport: "agentmail", ProviderID: seed + "-inbox", Address: seed + "-machine@example.test",
	})
	if err != nil {
		t.Fatalf("RegisterInbox: %v", err)
	}
	pair, err := client.CreatePair(deps.userHomeDir, client.Pair{UserEmail: seed + "@example.test", InboxID: inbox.ID})
	if err != nil {
		t.Fatalf("CreatePair: %v", err)
	}
	return pair
}
