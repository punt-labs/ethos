package layout_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestRepositoryPolicyPaths(t *testing.T) {
	base := t.TempDir()
	store, checkout := filepath.Join(base, "main"), filepath.Join(base, "linked")
	for _, mode := range []layout.RecordMode{layout.RecordBlame, layout.RecordFull} {
		t.Run(map[layout.RecordMode]string{layout.RecordBlame: "blame", layout.RecordFull: "full"}[mode], func(t *testing.T) {
			// Nonexistent roots are intentional: construction is pure.
			p, err := layout.NewRepository(store, checkout, mode)
			require.NoError(t, err)
			assert.Equal(t, filepath.Join(store, ".punt-labs", "ethos"), p.RecordRoot())
			assert.Equal(t, filepath.Join(store, ".punt-labs", "local", "ethos"), p.LocalRoot())
			assert.Equal(t, filepath.Join(checkout, ".punt-labs", "ethos"), p.CheckoutRecordRoot())
			assert.Equal(t, filepath.Join(store, ".punt-labs", "local", "ethos", "tmp"), p.TempDir())
			wantTrace := filepath.Join(store, ".punt-labs", "local", "ethos", "trace")
			assert.Equal(t, wantTrace, p.TraceArchiveRoot(), "both modes retain the canonical local trace archive")
			assert.NotEqual(t, p.RecordRoot(), p.CheckoutRecordRoot(), "shared authority and linked-checkout publication are distinct")
			for _, recordRoot := range []string{p.RecordRoot(), p.CheckoutRecordRoot()} {
				rel, err := filepath.Rel(recordRoot, p.TraceArchiveRoot())
				require.NoError(t, err)
				assert.False(t, filepath.IsLocal(rel), "trace archive must not be inside either record root")
				rel, err = filepath.Rel(p.TraceArchiveRoot(), recordRoot)
				require.NoError(t, err)
				assert.False(t, filepath.IsLocal(rel), "neither record root may be inside the trace archive")
			}
			assert.Equal(t, mode, p.RecordMode())
			assert.NoDirExists(t, store)
			assert.NoDirExists(t, checkout)

			same, err := layout.NewRepository(store+string(filepath.Separator)+".", checkout, mode)
			require.NoError(t, err)
			assert.Equal(t, "found", map[layout.Policy]string{p: "found"}[same], "comparable, normalized value")
		})
	}
}

func TestRepositoryPolicyRejectsInvalidInput(t *testing.T) {
	abs := t.TempDir()
	for _, tt := range []struct {
		name, store, checkout string
		mode                  layout.RecordMode
	}{
		{"absent store", "", abs, layout.RecordBlame},
		{"absent checkout", abs, "", layout.RecordBlame},
		{"relative store", "relative", abs, layout.RecordBlame},
		{"relative checkout", abs, "relative", layout.RecordBlame},
		{"NUL store", abs + "\x00", abs, layout.RecordBlame},
		{"NUL checkout", abs, abs + "\x00", layout.RecordBlame},
		{"zero mode", abs, abs, 0},
		{"unknown mode", abs, abs, 255},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, err := layout.NewRepository(tt.store, tt.checkout, tt.mode)
			var validation *layout.ValidationError
			require.ErrorAs(t, err, &validation)
			assert.Equal(t, layout.Policy{}, p)
		})
	}
	var zero layout.Policy
	assert.Empty(t, zero.RecordRoot())
	assert.Empty(t, zero.LocalRoot())
	assert.Empty(t, zero.TraceArchiveRoot())
	assert.Empty(t, zero.CheckoutRecordRoot())
	assert.Empty(t, zero.TempDir())
	_, err := zero.LockPath("mission", "m-1")
	assert.Error(t, err)
	_, err = zero.LocalExtensionPath("bwk", "quarry")
	assert.Error(t, err)
}

func TestRepositoryLockAndOverlayMappings(t *testing.T) {
	base := t.TempDir()
	store := filepath.Join(base, "main")
	a, err := layout.NewRepository(store, store, layout.RecordBlame)
	require.NoError(t, err)
	b, err := layout.NewRepository(store, filepath.Join(base, "linked"), layout.RecordFull)
	require.NoError(t, err)
	x, err := a.LockPath("mission", "m-1")
	require.NoError(t, err)
	y, err := b.LockPath("mission", "m-1")
	require.NoError(t, err)
	assert.Equal(t, x, y, "mode and invoking checkout must not change a shared lock")
	assert.Equal(t, filepath.Join(store, ".punt-labs", "local", "ethos", "locks", "mission"), filepath.Dir(x))
	z, err := a.LockPath("delegation", "m-1")
	require.NoError(t, err)
	assert.NotEqual(t, x, z, "distinct lock domains remain distinct")
	z, err = a.LockPath("mission", "m-2")
	require.NoError(t, err)
	assert.NotEqual(t, x, z)
	// Keys are opaque, not paths. Even hostile keys cannot traverse directories.
	z, err = a.LockPath("mission", "../../source/secret\x00")
	require.NoError(t, err)
	assert.Equal(t, filepath.Dir(x), filepath.Dir(z))
	for _, domain := range []string{"", "..", "../mission", `x\y`, "x:y", "Mission", "0mission"} {
		_, err := a.LockPath(domain, "m-1")
		assert.Error(t, err, domain)
	}
	_, err = a.LockPath("mission", "")
	assert.Error(t, err)

	for _, p := range []layout.Policy{a, b} {
		overlay, err := p.LocalExtensionPath("bwk", "quarry")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(store, ".punt-labs", "local", "ethos", "identities", "bwk.ext", "quarry.local.yaml"), overlay)
		// Existing identity namespace validation permits a trailing hyphen.
		_, err = p.LocalExtensionPath("bwk", "quarry-")
		require.NoError(t, err)
		for _, handle := range []string{"0", "7agent", "a--b"} {
			overlay, err := p.LocalExtensionPath(handle, "quarry")
			require.NoError(t, err)
			assert.Equal(t, filepath.Join(store, ".punt-labs", "local", "ethos", "identities", handle+".ext", "quarry.local.yaml"), overlay)
		}
		for _, bad := range []string{"", ".", "..", "../bwk", `x\y`, "x:y", "bwk/child"} {
			_, err := p.LocalExtensionPath(bad, "quarry")
			assert.Error(t, err)
			_, err = p.LocalExtensionPath("bwk", bad)
			assert.Error(t, err)
		}
	}
}
