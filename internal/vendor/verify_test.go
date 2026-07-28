package vendor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// applied returns a fixture whose repo layer holds a real vendored set.
func applied(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.seedOrg(t)
	f.writeGlobal(t, "identities/bwk.ext/quarry.yaml", "memory_collection: bwk-mem\n")
	_, err := f.run(t, Options{Handles: []string{"bwk"}, Apply: true})
	require.NoError(t, err)
	return f
}

func TestCheckPassesOnACompleteSet(t *testing.T) {
	f := applied(t)

	rep, err := Check(f.repo)
	require.NoError(t, err)
	assert.True(t, rep.Complete())
	assert.False(t, rep.ExtUnverifiable)
	assert.Equal(t, []string{"bwk", "claudia"}, rep.Handles)
	assert.Contains(t, rep.Summary(), "2 identities")
	assert.NoError(t, rep.Err())
}

// Each dimension of the predicate, removed one file at a time. The
// report must name the exact reference so `ethos vendor` closes it.
func TestCheckNamesEveryMissingDimension(t *testing.T) {
	tests := []struct {
		name    string
		remove  string
		wantRef string
	}{
		{
			name:    "attribute",
			remove:  "personalities/kernighan.md",
			wantRef: "personalities/kernighan",
		},
		{
			name:    "team member identity",
			remove:  "identities/claudia.yaml",
			wantRef: "identities/claudia",
		},
		{
			name:    "team member role",
			remove:  "roles/writer.yaml",
			wantRef: "roles/writer",
		},
		{
			name:    "manifest-recorded extension",
			remove:  "identities/bwk.ext/quarry.yaml",
			wantRef: "ext/bwk/quarry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := applied(t)
			require.NoError(t, os.Remove(filepath.Join(f.repo, tt.remove)))

			rep, err := Check(f.repo)
			require.NoError(t, err)
			require.False(t, rep.Complete())

			var refs []string
			for _, m := range rep.Missing {
				refs = append(refs, m.String())
			}
			assert.Contains(t, refs, tt.wantRef)
			assert.Contains(t, rep.Err().Error(), tt.wantRef)
			assert.Contains(t, rep.Err().Error(), "does not resolve on its own")
		})
	}
}

// A directory listing cannot distinguish "this identity has no
// extensions" from "vendor omitted them". Without a manifest the ext
// dimension is unverifiable, and saying so is better than assuming it
// away.
func TestCheckReportsUnverifiableExtWithoutAManifest(t *testing.T) {
	f := applied(t)
	require.NoError(t, os.Remove(ManifestPath(f.repo)))

	rep, err := Check(f.repo)
	require.NoError(t, err)
	assert.True(t, rep.Complete(), "the rest of the set still resolves")
	assert.True(t, rep.ExtUnverifiable)
	assert.Contains(t, rep.Summary(), "unverifiable")

	// And the missing ext file it cannot see is genuinely not reported.
	require.NoError(t, os.Remove(filepath.Join(f.repo, "identities", "bwk.ext", "quarry.yaml")))
	rep, err = Check(f.repo)
	require.NoError(t, err)
	assert.True(t, rep.Complete())
}

// Parity is a subset check: a namespace someone added by hand is not an
// incompleteness, and a .local companion is never required.
func TestCheckExtParityIgnoresExtras(t *testing.T) {
	f := applied(t)
	extra := filepath.Join(f.repo, "identities", "bwk.ext", "vox.yaml")
	require.NoError(t, os.WriteFile(extra, []byte("provider: elevenlabs\n"), 0o644))

	rep, err := Check(f.repo)
	require.NoError(t, err)
	assert.True(t, rep.Complete())
}

func TestLoadManifestRefusesANewerVersion(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(ManifestPath(root),
		[]byte("version: 999\nidentities: []\n"), 0o644))

	_, err := LoadManifest(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upgrade ethos")
}

func TestLoadManifestAbsentIsNotAnError(t *testing.T) {
	m, err := LoadManifest(t.TempDir())
	require.NoError(t, err)
	assert.Nil(t, m)
	assert.Nil(t, m.RequiredExt(), "a nil manifest requires nothing")
	assert.Nil(t, m.Handles())
}
