package autorepair

import (
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

// seedRepoOverlay puts a repository identity and its legacy overlay (the
// values a corpus release wrote to its global store) in the repository.
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

			again := s81113Run(t, "overlays", env)
			assert.Equal(t, 0, again.Items, "second run changes nothing")
		})
	}
}

func TestOverlaysStepBothLocationsKeepsNew(t *testing.T) {
	tr := s81113Copy(t, "damage/overlay-both-locations")
	env := tr.Env(t)
	base, legacy, local := overlayPaths(tr)
	// The new location is git-ignored, so the fixture cannot carry it.
	s81113Write(t, local, "notify: \"n\"\nvoice: newer\n")
	newBytes := s81113Read(t, local)
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
	again := s81113Run(t, "overlays", env)
	assert.Equal(t, 0, again.Items)
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
