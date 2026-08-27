package client

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

const PairRegistryVersion = 1
const PairSchemaVersion = 1

// Pair is the non-secret, durable identity of a paired user, Dear Machine
// address, and transport inbox. Its ID is deliberately random rather than
// derived from addresses so changes to an address cannot join state lanes.
type Pair struct {
	ID                 string   `toml:"id"`
	DisplayName        string   `toml:"display_name"`
	UserEmail          string   `toml:"user_email"`
	DearMachineAddress string   `toml:"dear_machine_address"`
	Transport          string   `toml:"transport"`
	InboxID            string   `toml:"inbox_id"`
	Allow              []string `toml:"allow"`
	Active             bool     `toml:"active"`
}

type PairRegistry struct {
	Version int    `toml:"version"`
	Pairs   []Pair `toml:"pairs"`
}

// PairState is the storage lane selected from a registry. Legacy is true only
// when no registry exists (or it has no pairs), preserving the historical DB.
type PairState struct {
	Pair   Pair
	Path   string
	Legacy bool
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
	if registry.Version == 0 {
		return PairRegistry{}, fmt.Errorf("pair registry version is required")
	}
	if registry.Version != PairRegistryVersion {
		return PairRegistry{}, fmt.Errorf("pair registry version must be %d", PairRegistryVersion)
	}
	if err := validatePairRegistry(registry); err != nil {
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
	for index := range registry.Pairs {
		pair, err := normalizePair(registry.Pairs[index])
		if err != nil {
			return fmt.Errorf("normalize pair %q: %w", registry.Pairs[index].DisplayName, err)
		}
		registry.Pairs[index] = pair
	}
	if err := validatePairRegistry(registry); err != nil {
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
	for _, pair := range registry.Pairs {
		content.WriteString("\n[[pairs]]\n")
		writePairTOML(&content, "id", pair.ID)
		writePairTOML(&content, "display_name", pair.DisplayName)
		writePairTOML(&content, "user_email", pair.UserEmail)
		writePairTOML(&content, "dear_machine_address", pair.DearMachineAddress)
		writePairTOML(&content, "transport", pair.Transport)
		writePairTOML(&content, "inbox_id", pair.InboxID)
		content.WriteString("allow = [")
		for index, address := range pair.Allow {
			if index > 0 {
				content.WriteString(", ")
			}
			content.WriteString(strconv.Quote(address))
		}
		content.WriteString("]\n")
		content.WriteString("active = ")
		content.WriteString(strconv.FormatBool(pair.Active))
		content.WriteString("\n")
	}
	if err := writeAtomicConfig(path, []byte(content.String())); err != nil {
		return fmt.Errorf("write pair registry: %w", err)
	}
	return nil
}

func writePairTOML(content *strings.Builder, key, value string) {
	content.WriteString(key)
	content.WriteString(" = ")
	content.WriteString(strconv.Quote(value))
	content.WriteString("\n")
}

func validatePairRegistry(registry PairRegistry) error {
	seenIDs := make(map[string]struct{}, len(registry.Pairs))
	active := 0
	for _, pair := range registry.Pairs {
		if _, err := normalizePair(pair); err != nil {
			return fmt.Errorf("invalid pair %q: %w", pair.DisplayName, err)
		}
		if _, found := seenIDs[pair.ID]; found {
			return fmt.Errorf("pair registry contains duplicate ID %q", pair.ID)
		}
		seenIDs[pair.ID] = struct{}{}
		if pair.Active {
			active++
		}
	}
	if active > 1 {
		return fmt.Errorf("pair registry has more than one active pair")
	}
	return nil
}

func normalizePair(pair Pair) (Pair, error) {
	pair.ID = strings.ToLower(strings.TrimSpace(pair.ID))
	if !isUUIDv4(pair.ID) {
		return Pair{}, fmt.Errorf("id must be a UUID v4")
	}
	pair.DisplayName = strings.TrimSpace(pair.DisplayName)
	if pair.DisplayName == "" {
		return Pair{}, fmt.Errorf("display name is required")
	}
	var err error
	if pair.UserEmail, err = canonicalPairAddress(pair.UserEmail); err != nil {
		return Pair{}, fmt.Errorf("user email: %w", err)
	}
	if pair.DearMachineAddress, err = canonicalPairAddress(pair.DearMachineAddress); err != nil {
		return Pair{}, fmt.Errorf("Dear Machine address: %w", err)
	}
	pair.Transport = strings.TrimSpace(strings.ToLower(pair.Transport))
	if pair.Transport == "" {
		return Pair{}, fmt.Errorf("transport is required")
	}
	pair.InboxID = strings.TrimSpace(pair.InboxID)
	if pair.InboxID == "" {
		return Pair{}, fmt.Errorf("inbox ID is required")
	}
	pair.Allow = normalizeAllow(pair.Allow)
	if len(pair.Allow) == 0 {
		return Pair{}, fmt.Errorf("allow set is required")
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

func normalizeAllow(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		canonical := strings.ToLower(strings.TrimSpace(value))
		if canonical != "" {
			seen[canonical] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for address := range seen {
		result = append(result, address)
	}
	slices.Sort(result)
	return result
}

// CreatePair persists a registry record and initializes its otherwise-empty
// state database with binding metadata before reporting success.
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
		pair.ID, err = newPairID()
		if err != nil {
			return Pair{}, err
		}
	}
	pair, err = normalizePair(pair)
	if err != nil {
		return Pair{}, err
	}
	if len(registry.Pairs) == 0 {
		pair.Active = true
	}
	if pair.Active {
		for index := range registry.Pairs {
			registry.Pairs[index].Active = false
		}
	}
	registry.Pairs = append(registry.Pairs, pair)
	if err := SavePairRegistry(registryPath, registry); err != nil {
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
	return pair, nil
}

// ResolvePairState selects by UUID or display name. With no selection, an
// active pair is required. The no-pairs fallback deliberately continues to
// use the legacy shared path so existing one-DB installations keep working
// until a later pairing flow explicitly creates a pair.
func ResolvePairState(userHomeDir func() (string, error), selection string) (PairState, error) {
	registryPath, err := DefaultPairRegistryPath(userHomeDir)
	if err != nil {
		return PairState{}, err
	}
	registry, err := LoadPairRegistry(registryPath)
	if err != nil {
		return PairState{}, err
	}
	selection = strings.TrimSpace(selection)
	if len(registry.Pairs) == 0 {
		if selection != "" {
			return PairState{}, fmt.Errorf("pair selection %q is unknown; no pairs are registered", selection)
		}
		path, err := DefaultDeviceDatabasePath(userHomeDir)
		if err != nil {
			return PairState{}, err
		}
		return PairState{Path: path, Legacy: true}, nil
	}

	var matches []Pair
	for _, pair := range registry.Pairs {
		if selection == "" {
			if pair.Active {
				matches = append(matches, pair)
			}
			continue
		}
		if strings.EqualFold(selection, pair.ID) || strings.EqualFold(selection, pair.DisplayName) {
			matches = append(matches, pair)
		}
	}
	if len(matches) == 0 {
		if selection == "" {
			return PairState{}, fmt.Errorf("pair selection is ambiguous: no active pair; select a pair by ID")
		}
		return PairState{}, fmt.Errorf("pair selection %q is unknown; choose a registered pair ID", selection)
	}
	if len(matches) > 1 {
		return PairState{}, fmt.Errorf("pair selection %q is ambiguous; select a unique pair ID", selection)
	}
	path, err := DefaultPairDatabasePath(userHomeDir, matches[0].ID)
	if err != nil {
		return PairState{}, err
	}
	return PairState{Pair: matches[0], Path: path}, nil
}

// OpenResolvedPairStore resolves the active registry lane (or the documented
// legacy fallback) and verifies pair metadata before returning a store.
func OpenResolvedPairStore(userHomeDir func() (string, error), selection string) (*Store, PairState, error) {
	state, err := ResolvePairState(userHomeDir, selection)
	if err != nil {
		return nil, PairState{}, err
	}
	if state.Legacy {
		store, err := OpenStore(state.Path)
		return store, state, err
	}
	store, err := OpenPairStore(state.Path, state.Pair)
	return store, state, err
}

// OpenPairStore opens a state lane and binds it to pair metadata. A copied or
// misrouted database cannot be used with a different registry record.
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
		return fmt.Errorf("read pair metadata: %w", err)
	}
	if storedID != pair.ID || storedFingerprint != pairCreationFingerprint(pair) || storedVersion != PairSchemaVersion {
		return fmt.Errorf("pair metadata mismatch: database belongs to pair %q, not registry pair %q", storedID, pair.ID)
	}
	return nil
}

func (s *Store) hasPairState() (bool, error) {
	for _, table := range []string{
		"thread_sessions",
		"processed_messages",
		"pending_messages",
		"skipped_messages",
		"thread_aliases",
	} {
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
	parts := append([]string{
		"pair-v1", pair.ID, pair.UserEmail, pair.DearMachineAddress, pair.Transport, pair.InboxID,
	}, pair.Allow...)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func newPairID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate pair ID: %w", err)
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
