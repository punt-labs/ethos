package hook

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

// leakyReason is an error string as an I/O failure produces it: an
// absolute path under the home directory, an address, and a credential.
func leakyReason(home string) (reason string, raw []string) {
	secret := "ghp_" + strings.Repeat("a1B2", 9)
	return "open " + filepath.Join(home, "work", "x.jsonl") + ": denied for jane@example.com " + secret,
		[]string{home, "jane@example.com", secret}
}

func TestEmitAuditSentinel_RedactsReason(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	reason, raw := leakyReason(home)
	repo := t.TempDir()
	sessionID := "sess-r"
	sealedDir := filepath.Join(repo, ".punt-labs", "ethos", "sessions", "2026-07-21-"+sessionID)
	livePath := audit.LiveAuditPath(repo, sessionID)
	require.NoError(t, os.MkdirAll(filepath.Dir(livePath), 0o700))

	require.NoError(t, emitAuditSentinel(livePath, sealedDir, filepath.Join(sealedDir, "audit.jsonl"),
		sessionID, time.Now().UTC(), reason))

	data, err := os.ReadFile(livePath)
	require.NoError(t, err)
	for _, r := range raw {
		assert.NotContains(t, string(data), r)
	}
	assert.Contains(t, string(data), "~/work/x.jsonl")
	assert.Contains(t, string(data), "audit_error")
}

func TestEmitLegacySentinel_RedactsReason(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	reason, raw := leakyReason(home)
	path := filepath.Join(t.TempDir(), "s.audit.jsonl")
	require.NoError(t, emitLegacySentinel(path, "sess-x", "2026-05-22T10:00:00Z", reason))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, r := range raw {
		assert.NotContains(t, string(data), r)
	}
}

// TestSentinels_UnusableHomeStillRecordTheLoss: the audit writer never
// blocks (audit_entry.go redactToolInput), so with no redactor the loss
// marker is written with a placeholder reason, not refused and not raw.
func TestSentinels_UnusableHomeStillRecordTheLoss(t *testing.T) {
	t.Setenv("HOME", "/")
	path := filepath.Join(t.TempDir(), "s.audit.jsonl")
	require.NoError(t, emitLegacySentinel(path, "sess-x", "2026-05-22T10:00:00Z", "open /home/alice/x: denied"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var got map[string]string
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(data), &got))
	assert.Equal(t, "[redacted]", got["audit_error"])
}

// legacyToolInput builds a legacy audit line the way an older ethos wrote
// it: tool_input unredacted, hash and preview over that unredacted form.
func legacyLine(t *testing.T, session, ts string, ti map[string]any) []byte {
	t.Helper()
	env := map[string]any{"tool_input": ti}
	b, err := json.Marshal(map[string]any{
		"ts": ts, "session": session, "tool": "Bash",
		"tool_input": ti, "tool_input_hash": hashToolInput(env), "tool_input_preview": toolInputPreview(env),
	})
	require.NoError(t, err)
	return append(b, '\n')
}

func TestMigrateAudit_RedactsLegacyLines(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()
	sessionDir := repoSessionDir(t, repoRoot, "2026-05-22", "sess-1")
	require.NoError(t, os.MkdirAll(globalDir, 0o700))

	secret := "ghp_" + strings.Repeat("a1B2", 9)
	leaky := map[string]any{"command": "cd " + filepath.Join(home, "work") + " && GITHUB_TOKEN=" + secret + " mail jane@example.com"}
	cleanTI := map[string]any{"command": "ls"}
	stage := func() {
		body := append(legacyLine(t, "sess-1", "2026-05-22T10:00:00Z", leaky),
			legacyLine(t, "sess-1", "2026-05-22T10:00:01Z", cleanTI)...)
		require.NoError(t, os.WriteFile(filepath.Join(globalDir, "sess-1.audit.jsonl"), body, 0o600))
	}
	stage()

	var out bytes.Buffer
	require.NoError(t, MigrateAudit(globalDir, repoRoot, false, &out))

	data, err := os.ReadFile(filepath.Join(sessionDir, "audit.jsonl"))
	require.NoError(t, err)
	for _, r := range []string{home, secret, "jane@example.com"} {
		assert.NotContains(t, string(data), r)
	}
	entries := readRepoAuditEntries(t, sessionDir)
	require.Len(t, entries, 2)
	assert.True(t, entries[0].Redacted)
	assert.Equal(t, hashToolInput(map[string]any{"tool_input": entries[0].ToolInput}), entries[0].ToolInputHash,
		"the hash follows the stored (redacted) tool_input, DES-054")
	assert.Equal(t, toolInputPreview(map[string]any{"tool_input": entries[0].ToolInput}), entries[0].ToolInputPreview)
	assert.False(t, entries[1].Redacted)
	assert.Contains(t, string(data), `"tool_input_hash":"`+hashToolInput(map[string]any{"tool_input": cleanTI}), "an unaffected line keeps its hash")

	// Re-running over the same legacy content dedupes against the redacted
	// lines already migrated.
	stage()
	out.Reset()
	require.NoError(t, MigrateAudit(globalDir, repoRoot, false, &out))
	assert.Contains(t, out.String(), "noop sess-1: already migrated")
	assert.Len(t, readRepoAuditEntries(t, sessionDir), 2)
}

func TestMigrateAudit_RefusesWithoutRedactor(t *testing.T) {
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()
	sessionDir := repoSessionDir(t, repoRoot, "2026-05-22", "sess-1")
	require.NoError(t, os.MkdirAll(globalDir, 0o700))
	legacy := filepath.Join(globalDir, "sess-1.audit.jsonl")
	require.NoError(t, os.WriteFile(legacy, legacyLine(t, "sess-1", "2026-05-22T10:00:00Z", map[string]any{"command": "x"}), 0o600))

	t.Setenv("HOME", "/")
	var out bytes.Buffer
	require.Error(t, MigrateAudit(globalDir, repoRoot, false, &out), "migration is an operator command and refuses")
	_, err := os.Stat(filepath.Join(sessionDir, "audit.jsonl"))
	assert.True(t, os.IsNotExist(err))
	// The legacy file stays, under its staging name; the next run resumes from it.
	_, err = os.Stat(legacy + stagedSuffix)
	assert.NoError(t, err, "legacy file stays, staged")
}

// TestMigrateAudit_PathOnlyLegacyLineIsRedacted: a legacy line whose only
// leak is the home path has no content to remove, so the content policy
// reports no change; the path rewrite alone must still reach the file.
func TestMigrateAudit_PathOnlyLegacyLineIsRedacted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()
	sessionDir := repoSessionDir(t, repoRoot, "2026-05-22", "sess-1")
	require.NoError(t, os.MkdirAll(globalDir, 0o700))

	ti := map[string]any{"command": "cat " + filepath.Join(home, "notes.txt")}
	require.NoError(t, os.WriteFile(filepath.Join(globalDir, "sess-1.audit.jsonl"),
		legacyLine(t, "sess-1", "2026-05-22T10:00:00Z", ti), 0o600))

	var out bytes.Buffer
	require.NoError(t, MigrateAudit(globalDir, repoRoot, false, &out))

	data, err := os.ReadFile(filepath.Join(sessionDir, "audit.jsonl"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), home)
	entries := readRepoAuditEntries(t, sessionDir)
	require.Len(t, entries, 1)
	assert.Equal(t, "cat ~/notes.txt", entries[0].ToolInput["command"])
	assert.Equal(t, hashToolInput(map[string]any{"tool_input": entries[0].ToolInput}), entries[0].ToolInputHash)
	assert.Equal(t, toolInputPreview(map[string]any{"tool_input": entries[0].ToolInput}), entries[0].ToolInputPreview)
}

// TestQuarantineChunk_ReasonUsesPathRedactor: a home outside /home and
// /Users, and the repo root, are named by the reason an I/O error carries.
// redact.Prose alone knows neither.
func TestQuarantineChunk_ReasonUsesPathRedactor(t *testing.T) {
	t.Setenv("HOME", "/var/home/zed")
	repo := t.TempDir()
	initGitRepo(t, repo)
	sid := "sess-q"
	sealedDir, err := resolveRepoSessionDir(repo, sid, time.Now().UTC())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(sealedDir, 0o700))
	chunk := audit.SessionChunkFile(100, 300)
	require.NoError(t, os.WriteFile(filepath.Join(sealedDir, chunk), []byte("\x00garbage\n"), 0o600))

	reason := "open /var/home/zed/work/x.jsonl and " + filepath.Join(repo, "y.jsonl") + ": bad"
	m, err := QuarantineChunk(repo, filepath.Join(sealedDir, chunk), reason)
	require.NoError(t, err)
	assert.Equal(t, "open ~/work/x.jsonl and <repo>/y.jsonl: bad", m.Reason)
	data, err := os.ReadFile(filepath.Join(sealedDir, strings.TrimSuffix(chunk, ".jsonl")+".quarantine"))
	require.NoError(t, err)
	assert.NotContains(t, string(data), "zed")
}

// TestQuarantineChunk_RefusesWithoutRedactor: quarantine is an operator
// command, so with no usable redactor it refuses rather than write a
// placeholder reason.
func TestQuarantineChunk_RefusesWithoutRedactor(t *testing.T) {
	t.Setenv("HOME", "/var/home/zed")
	repo := t.TempDir()
	initGitRepo(t, repo)
	sealedDir, err := resolveRepoSessionDir(repo, "sess-q", time.Now().UTC())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(sealedDir, 0o700))
	chunk := audit.SessionChunkFile(100, 300)
	require.NoError(t, os.WriteFile(filepath.Join(sealedDir, chunk), []byte("\x00garbage\n"), 0o600))

	t.Setenv("HOME", "/")
	_, err = QuarantineChunk(repo, filepath.Join(sealedDir, chunk), "bad")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path redactor")
	_, statErr := os.Stat(filepath.Join(sealedDir, strings.TrimSuffix(chunk, ".jsonl")+".quarantine"))
	assert.True(t, os.IsNotExist(statErr), "a refused quarantine writes no marker")
}

// TestRedactReason_PlaceholderIsAnnounced: the hook path never blocks, so it
// keeps the placeholder, but it says on stderr that it replaced the reason.
func TestRedactReason_PlaceholderIsAnnounced(t *testing.T) {
	t.Setenv("HOME", "/")
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stderr
	os.Stderr = w
	got := redactReason("open /home/x/y: boom")
	os.Stderr = orig
	require.NoError(t, w.Close())
	msg, err := io.ReadAll(r)
	require.NoError(t, err)

	assert.Equal(t, redactedValueToken, got)
	assert.Contains(t, string(msg), "reason replaced: ")
	assert.NotContains(t, string(msg), "boom")
}

// TestRedactLegacyLine_AuditErrorIsSweptBesideAChangedInput: audit_error is
// free text next to tool_input, so a change to tool_input must not spare it.
func TestRedactLegacyLine_AuditErrorIsSweptBesideAChangedInput(t *testing.T) {
	r := mission.PathRedactor{Home: "/h/user"}
	line := `{"ts":"2026-01-01T00:00:00Z","tool":"Bash","tool_input":{"command":"mail jane@example.com"},` +
		`"audit_error":"failed for bob@example.com with GITHUB_TOKEN=abc12345"}`
	out, changed, err := redactLegacyLine([]byte(line), "", r)
	require.NoError(t, err)
	assert.True(t, changed)
	for _, leak := range []string{"jane@example.com", "bob@example.com", "abc12345"} {
		assert.NotContains(t, string(out), leak)
	}
	assert.Contains(t, string(out), `"redacted":true`)

	// Only audit_error leaks: still swept.
	only := `{"ts":"2026-01-01T00:00:00Z","tool":"Bash","tool_input":{"command":"ls"},"audit_error":"bob@example.com"}`
	out, changed, err = redactLegacyLine([]byte(only), "", r)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.NotContains(t, string(out), "bob@example.com")
}

// TestQuarantineChunk_WithholdsRetiredBytesThatLeak: the .corrupt is staged in
// a tracked tree, so a chunk that holds this machine's home, a credential or an
// email address, raw or decoded, is replaced by a record of its hash, size and
// rule. Plain garbage is kept as evidence.
func TestQuarantineChunk_WithholdsRetiredBytesThatLeak(t *testing.T) {
	home := "/var/home/zed"
	secret := "ghp_" + strings.Repeat("c3D4", 9)
	cases := []struct {
		name, body string
		withheld   bool
	}{
		{"home", "\x00garbage " + home + "/x\n", true},
		{"escaped home", `{"cwd":"\/var\/home\/zed\/x"` + "\n", true},
		{"credential", "\x00garbage " + secret + "\n", true},
		{"env assignment", "\x00garbage GITHUB_TOKEN=abcdefghij1234567890\n", true},
		{"address", "\x00garbage jane@example.com\n", true},
		{"escaped address", `{"v":"jane\u0040example.com"}` + "\n", true},
		{"plain", "\x00garbage\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("HOME", home)
			repo := t.TempDir()
			initGitRepo(t, repo)
			sealedDir, err := resolveRepoSessionDir(repo, "sess-q", time.Now().UTC())
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(sealedDir, 0o700))
			chunk := audit.SessionChunkFile(100, 300)
			require.NoError(t, os.WriteFile(filepath.Join(sealedDir, chunk), []byte(c.body), 0o600))

			_, err = QuarantineChunk(repo, filepath.Join(sealedDir, chunk), "bad")
			require.NoError(t, err)
			got, err := os.ReadFile(filepath.Join(sealedDir, chunk+".corrupt"))
			require.NoError(t, err)
			if !c.withheld {
				assert.Equal(t, c.body, string(got))
				return
			}
			assert.Contains(t, string(got), "sha256")
			for _, s := range []string{home, `\/var\/home`, secret, "jane@example.com", "abcdefghij1234567890"} {
				assert.NotContains(t, string(got), s)
			}
		})
	}
}

// TestQuarantineChunk_TwiceIsANoOp: a chunk withheld for a slug leaves a record
// whose rule text names no leak, so a second quarantine of the same chunk
// finds nothing to withhold: the tracked bytes are the same and the local zone
// gets no new file.
func TestQuarantineChunk_TwiceIsANoOp(t *testing.T) {
	t.Setenv("HOME", "/var/home/zed")
	repo := t.TempDir()
	initGitRepo(t, repo)
	sealedDir, err := resolveRepoSessionDir(repo, "sess-q", time.Now().UTC())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(sealedDir, 0o700))
	chunk := audit.SessionChunkFile(100, 300)
	require.NoError(t, os.WriteFile(filepath.Join(sealedDir, chunk), []byte("\x00garbage -home-bob-proj\n"), 0o600))

	_, err = QuarantineChunk(repo, filepath.Join(sealedDir, chunk), "bad")
	require.NoError(t, err)
	corrupt := filepath.Join(sealedDir, chunk+".corrupt")
	first, err := os.ReadFile(corrupt)
	require.NoError(t, err)
	require.Contains(t, string(first), "withheld")
	local1, err := os.ReadDir(audit.QuarantineDir(repo))
	require.NoError(t, err)

	second := testhelpers.CaptureStderr(t, func() {
		_, err = QuarantineChunk(repo, filepath.Join(sealedDir, chunk), "bad")
	})
	require.NoError(t, err)
	again, err := os.ReadFile(corrupt)
	require.NoError(t, err)
	assert.Equal(t, string(first), string(again), "the tracked record is unchanged")
	local2, err := os.ReadDir(audit.QuarantineDir(repo))
	require.NoError(t, err)
	assert.Len(t, local2, len(local1), "no new local file")
	assert.NotContains(t, second, "withheld")
}

// TestQuarantineChunk_ReasonThatStillLeaksIsReplaced: a reason in a spelling
// the content pass does not decode (%2Fhome%2Falice%2Fprivate) is replaced by a
// fixed text before either branch writes it; the quarantine is not refused. The
// marker, which is staged, holds no byte of it.
func TestQuarantineChunk_ReasonThatStillLeaksIsReplaced(t *testing.T) {
	t.Setenv("HOME", "/var/home/zed")
	for _, mode := range []string{"session", "mission"} {
		t.Run(mode, func(t *testing.T) {
			repo := t.TempDir()
			initGitRepo(t, repo)
			var sealedDir, chunk string
			if mode == "session" {
				d, err := resolveRepoSessionDir(repo, "sess-q", time.Now().UTC())
				require.NoError(t, err)
				sealedDir, chunk = d, audit.SessionChunkFile(100, 300)
			} else {
				sealedDir, chunk = sealedMissionDir(repo, "m-2026-07-21-030"), audit.MissionChunkFile("sessX", 100, 300)
			}
			require.NoError(t, os.MkdirAll(sealedDir, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(sealedDir, chunk), []byte("\x00garbage\n"), 0o600))

			m, err := QuarantineChunk(repo, filepath.Join(sealedDir, chunk), "open %2Fhome%2Falice%2Fprivate: bad")
			require.NoError(t, err)
			assert.NotContains(t, m.Reason, "alice")
			assert.NotEmpty(t, m.Reason)
			data, err := os.ReadFile(filepath.Join(sealedDir, strings.TrimSuffix(chunk, ".jsonl")+".quarantine"))
			require.NoError(t, err)
			assert.NotContains(t, string(data), "alice")
		})
	}
}

// TestQuarantineChunk_LeakingTokenOfTheReasonIsReplacedAlone: only the token
// that names a home is replaced; the rest of the reason, which is what tells
// the operator why the chunk was retired, stays. A notice says the reason was
// edited and does not quote it.
func TestQuarantineChunk_LeakingTokenOfTheReasonIsReplacedAlone(t *testing.T) {
	t.Setenv("HOME", "/var/home/zed")
	repo := t.TempDir()
	initGitRepo(t, repo)
	dir, err := resolveRepoSessionDir(repo, "sess-q", time.Now().UTC())
	require.NoError(t, err)
	chunk := audit.SessionChunkFile(100, 300)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, chunk), []byte("\x00garbage\n"), 0o600))

	var m audit.Marker
	stderr := captureStderr(t, func() {
		m, err = QuarantineChunk(repo, filepath.Join(dir, chunk),
			"open %2Fhome%2Falice%2Fprivate: sha256 mismatch at line 4, disk full during seal")
	})
	require.NoError(t, err)
	assert.Equal(t, "open [redacted-path]: sha256 mismatch at line 4, disk full during seal", m.Reason)
	data, err := os.ReadFile(filepath.Join(dir, strings.TrimSuffix(chunk, ".jsonl")+".quarantine"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "sha256 mismatch at line 4")
	assert.NotContains(t, string(data), "alice")
	assert.Contains(t, stderr, "reason")
	assert.Contains(t, stderr, "edited")
	assert.NotContains(t, stderr, "alice")
}

// TestCorruptRule_NamesTheSameRuleEveryRun: a line whose decoded keys break two
// rules is judged in key order, so the rule the notice names does not change
// from run to run.
func TestCorruptRule_NamesTheSameRuleEveryRun(t *testing.T) {
	// JSON escapes keep both out of the raw bytes: only the decoded keys hold them.
	line := []byte(`{"jane\u0040example.com":"x","GITHUB_TOKEN\u003dabcdefghij1234567890":"y"}` + "\n")
	first := corruptRule(line)
	require.NotEmpty(t, first)
	for i := 0; i < 50; i++ {
		require.Equal(t, first, corruptRule(line))
	}
}

// TestCorruptRule_EveryRuleGetsAGrammaticalNotice: each rule corruptRule can
// return, in the withheld-chunk notice, follows "its bytes" with a plural verb.
func TestCorruptRule_EveryRuleGetsAGrammaticalNotice(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	inputs := map[string]string{
		"own home":           "x /home/zed/y\n",
		"foreign home":       "x /home/bob/y\n",
		"own slug":           "x -home-zed-y\n",
		"foreign slug":       "x -home-bob-y\n",
		"slug end":           "x/-var-home-bob\n",
		"climb":              "~/../x\n",
		"credential":         "x GITHUB_TOKEN=abcdefghij1234567890\n",
		"email":              "x jane@example.com\n",
		"invalid utf-8":      "x \xff\xfe\n",
		"unpaired surrogate": `{"a":"\ud800"}` + "\n",
		"duplicate key":      `{"a":"1","a":"2"}` + "\n",
	}
	plural := map[string]bool{"hold": true, "contain": true, "name": true, "climb": true, "are": true}
	seen := 0
	for name, in := range inputs {
		rule := corruptRule([]byte(in))
		if rule == "" {
			continue
		}
		seen++
		notice := audit.WithheldNotice("c.jsonl", rule)
		_, after, ok := strings.Cut(notice, "its bytes ")
		require.True(t, ok, name)
		verb, _, _ := strings.Cut(after, " ")
		assert.True(t, plural[verb], "%s: %q", name, notice)
	}
	assert.GreaterOrEqual(t, seen, 6, "most inputs hit a rule")
}
