package auditimport

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/lineguard"
)

// TestImportGlobalAuditStopsAtContext: a context that expires during the
// first file stops that file where it is (reported as Failed with the
// context's error, its global copy kept); the rest are reported as not
// attempted, and a later run with a live context imports all three.
func TestImportGlobalAuditStopsAtContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	p, err := layout.NewRepository(repo, repo, layout.RecordBlame)
	require.NoError(t, err)
	dir := filepath.Join(t.TempDir(), "sessions")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	for _, id := range []string{"s1", "s2", "s3"} {
		line := `{"ts":"2026-11-09T23:02:12Z","session":"` + id + `","tool":"Read"}` + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, id+".audit.jsonl"), []byte(line), 0o600))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessionGuard = func(string) lineguard.LineGuard {
		cancel() // the budget runs out while the first file is in progress
		return lineguard.GuardAuditLineFull
	}
	t.Cleanup(func() { sessionGuard = nil })

	res, err := ImportGlobalAudit(ctx, p, dir, repo)
	require.NoError(t, err)
	assert.Empty(t, res.Done, "the file in progress is not finished past the budget")
	// The first file attempted had the whole budget and still did not
	// finish; it and the files after it (no smaller) are reported, not
	// retried on every command.
	require.Len(t, res.Failed, 3)
	for _, f := range res.Failed {
		assert.ErrorIs(t, f.Err, context.Canceled)
	}
	assert.Zero(t, res.Remaining)

	sessionGuard = nil
	res, err = ImportGlobalAudit(context.Background(), p, dir, repo)
	require.NoError(t, err)
	assert.Len(t, res.Done, 3, "a live context imports every file, the cut-off one included")
	assert.Zero(t, res.Remaining)
	assert.Empty(t, res.Failed)
}

// TestImportGlobalAuditGivesUpOnAFileItCannotFinish: a file whose own
// import outlives the context is not left half done and retried forever. It
// is reported as Failed with the context's error, its global copy stays, and
// the run goes on to count the rest as Remaining.
func TestImportGlobalAuditGivesUpOnAFileItCannotFinish(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	p, err := layout.NewRepository(repo, repo, layout.RecordBlame)
	require.NoError(t, err)
	dir := filepath.Join(t.TempDir(), "sessions")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	var big []byte
	for i := 0; i < 50; i++ {
		big = append(big, []byte(`{"ts":"2026-11-09T23:02:12Z","session":"s1","tool":"Read","n":`+itoa(i)+`}`+"\n")...)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "s1.audit.jsonl"), big, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "s2.audit.jsonl"), []byte(`{"ts":"2026-11-09T23:02:13Z","session":"s2","tool":"Read"}`+"\n"), 0o600))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	sessionGuard = func(string) lineguard.LineGuard {
		return func(line []byte) ([]byte, string, bool) {
			calls++
			if calls == 10 {
				cancel() // the budget runs out in the middle of the big file
			}
			return lineguard.GuardAuditLineFull(line)
		}
	}
	t.Cleanup(func() { sessionGuard = nil })

	res, err := ImportGlobalAudit(ctx, p, dir, repo)
	require.NoError(t, err)
	// Smallest first: s2 (one line) is imported before the budget runs out
	// inside s1. s1 did not have the whole budget, so it is the next run's,
	// with a fresh budget.
	require.Len(t, res.Done, 1)
	assert.Equal(t, "s2", res.Done[0].Session)
	assert.Empty(t, res.Failed, "a file cut off after others used the budget is retried, not reported")
	assert.Equal(t, 1, res.Remaining, "s1 is the next run's")
	assert.FileExists(t, filepath.Join(dir, "s1.audit.jsonl"), "the global copy stays")
	assert.Less(t, calls, 50, "the import stopped at the context, not at the end of the file")

	// The next run attempts s1 first with a whole budget; when that is still
	// not enough, s1 is reported once and the run completes.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	calls = 0
	sessionGuard = func(string) lineguard.LineGuard {
		return func(line []byte) ([]byte, string, bool) {
			calls++
			if calls == 10 {
				cancel2()
			}
			return lineguard.GuardAuditLineFull(line)
		}
	}
	res, err = ImportGlobalAudit(ctx2, p, dir, repo)
	require.NoError(t, err)
	assert.Empty(t, res.Done)
	require.Len(t, res.Failed, 1)
	assert.Equal(t, "s1", res.Failed[0].Session)
	assert.ErrorIs(t, res.Failed[0].Err, context.Canceled)
	assert.Zero(t, res.Remaining)
	assert.FileExists(t, filepath.Join(dir, "s1.audit.jsonl"), "the global copy stays")
}

func itoa(i int) string { return string(rune('0'+i/10)) + string(rune('0'+i%10)) }

// TestEnumerateLegacyFilesSmallestFirst: a budget imports the most sessions
// when the files that fit come before the ones that may not.
func TestEnumerateLegacyFilesSmallestFirst(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.audit.jsonl"), make([]byte, 300), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.audit.jsonl"), make([]byte, 100), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "c.audit.jsonl"), make([]byte, 200), 0o600))
	files, err := EnumerateLegacyFiles(dir)
	require.NoError(t, err)
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	assert.Equal(t, []string{"b.audit.jsonl", "c.audit.jsonl", "a.audit.jsonl"}, names)
}

// A file that fails fast on every run (an identity conflict) must not count
// as having used the budget: the large file after it then had the whole
// budget to itself and is a residual, not a file left remaining forever.
func TestImportGlobalAuditFastFailureDoesNotShieldTheNextFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	p, err := layout.NewRepository(repo, repo, layout.RecordBlame)
	require.NoError(t, err)
	dir := filepath.Join(t.TempDir(), "sessions")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	// aaaa fails fast on every run: two lines share an identity (ts, tool,
	// tool_input_hash) with different payloads, which the import refuses as
	// a legacy identity conflict.
	bad := `{"ts":"2026-11-09T23:02:12Z","session":"aaaa","tool":"Read","tool_input_hash":"deadbeef","tool_input":{"file_path":"a"}}` + "\n" +
		`{"ts":"2026-11-09T23:02:12Z","session":"aaaa","tool":"Read","tool_input_hash":"deadbeef","tool_input":{"file_path":"b"}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "aaaa.audit.jsonl"), []byte(bad), 0o600))
	var big []byte
	for i := 0; i < 50; i++ {
		big = append(big, []byte(`{"ts":"2026-11-09T23:02:12Z","session":"zzzz","tool":"Read","n":`+itoa(i)+`}`+"\n")...)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zzzz.audit.jsonl"), big, 0o600))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	sessionGuard = func(string) lineguard.LineGuard {
		return func(line []byte) ([]byte, string, bool) {
			calls++
			if calls == 10 {
				cancel() // the budget runs out inside the big file
			}
			return lineguard.GuardAuditLineFull(line)
		}
	}
	t.Cleanup(func() { sessionGuard = nil })

	res, err := ImportGlobalAudit(ctx, p, dir, repo)
	require.NoError(t, err)
	assert.Zero(t, res.Remaining, "the big file had the whole budget; nothing is left for a retry")
	var ids []string
	for _, f := range res.Failed {
		ids = append(ids, f.Session)
	}
	assert.ElementsMatch(t, []string{"aaaa", "zzzz"}, ids, "both are residuals")
}

// A roster that exists but cannot be read is not "no owner": it is the one
// signal that could name another repository. The file is kept and recorded,
// never imported here and deleted.
func TestImportGlobalAuditKeepsAFileWhoseRosterIsUnreadable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	p, err := layout.NewRepository(repo, repo, layout.RecordBlame)
	require.NoError(t, err)
	dir := filepath.Join(t.TempDir(), "sessions")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "s1.audit.jsonl"),
		[]byte(`{"ts":"2026-11-09T23:02:12Z","session":"s1","tool":"Read"}`+"\n"), 0o600))
	// A directory where the roster file belongs: the read fails on every
	// platform, unlike a parse miss, which an old roster shape also produces
	// and which must count as no signal.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "s1.yaml"), 0o700))

	res, err := ImportGlobalAudit(context.Background(), p, dir, repo)
	require.NoError(t, err)
	assert.Empty(t, res.Done)
	require.Len(t, res.Failed, 1)
	assert.Equal(t, ReasonRosterUnreadable, res.Failed[0].Reason)
	assert.FileExists(t, filepath.Join(dir, "s1.audit.jsonl"), "the global copy stays")
}
