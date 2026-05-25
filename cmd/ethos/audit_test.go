//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// auditMigrateEnv stages a fake $HOME with a global sessions dir and
// a separate git-init repo. Returns the home dir, repo dir, and
// global sessions dir. Tests stage legacy files in globalDir and
// repo-tree session dirs under <repo>/.ethos/sessions.
func auditMigrateEnv(t *testing.T) (home, repo, globalDir string) {
	t.Helper()
	home = t.TempDir()
	repo = filepath.Join(home, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	gitInitDir(t, repo, home)

	globalDir = filepath.Join(home, ".punt-labs", "ethos", "sessions")
	require.NoError(t, os.MkdirAll(globalDir, 0o700))

	t.Setenv("HOME", home)
	t.Setenv("USER", "test-agent")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Chdir(repo)

	// Reset command-level flag state.
	auditMigrateDryRun = false
	auditMigrateVerbose = false
	t.Cleanup(func() {
		auditMigrateDryRun = false
		auditMigrateVerbose = false
	})
	return home, repo, globalDir
}

// writeLegacyLine appends a one-line JSONL entry shaped like a v3.11
// audit log into <globalDir>/<sessionID>.audit.jsonl.
func writeLegacyLine(t *testing.T, globalDir, sessionID, ts, tool string) string {
	t.Helper()
	path := filepath.Join(globalDir, sessionID+".audit.jsonl")
	line := map[string]any{
		"ts":               ts,
		"session":          sessionID,
		"tool":             tool,
		"tool_input_hash":  ts + "-" + tool,
		"tool_input_preview": "preview",
	}
	data, err := json.Marshal(line)
	require.NoError(t, err)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	require.NoError(t, err)
	return path
}

func TestAuditMigrate_NoLegacyFiles_Plain(t *testing.T) {
	auditMigrateEnv(t)

	stdout, _, err := execHandler(t, "audit", "migrate")
	require.NoError(t, err)
	assert.Contains(t, stdout, "audit migrate: complete")
}

func TestAuditMigrate_NoLegacyFiles_Verbose(t *testing.T) {
	auditMigrateEnv(t)

	stdout, _, err := execHandler(t, "audit", "migrate", "--verbose")
	require.NoError(t, err)
	assert.Contains(t, stdout, "nothing to migrate")
}

func TestAuditMigrate_CopiesAndDeletesLegacy(t *testing.T) {
	_, repo, globalDir := auditMigrateEnv(t)

	sessID := "sess-mig"
	legacyPath := writeLegacyLine(t, globalDir, sessID, "2026-05-22T10:00:00Z", "Bash")
	writeLegacyLine(t, globalDir, sessID, "2026-05-22T10:00:01Z", "Read")
	repoSessDir := filepath.Join(repo, ".punt-labs", "ethos", "sessions", "2026-05-22-"+sessID)
	require.NoError(t, os.MkdirAll(repoSessDir, 0o700))

	stdout, _, err := execHandler(t, "audit", "migrate", "--verbose")
	require.NoError(t, err)
	assert.Contains(t, stdout, "migrate sess-mig")
	assert.Contains(t, stdout, "2 new entries")

	// Legacy file gone.
	_, statErr := os.Stat(legacyPath)
	assert.True(t, os.IsNotExist(statErr))

	// Repo audit file populated.
	data, err := os.ReadFile(filepath.Join(repoSessDir, "audit.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(data), `"tool":"Bash"`)
	assert.Contains(t, string(data), `"tool":"Read"`)
}

func TestAuditMigrate_DryRunMakesNoChanges(t *testing.T) {
	_, repo, globalDir := auditMigrateEnv(t)

	sessID := "sess-dry"
	legacyPath := writeLegacyLine(t, globalDir, sessID, "2026-05-22T10:00:00Z", "Bash")
	repoSessDir := filepath.Join(repo, ".punt-labs", "ethos", "sessions", "2026-05-22-"+sessID)
	require.NoError(t, os.MkdirAll(repoSessDir, 0o700))

	stdout, _, err := execHandler(t, "audit", "migrate", "--dry-run")
	require.NoError(t, err)
	assert.Contains(t, stdout, "dry-run complete")

	// Legacy file untouched.
	_, statErr := os.Stat(legacyPath)
	require.NoError(t, statErr)

	// Repo audit file not created.
	_, statErr = os.Stat(filepath.Join(repoSessDir, "audit.jsonl"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestAuditMigrate_CrossRepoSessionLeftAlone(t *testing.T) {
	_, _, globalDir := auditMigrateEnv(t)

	// Legacy session has no repo-tree dir — belongs to a different
	// repo. Migrate must leave it in place.
	otherID := "sess-other"
	legacyPath := writeLegacyLine(t, globalDir, otherID, "2026-05-22T10:00:00Z", "Bash")

	stdout, _, err := execHandler(t, "audit", "migrate", "--verbose")
	require.NoError(t, err)
	assert.Contains(t, stdout, "skip sess-other")
	assert.Contains(t, stdout, "no repo-tree session")

	_, statErr := os.Stat(legacyPath)
	require.NoError(t, statErr, "cross-repo legacy file must survive")
}

func TestAuditMigrate_IdempotentSecondRun(t *testing.T) {
	_, repo, globalDir := auditMigrateEnv(t)

	sessID := "sess-idem"
	writeLegacyLine(t, globalDir, sessID, "2026-05-22T10:00:00Z", "Bash")
	repoSessDir := filepath.Join(repo, ".punt-labs", "ethos", "sessions", "2026-05-22-"+sessID)
	require.NoError(t, os.MkdirAll(repoSessDir, 0o700))

	// First migrate.
	_, _, err := execHandler(t, "audit", "migrate")
	require.NoError(t, err)

	// Stage a duplicate legacy file (same ts + tool) — simulates a
	// re-run with stale state.
	writeLegacyLine(t, globalDir, sessID, "2026-05-22T10:00:00Z", "Bash")

	stdout, _, err := execHandler(t, "audit", "migrate", "--verbose")
	require.NoError(t, err)
	assert.Contains(t, stdout, "noop sess-idem")

	// Repo audit file still has exactly one line.
	data, err := os.ReadFile(filepath.Join(repoSessDir, "audit.jsonl"))
	require.NoError(t, err)
	count := 0
	for _, b := range data {
		if b == '\n' {
			count++
		}
	}
	assert.Equal(t, 1, count, "second run must not duplicate entries")
}

func TestAuditMigrate_OutsideRepo_ExitsUsage(t *testing.T) {
	// t.TempDir uses TMPDIR which under .envrc points into the
	// project's .tmp/, so FindRepoRoot would walk up and find the
	// project's own .git. Allocate under /tmp explicitly so the walk
	// returns empty.
	nonRepo, err := os.MkdirTemp("/tmp", "ethos-audit-norepo-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(nonRepo) })
	home, err := os.MkdirTemp("/tmp", "ethos-audit-home-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(home) })

	t.Setenv("HOME", home)
	t.Setenv("USER", "test-agent")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Chdir(nonRepo)
	t.Cleanup(func() {
		auditMigrateDryRun = false
		auditMigrateVerbose = false
	})

	_, stderr, err := execHandler(t, "audit", "migrate")
	require.Error(t, err)
	_, isUsage := err.(usageError)
	assert.True(t, isUsage, "expected usageError, got %T: %v", err, err)
	assert.Contains(t, stderr, "must run inside a repo")
}

// auditShowEnv stages a repo-tree + global-sessions layout for the
// `audit show` CLI tests. Mirrors auditMigrateEnv but resets show
// flags instead of migrate flags.
func auditShowEnv(t *testing.T) (home, repo, globalDir string) {
	t.Helper()
	home = t.TempDir()
	repo = filepath.Join(home, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	gitInitDir(t, repo, home)

	globalDir = filepath.Join(home, ".punt-labs", "ethos", "sessions")
	require.NoError(t, os.MkdirAll(globalDir, 0o700))

	t.Setenv("HOME", home)
	t.Setenv("USER", "test-agent")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Chdir(repo)

	auditShowDelegation = ""
	auditShowFormat = "json"
	// Cobra remembers flag.Changed across in-process invocations, so a
	// later test that omits --delegation would still see Changed=true
	// from a prior test. Reset both Changed flags here and on cleanup.
	resetAuditShowFlagState()
	t.Cleanup(func() {
		auditShowDelegation = ""
		auditShowFormat = "json"
		resetAuditShowFlagState()
	})
	return home, repo, globalDir
}

// resetAuditShowFlagState clears cobra's per-invocation flag state for
// the show command so tests that share the in-process rootCmd see a
// fresh required-flag check.
func resetAuditShowFlagState() {
	for _, name := range []string{"delegation", "format"} {
		if f := auditShowCmd.Flags().Lookup(name); f != nil {
			f.Changed = false
		}
	}
}

// writeRepoTreeAudit appends one JSONL line to
// <repo>/.ethos/sessions/<date>-<id>/audit.jsonl shaped like the
// v3.12 entry, including the delegation_id and tool_input fields the
// show command renders.
func writeRepoTreeAudit(t *testing.T, repo, date, sessionID string, entry map[string]any) {
	t.Helper()
	dir := filepath.Join(repo, ".punt-labs", "ethos", "sessions", date+"-"+sessionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	path := filepath.Join(dir, "audit.jsonl")
	data, err := json.Marshal(entry)
	require.NoError(t, err)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	require.NoError(t, err)
}

func TestAuditShow_JSONFormat(t *testing.T) {
	_, repo, _ := auditShowEnv(t)

	writeRepoTreeAudit(t, repo, "2026-05-22", "sess-a", map[string]any{
		"ts":            "2026-05-22T10:00:00Z",
		"session":       "sess-a",
		"tool":          "Read",
		"delegation_id": "d-007",
		"tool_input":    map[string]any{"file_path": "/repo/foo.go"},
	})
	writeRepoTreeAudit(t, repo, "2026-05-22", "sess-a", map[string]any{
		"ts":            "2026-05-22T10:00:01Z",
		"session":       "sess-a",
		"tool":          "Edit",
		"delegation_id": "d-007",
		"tool_input":    map[string]any{"file_path": "/repo/bar.go"},
	})
	// Different delegation — must not appear.
	writeRepoTreeAudit(t, repo, "2026-05-22", "sess-a", map[string]any{
		"ts":            "2026-05-22T10:00:02Z",
		"session":       "sess-a",
		"tool":          "Write",
		"delegation_id": "d-008",
		"tool_input":    map[string]any{"file_path": "/repo/baz.go"},
	})

	stdout, _, err := execHandler(t, "audit", "show", "--delegation", "d-007")
	require.NoError(t, err)

	lines := splitNonEmpty(stdout)
	require.Len(t, lines, 2, "want exactly two JSONL lines, got %q", stdout)
	var first map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	assert.Equal(t, "Read", first["tool"])
	assert.Equal(t, "d-007", first["delegation_id"])
}

func TestAuditShow_TextFormat(t *testing.T) {
	_, repo, _ := auditShowEnv(t)

	writeRepoTreeAudit(t, repo, "2026-05-22", "sess-a", map[string]any{
		"ts":            "2026-05-22T10:00:00Z",
		"session":       "sess-a",
		"tool":          "Read",
		"delegation_id": "d-007",
		"tool_input":    map[string]any{"file_path": "/repo/foo.go"},
	})
	writeRepoTreeAudit(t, repo, "2026-05-22", "sess-a", map[string]any{
		"ts":                 "2026-05-22T10:00:01Z",
		"session":            "sess-a",
		"tool":               "Bash",
		"delegation_id":      "d-007",
		"tool_input_preview": `{"command":"ls"}`,
	})

	stdout, _, err := execHandler(t, "audit", "show", "--delegation", "d-007", "--format", "text")
	require.NoError(t, err)

	lines := splitNonEmpty(stdout)
	require.Len(t, lines, 2)
	assert.Equal(t, "2026-05-22T10:00:00Z\tRead\t/repo/foo.go", lines[0])
	assert.Equal(t, "2026-05-22T10:00:01Z\tBash\t"+`{"command":"ls"}`, lines[1])
}

func TestAuditShow_LegacyFallback(t *testing.T) {
	_, _, globalDir := auditShowEnv(t)

	// Repo tree empty; legacy session carries a matching entry.
	writeLegacyShowAudit(t, globalDir, "sess-legacy", map[string]any{
		"ts":            "2026-05-20T08:00:00Z",
		"session":       "sess-legacy",
		"tool":          "Bash",
		"delegation_id": "d-legacy",
		"tool_input":    map[string]any{"file_path": "/repo/old.go"},
	})

	stdout, _, err := execHandler(t, "audit", "show", "--delegation", "d-legacy", "--format", "text")
	require.NoError(t, err)
	assert.Contains(t, stdout, "sess-legacy\tBash"[len("sess-legacy\t"):])
	assert.Contains(t, stdout, "/repo/old.go")
}

func TestAuditShow_NoMatches(t *testing.T) {
	_, repo, _ := auditShowEnv(t)

	writeRepoTreeAudit(t, repo, "2026-05-22", "sess-a", map[string]any{
		"ts":            "2026-05-22T10:00:00Z",
		"session":       "sess-a",
		"tool":          "Read",
		"delegation_id": "d-007",
	})

	stdout, _, err := execHandler(t, "audit", "show", "--delegation", "d-missing")
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(stdout))
}

func TestAuditShow_MissingDelegation(t *testing.T) {
	auditShowEnv(t)

	_, _, err := execHandler(t, "audit", "show")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required flag")
}

func TestAuditShow_InvalidFormat(t *testing.T) {
	auditShowEnv(t)

	_, stderr, err := execHandler(t, "audit", "show", "--delegation", "d-007", "--format", "yaml")
	require.Error(t, err)
	_, isUsage := err.(usageError)
	assert.True(t, isUsage, "expected usageError, got %T: %v", err, err)
	assert.Contains(t, stderr, "--format must be json or text")
}

func TestAuditShow_OutsideRepo_ExitsUsage(t *testing.T) {
	// Like the migrate variant: use /tmp so FindRepoRoot returns empty.
	nonRepo, err := os.MkdirTemp("/tmp", "ethos-audit-show-norepo-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(nonRepo) })
	home, err := os.MkdirTemp("/tmp", "ethos-audit-show-home-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(home) })

	t.Setenv("HOME", home)
	t.Setenv("USER", "test-agent")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Chdir(nonRepo)
	t.Cleanup(func() {
		auditShowDelegation = ""
		auditShowFormat = "json"
	})

	_, stderr, err := execHandler(t, "audit", "show", "--delegation", "d-007")
	require.Error(t, err)
	_, isUsage := err.(usageError)
	assert.True(t, isUsage, "expected usageError, got %T: %v", err, err)
	assert.Contains(t, stderr, "must run inside a repo")
}

// writeLegacyShowAudit appends a v3.11 single-tree audit line at
// <globalDir>/<sessionID>.audit.jsonl. Mirrors writeLegacyLine but
// takes a full map so the test can stage delegation_id and tool_input.
func writeLegacyShowAudit(t *testing.T, globalDir, sessionID string, entry map[string]any) {
	t.Helper()
	path := filepath.Join(globalDir, sessionID+".audit.jsonl")
	data, err := json.Marshal(entry)
	require.NoError(t, err)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	require.NoError(t, err)
}

// splitNonEmpty splits s on '\n' and drops empty lines.
func splitNonEmpty(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
