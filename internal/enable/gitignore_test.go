package enable

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The two runtime patterns enable must ensure a consumer repo ignores: the
// DES-058 live-session zone and the mission runtime locks.
const (
	liveZonePattern = ".punt-labs/**/local/**"
	missionLockPat  = ".punt-labs/ethos/missions/**/*.lock"
)

func TestEnableAppendsGitignoreRuntimeZones(t *testing.T) {
	dir := gitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Enable(dir); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	got := readFile(t, filepath.Join(dir, ".gitignore"))
	for _, want := range []string{liveZonePattern, missionLockPat} {
		if !strings.Contains(got, want) {
			t.Errorf(".gitignore missing runtime pattern %q; got:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "*.log") {
		t.Error("existing .gitignore content not preserved")
	}
}

func TestEnableGitignoreIdempotent(t *testing.T) {
	dir := gitRepo(t)
	if _, err := Enable(dir); err != nil {
		t.Fatalf("first Enable: %v", err)
	}
	if _, err := Enable(dir); err != nil {
		t.Fatalf("second Enable: %v", err)
	}
	got := readFile(t, filepath.Join(dir, ".gitignore"))
	if n := strings.Count(got, liveZonePattern); n != 1 {
		t.Errorf("live-zone pattern appears %d times, want 1; got:\n%s", n, got)
	}
	if n := strings.Count(got, missionLockPat); n != 1 {
		t.Errorf("mission-lock pattern appears %d times, want 1; got:\n%s", n, got)
	}
}

func TestEnableCreatesGitignoreWhenMissing(t *testing.T) {
	dir := gitRepo(t)
	if exists(filepath.Join(dir, ".gitignore")) {
		t.Fatal("test precondition: repo should have no .gitignore")
	}
	if _, err := Enable(dir); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !exists(filepath.Join(dir, ".gitignore")) {
		t.Fatal(".gitignore not created when absent")
	}
	got := readFile(t, filepath.Join(dir, ".gitignore"))
	for _, want := range []string{liveZonePattern, missionLockPat} {
		if !strings.Contains(got, want) {
			t.Errorf("created .gitignore missing runtime pattern %q; got:\n%s", want, got)
		}
	}
}
