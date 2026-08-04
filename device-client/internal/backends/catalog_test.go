package backends

import (
	"reflect"
	"strings"
	"testing"
)

func TestCatalogAndEnvironmentRoundTrip(t *testing.T) {
	registered := All()
	if len(registered) != 2 || registered[0].ID != "codex" || registered[1].ID != "forgecode" {
		t.Fatalf("catalog = %+v", registered)
	}
	encoded, err := Encode([]string{"forgecode", "codex"})
	if err != nil || encoded != `["forgecode","codex"]` {
		t.Fatalf("Encode = %q, %v", encoded, err)
	}
	decoded, err := Decode(encoded)
	if err != nil || !reflect.DeepEqual(decoded, []string{"forgecode", "codex"}) {
		t.Fatalf("Decode = %v, %v", decoded, err)
	}
}

func TestCatalogRejectsRemovedAndDuplicateBackends(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: `[]`, want: "at least one backend"},
		{value: `["claude"]`, want: `unknown backend "claude"`},
		{value: `["codex","codex"]`, want: `duplicate backend "codex"`},
	} {
		if _, err := Decode(test.value); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("Decode(%s) error = %v, want %q", test.value, err, test.want)
		}
	}
}
