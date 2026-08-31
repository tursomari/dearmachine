package transports

import (
	"slices"
	"testing"
)

func TestCatalogContainsOnlyImplementedMailTransports(t *testing.T) {
	all := All()
	if !slices.Equal(IDs(), []string{"agentmail", "openmail", "sendmux"}) || len(all) != len(IDs()) {
		t.Fatalf("All = %+v", all)
	}
	for _, transport := range all {
		if transport.DisplayName == "" || transport.SDKOrEndpoint == "" ||
			transport.InstallHint == "" || len(transport.ConfigKeys) == 0 {
			t.Fatalf("incomplete catalog entry: %+v", transport)
		}
	}
	if _, ok := Lookup("gmail"); ok {
		t.Fatal("gmail must not be a selectable transport")
	}
}

func TestCatalogReturnsClonedConfigKeys(t *testing.T) {
	all := All()
	all[0].ConfigKeys[0] = "changed"
	agentmail, ok := Lookup("agentmail")
	if !ok || !slices.Equal(agentmail.ConfigKeys, []string{"AGENTMAIL_API_KEY", "AGENTMAIL_API_KEY_FILE"}) {
		t.Fatalf("Lookup(agentmail) = %+v, %v", agentmail, ok)
	}
	agentmail.ConfigKeys[0] = "changed-again"
	again, _ := Lookup("agentmail")
	if again.ConfigKeys[0] != "AGENTMAIL_API_KEY" {
		t.Fatalf("catalog aliases caller: %+v", again)
	}
}
