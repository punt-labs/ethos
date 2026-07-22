//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCLI_SealLifecycleAcrossEnableDisable is the end-to-end cross-feature
// integration nobody had covered: it drives a REAL git commit through the
// chained pre-commit hook with the built ethos binary on PATH and asserts the
// seal's observable effect at each phase of the enable/disable lifecycle.
//
//	enabled + commit   → live audit lines seal into a tracked chunk
//	disabled + commit  → no new chunk, and the foreign host hook still runs
//	re-enabled + commit → the lines stranded while disabled seal on the next
//	                      commit (the seal unions the live tail past the
//	                      watermark — the operator-ratified strand semantics)
//
// A foreign host pre-commit that appends to a sentinel proves the host hook is
// preserved and keeps running across the whole lifecycle, never masked and
// never lost.
func TestCLI_SealLifecycleAcrossEnableDisable(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := enableSubprocessRepo(t)

	// The built binary, reachable as `ethos` on PATH for the commit subprocess
	// (the pre-commit gate resolves `ethos` from PATH first).
	binDir := filepath.Join(se.home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ethosBinary, filepath.Join(binDir, "ethos")); err != nil {
		t.Fatal(err)
	}
	commitEnv := []string{
		"HOME=" + se.home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"PATH=" + binDir + ":" + os.Getenv("PATH"),
	}

	// A foreign host pre-commit that records each run. enable chains the ethos
	// seal section after it; disable strips only that section, leaving the host.
	sentinel := filepath.Join(se.home, "host-ran")
	hostHook := "#!/bin/sh\necho ran >> " + shellQuote(sentinel) + "\n"
	hooksDir := filepath.Join(se.repo, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(hostHook), 0o755); err != nil {
		t.Fatal(err)
	}

	// Seed two live audit lines, dated so the sealed dir is 2026-07-22-<sid>.
	livePath := filepath.Join(se.repo, ".punt-labs", "local", "ethos", "sessions", "testsid.audit.jsonl")
	if err := os.MkdirAll(filepath.Dir(livePath), 0o700); err != nil {
		t.Fatal(err)
	}
	line := func(sec, nano int) string {
		return `{"ts":"2026-07-22T00:00:0` + itoa(sec) + `.00000000` + itoa(nano) + `Z","event":"test","seq":` + itoa(sec) + `}` + "\n"
	}
	if err := os.WriteFile(livePath, []byte(line(1, 1)+line(2, 2)), 0o600); err != nil {
		t.Fatal(err)
	}

	sealedGlob := filepath.Join(se.repo, ".punt-labs", "ethos", "sessions", "*", "audit-*.jsonl")

	// --- Phase 1: enabled + commit → the two live lines seal. ---
	if _, stderr, code := runCLI(t, se, "enable"); code != 0 {
		t.Fatalf("enable exit = %d; stderr: %s", code, stderr)
	}
	commit(t, se.repo, commitEnv, "work.txt", "one", "first")
	if got := hostRuns(t, sentinel); got != 1 {
		t.Errorf("host hook runs after phase 1 = %d, want 1", got)
	}
	afterSeal := countGlob(t, sealedGlob)
	if afterSeal != 1 {
		t.Fatalf("sealed chunks after an enabled commit = %d, want 1 — the seal did not fire", afterSeal)
	}

	// --- Phase 2: disabled + commit → no new chunk, host hook still runs. ---
	// Append a third live line while enabled-then-disabled: it is stranded past
	// the sealed watermark.
	appendFile(t, livePath, line(3, 3))
	if _, stderr, code := runCLI(t, se, "disable"); code != 0 {
		t.Fatalf("disable exit = %d; stderr: %s", code, stderr)
	}
	commit(t, se.repo, commitEnv, "work.txt", "two", "second")
	if got := hostRuns(t, sentinel); got != 2 {
		t.Errorf("host hook runs after phase 2 = %d, want 2 (host must still run when ethos is dormant)", got)
	}
	if got := countGlob(t, sealedGlob); got != afterSeal {
		t.Errorf("sealed chunks after a disabled commit = %d, want %d — a dormant repo must not seal", got, afterSeal)
	}

	// --- Phase 3: re-enabled + commit → the stranded line seals. ---
	if _, stderr, code := runCLI(t, se, "enable"); code != 0 {
		t.Fatalf("re-enable exit = %d; stderr: %s", code, stderr)
	}
	commit(t, se.repo, commitEnv, "work.txt", "three", "third")
	if got := hostRuns(t, sentinel); got != 3 {
		t.Errorf("host hook runs after phase 3 = %d, want 3", got)
	}
	if got := countGlob(t, sealedGlob); got <= afterSeal {
		t.Errorf("sealed chunks after re-enable = %d, want > %d — the stranded line did not seal on re-enable", got, afterSeal)
	}
}

// commit stages fileName with content and commits, requiring exit 0. The
// pre-commit hook runs before git snapshots the index, so any chunk the seal
// git-adds lands in this same commit.
func commit(t *testing.T, repo string, env []string, fileName, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, fileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunEnv(t, repo, env, "add", fileName)
	cmd := exec.Command("git", "-C", repo, "commit", "-q", "-m", msg)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit %q: %v\n%s", msg, err, out)
	}
}

// hostRuns counts how many times the foreign host hook appended to the sentinel.
func hostRuns(t *testing.T, sentinel string) int {
	t.Helper()
	data, err := os.ReadFile(sentinel)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	return strings.Count(string(data), "ran\n")
}

// countGlob returns the number of paths matching pattern.
func countGlob(t *testing.T, pattern string) int {
	t.Helper()
	m, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	return len(m)
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

// shellQuote single-quotes s for safe embedding in a POSIX sh script.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// itoa renders a single-digit int; the fixtures use only 1–9.
func itoa(n int) string {
	return string(rune('0' + n))
}
