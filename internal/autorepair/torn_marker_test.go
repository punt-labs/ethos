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

// The marker at the stem's name covers another chunk and the chunk is damaged:
// no later run can change that, so it is one Residual, never Deferred.
func TestMarkerCoveringAnotherChunkIsResidual(t *testing.T) {
	env, _, _, dir, _ := fixEnv(t)
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 2000}
	mb, err := audit.MarshalMarker(audit.Marker{Chunk: "audit-other", VerifiedLast: fixT0, Reason: "x"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, cn.MarkerFile()), mb, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, cn.ChunkFile()), []byte(stableLine(fixT0, "A")+"garbage\n"+stableLine(fixT0+2000, "C")), 0o600))
	for i := 1; i <= 3; i++ {
		out := runStep(t, runCorruptChunks, env)
		assert.False(t, out.Deferred, "run %d", i)
		require.Len(t, out.Residuals, 1, "run %d", i)
		assert.Contains(t, out.Residuals[0].Reason, "covers another chunk")
	}
}

// A legacy raw .corrupt under a parseable covering marker is still raw bytes:
// the step salvages it through the guard and deletes it, so no token survives.
func TestCoveredLegacyCorruptIsSalvaged(t *testing.T) {
	env, _, s, dir, home := fixEnv(t)
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 2000}
	mb, err := audit.MarshalMarker(audit.Marker{Chunk: cn.Stem(), VerifiedLast: fixT0 + 2000, Reason: "old"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, cn.MarkerFile()), mb, 0o600))
	corrupt := filepath.Join(dir, audit.CorruptName(cn.Stem()))
	require.NoError(t, os.WriteFile(corrupt, []byte(fixCmd(fixT0, "Bash", "echo "+fakeToken)), 0o600))

	out := runStep(t, runCorruptChunks, env)
	assert.False(t, out.Deferred)
	assert.Empty(t, out.Residuals)
	assert.Positive(t, out.Items)
	_, err = os.Lstat(corrupt)
	assert.ErrorIs(t, err, os.ErrNotExist, "the raw .corrupt is removed")
	assert.Empty(t, walkContains(t, env.Roots.Store, fakeToken, home), "no token survives")
	data, err := audit.ReadStream(env.Policy, s)
	require.NoError(t, err)
	assert.True(t, len(data.Lines) == 1 || len(data.Gaps) > 0, "the line is installed or recorded as lost")
	fixSecondRun(t, env)
}

// A torn marker is a Residual whatever else is in the directory, even when no
// repair would write a marker: ScanSealedDir reads it as absent, so detection
// must look at it directly.
func TestTornMarkerWithoutAMarkerWriteIsResidual(t *testing.T) {
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 2000}
	torn := `{"chunk":"` + cn.Stem() + `","verified_last":1793786400000003000,"unrecovered_first":1793786400000001000,"unrec`
	other := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0 + 5000, Last: fixT0 + 5000}
	cases := map[string]map[string]string{
		"missing final newline": {cn.ChunkFile(): stableLine(fixT0, "A") + stableLine(fixT0+2000, "C")[:len(stableLine(fixT0+2000, "C"))-1]},
		"alone":                 {other.ChunkFile(): stableLine(fixT0+5000, "Z")},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			env, _, s, dir, _ := fixEnv(t)
			markerPath := filepath.Join(dir, cn.MarkerFile())
			require.NoError(t, os.WriteFile(markerPath, []byte(torn), 0o600))
			for f, body := range files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte(body), 0o600))
			}
			_, rerr := audit.ReadStream(env.Policy, s)
			require.Error(t, rerr)
			snap := fixStoreSnap(t, env)
			for i := 1; i <= 2; i++ {
				out := runStep(t, runCorruptChunks, env)
				assert.False(t, out.Deferred, "run %d", i)
				require.Len(t, out.Residuals, 1, "run %d", i)
				assert.Zero(t, out.Items, "run %d", i)
				assert.Contains(t, out.Residuals[0].Reason, "unreadable")
			}
			assert.Equal(t, snap, fixStoreSnap(t, env), "nothing in the directory is touched")
			got, err := os.ReadFile(markerPath)
			require.NoError(t, err)
			assert.Equal(t, torn, string(got))
			_, rerr = audit.ReadStream(env.Policy, s)
			require.Error(t, rerr, "reads still fail loud")
		})
	}
}
