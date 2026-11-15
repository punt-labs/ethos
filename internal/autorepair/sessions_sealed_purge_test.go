package autorepair_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/autorepair"
	"github.com/punt-labs/ethos/v4/internal/hook"
)

// A dead session whose live audit lines were all sealed by the real seal path
// (chunks land under trace/, DES-079) has nothing unsealed: the purge removes
// its roster and records no residual.
func TestSessionsPurgesRosterAfterRealSeal(t *testing.T) {
	env := autorepair.FixtureEnv(t, "corpus/v4.22.1")
	sessions := filepath.Join(env.Global, "sessions")
	autorepair.ResolvePlaceholders(t, sessions, "2147483646", env.Roots.Checkout)
	_, err := hook.SealRepoWithPolicy(env.Policy, time.Now(), hook.SealOptions{})
	require.NoError(t, err)
	chunks, err := filepath.Glob(filepath.Join(env.Policy.LocalRoot(), "trace", "sessions", "*-sess-corpus-0001", "audit-*.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, chunks, "fixture: sealed chunks sit under trace/sessions")

	out, err := autorepair.RunSessions(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, out.Residuals, "a fully sealed session is not a residual")
	assert.NoFileExists(t, filepath.Join(sessions, "sess-corpus-0001.yaml"))
}
