package transports

// Transport describes a selectable mail adapter. The catalog is metadata only;
// constructors and credentials live elsewhere.
type Transport struct {
	ID            string
	DisplayName   string
	SDKOrEndpoint string
	InstallHint   string
	ConfigKeys    []string
}

var catalog = []Transport{
	{
		ID:            "agentmail",
		DisplayName:   "AgentMail",
		SDKOrEndpoint: "github.com/agentmail-to/agentmail-go",
		InstallHint:   "Set AGENTMAIL_API_KEY or AGENTMAIL_API_KEY_FILE.",
		ConfigKeys:    []string{"AGENTMAIL_API_KEY", "AGENTMAIL_API_KEY_FILE"},
	},
	{
		ID:            "openmail",
		DisplayName:   "OpenMail",
		SDKOrEndpoint: "https://api.openmail.sh/v1",
		InstallHint:   "Set OPENMAIL_API_KEY or OPENMAIL_API_KEY_FILE and both OpenMail correspondent allow-lists.",
		ConfigKeys: []string{
			"OPENMAIL_API_KEY",
			"OPENMAIL_API_KEY_FILE",
			"DEARMACHINE_OPENMAIL_ALLOWED_FROM",
			"DEARMACHINE_OPENMAIL_ALLOWED_TO",
			"DEARMACHINE_LIVE_OPENMAIL",
			"DEARMACHINE_LIVE_OPENMAIL_APPLY",
		},
	},
	{
		ID:            "sendmux",
		DisplayName:   "Sendmux",
		SDKOrEndpoint: "sendmux.ai/go/mailbox",
		InstallHint:   "Set SENDMUX_MAILBOX_API_KEY or SENDMUX_MAILBOX_API_KEY_FILE and both Sendmux correspondent allow-lists.",
		ConfigKeys: []string{
			"SENDMUX_MAILBOX_API_KEY",
			"SENDMUX_MAILBOX_API_KEY_FILE",
			"DEARMACHINE_SENDMUX_ALLOWED_FROM",
			"DEARMACHINE_SENDMUX_ALLOWED_TO",
			"DEARMACHINE_LIVE_SENDMUX",
			"DEARMACHINE_LIVE_SENDMUX_APPLY",
		},
	},
}

func IDs() []string {
	ids := make([]string, 0, len(catalog))
	for _, transport := range catalog {
		ids = append(ids, transport.ID)
	}
	return ids
}

func All() []Transport {
	all := make([]Transport, len(catalog))
	for index, transport := range catalog {
		all[index] = transport
		all[index].ConfigKeys = append([]string(nil), transport.ConfigKeys...)
	}
	return all
}

func Lookup(id string) (Transport, bool) {
	for _, transport := range catalog {
		if transport.ID == id {
			transport.ConfigKeys = append([]string(nil), transport.ConfigKeys...)
			return transport, true
		}
	}
	return Transport{}, false
}
