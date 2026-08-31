package client

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

const PairRegistryVersion = 2
const PairSchemaVersion = 2

var (
	ErrUnknownInbox   = errors.New("unknown inbox")
	ErrAmbiguousInbox = errors.New("ambiguous inbox")
)

// Inbox is a provider mailbox that can be intentionally shared by multiple
// pairs. ID is DearMachine's immutable identity; ProviderID and Address are
// the exact values returned by (or adopted from) the provider.
type Inbox struct {
	ID         string `toml:"id"`
	Transport  string `toml:"transport"`
	ProviderID string `toml:"provider_id"`
	Address    string `toml:"address"`
}

// Pair is one isolated conversation and state lane. InboxID references an
// Inbox record; transport configuration deliberately does not live here.
type Pair struct {
	ID        string `toml:"id"`
	UserEmail string `toml:"user_email"`
	InboxID   string `toml:"inbox_id"`
}

type PairRegistry struct {
	Version int     `toml:"version"`
	Inboxes []Inbox `toml:"inboxes"`
	Pairs   []Pair  `toml:"pairs"`
}

type PairState struct {
	Pair  Pair
	Inbox Inbox
	Path  string
}

func DefaultPairRegistryPath(userHomeDir func() (string, error)) (string, error) {
	root, err := resolveDeviceHome(userHomeDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".dearmachine", "pairs.toml"), nil
}

func DefaultPairDatabasePath(userHomeDir func() (string, error), pairID string) (string, error) {
	root, err := resolveDeviceHome(userHomeDir)
	if err != nil {
		return "", err
	}
	pairID = strings.ToLower(strings.TrimSpace(pairID))
	if !isUUIDv4(pairID) {
		return "", fmt.Errorf("pair ID %q is not a UUID v4", pairID)
	}
	return filepath.Join(root, ".dearmachine", "pairs", pairID, "state", "dearmachine.db"), nil
}

func resolveDeviceHome(userHomeDir func() (string, error)) (string, error) {
	root, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("user home directory is empty")
	}
	return root, nil
}

func LoadPairRegistry(path string) (PairRegistry, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return PairRegistry{Version: PairRegistryVersion}, nil
		}
		return PairRegistry{}, fmt.Errorf("read pair registry: %w", err)
	}
	var registry PairRegistry
	if err := toml.Unmarshal(content, &registry); err != nil {
		return PairRegistry{}, fmt.Errorf("parse pair registry: %w", err)
	}
	if registry.Version != PairRegistryVersion {
		return PairRegistry{}, fmt.Errorf("pair registry version must be %d; move the old registry aside, then recreate pairing with `dearmachine up --create`", PairRegistryVersion)
	}
	registry, err = normalizePairRegistry(registry)
	if err != nil {
		return PairRegistry{}, err
	}
	return registry, nil
}

func SavePairRegistry(path string, registry PairRegistry) error {
	if registry.Version == 0 {
		registry.Version = PairRegistryVersion
	}
	if registry.Version != PairRegistryVersion {
		return fmt.Errorf("pair registry version must be %d", PairRegistryVersion)
	}
	registry, err := normalizePairRegistry(registry)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create pair registry directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("secure pair registry directory: %w", err)
	}
	var content strings.Builder
	content.WriteString("version = ")
	content.WriteString(strconv.Itoa(registry.Version))
	content.WriteString("\n")
	for _, inbox := range registry.Inboxes {
		content.WriteString("\n[[inboxes]]\n")
		writeRegistryTOML(&content, "id", inbox.ID)
		writeRegistryTOML(&content, "transport", inbox.Transport)
		writeRegistryTOML(&content, "provider_id", inbox.ProviderID)
		writeRegistryTOML(&content, "address", inbox.Address)
	}
	for _, pair := range registry.Pairs {
		content.WriteString("\n[[pairs]]\n")
		writeRegistryTOML(&content, "id", pair.ID)
		writeRegistryTOML(&content, "user_email", pair.UserEmail)
		writeRegistryTOML(&content, "inbox_id", pair.InboxID)
	}
	if err := writeAtomicConfig(path, []byte(content.String())); err != nil {
		return fmt.Errorf("write pair registry: %w", err)
	}
	return nil
}

func writeRegistryTOML(content *strings.Builder, key, value string) {
	content.WriteString(key)
	content.WriteString(" = ")
	content.WriteString(strconv.Quote(value))
	content.WriteString("\n")
}

func normalizePairRegistry(registry PairRegistry) (PairRegistry, error) {
	seenInboxIDs := make(map[string]struct{}, len(registry.Inboxes))
	seenProviders := make(map[string]struct{}, len(registry.Inboxes))
	for index := range registry.Inboxes {
		inbox, err := normalizeInbox(registry.Inboxes[index])
		if err != nil {
			return PairRegistry{}, fmt.Errorf("invalid inbox %q: %w", registry.Inboxes[index].ID, err)
		}
		if _, found := seenInboxIDs[inbox.ID]; found {
			return PairRegistry{}, fmt.Errorf("pair registry contains duplicate inbox ID %q", inbox.ID)
		}
		providerKey := inbox.Transport + "\x00" + strings.ToLower(inbox.ProviderID)
		if _, found := seenProviders[providerKey]; found {
			return PairRegistry{}, fmt.Errorf("pair registry contains duplicate provider inbox %q for transport %q", inbox.ProviderID, inbox.Transport)
		}
		seenInboxIDs[inbox.ID] = struct{}{}
		seenProviders[providerKey] = struct{}{}
		registry.Inboxes[index] = inbox
	}
	seenPairIDs := make(map[string]struct{}, len(registry.Pairs))
	seenRoutes := make(map[string]struct{}, len(registry.Pairs))
	for index := range registry.Pairs {
		pair, err := normalizePair(registry.Pairs[index])
		if err != nil {
			return PairRegistry{}, fmt.Errorf("invalid pair %q: %w", registry.Pairs[index].ID, err)
		}
		if _, found := seenPairIDs[pair.ID]; found {
			return PairRegistry{}, fmt.Errorf("pair registry contains duplicate pair ID %q", pair.ID)
		}
		if _, found := seenInboxIDs[pair.InboxID]; !found {
			return PairRegistry{}, fmt.Errorf("pair %q references unknown inbox %q", pair.ID, pair.InboxID)
		}
		routeKey := pair.InboxID + "\x00" + pair.UserEmail
		if _, found := seenRoutes[routeKey]; found {
			return PairRegistry{}, fmt.Errorf("pair registry contains ambiguous route for %q on inbox %q", pair.UserEmail, pair.InboxID)
		}
		seenPairIDs[pair.ID] = struct{}{}
		seenRoutes[routeKey] = struct{}{}
		registry.Pairs[index] = pair
	}
	return registry, nil
}

func normalizeInbox(inbox Inbox) (Inbox, error) {
	inbox.ID = strings.ToLower(strings.TrimSpace(inbox.ID))
	if !isUUIDv4(inbox.ID) {
		return Inbox{}, fmt.Errorf("id must be a UUID v4")
	}
	inbox.Transport = strings.ToLower(strings.TrimSpace(inbox.Transport))
	if inbox.Transport == "" {
		return Inbox{}, fmt.Errorf("transport is required")
	}
	inbox.ProviderID = strings.TrimSpace(inbox.ProviderID)
	if inbox.ProviderID == "" {
		return Inbox{}, fmt.Errorf("provider ID is required")
	}
	address, err := canonicalPairAddress(inbox.Address)
	if err != nil {
		return Inbox{}, fmt.Errorf("address: %w", err)
	}
	inbox.Address = address
	return inbox, nil
}

func normalizePair(pair Pair) (Pair, error) {
	pair.ID = strings.ToLower(strings.TrimSpace(pair.ID))
	if !isUUIDv4(pair.ID) {
		return Pair{}, fmt.Errorf("id must be a UUID v4")
	}
	address, err := canonicalPairAddress(pair.UserEmail)
	if err != nil {
		return Pair{}, fmt.Errorf("user email: %w", err)
	}
	pair.UserEmail = address
	pair.InboxID = strings.ToLower(strings.TrimSpace(pair.InboxID))
	if !isUUIDv4(pair.InboxID) {
		return Pair{}, fmt.Errorf("inbox ID must be a UUID v4")
	}
	return pair, nil
}

func canonicalPairAddress(value string) (string, error) {
	address, err := mail.ParseAddress(strings.TrimSpace(value))
	if err != nil || address.Address == "" {
		return "", fmt.Errorf("must be an RFC 5322 address")
	}
	return strings.ToLower(strings.TrimSpace(address.Address)), nil
}

func RegisterInbox(userHomeDir func() (string, error), inbox Inbox) (Inbox, error) {
	path, err := DefaultPairRegistryPath(userHomeDir)
	if err != nil {
		return Inbox{}, err
	}
	registry, err := LoadPairRegistry(path)
	if err != nil {
		return Inbox{}, err
	}
	if strings.TrimSpace(inbox.ID) == "" {
		inbox.ID, err = newIdentityID()
		if err != nil {
			return Inbox{}, err
		}
	}
	inbox, err = normalizeInbox(inbox)
	if err != nil {
		return Inbox{}, err
	}
	registry.Inboxes = append(registry.Inboxes, inbox)
	if err := SavePairRegistry(path, registry); err != nil {
		return Inbox{}, err
	}
	return inbox, nil
}

// CreatePair only accepts a reference to an inbox that is already explicitly
// registered. Its database is created before the registry entry is published.
func CreatePair(userHomeDir func() (string, error), pair Pair) (Pair, error) {
	registryPath, err := DefaultPairRegistryPath(userHomeDir)
	if err != nil {
		return Pair{}, err
	}
	registry, err := LoadPairRegistry(registryPath)
	if err != nil {
		return Pair{}, err
	}
	if strings.TrimSpace(pair.ID) == "" {
		pair.ID, err = newIdentityID()
		if err != nil {
			return Pair{}, err
		}
	}
	pair, err = normalizePair(pair)
	if err != nil {
		return Pair{}, err
	}
	prospective := registry
	prospective.Pairs = append(append([]Pair(nil), registry.Pairs...), pair)
	if _, err := normalizePairRegistry(prospective); err != nil {
		return Pair{}, err
	}
	path, err := DefaultPairDatabasePath(userHomeDir, pair.ID)
	if err != nil {
		return Pair{}, err
	}
	store, err := OpenPairStore(path, pair)
	if err != nil {
		return Pair{}, err
	}
	if err := store.Close(); err != nil {
		return Pair{}, fmt.Errorf("close new pair store: %w", err)
	}
	if err := SavePairRegistry(registryPath, prospective); err != nil {
		return Pair{}, err
	}
	return pair, nil
}

func ResolveInbox(registry PairRegistry, selection string) (Inbox, error) {
	selection = strings.TrimSpace(selection)
	if selection == "" {
		return Inbox{}, fmt.Errorf("inbox selection is required")
	}
	var matches []Inbox
	for _, inbox := range registry.Inboxes {
		if strings.EqualFold(selection, inbox.ID) || strings.EqualFold(selection, inbox.ProviderID) || strings.EqualFold(selection, inbox.Address) {
			matches = append(matches, inbox)
		}
	}
	if len(matches) == 0 {
		return Inbox{}, fmt.Errorf("%w: inbox selection %q", ErrUnknownInbox, selection)
	}
	if len(matches) > 1 {
		return Inbox{}, fmt.Errorf("%w: inbox selection %q; select the inbox UUID", ErrAmbiguousInbox, selection)
	}
	return matches[0], nil
}

// ResolvePairStates returns all registered pairs when selections is empty.
// Each explicit selector must match exactly one UUID or canonical user email.
func ResolvePairStates(userHomeDir func() (string, error), selections []string) ([]PairState, error) {
	registryPath, err := DefaultPairRegistryPath(userHomeDir)
	if err != nil {
		return nil, err
	}
	registry, err := LoadPairRegistry(registryPath)
	if err != nil {
		return nil, err
	}
	if len(registry.Pairs) == 0 {
		return nil, fmt.Errorf("no pairs are registered; run `dearmachine up --create`")
	}
	selected := registry.Pairs
	if len(selections) > 0 {
		selected = nil
		seen := make(map[string]struct{})
		for _, selection := range selections {
			selection = strings.TrimSpace(selection)
			var matches []Pair
			for _, pair := range registry.Pairs {
				if strings.EqualFold(selection, pair.ID) || strings.EqualFold(selection, pair.UserEmail) {
					matches = append(matches, pair)
				}
			}
			if len(matches) == 0 {
				return nil, fmt.Errorf("pair selection %q is unknown", selection)
			}
			if len(matches) > 1 {
				return nil, fmt.Errorf("pair selection %q is ambiguous; select the pair UUID", selection)
			}
			if _, found := seen[matches[0].ID]; !found {
				selected = append(selected, matches[0])
				seen[matches[0].ID] = struct{}{}
			}
		}
	}
	inboxes := make(map[string]Inbox, len(registry.Inboxes))
	for _, inbox := range registry.Inboxes {
		inboxes[inbox.ID] = inbox
	}
	states := make([]PairState, 0, len(selected))
	for _, pair := range selected {
		path, err := DefaultPairDatabasePath(userHomeDir, pair.ID)
		if err != nil {
			return nil, err
		}
		states = append(states, PairState{Pair: pair, Inbox: inboxes[pair.InboxID], Path: path})
	}
	return states, nil
}

func ResolvePairState(userHomeDir func() (string, error), selection string) (PairState, error) {
	states, err := ResolvePairStates(userHomeDir, nonemptySelection(selection))
	if err != nil {
		return PairState{}, err
	}
	if len(states) != 1 {
		return PairState{}, fmt.Errorf("pair selection is ambiguous; select a pair by email or UUID")
	}
	return states[0], nil
}

func nonemptySelection(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return []string{value}
}

func OpenResolvedPairStore(userHomeDir func() (string, error), selection string) (*Store, PairState, error) {
	state, err := ResolvePairState(userHomeDir, selection)
	if err != nil {
		return nil, PairState{}, err
	}
	store, err := OpenPairStore(state.Path, state.Pair)
	return store, state, err
}

func OpenPairStore(path string, pair Pair) (*Store, error) {
	pair, err := normalizePair(pair)
	if err != nil {
		return nil, fmt.Errorf("validate pair for store: %w", err)
	}
	store, err := OpenStore(path)
	if err != nil {
		return nil, err
	}
	if err := store.bindPair(pair); err != nil {
		store.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) bindPair(pair Pair) error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS pair_meta (
    pair_id TEXT PRIMARY KEY,
    creation_fingerprint TEXT NOT NULL,
    schema_version INTEGER NOT NULL
)`); err != nil {
		return fmt.Errorf("create pair metadata: %w", err)
	}
	rows, err := s.db.Query(`SELECT pair_id, creation_fingerprint, schema_version FROM pair_meta`)
	if err != nil {
		return fmt.Errorf("read pair metadata: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return fmt.Errorf("read pair metadata: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close pair metadata query: %w", err)
		}
		populated, err := s.hasPairState()
		if err != nil {
			return err
		}
		if populated {
			return fmt.Errorf("pair metadata mismatch: populated database has no pair metadata")
		}
		if _, err := s.db.Exec(`INSERT INTO pair_meta (pair_id, creation_fingerprint, schema_version) VALUES (?, ?, ?)`, pair.ID, pairCreationFingerprint(pair), PairSchemaVersion); err != nil {
			return fmt.Errorf("write pair metadata: %w", err)
		}
		return nil
	}
	var storedID, storedFingerprint string
	var storedVersion int
	if err := rows.Scan(&storedID, &storedFingerprint, &storedVersion); err != nil {
		return fmt.Errorf("read pair metadata: %w", err)
	}
	if rows.Next() {
		return fmt.Errorf("pair metadata mismatch: more than one pair metadata row")
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pair metadata: %w", err)
	}
	if storedID != pair.ID || storedFingerprint != pairCreationFingerprint(pair) || storedVersion != PairSchemaVersion {
		return fmt.Errorf("pair metadata mismatch: database belongs to pair %q, not registry pair %q", storedID, pair.ID)
	}
	return nil
}

func (s *Store) hasPairState() (bool, error) {
	for _, table := range []string{"thread_sessions", "processed_messages", "pending_messages", "skipped_messages", "thread_aliases"} {
		var found int
		if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM ` + table + ` LIMIT 1)`).Scan(&found); err != nil {
			return false, fmt.Errorf("inspect existing pair state: %w", err)
		}
		if found != 0 {
			return true, nil
		}
	}
	return false, nil
}

func pairCreationFingerprint(pair Pair) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{"pair-v2", pair.ID, pair.UserEmail, pair.InboxID}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func newIdentityID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate identity ID: %w", err)
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16]), nil
}

func isUUIDv4(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || value[14] != '4' || (value[19] != '8' && value[19] != '9' && value[19] != 'a' && value[19] != 'b') {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}
