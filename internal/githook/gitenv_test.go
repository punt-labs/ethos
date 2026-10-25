package githook

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitOutCLocale: git runs in the C locale, so its message is the English
// one Remedy and callers match, whatever the user locale.
func TestGitOutCLocale(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	probe := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	probe.Env = append(os.Environ(), "LC_ALL=de_DE.UTF-8", "LANGUAGE=de")
	if out, _ := probe.CombinedOutput(); strings.Contains(string(out), "not a git repository") {
		t.Skip("git has no German messages on this host")
	}
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("LANGUAGE", "de")
	_, err := gitOut(dir, "rev-parse", "--show-toplevel")
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("error = %v, want the English message", err)
	}
}

// TestHooksDirIgnoresExportedGitDir: a GIT_DIR exported by a hook or a shell
// does not make git answer for another repository.
func TestHooksDirIgnoresExportedGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	other := t.TempDir()
	gitInit(t, other)
	dir := t.TempDir()
	gitInit(t, dir)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	got, err := HooksDir(dir).Verified()
	if err != nil {
		t.Fatalf("Verified: %v", err)
	}
	if want := filepath.Join(dir, ".git", "hooks"); evalPath(got) != evalPath(want) {
		t.Errorf("hooks dir = %q, want %q", got, want)
	}
}
