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
// first file ends the run after that file; the rest are reported as not
// attempted, and a later run with a live context imports them.
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
	assert.Len(t, res.Done, 1, "the file in progress finishes")
	assert.Equal(t, 2, res.Remaining, "the others were not attempted")
	assert.Empty(t, res.Failed)

	sessionGuard = lineguard.SessionLineGuardFor
	res, err = ImportGlobalAudit(context.Background(), p, dir, repo)
	require.NoError(t, err)
	assert.Len(t, res.Done, 2)
	assert.Zero(t, res.Remaining)
}
