package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dearmachine/dearmachine/internal/client"
)

func guestHelp(output io.Writer) error {
	_, err := fmt.Fprint(outputOrDiscard(output), `Usage:
  dearmachine guest allow <address> --pair <pair> --message-id <message>
  dearmachine guest list --pair <pair>
  dearmachine guest revoke <address> --pair <pair> --thread-id <thread>
  dearmachine guest revoke <address> --pair <pair> --all

Guest grants persist for the exact pair, inbox, guest and provider thread.
On AgentMail, an authenticated owner's To or CC automatically invites guests.
DearMachine locally verifies exact-domain DKIM and signed author/routing headers;
this trusts the sender domain's operator to control its mailboxes. Raw From or
SPF/DKIM/DMARC verdicts alone are insufficient. OpenMail and Sendmux currently
lack supported evidence and reject all inbound work, including owner messages.

Guests must Reply All to Dear Machine and the owner. Every guest instruction
requires its own private owner approval; there is no separate admission exchange
and retained trust never bypasses approval. Answers go To the owner and CC active
thread guests visible in the original request. Owner continuations omitting the
guest stay private and leave participation and pending approvals intact.

Private approval prompts and owner-only answers include REMOVE GUEST <code>.
Reply with that command to revoke the exact guest/thread grant. Revocation blocks
new starts, pending approvals and old work after reinvitation; it cannot recall
already-started effects or submitted mail. Ordinary reply-all cannot restore a
revoked grant. Explicit local allow restores it using a provider-resolved owner
message with Dear Machine and the guest visibly in To or CC. Send a new guest
instruction afterward. Allow is trusted operator authorization, not sender proof.

Provider address lists synchronize automatically. Provider errors leave sync
pending; local revocation still applies. Commands work while the daemon runs.
List reports grants, provider synchronization and sender authentication support.
`)
	return err
}
func runGuest(args []string, deps dependencies) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return guestHelp(deps.flagOutput)
	}
	command := args[0]
	if command != "allow" && command != "list" && command != "revoke" {
		return fmt.Errorf("unknown guest command %q", command)
	}
	flags := flag.NewFlagSet("guest "+command, flag.ContinueOnError)
	flags.SetOutput(outputOrDiscard(deps.flagOutput))
	flags.Usage = func() { _ = guestHelp(deps.flagOutput) }
	pair := flags.String("pair", "", "registered pair email or UUID (required)")
	messageID := flags.String("message-id", "", "visible controller invitation message ID")
	threadID := flags.String("thread-id", "", "exact provider thread ID")
	all := flags.Bool("all", false, "revoke all grants for this guest and pair")
	rest := args[1:]
	address := ""
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		address = rest[0]
		rest = rest[1:]
	}
	if err := flags.Parse(rest); err != nil {
		return err
	}
	if flags.NArg() == 1 && address == "" {
		address = flags.Arg(0)
	} else if flags.NArg() != 0 {
		return errors.New("unexpected guest arguments")
	}
	if strings.TrimSpace(*pair) == "" {
		return errors.New("--pair is required")
	}
	switch command {
	case "allow":
		if address == "" || *messageID == "" || *threadID != "" || *all {
			return errors.New("allow requires address, --pair and --message-id only")
		}
	case "revoke":
		if address == "" || *messageID != "" || (*all == (*threadID != "")) {
			return errors.New("revoke requires address, --pair and exactly one of --thread-id or --all")
		}
	case "list":
		if address != "" || *messageID != "" || *threadID != "" || *all {
			return errors.New("list accepts only --pair")
		}
	}
	state, err := client.ResolvePairState(deps.userHomeDir, *pair)
	if err != nil {
		return err
	}
	path, err := client.DefaultGuestDatabasePath(deps.userHomeDir)
	if err != nil {
		return err
	}
	guests, err := client.OpenGuestStore(path)
	if err != nil {
		return err
	}
	defer guests.Close()
	if command == "list" {
		grants, err := guests.List(state.Pair.ID)
		if err != nil {
			return err
		}
		permissions, err := guests.InboxPermissionStatus(state.Inbox)
		if err != nil {
			return err
		}
		return json.NewEncoder(outputOrDiscard(deps.stdout)).Encode(struct {
			Grants      []client.GuestGrant            `json:"grants"`
			Permissions []client.GuestPermissionStatus `json:"permissions"`
			Automatic   string                         `json:"automatic"`
		}{grants, permissions, client.SenderAuthenticationStatus(state.Inbox.Transport)})
	}
	if command == "revoke" {
		if err = guests.RevokeGuest(state.Pair, state.Inbox, address, *threadID, *all); err != nil {
			return err
		}
		fmt.Fprintln(outputOrDiscard(deps.stdout), "Guest grants revoked locally. Provider synchronization pending.")
	}
	partial := func(err error) error {
		if command == "revoke" {
			return fmt.Errorf("local revocation committed; provider synchronization pending: %w", err)
		}
		return err
	}
	if deps.newRawTransport == nil {
		return partial(errors.New("mail transport constructor unavailable"))
	}
	raw, err := deps.newRawTransport(state.Inbox.Transport, state.Inbox.ProviderID)
	if err != nil {
		return partial(err)
	}
	router, err := client.NewInboxRouter(raw, state.Inbox, []client.Pair{state.Pair}, time.Second)
	if err != nil {
		return partial(err)
	}
	registryPath, err := client.DefaultPairRegistryPath(deps.userHomeDir)
	if err != nil {
		return partial(err)
	}
	registry, err := client.LoadPairRegistry(registryPath)
	if err != nil {
		return partial(err)
	}
	if err = router.ConfigureGuests(guests, registry.Pairs); err != nil {
		return partial(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if command == "allow" {
		grant, err := router.Invite(ctx, state.Pair.ID, *messageID, address)
		if err != nil {
			return err
		}
		fmt.Fprintf(outputOrDiscard(deps.stdout), "Guest grant active for thread %s (generation %d). Provider synchronization pending.\n", grant.ThreadID, grant.Generation)
	}
	if err = router.ReconcileGuests(ctx); err != nil {
		return fmt.Errorf("local guest change committed; provider synchronization pending: %w", err)
	}
	_, err = fmt.Fprintln(outputOrDiscard(deps.stdout), "Provider guest permissions synchronized.")
	return err
}
