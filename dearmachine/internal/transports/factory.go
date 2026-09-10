package transports

import (
	"context"
	"fmt"

	"github.com/dearmachine/dearmachine/internal/client"
)

type constructor func(string) (client.Transport, error)

var constructors = map[string]constructor{
	"agentmail": func(inboxID string) (client.Transport, error) {
		return client.NewAgentMailTransport(inboxID)
	},
	"openmail": func(inboxID string) (client.Transport, error) {
		return client.NewOpenMailTransport(inboxID)
	},
	"sendmux": func(inboxID string) (client.Transport, error) {
		return client.NewSendmuxTransport(inboxID)
	},
}

// NewRaw constructs a provider adapter without per-pair filtering. The
// multi-pair inbox router owns authorization before a message reaches a pair.
func NewRaw(id, inboxID string) (client.Transport, error) {
	build, ok := constructors[id]
	if ok {
		transport, err := build(inboxID)
		if err != nil {
			return nil, err
		}
		return client.WithTransportRetries(transport), nil
	}
	if _, cataloged := Lookup(id); cataloged {
		return nil, fmt.Errorf("transport %q is not implemented", id)
	}
	return nil, fmt.Errorf("unknown transport %q", id)
}

// ProvisionInbox uses a real provider creation API when the adapter supports
// one. Existing inboxes on other transports can still be registered via
// `dearmachine up --create --inbox`.
func ProvisionInbox(ctx context.Context, id string) (client.Inbox, error) {
	switch id {
	case "agentmail":
		return client.ProvisionAgentMailInbox(ctx)
	case "openmail":
		return client.ProvisionOpenMailInbox(ctx)
	case "sendmux":
		return client.ProvisionSendmuxInbox(ctx)
	default:
		return client.Inbox{}, fmt.Errorf("unknown transport %q", id)
	}
}

func InspectInbox(ctx context.Context, id, selection string) (client.Inbox, error) {
	switch id {
	case "agentmail":
		return client.InspectAgentMailInbox(ctx, selection)
	case "openmail":
		return client.InspectOpenMailInbox(ctx, selection)
	case "sendmux":
		return client.InspectSendmuxInbox(ctx, selection)
	default:
		return client.Inbox{}, fmt.Errorf("unknown transport %q", id)
	}
}

// AuthorizePair asks the selected adapter to establish any provider-side
// correspondent policy required for a local pair. Providers without such a
// policy rely on the central inbox router's fail-closed authorization.
func AuthorizePair(ctx context.Context, id, inboxID, email string) error {
	if id == "sendmux" {
		return client.AuthorizeSendmuxPair(ctx, inboxID, email)
	}
	transport, err := NewRaw(id, inboxID)
	if err != nil {
		return err
	}
	authorizer, ok := transport.(client.PairAuthorizer)
	if !ok {
		return nil
	}
	return authorizer.AuthorizePair(ctx, email)
}
