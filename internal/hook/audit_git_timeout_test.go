package hook

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The seal, the mission seal, the audit diagnostics and the vacuum
// cross-check each ask git something; a git that does not answer is an
// error from each, not a deferred seal or an empty identity.
func TestAuditGitTimeouts(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".punt-labs", "ethos", "sessions", "2026-01-01-s1"), 0o755))
	testhelpers.HangingGit(t)
	now := time.Now()

	t.Run("SealRepo", func(t *testing.T) {
		res, err := SealRepo(repo, now, SealOptions{})
		assert.ErrorIs(t, err, gitenv.ErrTimeout)
		assert.False(t, res.Deferred, "a timeout is not a gitlink deferral")
	})
	t.Run("SealMission", func(t *testing.T) {
		res, err := SealMission(repo, "m-2026-01-01-001", now, SealOptions{})
		assert.ErrorIs(t, err, gitenv.ErrTimeout)
		assert.False(t, res.Deferred)
	})
	t.Run("CollectAuditDiagnostics", func(t *testing.T) {
		_, err := CollectAuditDiagnostics(repo, now)
		assert.ErrorIs(t, err, gitenv.ErrTimeout)
	})
	t.Run("VacuumCrossCheck", func(t *testing.T) {
		err := VacuumCrossCheck(repo, t.TempDir(), nil, io.Discard)
		assert.ErrorIs(t, err, gitenv.ErrTimeout)
	})
}
