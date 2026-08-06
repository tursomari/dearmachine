package backends

import (
	"reflect"
	"strings"
	"testing"
)

func TestCatalogAndEnvironmentRoundTrip(t *testing.T) {
	registered := All()
	if len(registered) != 2 || registered[0].ID != "codex" || registered[1].ID != "forge" {
		t.Fatalf("catalog = %+v", registered)
	}
	for _, backend := range registered {
		if backend.ID != backend.Executable {
			t.Errorf("backend ID %q does not match executable %q", backend.ID, backend.Executable)
		}
	}
	encoded, err := Encode([]string{"forge", "codex"})
	if err != nil || encoded != `["forge","codex"]` {
		t.Fatalf("Encode = %q, %v", encoded, err)
	}
	decoded, err := Decode(encoded)
	if err != nil || !reflect.DeepEqual(decoded, []string{"forge", "codex"}) {
		t.Fatalf("Decode = %v, %v", decoded, err)
	}
}

func TestDecodeCanonicalizesLegacyForgecodeID(t *testing.T) {
	decoded, err := Decode(`["forgecode","codex"]`)
	if err != nil || !reflect.DeepEqual(decoded, []string{"forge", "codex"}) {
		t.Fatalf("Decode legacy IDs = %v, %v", decoded, err)
	}
	if _, ok := Lookup("forgecode"); ok {
		t.Fatal("legacy forgecode ID remains a catalog backend")
	}
}

func TestCatalogRejectsRemovedAndDuplicateBackends(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: `[]`, want: "at least one backend"},
		{value: `["claude"]`, want: `unknown backend "claude"`},
		{value: `["forge","forgecode"]`, want: `duplicate backend "forge"`},
		{value: `["codex","codex"]`, want: `duplicate backend "codex"`},
	} {
		if _, err := Decode(test.value); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("Decode(%s) error = %v, want %q", test.value, err, test.want)
		}
	}
}
