package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/synctrigger"
)

func TestPairMaintenanceCountsAllStoresAndAdvancesOneCheckpoint(t *testing.T) {
	for _, counts := range [][2]int{{0, 2}, {1, 1}} {
		t.Run(fmt.Sprint(counts), func(t *testing.T) {
			deps := testDependencies(t, &fakeApplication{})
			home, _ := deps.userHomeDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			first := makeInboxTestPair(t, home, "agentmail")
			secondPair, err := client.CreatePair(deps.userHomeDir, client.Pair{UserEmail: "second@example.test", InboxID: first.Inbox.ID})
			if err != nil {
				t.Fatal(err)
			}
			second, err := client.ResolvePairState(deps.userHomeDir, secondPair.ID)
			if err != nil {
				t.Fatal(err)
			}
			states := []client.PairState{first, second}
			var stores []*client.Store
			var orchestrators []*synctrigger.Orchestrator
			deps.newApp = func(_ client.Transport, store *client.Store, _ *client.AgentRunner, orchestrator *synctrigger.Orchestrator, _ int, _ time.Duration, _ *log.Logger, _ bool, _ string, _ client.ResponseTier) (application, error) {
				stores = append(stores, store)
				if orchestrator != nil {
					orchestrators = append(orchestrators, orchestrator)
				}
				return &fakeApplication{}, nil
			}
			deps.newPairDaemon = func(_ []application, _ int, _ string) (application, error) {
				if len(orchestrators) != 1 {
					t.Fatalf("maintenance lanes = %d, want 1", len(orchestrators))
				}
				o := orchestrators[0]
				if n, err := o.TurnCounter(time.Time{}); err != nil || n != 0 {
					t.Fatalf("initial count = %d, %v", n, err)
				}
				for i, count := range counts {
					for j := 0; j < count; j++ {
						// Provider IDs overlap across pair stores and count independently.
						id := fmt.Sprintf("message-%d", j)
						pending, _, err := stores[i].BeginMessage(id, id, client.TierPlain)
						if err != nil {
							t.Fatal(err)
						}
						if err := stores[i].MarkRunning(pending.MessageID, "fixture"); err != nil {
							t.Fatal(err)
						}
						if err := stores[i].StoreResult(id, client.RunResult{Kind: client.ResultAnswer, Text: "done"}); err != nil {
							t.Fatal(err)
						}
						if err := stores[i].Complete(id, "done", "outbound-"+id); err != nil {
							t.Fatal(err)
						}
					}
				}
				if _, _, err := stores[0].BeginMessage("pending", "pending", client.TierPlain); err != nil {
					t.Fatal(err)
				}
				if n, err := o.TurnCounter(time.Time{}); err != nil || n != 2 {
					t.Fatalf("aggregate count = %d, %v", n, err)
				}
				older := time.Now().Add(-time.Hour)
				o.Lister = func(context.Context, string) ([]synctrigger.SessionInfo, error) {
					return []synctrigger.SessionInfo{{SessionID: "review", UpdatedAt: older}, {SessionID: "hold", UpdatedAt: older.Add(time.Minute)}}, nil
				}
				o.GitLastCommitTime = func(string) (time.Time, error) { return time.Time{}, nil }
				o.StatePath = filepath.Join(t.TempDir(), "checkpoint.json")
				var commands []string
				o.RunCommand = func(_ context.Context, _, _ string, args ...string) ([]byte, error) {
					commands = append(commands, strings.Join(args, " "))
					return []byte("fixture-fork"), nil
				}
				for i := 0; i < 2; i++ {
					if err := o.OrchestrateSync(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				if len(commands) != 4 || commands[3] != "sync --include-docs" {
					t.Fatalf("maintenance commands = %v", commands)
				}
				data, err := os.ReadFile(o.StatePath)
				if err != nil {
					t.Fatal(err)
				}
				var checkpoint struct {
					SessionID      string    `json:"session_id"`
					CountedThrough time.Time `json:"counted_through"`
				}
				if err := json.Unmarshal(data, &checkpoint); err != nil {
					t.Fatal(err)
				}
				if checkpoint.SessionID != "review" || checkpoint.CountedThrough.IsZero() {
					t.Fatalf("checkpoint = %+v", checkpoint)
				}
				if n, err := o.TurnCounter(checkpoint.CountedThrough); err != nil || n != 0 {
					t.Fatalf("count after maintenance = %d, %v", n, err)
				}
				if err := stores[1].Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := o.TurnCounter(time.Time{}); err == nil || !strings.Contains(err.Error(), second.Pair.ID) {
					t.Fatalf("pair count error = %v", err)
				}
				return &fakeApplication{}, nil
			}
			cfg := config{agentBinary: "machtiani", projectDir: t.TempDir(), entryPointRepo: t.TempDir(), entryPointPrompt: filepath.Join(t.TempDir(), "prompt.md"), maintenanceMinTurns: 2, maintenanceMinTurnsSet: true, pollInterval: time.Minute, concurrency: 2, once: true}
			if err := runPairStates(cfg, func(string) string { return "" }, deps, states); err != nil {
				t.Fatal(err)
			}
		})
	}
}
