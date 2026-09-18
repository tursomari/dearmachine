package entrypoint

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceIdentityWithRealGit(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		personal  bool
		interrupt bool
	}{
		{name: "no user identity"},
		{name: "personal defaults and signing", personal: true},
		{name: "resumed partial identity setup", personal: true, interrupt: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			repo := filepath.Join(root, "workspace")
			global := filepath.Join(root, ".gitconfig")
			var original []byte
			if scenario.personal {
				original = []byte("[user]\n name = Personal User\n email = personal@example.test\n[author]\n name = Personal Author\n email = author@example.test\n[committer]\n name = Personal Committer\n email = committer@example.test\n[commit]\n gpgSign = true\n[gpg]\n program = /nonexistent-signing-program\n")
				if err := os.WriteFile(global, original, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			env := []string{}
			for _, entry := range os.Environ() {
				key := strings.SplitN(entry, "=", 2)[0]
				if key != "HOME" && key != "XDG_CONFIG_HOME" && key != "EMAIL" && !strings.HasPrefix(key, "GIT_") {
					env = append(env, entry)
				}
			}
			env = append(env, "HOME="+root, "XDG_CONFIG_HOME="+root, "GIT_CONFIG_GLOBAL="+global, "GIT_CONFIG_NOSYSTEM=1")
			git := func(args ...string) ([]byte, error) {
				cmd := exec.Command("git", args...)
				cmd.Dir, cmd.Env = repo, env
				return cmd.CombinedOutput()
			}
			fake := &fakeCommandRunner{store: filepath.Join(root, "store")}
			interrupted := false
			runner := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
				if name != "git" {
					return fake.Run(ctx, dir, name, args...)
				}
				if scenario.interrupt && !interrupted && strings.Join(args, " ") == "config --local author.email " {
					interrupted = true
					return nil, errors.New("interrupted identity setup")
				}
				// This test exercises real Git identity/commits; LFS and model work
				// belong to their own integration gates.
				if args[0] == "lfs" {
					return nil, nil
				}
				return git(args...)
			}
			options := Options{RepoPath: repo, AgentBinary: "/fake/machtiani", RunCommand: runner}
			_, err := Initialize(context.Background(), options)
			if scenario.interrupt {
				if err == nil || !interrupted {
					t.Fatalf("expected interrupted bootstrap, got %v", err)
				}
				options.Resume = true
				_, err = Initialize(context.Background(), options)
			}
			if err != nil {
				t.Fatal(err)
			}
			// Subsequent work uses the persisted repository identity too.
			if out, err := git("commit", "--allow-empty", "-m", "later workspace work"); err != nil {
				t.Fatalf("later commit: %v: %s", err, out)
			}
			out, err := git("log", "--format=%an <%ae>|%cn <%ce>")
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Repeat("machtiani <>|machtiani <>\n", 3)
			if string(out) != want {
				t.Fatalf("commit identities = %q, want %q", out, want)
			}
			if out, err := git("fsck", "--strict"); err != nil {
				t.Fatalf("fsck: %v: %s", err, out)
			}
			actual, err := os.ReadFile(global)
			if scenario.personal {
				if err != nil || !bytes.Equal(actual, original) {
					t.Fatalf("global Git configuration changed: %v", err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("created global Git configuration: %v", err)
			}
			// Adopting an existing repository must not rewrite its identity.
			if out, err := git("config", "--local", "user.name", "Existing Owner"); err != nil {
				t.Fatalf("config: %v: %s", err, out)
			}
			configPath := filepath.Join(repo, ".git", "config")
			before, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			result, err := Initialize(context.Background(), options)
			if err != nil || !result.AlreadyInitialized {
				t.Fatalf("existing repository: %+v, %v", result, err)
			}
			after, err := os.ReadFile(configPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("existing repository configuration changed: %v", err)
			}
		})
	}
}
