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

Guest authorization persists for the exact pair, inbox and provider thread.
Allow is an explicit local operator authorization using a provider-resolved
controller message with Dear Machine and the guest visibly in To or CC.
Current adapters lack exact mailbox-owner attribution, so automatic invitations
are disabled. From matching or SPF/DKIM/DMARC alone does not authenticate an owner.

Guests must Reply All to Dear Machine and the controller. Delivery authorization,
participant admission, instruction approval and trust are separate gates.
Task answers go To the owner and CC active thread guests visible in the original
request's From/To/CC. Owner continuations without the guest stay private.
Admission and instruction approval prompts remain private to the owner.
Revocation blocks subsequent guest execution starts, including queued/recovered
work. It does not roll back already-started work or erase pairing/admission/trust.
Provider errors leave synchronization pending; local revocation still applies.
Commands may run while the daemon is active. List reports grants and pending sync.
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
		}{grants, permissions, "disabled: current adapters lack exact mailbox-owner attribution"})
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
