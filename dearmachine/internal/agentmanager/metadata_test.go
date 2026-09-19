package agentmanager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatusReadsCompleteMetadataDuringReplacement(t *testing.T) {
	manager := New(t.TempDir())
	meta := Meta{TicketID: "concurrent", Worker: "codex", Status: StatusOpen, NativeSession: strings.Repeat("s", 256*1024)}
	if err := os.MkdirAll(manager.TicketDir(meta.TicketID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := manager.writeMeta(meta); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		for {
			select {
			case <-done:
				result <- nil
				return
			default:
			}
			got, err := manager.Status(meta.TicketID)
			if err != nil {
				result <- err
				return
			}
			if got.TicketID != meta.TicketID || got.Status != meta.Status || got.NativeSession != meta.NativeSession {
				result <- fmt.Errorf("read incomplete metadata")
				return
			}
		}
	}()
	for range 100 {
		if err := manager.writeMeta(meta); err != nil {
			close(done)
			<-result
			t.Fatal(err)
		}
	}
	close(done)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(manager.TicketDir(meta.TicketID), "meta.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("metadata mode = %o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(manager.TicketDir(meta.TicketID))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "meta.json" {
		t.Fatalf("temporary metadata files remain: %v", entries)
	}
}
