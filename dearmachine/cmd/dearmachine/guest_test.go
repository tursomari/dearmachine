package main

import (
	"context"
	"errors"
	"flag"
	"github.com/dearmachine/dearmachine/internal/client"
	"io"
	"strings"
	"testing"
)

func TestGuestCommandContract(t *testing.T) {
	for _, args := range [][]string{{"guest", "--help"}, {"guest", "allow", "--help"}, {"guest", "revoke", "--help"}, {"guest", "list", "--help"}} {
		var output strings.Builder
		err := run(args, func(string) string { return "" }, dependencies{flagOutput: &output})
		if err != nil && !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("%v: %v", args, err)
		}
		for _, want := range []string{"--pair", "--message-id", "--thread-id", "--all", "automatic"} {
			if !strings.Contains(output.String(), want) {
				t.Fatalf("help missing %s", want)
			}
		}
	}
}
func TestGuestRequiresExactSelectionAndCommand(t *testing.T) {
	for _, args := range [][]string{{"allow", "guest@example.test"}, {"allow", "guest@example.test", "--pair", "p"}, {"revoke", "guest@example.test", "--pair", "p"}, {"revoke", "guest@example.test", "--pair", "p", "--all", "--thread-id", "t"}, {"list"}, {"unknown"}} {
		if err := runGuest(args, dependencies{flagOutput: io.Discard}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
func TestGuestListNeedsNoProviderOrDaemonStop(t *testing.T) {
	home := t.TempDir()
	state := makeInboxTestPair(t, home, "openmail")
	var out strings.Builder
	err := runGuest([]string{"list", "--pair", state.Pair.ID}, dependencies{flagOutput: io.Discard, stdout: &out, userHomeDir: func() (string, error) { return home, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "automatic") || !strings.Contains(out.String(), "grants") {
		t.Fatalf("list=%s", out.String())
	}
}

type guestCLITransport struct {
	inboxTestTransport
	entries map[string]bool
}

func (t *guestCLITransport) InspectReceive(_ context.Context, address string) (client.ReceivePermission, error) {
	return client.ReceivePermission{Present: t.entries[address], Token: address}, nil
}
func (t *guestCLITransport) AddReceive(_ context.Context, address string) (client.ReceivePermission, error) {
	t.entries[address] = true
	return client.ReceivePermission{Present: true, Token: address}, nil
}
func (t *guestCLITransport) RemoveReceive(_ context.Context, address, _ string) error {
	delete(t.entries, address)
	return nil
}

func TestGuestAllowListRevokeWhileDaemonRunsAndProviderFails(t *testing.T) {
	home := t.TempDir()
	state := makeInboxTestPair(t, home, "openmail")
	var output strings.Builder
	raw := &guestCLITransport{entries: map[string]bool{}, inboxTestTransport: inboxTestTransport{message: client.Message{MessageID: "invitation", ThreadID: "thread", From: state.Pair.UserEmail, To: []string{state.Inbox.Address, "guest@example.test"}}}}
	deps := dependencies{flagOutput: io.Discard, stdout: &output, userHomeDir: func() (string, error) { return home, nil }, newRawTransport: func(transport, id string) (client.Transport, error) {
		if transport != "openmail" || id != state.Inbox.ProviderID {
			t.Fatalf("wrong provider inbox %s/%s", transport, id)
		}
		return raw, nil
	}, daemonStatus: func(string) (int, bool, error) { t.Fatal("guest command required daemon stop"); return 0, false, nil }}
	if err := runGuest([]string{"allow", "guest@example.test", "--pair", state.Pair.ID, "--message-id", "invitation"}, deps); err != nil {
		t.Fatal(err)
	}
	if !raw.entries["guest@example.test"] {
		t.Fatal("guest receive permission not synchronized")
	}
	output.Reset()
	if err := runGuest([]string{"list", "--pair", state.Pair.ID}, deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"Active":true`) {
		t.Fatalf("active list=%s", output.String())
	}
	deps.newRawTransport = func(string, string) (client.Transport, error) { return nil, errors.New("provider offline") }
	output.Reset()
	err := runGuest([]string{"revoke", "guest@example.test", "--pair", state.Pair.ID, "--thread-id", "thread"}, deps)
	if err == nil || !strings.Contains(err.Error(), "local revocation committed") {
		t.Fatalf("partial revoke=%v", err)
	}
	output.Reset()
	if err = runGuest([]string{"list", "--pair", state.Pair.ID}, deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"Active":false`) || !strings.Contains(output.String(), `"Pending":true`) {
		t.Fatalf("partial state=%s", output.String())
	}
}
