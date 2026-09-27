package machtianiconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunModelArgs(t *testing.T) {
	for _, test := range []struct {
		name, config, oneRun string
		want                 []string
	}{
		{name: "missing configuration"},
		{name: "default supplies every role", config: `default_model = "current"`, want: []string{"current", "current", "current", "current"}},
		{name: "distinct roles", config: "default_model = \"planner\"\nanswer_model = \"answer\"\nfile_discovery_model = \"discovery\"\nshell_agent_model = \"shell\"", want: []string{"planner", "answer", "discovery", "shell"}},
		{name: "one run without configuration", oneRun: " override ", want: []string{"override", "override", "override", "override"}},
		{name: "one run preserves explicit roles", oneRun: "override", config: "default_model = \"planner\"\nanswer_model = \"answer\"\nshell_agent_model = \"shell\"", want: []string{"override", "answer", "override", "shell"}},
		{name: "blank roles follow planner", config: "default_model = \" current \"\nanswer_model = \" \"\nfile_discovery_model = \"\"\nshell_agent_model = \"\"", want: []string{"current", "current", "current", "current"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			// An inherited standalone selection must not choose DearMachine's models.
			t.Setenv("MACHTIANI_CONFIG", filepath.Join(home, "personal.toml"))
			t.Setenv("MACHTIANI_MODEL", "inherited")
			if test.config != "" {
				syncFixture(t, home, test.config, "unused")
			}
			got, err := RunModelArgs(test.oneRun)
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			for i, alias := range test.want {
				want = append(want, []string{"--model", "--answer-model", "--file-discovery-model", "--shell-agent-model"}[i], alias)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("model args = %q, want %q", got, want)
			}
		})
	}
}

func TestRunModelArgsReadsCurrentConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, alias := range []string{"before", "after"} {
		syncFixture(t, home, "default_model = \""+alias+"\"", "unused")
		got, err := RunModelArgs("")
		if err != nil || len(got) != 8 {
			t.Fatalf("model args = %q, error = %v", got, err)
		}
		for i := 1; i < len(got); i += 2 {
			if got[i] != alias {
				t.Fatalf("model args retain an earlier selection: %q", got)
			}
		}
	}
}

func TestRunModelArgsRejectsUnreadableOrMalformedConfiguration(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unreadable", true: "malformed"}[malformed], func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			path := filepath.Join(home, ".config/dearmachine/machtiani/config.toml")
			if malformed {
				syncFixture(t, home, "default_model = [", "unused")
			} else if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := RunModelArgs("override"); err == nil || !strings.Contains(err.Error(), "run model configuration") {
				t.Fatalf("error = %v, want configuration error", err)
			}
		})
	}
}
