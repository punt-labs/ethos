package hook

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/autorepair"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// A flagged purge tombstone makes every commit's seal warn until something
// retires it. The session-start pass is that something: after it runs once the
// seal is silent, and the retirement is in the repair log.
func TestSessionStartPassSilencesFlaggedTombstoneWarning(t *testing.T) {
	repo := t.TempDir()
	gitRepoWithOrigin(t, repo, vacuumTestRepoID)
	home := t.TempDir()
	global := filepath.Join(home, ".punt-labs", "ethos")
	require.NoError(t, audit.WriteTombstone(globalSessionsDir(t, global), audit.Tombstone{
		Session: "sess-lost", Repo: vacuumTestRepoID, LiveFileGone: true,
	}))
	p, err := layout.NewRepository(repo, repo, layout.RecordBlame)
	require.NoError(t, err)

	seal := func() string {
		var buf bytes.Buffer
		require.NoError(t, VacuumCrossCheckWithPolicy(p, global, nil, &buf))
		return buf.String()
	}
	assert.Contains(t, seal(), "sess-lost", "the first seal warns")
	assert.Contains(t, seal(), "sess-lost", "the warning repeats until the tombstone is retired")

	env := autorepair.Env{
		Roots:  resolve.Located{Store: repo, Checkout: repo},
		Policy: p,
		Global: global,
		Now:    time.Now(),
		Log:    autorepair.NewFileLog(global, repo, repo, "test"),
	}
	n, err := autorepair.SessionStart(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "one tombstone retired")

	assert.Empty(t, seal(), "after the pass the seal prints nothing")
	assert.FileExists(t, filepath.Join(global, "sessions", "sess-lost.purged.acked"), "the loss record is kept")

	logged, err := os.ReadFile(autorepair.LogPath(global))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(logged), "retire-tombstone"), "retired with one log line")
}
