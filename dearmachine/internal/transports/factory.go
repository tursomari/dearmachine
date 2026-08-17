package transports

import (
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
}

// New constructs the selected mail transport. Authentication remains local to
// the selected adapter.
func New(id, inboxID string) (client.Transport, error) {
	build, ok := constructors[id]
	if ok {
		return build(inboxID)
	}
	if _, cataloged := Lookup(id); cataloged {
		return nil, fmt.Errorf("transport %q is not implemented", id)
	}
	return nil, fmt.Errorf("unknown transport %q", id)
}
