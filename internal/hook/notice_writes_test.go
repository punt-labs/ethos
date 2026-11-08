package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

// rewriteNotices are the phrases of a notice that something stored was changed.
var rewriteNotices = []string{"was rewritten", "loss marker", "was withheld", "rewritten before"}

// These historical/global writer checks do not establish acceptance for the
// current repository policy path. See repository_notice_writes_test.go for
// HandleAuditLogWithPolicy and SealRepoWithPolicy coverage.
// TestLegacyWriters_FailingWritesPrintNoRewriteNotice retains the legacy sink
// failures, including the non-repository HandleAuditLog branch and the old
// sealDirLocked helper. Some failures precede guarding, not just persistence.
func TestLegacyWriters_FailingWritesPrintNoRewriteNotice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ts := audit.FormatLineTS(100)
	leaky := `{"ts":"` + ts + `","session":"s","tool":"Read","cwd":"/home/bob/x"}`
	// A path below a regular file: no write there can succeed, whatever
	// creates directories.
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	missing := filepath.Join(blocker, "dir")

	cases := map[string]func(t *testing.T){
		"appendRawAuditLines": func(t *testing.T) {
			_, err := appendRawAuditLines(filepath.Join(missing, "audit.jsonl"), [][]byte{[]byte(leaky)})
			require.Error(t, err)
		},
		"emitAuditSentinel": func(t *testing.T) {
			err := emitAuditSentinel(filepath.Join(missing, "live.jsonl"), missing, filepath.Join(missing, "audit.jsonl"), "/home/bob/s", time.Now(), "boom")
			require.Error(t, err)
		},
		"emitLegacySentinel": func(t *testing.T) {
			require.Error(t, emitLegacySentinel(filepath.Join(missing, "a.jsonl"), "/home/bob/s", ts, "boom"))
		},
		"HandleAuditLog global fallback": func(t *testing.T) {
			blocked := filepath.Join(t.TempDir(), "sessions")
			require.NoError(t, os.Mkdir(blocked, 0o000))
			defer func() { _ = os.Chmod(blocked, 0o700) }()
			b, _ := json.Marshal(map[string]any{"session_id": "s", "tool_name": "Read",
				"tool_input": map[string]any{"file_path": filepath.Join(home, "x")}})
			require.ErrorIs(t, HandleAuditLog(bytes.NewReader(b), "", blocked), ErrAuditDropped)
		},
		"historical sealDirLocked": func(t *testing.T) {
			repo := t.TempDir()
			live := filepath.Join(repo, "live.jsonl")
			require.NoError(t, os.WriteFile(live, []byte(leaky+"\n"), 0o600))
			sealed := filepath.Join(repo, "sealed")
			require.NoError(t, os.MkdirAll(filepath.Join(sealed, "tmp"), 0o700))
			_, err := sealDirLocked(sealDirParams{
				repoRoot: repo, ns: audit.SessionNS, sealedDir: sealed, livePath: live,
				chunkName: func(f, l int64) string { return audit.SessionChunkFile(f, l) },
				tempName:  func(int64, int64) string { return "tmp" }, // a directory: the write fails
				label:     "session s",
			}, time.Now(), SealOptions{})
			require.Error(t, err)
		},
		"quarantine re-seal": func(t *testing.T) {
			repo := t.TempDir()
			initGitRepo(t, repo)
			sealed := filepath.Join(repo, "sealed")
			require.NoError(t, os.MkdirAll(sealed, 0o700))
			chunk := audit.SessionChunkFile(100, 300)
			require.NoError(t, os.WriteFile(filepath.Join(sealed, chunk), []byte("\x00garbage\n"), 0o600))
			live := filepath.Join(repo, "live.jsonl")
			require.NoError(t, os.WriteFile(live, []byte(leaky+"\n"), 0o600))
			cn, _ := audit.Classify(chunk, audit.SessionNS)
			re := audit.ChunkName{Namespace: cn.Namespace, First: 100, Last: 100}
			require.NoError(t, os.MkdirAll(filepath.Join(sealed, re.TempFile()), 0o700)) // the write fails
			_, err := audit.Quarantine(repo, sealed, cn, live, "bad", redactReason, guardAuditLineFull, corruptRule)
			require.Error(t, err)
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			notice := testhelpers.CaptureStderr(t, func() { run(t) })
			// A retired chunk is always withheld, and that notice is true even
			// when a later write fails: the record was written. Only the notices
			// about the failed write are judged.
			var kept []string
			for _, l := range strings.Split(notice, "\n") {
				if !strings.Contains(l, "retired chunk") {
					kept = append(kept, l)
				}
			}
			notice = strings.Join(kept, "\n")
			for _, p := range rewriteNotices {
				assert.NotContains(t, notice, p, "a failed write must not announce a rewrite: %s", notice)
			}
		})
	}
}

// TestLegacySeal_PrintsTheGuardNoticesAfterTheChunkIsWritten exercises the old
// rewrite-on-seal helper, not current immutable repository archival, which
// refuses a payload that would require rewriting.
func TestLegacySeal_PrintsTheGuardNoticesAfterTheChunkIsWritten(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ts := audit.FormatLineTS(100)
	repo := t.TempDir()
	live := filepath.Join(repo, "live.jsonl")
	require.NoError(t, os.WriteFile(live, []byte(`{"ts":"`+ts+`","session":"s","tool":"Read","cwd":"/home/bob/x"}`+"\n"), 0o600))
	sealed := filepath.Join(repo, "sealed")
	var err error
	notice := testhelpers.CaptureStderr(t, func() {
		_, err = sealDirLocked(sealDirParams{
			repoRoot: repo, ns: audit.SessionNS, sealedDir: sealed, livePath: live,
			chunkName: func(f, l int64) string { return audit.SessionChunkFile(f, l) },
			tempName:  func(f, l int64) string { return audit.SessionTempFile(f, l) },
			label:     "session s",
		}, time.Now(), SealOptions{})
	})
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(notice, "rewritten before sealing"), notice)
}

// TestMigrate_NoticesOnlyForLinesAppended: a dry run says what it would
// rewrite; a real run says what it did, once, after the append; a re-run that
// finds the lines already migrated says nothing.
func TestMigrate_NoticesOnlyForLinesAppended(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()
	repoSessionDir(t, repoRoot, "2026-05-22", "sess-n")
	require.NoError(t, os.MkdirAll(globalDir, 0o700))
	legacy := filepath.Join(globalDir, "sess-n.audit.jsonl")
	write := func() {
		require.NoError(t, os.WriteFile(legacy,
			legacyLine(t, "sess-n", "2026-05-22T10:00:00Z", map[string]any{"command": "cat " + filepath.Join(home, "n.txt")}), 0o600))
	}
	run := func(dry bool) string {
		var out bytes.Buffer
		return testhelpers.CaptureStderr(t, func() { require.NoError(t, MigrateAudit(globalDir, repoRoot, dry, &out)) })
	}

	write()
	dry := run(true)
	assert.Contains(t, dry, "would rewrite 1 of 1 line(s)")
	assert.NotContains(t, dry, "was rewritten")
	assert.NotContains(t, dry, home)

	real := run(false)
	assert.Equal(t, 1, strings.Count(real, "1 of 1 line(s) rewritten"), real)
	assert.Equal(t, 1, strings.Count(real, "\n"), real)
	assert.NotContains(t, real, "would")

	write() // a crash left the legacy file; its lines are already migrated
	again := run(false)
	assert.Empty(t, again)
}

// TestMigrate_NoLegacyLineVanishes: a legacy line that does not decode as an
// entry (a string tool_input, junk, a torn tail) is routed through the guard
// and imported, as a coerced line or a loss marker. The global source remains
// intact after import. Notices name the FILE line. A dry run reports the same
// lines and writes nothing.
func TestMigrate_NoLegacyLineVanishes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()
	sessionDir := migrationSessionDir(t, repoRoot, "2026-05-22", "sess-d")
	require.NoError(t, os.MkdirAll(globalDir, 0o700))
	legacy := filepath.Join(globalDir, "sess-d.audit.jsonl")

	var body []byte
	for i := 1; i <= 13; i++ {
		ts := fmt.Sprintf("2026-05-22T10:00:%02dZ", i)
		switch i {
		case 5:
			body = append(body, []byte(`{"ts":"`+ts+`","session":"sess-d","tool":"Bash","tool_input":"ls"}`+"\n")...)
		case 8:
			body = append(body, []byte("this is junk\n")...)
		case 13:
			body = append(body, []byte(`{"ts":"`+ts+`","session":"sess-d","tool":"Bash","tool_in`)...) // torn: no newline
		default:
			body = append(body, legacyLine(t, "sess-d", ts, map[string]any{"command": fmt.Sprintf("echo %d", i)})...)
		}
	}
	require.NoError(t, os.WriteFile(legacy, body, 0o600))
	run := func(dry bool) (string, string, error) {
		var out bytes.Buffer
		var err error
		notice := testhelpers.CaptureStderr(t, func() { err = MigrateAudit(globalDir, repoRoot, dry, &out) })
		return notice, out.String(), err
	}

	dryNotice, dryOut, err := run(true)
	require.NoError(t, err)
	assert.Contains(t, dryOut, "13 new entries")
	assert.Contains(t, dryNotice, "would rewrite 3 of 13 line(s)")
	assert.Contains(t, dryNotice, "loss marker 2 (lines 8, 13)")
	assert.Contains(t, dryNotice, "re-encoded 1 (line 5)")
	assert.Equal(t, 1, strings.Count(dryNotice, "audit migrate"), dryNotice)
	assert.FileExists(t, legacy)
	_, statErr := os.Stat(filepath.Join(sessionDir, "audit.jsonl"))
	assert.True(t, os.IsNotExist(statErr), "a dry run writes nothing")

	notice, out, err := run(false)
	require.NoError(t, err)
	assert.Contains(t, out, "13 new entries")
	assert.Contains(t, notice, "3 of 13 line(s) rewritten")
	assert.Contains(t, notice, "loss marker 2 (lines 8, 13)")
	assert.Equal(t, 1, strings.Count(notice, "audit migrate"), notice)
	retained, err := os.ReadFile(legacy)
	require.NoError(t, err)
	assert.Equal(t, body, retained, "repository import must preserve the global source")
	data, err := os.ReadFile(filepath.Join(sessionDir, "audit.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, 13, bytes.Count(data, []byte("\n")), string(data))
}

// TestMigrate_NoLineLostToALiveAppender: a writer appends to the legacy file
// while MigrateAudit runs again and again. Each import retains the global
// source and deduplicates its snapshot against the local archive. Every
// appended line remains available and the final import includes the full tail.
func TestMigrate_NoLineLostToALiveAppender(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()
	sessionDir := migrationSessionDir(t, repoRoot, "2026-05-22", "sess-r")
	require.NoError(t, os.MkdirAll(globalDir, 0o700))
	legacy := filepath.Join(globalDir, "sess-r.audit.jsonl")

	const n = 300
	done := make(chan error, 1)
	go func() {
		for i := 0; i < n; i++ {
			env := map[string]any{"tool_input": map[string]any{"command": fmt.Sprintf("echo %d", i)}}
			b, err := json.Marshal(map[string]any{
				"ts": fmt.Sprintf("2026-05-22T10:%02d:%02dZ", i/60, i%60), "session": "sess-r", "tool": "Bash",
				"tool_input": map[string]any{"command": fmt.Sprintf("echo %d", i)}, "tool_input_hash": hashToolInput(env),
			})
			if err != nil {
				done <- err
				return
			}
			f, err := os.OpenFile(legacy, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				done <- err
				return
			}
			_, err = f.Write(append(b, '\n'))
			_ = f.Close()
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	run := func() {
		var out bytes.Buffer
		testhelpers.CaptureStderr(t, func() { require.NoError(t, MigrateAudit(globalDir, repoRoot, false, &out)) })
	}
	for finished := false; !finished; {
		select {
		case err := <-done:
			require.NoError(t, err)
			finished = true
		default:
			run()
		}
	}
	run()

	seen := map[string]bool{}
	data, err := os.ReadFile(filepath.Join(sessionDir, "audit.jsonl"))
	require.NoError(t, err)
	for _, l := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var e auditEntry
		require.NoError(t, json.Unmarshal(l, &e))
		if c, ok := e.ToolInput["command"].(string); ok {
			seen[c] = true
		}
	}
	assert.FileExists(t, legacy)
	for i := 0; i < n; i++ {
		assert.True(t, seen[fmt.Sprintf("echo %d", i)], "line %d was lost", i)
	}
}

// TestMigrate_OneNoticePerFileWithTheFirstTenLines: a file with many rewritten
// lines still prints one notice, with a count per kind and the first ten line
// numbers of each.
func TestMigrate_OneNoticePerFileWithTheFirstTenLines(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(t.TempDir(), "global")
	repoRoot := t.TempDir()
	repoSessionDir(t, repoRoot, "2026-05-22", "sess-v")
	require.NoError(t, os.MkdirAll(globalDir, 0o700))
	var body []byte
	for i := 1; i <= 25; i++ {
		body = append(body, legacyLine(t, "sess-v", fmt.Sprintf("2026-05-22T10:00:%02dZ", i),
			map[string]any{"command": fmt.Sprintf("cat %s/f%d", home, i)})...)
	}
	require.NoError(t, os.WriteFile(filepath.Join(globalDir, "sess-v.audit.jsonl"), body, 0o600))
	var out bytes.Buffer
	notice := testhelpers.CaptureStderr(t, func() { require.NoError(t, MigrateAudit(globalDir, repoRoot, false, &out)) })
	assert.Equal(t, 1, strings.Count(notice, "\n"), notice)
	assert.Contains(t, notice, "25 of 25 line(s) rewritten")
	assert.Contains(t, notice, "home path or content 25 (lines 1, 2, 3, 4, 5, 6, 7, 8, 9, 10 and 15 more)")
	assert.NotContains(t, notice, home)
}

// TestQueryLegacyAudit_ReadsTheStagedFile: mid-migration the legacy lines are
// in the staged file, and `audit show` still sees them.
func TestQueryLegacyAudit_ReadsTheStagedFile(t *testing.T) {
	dir := t.TempDir()
	line := legacyLine(t, "sess-s", "2026-05-22T10:00:00Z", map[string]any{"command": "ls"})
	var m map[string]any
	require.NoError(t, json.Unmarshal(line, &m))
	m["delegation_id"] = "d-1"
	b, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sess-s.audit.jsonl"+stagedSuffix), append(b, '\n'), 0o600))
	got, err := queryLegacyAudit(dir, "d-1", nil)
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

// TestLegacySeal_OneNoticePerKindNoMatterHowManyLines: a tail with many rewritten
// lines and many loss markers prints two lines, with the counts and the rules,
// and never the value.
func TestLegacySeal_OneNoticePerKindNoMatterHowManyLines(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	live := filepath.Join(repo, "live.jsonl")
	var body strings.Builder
	for i := int64(100); i < 105; i++ {
		fmt.Fprintf(&body, `{"ts":"%s","session":"s","tool":"Read","cwd":"/home/bob/x"}`+"\n", audit.FormatLineTS(i))
	}
	for i := int64(105); i < 108; i++ {
		fmt.Fprintf(&body, `{"ts":"%s","session":"s","cwd":"\ud800"}`+"\n", audit.FormatLineTS(i))
	}
	require.NoError(t, os.WriteFile(live, []byte(body.String()), 0o600))
	var err error
	notice := testhelpers.CaptureStderr(t, func() {
		_, err = sealDirLocked(sealDirParams{
			repoRoot: repo, ns: audit.SessionNS, sealedDir: filepath.Join(repo, "sealed"), livePath: live,
			chunkName: func(f, l int64) string { return audit.SessionChunkFile(f, l) },
			tempName:  func(f, l int64) string { return audit.SessionTempFile(f, l) },
			label:     "session s",
		}, time.Now(), SealOptions{})
	})
	require.NoError(t, err)
	got := strings.Split(strings.TrimSpace(notice), "\n")
	require.Len(t, got, 2, notice)
	assert.Contains(t, got[0], "ethos: audit seal: session s: 5 lines rewritten before sealing (it ")
	assert.Contains(t, got[1], "ethos: audit seal: session s: 3 lines replaced by a loss marker before sealing (it ")
	assert.NotContains(t, notice, "/home/bob")
}

// TestLegacyQuarantine_OneNoticeForManyRewrittenLines retains the old quarantine
// helper's rewrite-on-reseal behavior, not current policy-aware archival.
func TestLegacyQuarantine_OneNoticeForManyRewrittenLines(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	initGitRepo(t, repo)
	sealed := filepath.Join(repo, "sealed")
	require.NoError(t, os.MkdirAll(sealed, 0o700))
	chunk := audit.SessionChunkFile(100, 104)
	require.NoError(t, os.WriteFile(filepath.Join(sealed, chunk), []byte("\x00garbage\n"), 0o600))
	var body strings.Builder
	for i := int64(100); i < 105; i++ {
		fmt.Fprintf(&body, `{"ts":"%s","session":"s","tool":"Read","cwd":"/home/bob/x"}`+"\n", audit.FormatLineTS(i))
	}
	live := filepath.Join(repo, "live.jsonl")
	require.NoError(t, os.WriteFile(live, []byte(body.String()), 0o600))
	cn, _ := audit.Classify(chunk, audit.SessionNS)
	var err error
	notice := testhelpers.CaptureStderr(t, func() {
		_, err = audit.Quarantine(repo, sealed, cn, live, "bad", redactReason, guardAuditLineFull, corruptRule)
	})
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(notice, "5 lines rewritten before sealing"), notice)
	assert.NotContains(t, notice, "a line was rewritten")
}
