package autorepair

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

// A torn marker that recorded a loss, beside a legacy raw .corrupt of the same
// stem. The step must not replace the marker: one Residual, the damaged bytes
// stay, and reads keep failing loud.
func TestTornMarkerBesideLegacyCorruptIsResidual(t *testing.T) {
	env, _, s, dir, _ := fixEnv(t)
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 2000}
	torn := `{"chunk":"` + cn.Stem() + `","verified_last":1793786400000003000,"unrecovered_first":1793786400000001000,"unrecovered_last":1793786400000009000,"rea`
	markerPath := filepath.Join(dir, cn.MarkerFile())
	corruptPath := filepath.Join(dir, audit.CorruptName(cn.Stem()))
	legacy := stableLine(fixT0, "A") + stableLine(fixT0+2000, "C")
	require.NoError(t, os.WriteFile(markerPath, []byte(torn), 0o600))
	require.NoError(t, os.WriteFile(corruptPath, []byte(legacy), 0o600))

	_, before := audit.ReadStream(env.Policy, s)
	require.Error(t, before, "an orphan .corrupt fails loud before repair")

	out := runStep(t, runCorruptChunks, env)
	assert.False(t, out.Deferred)
	require.Len(t, out.Residuals, 1)
	assert.Contains(t, out.Residuals[0].Reason, "unreadable")
	assert.NotContains(t, out.Residuals[0].Reason, dir)

	got, err := os.ReadFile(markerPath)
	require.NoError(t, err)
	assert.Equal(t, torn, string(got), "the torn marker is byte-identical")
	got, err = os.ReadFile(corruptPath)
	require.NoError(t, err)
	assert.Equal(t, legacy, string(got), "the legacy bytes stay")
	_, err = audit.ReadStream(env.Policy, s)
	require.Error(t, err, "reads still fail loud")

	snap := fixStoreSnap(t, env)
	again := runStep(t, runCorruptChunks, env)
	assert.Len(t, again.Residuals, 1, "the condition is reported again, and only once")
	assert.False(t, again.Deferred)
	assert.Equal(t, snap, fixStoreSnap(t, env), "run 2 changes nothing")
}
