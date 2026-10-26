package claudesettings

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

var rules = []string{"Bash(t:*)", "mcp__t__*"}

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
	added, err := Register(root, rules)
	if err != nil || !slices.Equal(added, rules) {
		t.Fatalf("Register = %v, %v", added, err)
	}
	want := "{\n  \"permissions\": {\n    \"allow\": [\n      \"Bash(t:*)\",\n      \"mcp__t__*\"\n    ]\n  }\n}\n"
	if got := readString(t, settingsPath(root)); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRegisterKeepsOrderAndOtherEntriesAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	path := writeSettings(t, root, `{"z":1,"permissions":{"deny":["X"],"allow":["Bash(a && b)"]},"a":{"k":true}}`)
	if _, err := Register(root, rules); err != nil {
		t.Fatal(err)
	}
	first := readString(t, path)
	added, err := Register(root, rules)
	if err != nil || len(added) != 0 {
		t.Fatalf("second Register = %v, %v", added, err)
	}
	if readString(t, path) != first {
		t.Error("idempotent Register rewrote the file")
	}
	want := "{\n  \"z\": 1,\n  \"permissions\": {\n    \"deny\": [\n      \"X\"\n    ],\n    \"allow\": [\n      \"Bash(a && b)\",\n      \"Bash(t:*)\",\n      \"mcp__t__*\"\n    ]\n  },\n  \"a\": {\n    \"k\": true\n  }\n}\n"
	if first != want {
		t.Errorf("got %q, want %q", first, want)
	}
}

func TestDeregisterRemovesExactlyTheOwnedRules(t *testing.T) {
	root := t.TempDir()
	path := writeSettings(t, root, `{"permissions":{"allow":["Read","Bash(t:*)","Bash(t:x)","mcp__t__*"]}}`)
	removed, err := Deregister(root, rules)
	if err != nil || !slices.Equal(removed, rules) {
		t.Fatalf("Deregister = %v, %v", removed, err)
	}
	want := "{\n  \"permissions\": {\n    \"allow\": [\n      \"Read\",\n      \"Bash(t:x)\"\n    ]\n  }\n}\n"
	if got := readString(t, path); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDeregisterWithoutAFileCreatesNothing(t *testing.T) {
	root := t.TempDir()
	if removed, err := Deregister(root, rules); err != nil || removed != nil {
		t.Fatalf("Deregister = %v, %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude")); !errors.Is(err, os.ErrNotExist) {
		t.Error(".claude/ created by a no-op disable")
	}
}

func TestUnparseableFilesAreRefusedUntouched(t *testing.T) {
	for name, content := range map[string]string{
		"malformed":   "{nope",
		"not object":  "[]",
		"allow type":  `{"permissions":{"allow":"Read"}}`,
		"allow items": `{"permissions":{"allow":[1]}}`,
		"invalid utf": "{\"a\":\"\xff\"}",
		"trailing":    "{} {}",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := writeSettings(t, root, content)
			if _, err := Register(root, rules); !errors.Is(err, ErrRefused) {
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
		if _, err := Register(root, rules); !errors.Is(err, ErrRefused) {
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
		if _, err := Register(root, rules); !errors.Is(err, ErrRefused) {
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
	if _, err := Register(root, rules); !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude")); !errors.Is(err, os.ErrNotExist) {
		t.Error(".claude/ created in the home directory")
	}
}
