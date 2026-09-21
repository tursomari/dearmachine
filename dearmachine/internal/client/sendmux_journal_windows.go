package client

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/dearmachine/dearmachine/internal/hostos"
	"golang.org/x/sys/windows"
)

// SQLite's Windows VFS provides the native FlushFileBuffers transaction
// protocol. PERSIST retains its rollback journal rather than relying on Unix
// directory-fsync semantics for an unlink after every submission checkpoint.
// A separate reply lock remains held across provider calls, as on Unix.
func openSendmuxJournal(ctx context.Context, directory, key string) ([]byte, func([]byte) error, func(), error) {
	if key == "" || filepath.Base(key) != key || strings.ContainsAny(key, `:/\`) {
		return nil, nil, nil, errors.New("invalid Sendmux journal key")
	}
	if err := privateJournalDirectory(directory); err != nil {
		return nil, nil, nil, err
	}
	path := filepath.Join(directory, key+".sqlite")
	for _, candidate := range []string{path, path + "-journal", path + "-wal", path + "-shm", filepath.Join(directory, key+".json"), filepath.Join(directory, key+".json.lock")} {
		if info, err := os.Lstat(candidate); err == nil {
			if !info.Mode().IsRegular() || !hostos.Private(candidate, info, 0077) {
				return nil, nil, nil, errors.New("unsafe Sendmux journal file")
			}
		} else if !os.IsNotExist(err) {
			return nil, nil, nil, err
		}
	}
	unlock, err := privateFileLock(ctx, filepath.Join(directory, key+".json.lock"))
	if err != nil {
		return nil, nil, nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		unlock()
		return nil, nil, nil, err
	}
	uriPath := filepath.ToSlash(absolute)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := url.URL{Scheme: "file", Path: uriPath}
	db, err := sql.Open("sqlite3", uri.String()+"?_busy_timeout=10000&_journal_mode=PERSIST&_synchronous=EXTRA")
	if err != nil {
		unlock()
		return nil, nil, nil, err
	}
	db.SetMaxOpenConns(1)
	closeStore := func() { _ = db.Close(); unlock() }
	fail := func(err error) ([]byte, func([]byte) error, func(), error) {
		closeStore()
		return nil, nil, nil, err
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS submission (id INTEGER PRIMARY KEY CHECK(id=1), payload BLOB NOT NULL)`); err != nil {
		return fail(err)
	}
	var mode string
	var synchronous int
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return fail(err)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return fail(err)
	}
	if mode != "persist" || synchronous != 3 {
		return fail(errors.New("Sendmux journal durability could not be established"))
	}
	persist := func(data []byte) error {
		_, err := db.ExecContext(ctx, `INSERT INTO submission(id,payload) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`, data)
		return err
	}
	var data []byte
	err = db.QueryRowContext(ctx, "SELECT payload FROM submission WHERE id=1").Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		// Preserve any journal imported from a Unix installation. Never discard
		// an uncertain submission merely because the persistence format changed.
		data, err = os.ReadFile(filepath.Join(directory, key+".json"))
		if os.IsNotExist(err) {
			data, err = nil, nil
		} else if err == nil {
			err = persist(data)
		}
	}
	if err != nil {
		return fail(err)
	}
	return data, persist, closeStore, nil
}

func privateJournalDirectory(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	// Refuse junctions and symlinks in every existing ancestor before creating
	// or protecting anything. Never repair a preexisting public journal.
	for at := absolute; ; at = filepath.Dir(at) {
		if info, err := os.Lstat(at); err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("redirected Sendmux journal directory")
			}
			f, err := hostos.Open(at, os.O_RDONLY, 0)
			if err != nil {
				return err
			}
			f.Close()
		} else if !os.IsNotExist(err) {
			return err
		}
		if filepath.Dir(at) == at {
			break
		}
	}
	info, err := os.Lstat(absolute)
	if os.IsNotExist(err) {
		if err = createPrivateJournalDirectory(absolute); err != nil {
			return err
		}
		info, err = os.Lstat(absolute)
	}
	if err != nil {
		return err
	}
	if !hostos.Private(absolute, info, 0077) {
		return fmt.Errorf("Sendmux journal directory must be private")
	}
	return nil
}

// Set the inheritable DACL at creation. Creating then protecting the directory
// leaves a public interval in which another sender can reject it or create a
// journal with inherited public permissions.
func createPrivateJournalDirectory(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return errors.New("Sendmux journal volume is unavailable")
	}
	if err := createPrivateJournalDirectory(parent); err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)")
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	name, err := windowsPath(path)
	if err != nil {
		return err
	}
	err = windows.CreateDirectory(name, &sa)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil // The caller checks the winner's directory before opening state.
	}
	return err
}
