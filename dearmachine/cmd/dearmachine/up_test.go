package main

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dearmachine/dearmachine/internal/client"
)

func TestUpDecisionTree(t *testing.T) {
	t.Run("zero pairs creates through the wizard", func(t *testing.T) {
		app := &fakeApplication{}
		deps := testDependencies(t, app)
		var output strings.Builder
		deps.stdin = strings.NewReader("First pair\nuser@example.test\nmachine@example.test\ntest\ninbox-1\nuser@example.test,machine@example.test\n")
		deps.stdout = &output
		if err := run([]string{"up", "--once"}, func(string) string { return "test-key" }, deps); err != nil {
			t.Fatalf("run up: %v", err)
		}
		state, err := client.ResolvePairState(deps.userHomeDir, "")
		if err != nil || state.Legacy || state.Pair.DisplayName != "First pair" {
			t.Fatalf("created state = %+v, %v", state, err)
		}
		if !strings.Contains(output.String(), "Create a paired inbox") {
			t.Fatalf("wizard output = %q", output.String())
		}
	})

	t.Run("one pair is announced and started", func(t *testing.T) {
		app := &fakeApplication{}
		deps := testDependencies(t, app)
		pair := makeUpTestPair(t, deps, "Only")
		var output strings.Builder
		deps.stdout = &output
		if err := run([]string{"up", "--once"}, func(string) string { return "test-key" }, deps); err != nil {
			t.Fatalf("run up: %v", err)
		}
		if app.runOnceCount != 1 || !strings.Contains(output.String(), "Using pair Only") || !strings.Contains(output.String(), pair.ID) {
			t.Fatalf("application/output = %d/%q", app.runOnceCount, output.String())
		}
	})

	t.Run("several pairs use the numbered picker", func(t *testing.T) {
		app := &fakeApplication{}
		deps := testDependencies(t, app)
		first := makeUpTestPair(t, deps, "First")
		second := makeUpTestPair(t, deps, "Second")
		var opened client.Pair
		deps.openPairStore = func(path string, pair client.Pair) (*client.Store, error) {
			opened = pair
			return client.OpenPairStore(path, pair)
		}
		var output strings.Builder
		deps.stdin = strings.NewReader("2\n")
		deps.stdout = &output
		if err := run([]string{"up", "--once"}, func(string) string { return "test-key" }, deps); err != nil {
			t.Fatalf("run up: %v", err)
		}
		if opened.ID != second.ID || opened.ID == first.ID || !strings.Contains(output.String(), "Choose a pair") {
			t.Fatalf("picker selected %+v; output %q", opened, output.String())
		}
	})
}

func TestUpNewCreatesFreshPairState(t *testing.T) {
	app := &fakeApplication{}
	deps := testDependencies(t, app)
	deps.stdin = strings.NewReader("New pair\nnew-user@example.test\nnew-machine@example.test\ntest\nnew-inbox\nnew-user@example.test,new-machine@example.test\n")
	deps.stdout = io.Discard
	if err := run([]string{"up", "--new", "--once"}, func(string) string { return "test-key" }, deps); err != nil {
		t.Fatalf("run up --new: %v", err)
	}
	state, err := client.ResolvePairState(deps.userHomeDir, "")
	if err != nil || state.Legacy {
		t.Fatalf("ResolvePairState = %+v, %v", state, err)
	}
	db, err := sql.Open("sqlite3", state.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var pairID string
	if err := db.QueryRow("SELECT pair_id FROM pair_meta").Scan(&pairID); err != nil || pairID != state.Pair.ID {
		t.Fatalf("pair metadata = %q, %v; want %q", pairID, err, state.Pair.ID)
	}
	for _, table := range []string{"thread_sessions", "pending_messages", "processed_messages", "skipped_messages", "thread_aliases"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count = %d, %v", table, count, err)
		}
	}
}

func TestUpSwitchChangesActiveWithoutTouchingOtherPair(t *testing.T) {
	app := &fakeApplication{}
	deps := testDependencies(t, app)
	first := makeUpTestPair(t, deps, "First")
	second := makeUpTestPair(t, deps, "Second")
	secondPath, err := client.DefaultPairDatabasePath(deps.userHomeDir, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	deps.stdout = io.Discard
	if err := run([]string{"up", "--switch", first.ID, "--once"}, func(string) string { return "test-key" }, deps); err != nil {
		t.Fatalf("run up --switch: %v", err)
	}
	state, err := client.ResolvePairState(deps.userHomeDir, "")
	if err != nil || state.Pair.ID != first.ID {
		t.Fatalf("active state = %+v, %v", state, err)
	}
	after, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("switch changed non-selected pair database %s", filepath.Base(secondPath))
	}
}

func TestPublicHelpDoesNotExposePairFlagOrSubcommand(t *testing.T) {
	var output strings.Builder
	_, err := parseConfig([]string{"--help"}, &output)
	if err == nil || strings.Contains(output.String(), "--pair") {
		t.Fatalf("main help exposes pair selection: %q, %v", output.String(), err)
	}
	if err := run([]string{"pair", "--help"}, func(string) string { return "" }, dependencies{flagOutput: io.Discard}); err == nil {
		t.Fatal("pair subcommand unexpectedly succeeded")
	}
}

func makeUpTestPair(t *testing.T, deps dependencies, name string) client.Pair {
	t.Helper()
	pair, err := client.CreatePair(deps.userHomeDir, client.Pair{
		DisplayName: name, UserEmail: strings.ToLower(name) + "-user@example.test", DearMachineAddress: strings.ToLower(name) + "-machine@example.test",
		Transport: "agentmail", InboxID: strings.ToLower(name) + "-inbox", Allow: []string{strings.ToLower(name) + "-user@example.test", strings.ToLower(name) + "-machine@example.test"},
	})
	if err != nil {
		t.Fatalf("CreatePair: %v", err)
	}
	return pair
}
