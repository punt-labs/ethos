package claudesettings

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

var rules = map[string][]string{
	"allow": {"Bash(t:*)", "mcp__t__*"},
	"ask":   {"Bash(t admin:*)"},
}

// policy gives root a shared-local store outside the checkout, so a test can
// assert that nothing but settings.json is written under root.
func policy(t *testing.T, root string) layout.Policy {
	t.Helper()
	p, err := layout.NewRepository(t.TempDir(), root, layout.RecordBlame)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func writeSettings(t *testing.T, root, content string) string {
	t.Helper()
	path := settingsPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRegisterCreatesTheFile(t *testing.T) {
	root := t.TempDir()
	added, err := Register(root, rules, policy(t, root))
	if err != nil || !slices.Equal(added, append(rules["allow"], rules["ask"]...)) {
		t.Fatalf("Register = %v, %v", added, err)
	}
	want := "{\n  \"permissions\": {\n    \"allow\": [\n      \"Bash(t:*)\",\n      \"mcp__t__*\"\n    ],\n    \"ask\": [\n      \"Bash(t admin:*)\"\n    ]\n  }\n}\n"
	if got := readString(t, settingsPath(root)); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRegisterKeepsOrderAndOtherEntriesAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	path := writeSettings(t, root, `{"z":1,"permissions":{"deny":["X"],"allow":["Bash(a && b)"]},"a":{"k":true}}`)
	if _, err := Register(root, rules, policy(t, root)); err != nil {
		t.Fatal(err)
	}
	first := readString(t, path)
	added, err := Register(root, rules, policy(t, root))
	if err != nil || len(added) != 0 {
		t.Fatalf("second Register = %v, %v", added, err)
	}
	if readString(t, path) != first {
		t.Error("idempotent Register rewrote the file")
	}
	want := "{\n  \"z\": 1,\n  \"permissions\": {\n    \"deny\": [\n      \"X\"\n    ],\n    \"allow\": [\n      \"Bash(a && b)\",\n      \"Bash(t:*)\",\n      \"mcp__t__*\"\n    ],\n    \"ask\": [\n      \"Bash(t admin:*)\"\n    ]\n  },\n  \"a\": {\n    \"k\": true\n  }\n}\n"
	if first != want {
		t.Errorf("got %q, want %q", first, want)
	}
}

func TestDeregisterRemovesExactlyTheOwnedRules(t *testing.T) {
	root := t.TempDir()
	path := writeSettings(t, root, `{"permissions":{"allow":["Read","Bash(t:*)","Bash(t:x)","mcp__t__*"],"ask":["Bash(t admin:*)","Bash(other:*)"]}}`)
	removed, err := Deregister(root, rules, policy(t, root))
	if err != nil || !slices.Equal(removed, append(rules["allow"], rules["ask"]...)) {
		t.Fatalf("Deregister = %v, %v", removed, err)
	}
	want := "{\n  \"permissions\": {\n    \"allow\": [\n      \"Read\",\n      \"Bash(t:x)\"\n    ],\n    \"ask\": [\n      \"Bash(other:*)\"\n    ]\n  }\n}\n"
	if got := readString(t, path); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDeregisterWithoutAFileCreatesNothing(t *testing.T) {
	root := t.TempDir()
	if removed, err := Deregister(root, rules, policy(t, root)); err != nil || removed != nil {
		t.Fatalf("Deregister = %v, %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude")); !errors.Is(err, os.ErrNotExist) {
		t.Error(".claude/ created by a no-op disable")
	}
}

func TestDeregisterDoesNotCreateAnAbsentTier(t *testing.T) {
	root := t.TempDir()
	path := writeSettings(t, root, `{"permissions":{"allow":["Read"]}}`)
	removed, err := Deregister(root, rules, policy(t, root))
	if err != nil || len(removed) != 0 {
		t.Fatalf("Deregister = %v, %v", removed, err)
	}
	if got := readString(t, path); got != `{"permissions":{"allow":["Read"]}}` {
		t.Errorf("no-op deregister rewrote the file: %q", got)
	}
}

func TestUnparseableFilesAreRefusedUntouched(t *testing.T) {
	for name, content := range map[string]string{
		"malformed":   "{nope",
		"not object":  "[]",
		"allow type":  `{"permissions":{"allow":"Read"}}`,
		"allow items": `{"permissions":{"allow":[1]}}`,
		"allow null":  `{"permissions":{"allow":null}}`,
		"ask type":    `{"permissions":{"ask":"Bash(t admin:*)"}}`,
		"ask items":   `{"permissions":{"ask":[1]}}`,
		"ask null":    `{"permissions":{"ask":null}}`,
		"perms null":  `{"permissions":null}`,
		"invalid utf": "{\"a\":\"\xff\"}",
		"trailing":    "{} {}",
		"dup ask":     `{"permissions":{"ask":[],"ask":["Other"]}}`,
		"dup perms":   `{"permissions":{},"permissions":{"allow":[]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := writeSettings(t, root, content)
			if _, err := Register(root, rules, policy(t, root)); !errors.Is(err, ErrRefused) {
				t.Fatalf("Register err = %v, want ErrRefused", err)
			}
			if readString(t, path) != content {
				t.Error("refused file was changed")
			}
		})
	}
}

func TestSymlinksAreRefused(t *testing.T) {
	t.Run("settings.json", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "elsewhere.json")
		if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, settingsPath(root)); err != nil {
			t.Fatal(err)
		}
		if _, err := Register(root, rules, policy(t, root)); !errors.Is(err, ErrRefused) {
			t.Fatalf("err = %v, want ErrRefused", err)
		}
		if readString(t, target) != "{}" {
			t.Error("symlink target changed")
		}
	})
	t.Run(".claude", func(t *testing.T) {
		root := t.TempDir()
		elsewhere := filepath.Join(root, "elsewhere")
		if err := os.Mkdir(elsewhere, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, filepath.Join(root, ".claude")); err != nil {
			t.Fatal(err)
		}
		if _, err := Register(root, rules, policy(t, root)); !errors.Is(err, ErrRefused) {
			t.Fatalf("err = %v, want ErrRefused", err)
		}
		if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
			t.Errorf("wrote through the symlink: %v", entries)
		}
	})
}

func TestTheHomeDirectoryIsRefused(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	if _, err := Register(root, rules, policy(t, root)); !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude")); !errors.Is(err, os.ErrNotExist) {
		t.Error(".claude/ created in the home directory")
	}
}

func TestARetryConfirmsAFailedDirectorySync(t *testing.T) {
	root := t.TempDir()
	p := policy(t, root)
	injected := errors.New("injected directory sync failure")
	calls := 0
	syncDestination = func(layout.Policy, string) error {
		calls++
		if calls == 1 {
			return injected
		}
		return nil
	}
	t.Cleanup(func() { syncDestination = layout.SyncRepositoryDestination })
	var applied *layout.InstallDurabilityError
	if _, err := Register(root, rules, p); !errors.As(err, &applied) {
		t.Fatalf("Register err = %v, want InstallDurabilityError", err)
	}
	added, err := Register(root, rules, p)
	if err != nil || added != nil {
		t.Fatalf("retry = %v, %v; want nothing added, no error", added, err)
	}
	if calls != 2 {
		t.Errorf("retry made %d directory syncs in total, want 2", calls)
	}
}

func TestRegisterWithEnvCreatesPreservesAndRoundTrips(t *testing.T) {
	envDefaults := map[string]string{"GIT_OPTIONAL_LOCKS": "0"}
	t.Run("creates file with permissions and env", func(t *testing.T) {
		root := t.TempDir()
		addedRules, addedEnv, err := RegisterWithEnv(root, rules, envDefaults, policy(t, root))
		if err != nil {
			t.Fatalf("RegisterWithEnv: %v", err)
		}
		if !slices.Equal(addedRules, append(rules["allow"], rules["ask"]...)) {
			t.Errorf("addedRules = %v", addedRules)
		}
		if !slices.Equal(addedEnv, []string{"GIT_OPTIONAL_LOCKS=0"}) {
			t.Errorf("addedEnv = %v", addedEnv)
		}
		want := "{\n  \"permissions\": {\n    \"allow\": [\n      \"Bash(t:*)\",\n      \"mcp__t__*\"\n    ],\n    \"ask\": [\n      \"Bash(t admin:*)\"\n    ]\n  },\n  \"env\": {\n    \"GIT_OPTIONAL_LOCKS\": \"0\"\n  }\n}\n"
		if got := readString(t, settingsPath(root)); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		// Idempotent second call.
		addedRules, addedEnv, err = RegisterWithEnv(root, rules, envDefaults, policy(t, root))
		if err != nil || len(addedRules) != 0 || len(addedEnv) != 0 {
			t.Fatalf("second RegisterWithEnv = %v, %v, %v", addedRules, addedEnv, err)
		}
		// Deregister drops env when empty.
		_, removedEnv, err := DeregisterWithEnv(root, rules, envDefaults, policy(t, root))
		if err != nil || !slices.Equal(removedEnv, []string{"GIT_OPTIONAL_LOCKS=0"}) {
			t.Fatalf("DeregisterWithEnv = %v, %v", removedEnv, err)
		}
		wantAfter := "{\n  \"permissions\": {\n    \"allow\": [],\n    \"ask\": []\n  }\n}\n"
		if got := readString(t, settingsPath(root)); got != wantAfter {
			t.Errorf("after deregister got %q, want %q", got, wantAfter)
		}
	})
	t.Run("preserves other env keys and user override", func(t *testing.T) {
		root := t.TempDir()
		path := writeSettings(t, root, `{"env":{"FOO":"bar","GIT_OPTIONAL_LOCKS":"1"}}`)
		_, addedEnv, err := RegisterWithEnv(root, rules, envDefaults, policy(t, root))
		if err != nil {
			t.Fatalf("RegisterWithEnv: %v", err)
		}
		if len(addedEnv) != 0 {
			t.Errorf("override should not be overwritten: addedEnv = %v", addedEnv)
		}
		want := "{\n  \"env\": {\n    \"FOO\": \"bar\",\n    \"GIT_OPTIONAL_LOCKS\": \"1\"\n  },\n  \"permissions\": {\n    \"allow\": [\n      \"Bash(t:*)\",\n      \"mcp__t__*\"\n    ],\n    \"ask\": [\n      \"Bash(t admin:*)\"\n    ]\n  }\n}\n"
		if got := readString(t, path); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		_, removedEnv, err := DeregisterWithEnv(root, rules, envDefaults, policy(t, root))
		if err != nil || len(removedEnv) != 0 {
			t.Fatalf("DeregisterWithEnv must not remove user override: %v, %v", removedEnv, err)
		}
	})
	t.Run("deregister keeps other env keys", func(t *testing.T) {
		root := t.TempDir()
		path := writeSettings(t, root, `{"env":{"FOO":"bar","GIT_OPTIONAL_LOCKS":"0"}}`)
		_, removedEnv, err := DeregisterWithEnv(root, nil, envDefaults, policy(t, root))
		if err != nil || !slices.Equal(removedEnv, []string{"GIT_OPTIONAL_LOCKS=0"}) {
			t.Fatalf("DeregisterWithEnv = %v, %v", removedEnv, err)
		}
		want := "{\n  \"env\": {\n    \"FOO\": \"bar\"\n  }\n}\n"
		if got := readString(t, path); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("refuses malformed env", func(t *testing.T) {
		for name, content := range map[string]string{
			"env null":      `{"env":null}`,
			"env array":     `{"env":[]}`,
			"env value int": `{"env":{"GIT_OPTIONAL_LOCKS":0}}`,
			"env value nul": `{"env":{"GIT_OPTIONAL_LOCKS":null}}`,
			"env dup key":   `{"env":{"GIT_OPTIONAL_LOCKS":"0","GIT_OPTIONAL_LOCKS":"1"}}`,
		} {
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				path := writeSettings(t, root, content)
				if err := PreflightWithEnv(root, rules, envDefaults); !errors.Is(err, ErrRefused) {
					t.Fatalf("PreflightWithEnv err = %v, want ErrRefused", err)
				}
				if _, _, err := RegisterWithEnv(root, rules, envDefaults, policy(t, root)); !errors.Is(err, ErrRefused) {
					t.Fatalf("RegisterWithEnv err = %v, want ErrRefused", err)
				}
				if readString(t, path) != content {
					t.Error("refused file was changed")
				}
			})
		}
	})
}
