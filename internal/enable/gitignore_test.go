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

func TestEnableGitignoreIndentedNearDuplicateNotCoverage(t *testing.T) {
	dir := gitRepo(t)
	// Leading whitespace is significant in .gitignore: these indented lines
	// match indented paths, not the real runtime files. The presence check must
	// not treat them as coverage.
	initial := "  " + liveZonePattern + "\n  " + missionLockPat + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Enable(dir); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	got := readFile(t, filepath.Join(dir, ".gitignore"))
	for _, want := range []string{liveZonePattern, missionLockPat} {
		if !hasExactLine(got, want) {
			t.Errorf("unindented runtime pattern %q not appended; got:\n%s", want, got)
		}
	}
}

// hasExactLine reports whether s contains want as a complete line.
func hasExactLine(s, want string) bool {
	for _, line := range strings.Split(s, "\n") {
		if line == want {
			return true
		}
	}
	return false
}

func TestEnableWarnsOnAlreadyTrackedRuntimeFiles(t *testing.T) {
	dir := gitRepo(t)
	rel := ".punt-labs/local/ethos/sessions/s1.audit.jsonl"
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rel), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The repo committed the runtime file before enabling — the exact state the
	// guard targets. The .gitignore cannot untrack it.
	gitRun(t, dir, "add", rel)
	gitRun(t, dir, "commit", "-q", "-m", "runtime file tracked before enable")

	rep, err := Enable(dir)
	if err != nil {
		t.Fatalf("Enable: %v", err)
	}
	var warn string
	for _, w := range rep.Warnings {
		if strings.Contains(w, "already git-tracked") {
			warn = w
		}
	}
	if warn == "" {
		t.Fatalf("expected an already-tracked warning; warnings=%v", rep.Warnings)
	}
	if !strings.Contains(warn, rel) {
		t.Errorf("warning does not name the tracked file %q: %s", rel, warn)
	}
	// The remedy must be copy-pasteable: the -- separator and the shell-quoted
	// path guard against paths with spaces or special characters.
	if !strings.Contains(warn, "git rm -r --cached -- '"+rel+"'") {
		t.Errorf("warning remedy is not shell-safe (missing -- or quoting): %s", warn)
	}
}

func TestEnableGitignorePreservesMode(t *testing.T) {
	dir := gitRepo(t)
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, []byte("*.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Enable(dir); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600 (existing mode preserved)", got)
	}
	got := readFile(t, path)
	if !strings.HasPrefix(got, "*.log\n") {
		t.Errorf("existing content not preserved; got:\n%s", got)
	}
	if !strings.Contains(got, liveZonePattern) || !strings.Contains(got, missionLockPat) {
		t.Errorf("runtime patterns not appended; got:\n%s", got)
	}
}

func TestEnableGitignoreSingleMarkerBlock(t *testing.T) {
	dir := gitRepo(t)
	// A prior enable's block with one pattern removed but the marker kept. The
	// missing pattern must go under the SAME marker, not a second block.
	initial := gitignoreMarker + "\n" + missionLockPat + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Enable(dir); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	got := readFile(t, filepath.Join(dir, ".gitignore"))
	if n := strings.Count(got, gitignoreMarker); n != 1 {
		t.Errorf("marker comment appears %d times, want 1 (single block); got:\n%s", n, got)
	}
	if !strings.Contains(got, liveZonePattern) {
		t.Errorf("missing pattern not added under existing marker; got:\n%s", got)
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
