package backends

import (
	"reflect"
	"strings"
	"testing"
)

func TestCatalogAndEnvironmentRoundTrip(t *testing.T) {
	registered := All()
	if len(registered) != 5 || registered[0].ID != "codex" ||
		registered[1].ID != "codex-yolo" || registered[2].ID != "forge" ||
		registered[3].ID != "omp" || registered[4].ID != "claude" {
		t.Fatalf("catalog = %+v", registered)
	}
	yolo, ok := Lookup("codex-yolo")
	if !ok || yolo.Executable != "codex" || !yolo.ExplicitOptIn {
		t.Fatalf("codex-yolo backend = %+v, found = %v", yolo, ok)
	}
	omp, ok := Lookup("omp")
	if !ok || omp.Executable != "omp" || omp.ExplicitOptIn {
		t.Fatalf("omp backend = %+v, found = %v", omp, ok)
	}
	claude, ok := Lookup("claude")
	if !ok || claude.Executable != "claude" || !claude.ExplicitOptIn || !strings.Contains(claude.InstallHelp, "https://claude.ai/install.sh") {
		t.Fatalf("claude backend = %+v, found = %v", claude, ok)
	}
	if decoded, err := Decode(`["claude"]`); err != nil || !reflect.DeepEqual(decoded, []string{"claude"}) {
		t.Fatalf("Decode claude = %v, %v", decoded, err)
	}
	encoded, err := Encode([]string{"codex-yolo", "forge", "omp", "codex"})
	if err != nil || encoded != `["codex-yolo","forge","omp","codex"]` {
		t.Fatalf("Encode = %q, %v", encoded, err)
	}
	decoded, err := Decode(encoded)
	if err != nil || !reflect.DeepEqual(decoded, []string{"codex-yolo", "forge", "omp", "codex"}) {
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
		{value: `["unknown"]`, want: `unknown backend "unknown"`},
		{value: `["forge","forgecode"]`, want: `duplicate backend "forge"`},
		{value: `["codex","codex"]`, want: `duplicate backend "codex"`},
	} {
		if _, err := Decode(test.value); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("Decode(%s) error = %v, want %q", test.value, err, test.want)
		}
	}
}
