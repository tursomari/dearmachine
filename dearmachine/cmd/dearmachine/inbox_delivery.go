package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/dearmachine/dearmachine/internal/client"
)

func runInboxDelivery(args []string, deps dependencies) error {
	flags := flag.NewFlagSet("inbox delivery", flag.ContinueOnError)
	flags.SetOutput(outputOrDiscard(deps.flagOutput))
	flags.Usage = func() { _ = inboxDeliveryHelp(deps.flagOutput) }
	pair := flags.String("pair", "", "registered pair email address or UUID (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *pair == "" || flags.NArg() != 1 {
		return errors.New("usage: dearmachine inbox delivery --pair <email-or-uuid> <outbound-message-id>")
	}
	state, err := client.ResolvePairState(deps.userHomeDir, *pair)
	if err != nil {
		return err
	}
	if deps.newRawTransport == nil {
		return errors.New("raw mail transport constructor is unavailable")
	}
	raw, err := deps.newRawTransport(state.Inbox.Transport, state.Inbox.ProviderID)
	if err != nil {
		return err
	}
	result, err := client.InspectReplyDelivery(context.Background(), raw, flags.Arg(0))
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(outputOrDiscard(deps.stdout))
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
func inboxDeliveryHelp(output io.Writer) error {
	_, err := fmt.Fprint(outputOrDiscard(output), `Usage:
  dearmachine inbox delivery --pair <email-or-uuid> <outbound-message-id>

Reads the Sendmux submission and per-recipient delivery status as JSON.
A Sent copy, queued submission, or SMTP acceptance does not prove delivery.
The client can remain running; this command makes no remote changes.
`)
	return err
}
