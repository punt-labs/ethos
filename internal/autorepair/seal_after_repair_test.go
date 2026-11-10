package autorepair_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/autorepair"
	"github.com/punt-labs/ethos/v4/internal/hook"
)

// After live-tails redacts a pre-redaction line, a seal through the real code
// path has nothing to refuse.
func TestSealAfterLiveTailsRedaction(t *testing.T) {
	home := autorepair.FixtureHome(t)
	env := autorepair.DamageEnv(t, "live-audit-pre-redaction")
	autorepair.ResolveHome(t, env.Roots.Store, home)
	out, err := autorepair.RunLiveTails(context.Background(), env)
	require.NoError(t, err)
	require.Equal(t, 1, out.Items)
	res, err := hook.SealRepoWithPolicy(env.Policy, time.Now(), hook.SealOptions{})
	require.NoError(t, err, "a following seal through the real code path has nothing to refuse")
	assert.Equal(t, 2, res.LinesSealed)
}
