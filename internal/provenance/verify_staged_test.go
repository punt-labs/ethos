package provenance

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProvenanceStagedManifestSelection(t *testing.T) {
	p, base := repositoryTest(t)
	r := p.Repository
	names, err := r.StagedManifests(t.Context())
	require.NoError(t, err)
	assert.Empty(t, names)
	receipt, err := p.Publish(t.Context(), selectedTest(t), base)
	require.NoError(t, err)
	names, err = r.StagedManifests(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{receipt.Manifest}, names)
	require.NoError(t, r.VerifyIndex(t.Context(), names))

	// A second selected manifest cannot be skipped after the first passes.
	bad := Directory + strings.Repeat("f", 64) + ".yaml"
	writeTest(t, r.Root, bad, []byte("malformed: manifest\n"))
	gitTest(t, r, "add", bad)
	names, err = r.StagedManifests(t.Context())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{receipt.Manifest, bad}, names)
	require.Error(t, r.VerifyIndex(t.Context(), names))
	gitTest(t, r, "rm", "--cached", bad)
	require.NoError(t, os.Remove(filepath.Join(r.Root, filepath.FromSlash(bad))))
	gitTest(t, r, "commit", "-m", "publication")

	// Later contract versions intentionally differ from old snapshots. An
	// ordinary commit must not reverify that historical manifest against them.
	writeTest(t, r.Root, selectedTest(t).Records[0].Path, []byte("later contract version\n"))
	gitTest(t, r, "add", selectedTest(t).Records[0].Path)
	names, err = r.StagedManifests(t.Context())
	require.NoError(t, err)
	assert.Empty(t, names)
	require.Error(t, r.VerifyIndex(t.Context(), []string{receipt.Manifest}))

	writeTest(t, r.Root, receipt.Manifest, []byte("modified manifest\n"))
	gitTest(t, r, "add", receipt.Manifest)
	names, err = r.StagedManifests(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{receipt.Manifest}, names, "modifications require verification too")
	require.Error(t, r.VerifyIndex(t.Context(), names))
	gitTest(t, r, "rm", "-f", receipt.Manifest)
	names, err = r.StagedManifests(t.Context())
	require.NoError(t, err)
	assert.Empty(t, names, "deletions do not create provenance claims")
}

func TestProvenanceStagedManifestPathBoundaries(t *testing.T) {
	p, _ := repositoryTest(t)
	r := p.Repository
	// --name-only without -z would quote the tab and lose this exact identity.
	name := Directory + "odd\tname.yaml"
	writeTest(t, r.Root, name, []byte("invalid manifest\n"))
	writeTest(t, r.Root, strings.TrimSuffix(Directory, "/")+"-other/not-a-manifest", []byte("unrelated\n"))
	gitTest(t, r, "add", ".punt-labs/ethos")
	names, err := r.StagedManifests(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{name}, names)
	require.Error(t, r.VerifyIndex(t.Context(), names))
}

func TestProvenanceStagedManifestUnbornBranch(t *testing.T) {
	p, _ := repositoryTest(t)
	r := p.Repository
	gitTest(t, r, "checkout", "--orphan", "unborn")
	name := Directory + strings.Repeat("a", 64) + ".yaml"
	writeTest(t, r.Root, name, []byte("invalid manifest\n"))
	gitTest(t, r, "add", name)
	names, err := r.StagedManifests(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{name}, names)
	require.Error(t, r.VerifyIndex(t.Context(), names))
}

func TestProvenanceStagedVerificationChecksInventory(t *testing.T) {
	for _, kind := range []string{"missing-record", "altered-record", "omitted-inventory"} {
		t.Run(kind, func(t *testing.T) {
			p, base := repositoryTest(t)
			r := p.Repository
			receipt, err := p.Publish(t.Context(), selectedTest(t), base)
			require.NoError(t, err)
			path := selectedTest(t).Records[0].Path
			switch kind {
			case "missing-record":
				gitTest(t, r, "rm", "--cached", path)
			case "altered-record":
				writeTest(t, r.Root, path, []byte("altered record\n"))
				gitTest(t, r, "add", path)
			case "omitted-inventory":
				path = ".punt-labs/ethos/missions/" + missionA + "/delegations/d-extra/record.yaml"
				writeTest(t, r.Root, path, []byte("id: d-extra\n"))
				gitTest(t, r, "add", path)
			}
			names, err := r.StagedManifests(t.Context())
			require.NoError(t, err)
			assert.Equal(t, []string{receipt.Manifest}, names)
			_, before := indexBytesTest(t, r)
			require.Error(t, r.VerifyIndex(t.Context(), names))
			_, after := indexBytesTest(t, r)
			assert.True(t, bytes.Equal(before, after), "verification must not rewrite the index")
		})
	}
}
