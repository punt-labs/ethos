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
	t.Cleanup(func() { sessionGuard = lineguard.SessionLineGuardFor })

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

	sessionGuard = lineguard.SessionLineGuardFor
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
	t.Cleanup(func() { sessionGuard = lineguard.SessionLineGuardFor })

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
