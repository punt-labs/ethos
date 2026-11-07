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

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
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
		"ts":                 ts,
		"session":            sessionID,
		"tool":               tool,
		"tool_input_hash":    ts + "-" + tool,
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

func TestAuditMigrate_CopiesLocallyAndRetainsLegacy(t *testing.T) {
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

	// The original remains safe for a late older writer.
	_, statErr := os.Stat(legacyPath)
	assert.NoError(t, statErr)

	// Only the shared local archive is populated, never the record tree.
	local := filepath.Join(repo, ".punt-labs", "local", "ethos", "trace", "sessions", filepath.Base(repoSessDir), "audit.jsonl")
	data, err := os.ReadFile(local)
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(repoSessDir, "audit.jsonl"))
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

	// Retry against the same retained legacy source.

	stdout, _, err := execHandler(t, "audit", "migrate", "--verbose")
	require.NoError(t, err)
	assert.Contains(t, stdout, "noop sess-idem")

	// Repo audit file still has exactly one line.
	data, err := os.ReadFile(filepath.Join(repo, ".punt-labs", "local", "ethos", "trace", "sessions", filepath.Base(repoSessDir), "audit.jsonl"))
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
	// The suite's Git ceiling isolates approved scratch from the real checkout.
	nonRepo, home := t.TempDir(), t.TempDir()

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
	nonRepo, home := t.TempDir(), t.TempDir()

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

// trackMode says what the repo committed (with git add -f) before it began to
// ignore the session directory.
type trackMode int

const (
	trackNothing trackMode = iota
	trackChunk             // the chunk about to be quarantined
	trackSibling           // an older, healthy chunk beside it
)

// quarantineOutput quarantines a corrupt session chunk in a fresh repo whose
// .gitignore is ignore, with its lines still in the live file, and returns what
// the command printed.
func quarantineOutput(t *testing.T, ignore string, track trackMode) string {
	t.Helper()
	_, repo, _ := auditMigrateEnv(t)
	dir := filepath.Join(repo, ".punt-labs", "ethos", "sessions", "2026-01-01-sq")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	chunk := filepath.Join(dir, audit.SessionChunkFile(100, 200))
	require.NoError(t, os.WriteFile(chunk, []byte("not json\n"), 0o600))
	if track == trackSibling {
		sib := filepath.Join(dir, audit.SessionChunkFile(10, 20))
		body := `{"ts":"` + audit.FormatLineTS(10) + `","session":"sq","tool":"Read"}` + "\n" +
			`{"ts":"` + audit.FormatLineTS(20) + `","session":"sq","tool":"Read"}` + "\n"
		require.NoError(t, os.WriteFile(sib, []byte(body), 0o600))
	}
	if track != trackNothing {
		dirRel := filepath.Join(".punt-labs", "ethos", "sessions")
		if track == trackSibling {
			dirRel = filepath.Join(dirRel, "2026-01-01-sq", audit.SessionChunkFile(10, 20))
		}
		for _, args := range [][]string{{"add", "-f", dirRel}, {"-c", "user.email=t@e.com", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-m", "chunk"}} {
			cmd := testhelpers.GitCommand(args...)
			cmd.Dir = repo
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "git %v: %s", args, out)
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(ignore), 0o600))
	var live strings.Builder
	for _, ts := range []int64{100, 200} {
		live.WriteString(`{"ts":"` + audit.FormatLineTS(ts) + `","session":"sq","tool":"Read"}` + "\n")
	}
	livePath := audit.LiveAuditPath(repo, "sq")
	require.NoError(t, os.MkdirAll(filepath.Dir(livePath), 0o700))
	require.NoError(t, os.WriteFile(livePath, []byte(live.String()), 0o600))
	index := filepath.Join(repo, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		require.ErrorIs(t, err, os.ErrNotExist)
	}

	stdout, _, err := execHandler(t, "audit", "quarantine", chunk)
	require.NoError(t, err)
	after, err := os.ReadFile(index)
	if err != nil {
		require.ErrorIs(t, err, os.ErrNotExist)
	}
	assert.Equal(t, before, after, "quarantine preserves the actual index in every tracking/ignore state")
	return stdout
}

func TestAuditQuarantine_IgnoredSessionRemainsLocal(t *testing.T) {
	out := quarantineOutput(t, ".punt-labs/ethos/sessions/\n", trackNothing)
	assert.Contains(t, out, "retained in the shared local archive; nothing staged")
}

// quarantineStderr is quarantineOutput returning the notices on stderr.
func quarantineStderr(t *testing.T, ignore string, track trackMode) string {
	t.Helper()
	return testhelpers.CaptureStderr(t, func() { quarantineOutput(t, ignore, track) })
}

func TestAuditQuarantine_IgnoredSessionDoesNotClaimTrackedRetirement(t *testing.T) {
	notice := quarantineStderr(t, ".punt-labs/ethos/sessions/\n", trackNothing)
	assert.NotContains(t, notice, "withheld from the tracked tree")
}

func TestAuditQuarantine_UnignoredSessionDoesNotClaimTrackedRetirement(t *testing.T) {
	notice := quarantineStderr(t, "unrelated/\n", trackNothing)
	assert.NotContains(t, notice, "withheld from the tracked tree")
}

func TestAuditQuarantine_UnignoredSessionRemainsLocal(t *testing.T) {
	out := quarantineOutput(t, "unrelated/\n", trackNothing)
	assert.Contains(t, out, "retained in the shared local archive; nothing staged")
}

// Historical tracking state never turns local quarantine into publication.
func TestAuditQuarantine_TrackedChunkInIgnoredDirDoesNotStage(t *testing.T) {
	var out string
	notice := testhelpers.CaptureStderr(t, func() {
		out = quarantineOutput(t, ".punt-labs/ethos/sessions/\n", trackChunk)
	})
	assert.Contains(t, out, "retained in the shared local archive; nothing staged")
	assert.NotContains(t, out, "git add -f")
	assert.NotContains(t, notice, "withheld from the tracked tree")
}

func TestAuditQuarantine_TrackedSiblingDoesNotStageLocalRecovery(t *testing.T) {
	out := quarantineOutput(t, ".punt-labs/ethos/sessions/\n", trackSibling)
	assert.Contains(t, out, "retained in the shared local archive; nothing staged")
}

// requarantineOutput quarantines a corrupt chunk (its lines still live), then
// damages the re-sealed chunk that now stands at the same name and quarantines
// again under the existing marker. Both damaged originals must survive under
// distinct digests, with published-availability diagnostics kept visible.
// commitBetween commits only unrelated source.
func requarantineOutput(t *testing.T, commitBetween bool) string {
	t.Helper()
	_, repo, _ := auditMigrateEnv(t)
	dir := filepath.Join(repo, ".punt-labs", "ethos", "sessions", "2026-01-01-sq")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	chunk := filepath.Join(dir, audit.SessionChunkFile(100, 200))
	require.NoError(t, os.WriteFile(chunk, []byte("not json\n"), 0o600))
	var live strings.Builder
	for _, ts := range []int64{100, 200} {
		live.WriteString(`{"ts":"` + audit.FormatLineTS(ts) + `","session":"sq","tool":"Read"}` + "\n")
	}
	livePath := audit.LiveAuditPath(repo, "sq")
	require.NoError(t, os.MkdirAll(filepath.Dir(livePath), 0o700))
	require.NoError(t, os.WriteFile(livePath, []byte(live.String()), 0o600))

	_, _, err := execHandler(t, "audit", "quarantine", chunk)
	require.NoError(t, err)
	if commitBetween {
		require.NoError(t, os.WriteFile(filepath.Join(repo, "source"), []byte("fixture\n"), 0o600))
		for _, args := range [][]string{{"add", "--", "source"}, {"-c", "user.email=t@e.com", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-m", "first"}} {
			cmd := testhelpers.GitCommand(args...)
			cmd.Dir = repo
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "git %v: %s", args, out)
		}
	}
	require.NoError(t, os.WriteFile(chunk, []byte("damaged again\n"), 0o600))
	var stderr string
	notice := testhelpers.CaptureStderr(t, func() {
		_, stderr, err = execHandler(t, "audit", "quarantine", chunk)
	})
	require.NoError(t, err)
	assert.NoFileExists(t, chunk, "the explicitly selected publication is retired")
	files, err := filepath.Glob(filepath.Join(repo, ".punt-labs", "local", "ethos", "quarantine", "*", "*.corrupt"))
	require.NoError(t, err)
	require.Len(t, files, 2)
	seen := map[string]bool{}
	for _, path := range files {
		body, err := os.ReadFile(path)
		require.NoError(t, err)
		seen[string(body)] = true
	}
	assert.True(t, seen["not json\n"])
	assert.True(t, seen["damaged again\n"])
	marker, err := audit.ReadMarker(strings.TrimSuffix(chunk, ".jsonl") + ".quarantine")
	require.NoError(t, err)
	assert.True(t, marker.HasGap(), "local recovery does not prove published availability")
	assert.Equal(t, int64(100), marker.UnrecoveredFirst)
	assert.Equal(t, int64(200), marker.UnrecoveredLast)
	return stderr + notice
}

func TestAuditQuarantine_RerunAfterSourceCommitReportsFreshDamage(t *testing.T) {
	out := requarantineOutput(t, true)
	assert.Contains(t, out, "SHARED REPAIR PENDING")
}

func TestAuditQuarantine_RerunWithoutCommitReportsFreshDamage(t *testing.T) {
	out := requarantineOutput(t, false)
	assert.Contains(t, out, "SHARED REPAIR PENDING")
}

// Quarantining one chunk does not hide independent damage in another, even
// inside its nominal range. Manual sealing still reports that damaged source.
func TestAuditQuarantine_DoesNotHideOtherDamagedChunk(t *testing.T) {
	_, repo, _ := auditMigrateEnv(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".punt-labs/ethos/sessions/\n"), 0o600))
	dir := filepath.Join(repo, ".punt-labs", "ethos", "sessions", "2026-01-01-sq")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	chunk := filepath.Join(dir, audit.SessionChunkFile(100, 200))
	require.NoError(t, os.WriteFile(chunk, []byte("not json\n"), 0o600))
	var live strings.Builder
	for _, ts := range []int64{100, 200} {
		live.WriteString(`{"ts":"` + audit.FormatLineTS(ts) + `","session":"sq","tool":"Read"}` + "\n")
	}
	livePath := audit.LiveAuditPath(repo, "sq")
	require.NoError(t, os.MkdirAll(filepath.Dir(livePath), 0o700))
	require.NoError(t, os.WriteFile(livePath, []byte(live.String()), 0o600))
	_, _, err := execHandler(t, "audit", "quarantine", chunk)
	require.NoError(t, err)

	// A tracked chunk inside the marker's range, damaged since.
	other := filepath.Join(dir, audit.SessionChunkFile(110, 120))
	good := `{"ts":"` + audit.FormatLineTS(110) + `","session":"sq","tool":"Read"}` + "\n" +
		`{"ts":"` + audit.FormatLineTS(120) + `","session":"sq","tool":"Read"}` + "\n"
	require.NoError(t, os.WriteFile(other, []byte(good), 0o600))
	for _, args := range [][]string{{"add", "-f", other}, {"-c", "user.email=t@e.com", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-m", "other"}} {
		cmd := testhelpers.GitCommand(args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	require.NoError(t, os.WriteFile(other, []byte("damaged\n"), 0o600))

	stdout, _, err := execHandler(t, "audit", "quarantine", chunk)
	require.NoError(t, err)
	assert.Contains(t, stdout, "retained in the shared local archive; nothing staged")
	_, stderr, err := execHandler(t, "audit", "seal")
	require.Error(t, err)
	assert.Contains(t, stderr, other)
}
