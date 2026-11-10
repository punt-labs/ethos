package autorepair

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/identity"
)

const overlayHandle = "tperson"

func overlayPaths(tr s81113Tree) (base, legacy, local string) {
	base = filepath.Join(tr.Repo, ".punt-labs", "ethos", "identities", overlayHandle+".yaml")
	legacy = filepath.Join(tr.Repo, ".punt-labs", "ethos", "identities", overlayHandle+".ext", "vox.local.yaml")
	local = filepath.Join(tr.Repo, ".punt-labs", "local", "ethos", "identities", overlayHandle+".ext", "vox.local.yaml")
	return base, legacy, local
}

// overlayStore is the repository identity store as the current binary reads it.
func overlayStore(t *testing.T, env Env) *identity.Store {
	t.Helper()
	return identity.NewStore(env.Policy.RecordRoot(), identity.RepositoryPolicy(env.Policy))
}

// seedRepoOverlay puts a repository identity and its legacy overlay in the
// repository. The corpora v4.13.0, v4.17.0 and v4.20.4 hold no repository
// overlays, so the files are seeded from the values the same corpus release
// wrote to its global store; they are not captured from a real release.
func seedRepoOverlay(t *testing.T, tr s81113Tree) (base, legacy, local string) {
	t.Helper()
	base, legacy, local = overlayPaths(tr)
	gid := filepath.Join(tr.Home, "identities")
	s81113Write(t, base, string(s81113Read(t, filepath.Join(gid, overlayHandle+".yaml"))))
	s81113Write(t, legacy, string(s81113Read(t, filepath.Join(gid, overlayHandle+".ext", "vox.local.yaml"))))
	s81113Write(t, filepath.Join(filepath.Dir(legacy), "vox.yaml"), string(s81113Read(t, filepath.Join(gid, overlayHandle+".ext", "vox.yaml"))))
	return base, legacy, local
}

func TestOverlaysStepMovesLegacyOverlay(t *testing.T) {
	for _, fixture := range []string{"corpus/v4.13.0", "corpus/v4.17.0", "corpus/v4.20.4"} {
		t.Run(fixture, func(t *testing.T) {
			tr := s81113Copy(t, fixture)
			env := tr.Env(t)
			base, legacy, local := seedRepoOverlay(t, tr)
			want := s81113Read(t, legacy)
			baseBefore := s81113Read(t, base)
			ext := filepath.Join(filepath.Dir(legacy), "vox.yaml")
			extBefore := s81113Read(t, ext)

			out := s81113Run(t, "overlays", env)
			assert.Equal(t, 1, out.Items)
			assert.False(t, out.Deferred)
			assert.False(t, s81113Exists(legacy), "legacy overlay must be gone")
			assert.Equal(t, want, s81113Read(t, local), "overlay bytes move unchanged")
			assert.Equal(t, baseBefore, s81113Read(t, base), "base identity is byte-for-byte unchanged")
			assert.Equal(t, extBefore, s81113Read(t, ext), "base extension is untouched")

			got, err := overlayStore(t, env).ExtGet(overlayHandle, "vox", "")
			require.NoError(t, err)
			assert.Equal(t, "test-voice", got["voice"])
			assert.Equal(t, "y", got["notify"])

			require.NotEmpty(t, tr.Log.entries)
			for _, e := range tr.Log.entries {
				assert.Equal(t, "overlays", e.step)
				assert.NoError(t, e.err)
				for _, p := range e.paths {
					assert.NotContains(t, p, "test-voice")
				}
			}

			after := s81113Snapshot(t, tr)
			again := s81113Run(t, "overlays", env)
			assert.Equal(t, 0, again.Items, "second run changes nothing")
			assert.Equal(t, after, s81113Snapshot(t, tr), "second run leaves the tree identical")
		})
	}
}

func TestOverlaysStepBothLocationsKeepsNew(t *testing.T) {
	tr := s81113Copy(t, "damage/overlay-both-locations")
	env := tr.Env(t)
	base, legacy, local := overlayPaths(tr)
	require.True(t, s81113Exists(legacy), "fixture holds the legacy copy")
	require.True(t, s81113Exists(local), "fixture holds the new copy")
	newBytes := s81113Read(t, local)
	require.NotEqual(t, s81113Read(t, legacy), newBytes)
	baseBefore := s81113Read(t, base)

	out := s81113Run(t, "overlays", env)
	assert.Equal(t, 1, out.Items)
	assert.False(t, s81113Exists(legacy))
	assert.Equal(t, newBytes, s81113Read(t, local), "the new overlay is kept byte for byte")
	assert.Equal(t, baseBefore, s81113Read(t, base))

	got, err := overlayStore(t, env).ExtGet(overlayHandle, "vox", "voice")
	require.NoError(t, err)
	assert.Equal(t, "newer", got["voice"])

	for _, e := range tr.Log.entries {
		for _, p := range e.paths {
			assert.NotContains(t, p, "newer")
		}
	}
	after := s81113Snapshot(t, tr)
	again := s81113Run(t, "overlays", env)
	assert.Equal(t, 0, again.Items)
	assert.Equal(t, after, s81113Snapshot(t, tr), "second run leaves the tree identical")
}

func TestOverlaysStepUnreadableNewKeepsBothAndRecordsResidual(t *testing.T) {
	tr := s81113Copy(t, "damage/overlay-both-locations")
	env := tr.Env(t)
	_, legacy, local := overlayPaths(tr)
	s81113Write(t, local, "{{{ not yaml")
	legacyBytes := s81113Read(t, legacy)

	out := s81113Run(t, "overlays", env)
	assert.Equal(t, 0, out.Items)
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, "overlays", out.Residuals[0].Step)
	assert.Equal(t, overlayHandle+"/vox", out.Residuals[0].ID)
	assert.Equal(t, legacyBytes, s81113Read(t, legacy), "the readable legacy copy survives")
	assert.Equal(t, "{{{ not yaml", string(s81113Read(t, local)))
}

func TestOverlaysStepSymlinkedLegacyIsResidual(t *testing.T) {
	tr := s81113Copy(t, "damage/overlay-both-locations")
	env := tr.Env(t)
	_, legacy, local := overlayPaths(tr)
	require.NoError(t, os.Remove(legacy))
	require.NoError(t, os.Remove(local))
	target := filepath.Join(filepath.Dir(tr.Repo), "elsewhere.yaml")
	s81113Write(t, target, "notify: y\n")
	require.NoError(t, os.Symlink(target, legacy))

	out := s81113Run(t, "overlays", env)
	assert.Equal(t, 0, out.Items)
	require.Len(t, out.Residuals, 1)
	assert.Contains(t, out.Residuals[0].Reason, legacy)
	_, err := os.Lstat(legacy)
	assert.NoError(t, err, "the symlink is left alone")
}

func TestOverlaysStepMovedOverlayIs0600(t *testing.T) {
	tr := s81113Copy(t, "damage/overlay-both-locations")
	env := tr.Env(t)
	_, legacy, local := overlayPaths(tr)
	require.NoError(t, os.Remove(local))
	require.NoError(t, os.Chmod(legacy, 0o644))

	s81113Run(t, "overlays", env)
	info, err := os.Stat(local)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestOverlaysStepLeavesCurrentLayoutAlone(t *testing.T) {
	for _, fixture := range []string{"corpus/v4.17.0", "corpus/v4.22.1", "corpus/v3.11.0"} {
		t.Run(fixture, func(t *testing.T) {
			tr := s81113Copy(t, fixture)
			globalOverlay := filepath.Join(tr.Home, "identities", overlayHandle+".ext", "vox.local.yaml")
			var before []byte
			if s81113Exists(globalOverlay) {
				before = s81113Read(t, globalOverlay)
			}
			out := s81113Run(t, "overlays", tr.Env(t))
			assert.Equal(t, 0, out.Items)
			assert.False(t, out.Deferred)
			assert.Empty(t, tr.Log.entries)
			if before != nil {
				assert.Equal(t, before, s81113Read(t, globalOverlay), "global overlays are not relocated")
			}
		})
	}
}

func TestOverlaysStepSymlinkedIdentityDirIsResidualAndOthersProceed(t *testing.T) {
	tr := s81113Copy(t, "damage/overlay-both-locations")
	env := tr.Env(t)
	_, legacy, local := overlayPaths(tr)
	require.NoError(t, os.Remove(local))
	dir := filepath.Join(tr.Repo, ".punt-labs", "ethos", "identities")
	// A symlinked extension directory sorts before tperson.ext.
	target := filepath.Join(filepath.Dir(tr.Repo), "elsewhere")
	s81113Write(t, filepath.Join(target, "vox.local.yaml"), "notify: y\n")
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "aperson.ext")))

	out, err := runOverlays(t.Context(), env)
	require.NoError(t, err, "a refused path is a residual, not an error")
	assert.Equal(t, 1, out.Items, "the other handle is still relocated")
	assert.False(t, s81113Exists(legacy))
	assert.NotEmpty(t, out.Residuals)
}

func TestOverlaysStepSymlinkedLocalOverlayIsResidual(t *testing.T) {
	tr := s81113Copy(t, "damage/overlay-both-locations")
	_, legacy, local := overlayPaths(tr)
	require.NoError(t, os.Remove(local))
	target := filepath.Join(filepath.Dir(tr.Repo), "elsewhere.yaml")
	s81113Write(t, target, "notify: y\n")
	require.NoError(t, os.Symlink(target, local))

	out, err := runOverlays(t.Context(), tr.Env(t))
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items)
	require.Len(t, out.Residuals, 1)
	assert.True(t, s81113Exists(legacy), "the legacy copy is kept")
}

// The released corpora hold no repository overlay, so this fixture is a
// hand-made legacy-location overlay (damage/overlay-both-locations minus the
// new copy); the move is not captured from a real release.
func TestOverlaysStepHandMadeLegacyFixtureMoves(t *testing.T) {
	tr := s81113Copy(t, "damage/overlay-both-locations")
	_, legacy, local := overlayPaths(tr)
	require.NoError(t, os.Remove(local))
	out := s81113Run(t, "overlays", tr.Env(t))
	assert.Equal(t, 1, out.Items)
	assert.False(t, s81113Exists(legacy))
	assert.True(t, s81113Exists(local))
}
