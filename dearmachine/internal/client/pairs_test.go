package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPairStoreRecoveryDoesNotReachOtherPairTransport(t *testing.T) {
	home := t.TempDir()
	pairA := createTestPair(t, home, "Alpha")
	pairB := createTestPair(t, home, "Beta")

	storeA := openTestPairStore(t, home, pairA)
	pending, _, err := storeA.BeginMessage("provider-message", "provider-thread", TierPlain)
	if err != nil {
		t.Fatalf("begin pair A message: %v", err)
	}
	if err := storeA.MarkRunning(pending.MessageID, "durable work"); err != nil {
		t.Fatalf("mark pair A message running: %v", err)
	}
	if err := storeA.Close(); err != nil {
		t.Fatalf("close pair A store: %v", err)
	}

	storeB := openTestPairStore(t, home, pairB)
	transportB := &pairTestTransport{}
	app := &App{store: storeB, transport: transportB}
	if err := app.recoverPending(context.Background(), newThreadWorkQueue()); err != nil {
		t.Fatalf("recover pair B: %v", err)
	}
	if len(transportB.messageIDs) != 0 {
		t.Fatalf("pair B transport fetched pair A pending message: %v", transportB.messageIDs)
	}
}

func TestPairStoreFooterCannotJoinSessionFromOtherPair(t *testing.T) {
	home := t.TempDir()
	pairA := createTestPair(t, home, "Alpha")
	pairB := createTestPair(t, home, "Beta")
	storeA := openTestPairStore(t, home, pairA)
	first, _, err := storeA.BeginMessage("message-a", "thread-a", TierPlain)
	if err != nil {
		t.Fatalf("begin pair A message: %v", err)
	}

	storeB := openTestPairStore(t, home, pairB)
	continued, existed, err := storeB.BeginMessageWithReference(
		"message-b", "thread-b", first.Session.SessionID, TierPlain,
	)
	if err != nil || existed {
		t.Fatalf("begin pair B message = %+v, %v, %v", continued, existed, err)
	}
	if continued.ThreadID != "thread-b" || continued.Session.SessionID == first.Session.SessionID {
		t.Fatalf("pair B joined pair A session: pairA=%+v pairB=%+v", first.Session, continued.Session)
	}
}

func TestPairStoresKeepProviderIDsAndAliasesIndependent(t *testing.T) {
	home := t.TempDir()
	pairA := createTestPair(t, home, "Alpha")
	pairB := createTestPair(t, home, "Beta")
	storeA := openTestPairStore(t, home, pairA)
	storeB := openTestPairStore(t, home, pairB)

	for label, store := range map[string]*Store{"A": storeA, "B": storeB} {
		first, _, err := store.BeginMessage("same-message", "canonical-"+label, TierPlain)
		if err != nil {
			t.Fatalf("begin pair %s canonical message: %v", label, err)
		}
		if label == "A" {
			if err := store.MarkRunning(first.MessageID, "pair A work"); err != nil {
				t.Fatalf("run pair A message: %v", err)
			}
			if err := store.StoreResult(first.MessageID, RunResult{Kind: ResultAnswer, Text: "done"}); err != nil {
				t.Fatalf("store pair A result: %v", err)
			}
			if err := store.Complete(first.MessageID, "done", "outbound-a"); err != nil {
				t.Fatalf("complete pair A message: %v", err)
			}
		}
		aliased, _, err := store.BeginMessageWithReference(
			"same-alias-message", "same-provider-thread", first.Session.SessionID, TierPlain,
		)
		if err != nil {
			t.Fatalf("begin pair %s aliased message: %v", label, err)
		}
		if aliased.ThreadID != first.ThreadID {
			t.Fatalf("pair %s alias = %q, want %q", label, aliased.ThreadID, first.ThreadID)
		}
	}
	seenA, err := storeA.Seen("same-message")
	if err != nil || !seenA {
		t.Fatalf("pair A processed record = %v, %v", seenA, err)
	}
	seenB, err := storeB.Seen("same-message")
	if err != nil || seenB {
		t.Fatalf("pair B dedupe observed pair A message: %v, %v", seenB, err)
	}
	aliasA, err := storeA.Session("same-provider-thread")
	if err != nil {
		t.Fatalf("pair A alias: %v", err)
	}
	aliasB, err := storeB.Session("same-provider-thread")
	if err != nil {
		t.Fatalf("pair B alias: %v", err)
	}
	if aliasA.ThreadID == aliasB.ThreadID {
		t.Fatalf("pair aliases leaked across stores: A=%+v B=%+v", aliasA, aliasB)
	}
}

func TestOpenPairStoreRejectsPairMetaMismatch(t *testing.T) {
	home := t.TempDir()
	pair := createTestPair(t, home, "Alpha")
	state, err := ResolvePairState(homeDir(home), pair.ID)
	if err != nil {
		t.Fatalf("resolve pair state: %v", err)
	}
	store, err := OpenPairStore(state.Path, pair)
	if err != nil {
		t.Fatalf("open pair store: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE pair_meta SET pair_id = ?`, "00000000-0000-4000-8000-000000000000"); err != nil {
		t.Fatalf("corrupt pair metadata: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close pair store: %v", err)
	}
	if _, err := OpenPairStore(state.Path, pair); err == nil || !strings.Contains(err.Error(), "pair metadata mismatch") {
		t.Fatalf("OpenPairStore mismatch error = %v", err)
	}
}

func TestResolvePairStateRejectsUnknownAndAmbiguousSelection(t *testing.T) {
	home := t.TempDir()
	first := createTestPair(t, home, "Shared")
	inbox, err := RegisterInbox(homeDir(home), Inbox{Transport: "test", ProviderID: "second-inbox", Address: "second-machine@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreatePair(homeDir(home), Pair{ID: "00000000-0000-4000-8000-000000000002", UserEmail: first.UserEmail, InboxID: inbox.ID})
	if err != nil {
		t.Fatal(err)
	}
	registryPath, err := DefaultPairRegistryPath(homeDir(home))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := LoadPairRegistry(registryPath)
	if err != nil || len(registry.Pairs) != 2 || second.ID == first.ID {
		t.Fatalf("registry = %+v, %v", registry, err)
	}
	for _, selection := range []string{"missing", first.UserEmail} {
		if _, err := ResolvePairState(homeDir(home), selection); err == nil || !strings.Contains(err.Error(), "pair selection") {
			t.Fatalf("ResolvePairState(%q) error = %v", selection, err)
		}
	}
}

func TestCreatePairCreatesFreshStoreWithMatchingMeta(t *testing.T) {
	home := t.TempDir()
	inbox, err := RegisterInbox(homeDir(home), Inbox{Transport: "test", ProviderID: "inbox", Address: " MACHINE@example.test "})
	if err != nil {
		t.Fatalf("RegisterInbox: %v", err)
	}
	pair, err := CreatePair(homeDir(home), Pair{UserEmail: " user@example.test ", InboxID: inbox.ID})
	if err != nil {
		t.Fatalf("CreatePair: %v", err)
	}
	registryPath, err := DefaultPairRegistryPath(homeDir(home))
	if err != nil {
		t.Fatalf("DefaultPairRegistryPath: %v", err)
	}
	registry, err := LoadPairRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadPairRegistry: %v", err)
	}
	if len(registry.Pairs) != 1 || registry.Pairs[0].UserEmail != "user@example.test" ||
		len(registry.Inboxes) != 1 || registry.Inboxes[0].Address != "machine@example.test" {
		t.Fatalf("normalized registry = %+v", registry)
	}
	state, err := ResolvePairState(homeDir(home), "")
	if err != nil {
		t.Fatalf("ResolvePairState: %v", err)
	}
	if state.Pair.ID != pair.ID || state.Inbox.ID != inbox.ID || state.Path != filepath.Join(home, ".dearmachine", "pairs", pair.ID, "state", "dearmachine.db") {
		t.Fatalf("resolved state = %+v", state)
	}
	store, err := OpenPairStore(state.Path, state.Pair)
	if err != nil {
		t.Fatalf("open created pair store: %v", err)
	}
	defer store.Close()
	var id, fingerprint string
	var version int
	if err := store.db.QueryRow(`SELECT pair_id, creation_fingerprint, schema_version FROM pair_meta`).Scan(&id, &fingerprint, &version); err != nil {
		t.Fatalf("read pair metadata: %v", err)
	}
	if id != pair.ID || fingerprint != pairCreationFingerprint(pair) || version != PairSchemaVersion {
		t.Fatalf("pair metadata = %q, %q, %d", id, fingerprint, version)
	}
	var pending int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pending_messages`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("fresh pending count = %d, %v", pending, err)
	}
}

func TestResolvePairStateRequiresExplicitCreationWhenRegistryIsEmpty(t *testing.T) {
	home := t.TempDir()
	_, err := ResolvePairState(homeDir(home), "")
	if err == nil || !strings.Contains(err.Error(), "up --create") {
		t.Fatalf("ResolvePairState error = %v", err)
	}
}

func TestPairRegistryVersionOneIsRejectedWithoutMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairs.toml")
	if err := os.WriteFile(path, []byte("version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadPairRegistry(path)
	if err == nil || !strings.Contains(err.Error(), "recreate pairing") {
		t.Fatalf("LoadPairRegistry error = %v", err)
	}
}

func TestResolvePairStatesDefaultsToAllAndSelectorsDoNotMutateRegistry(t *testing.T) {
	home := t.TempDir()
	first := createTestPair(t, home, "Alpha")
	second := createTestPair(t, home, "Beta")
	path, _ := DefaultPairRegistryPath(homeDir(home))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	all, err := ResolvePairStates(homeDir(home), nil)
	if err != nil || len(all) != 2 {
		t.Fatalf("all states = %+v, %v", all, err)
	}
	selected, err := ResolvePairStates(homeDir(home), []string{second.UserEmail, first.ID})
	if err != nil || len(selected) != 2 || selected[0].Pair.ID != second.ID || selected[1].Pair.ID != first.ID {
		t.Fatalf("selected states = %+v, %v", selected, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("selector mutated registry: %v", err)
	}
}

func createTestPair(t *testing.T, home, name string) Pair {
	t.Helper()
	inbox, err := RegisterInbox(homeDir(home), Inbox{Transport: "test", ProviderID: strings.ToLower(name) + "-inbox", Address: strings.ToLower(name) + "-machine@example.test"})
	if err != nil {
		t.Fatalf("RegisterInbox(%s): %v", name, err)
	}
	pair, err := CreatePair(homeDir(home), Pair{UserEmail: strings.ToLower(name) + "-user@example.test", InboxID: inbox.ID})
	if err != nil {
		t.Fatalf("CreatePair(%s): %v", name, err)
	}
	return pair
}

func openTestPairStore(t *testing.T, home string, pair Pair) *Store {
	t.Helper()
	state, err := ResolvePairState(homeDir(home), pair.ID)
	if err != nil {
		t.Fatalf("ResolvePairState: %v", err)
	}
	store, err := OpenPairStore(state.Path, state.Pair)
	if err != nil {
		t.Fatalf("OpenPairStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func homeDir(home string) func() (string, error) {
	return func() (string, error) { return home, nil }
}

type pairTestTransport struct{ messageIDs []string }

func (transport *pairTestTransport) Poll(context.Context) ([]Message, error) { return nil, nil }
func (transport *pairTestTransport) Thread(context.Context, string) ([]Message, error) {
	return nil, nil
}
func (transport *pairTestTransport) Message(_ context.Context, id string) (Message, error) {
	transport.messageIDs = append(transport.messageIDs, id)
	return Message{}, errors.New("pair B transport must not be called")
}
func (transport *pairTestTransport) Reply(context.Context, string, ReplyPayload, string) (string, error) {
	return "", nil
}
func (transport *pairTestTransport) ReplyReceipt(context.Context, Message) (string, bool, error) {
	return "", false, nil
}
func (transport *pairTestTransport) MarkProcessed(context.Context, string) error { return nil }
func (transport *pairTestTransport) FetchAttachment(context.Context, string, int64) ([]byte, error) {
	return nil, nil
}
