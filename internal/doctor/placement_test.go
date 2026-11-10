package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPhysicalPlacementIsNotClassificationOrTracking(t *testing.T) {
	for _, rel := range []string{
		".punt-labs/ethos/identities/bwk.ext/quarry.local.yaml",
		".punt-labs/ethos/identities/bwk.yaml.lock",
		".punt-labs/ethos/missions/m-1/contract.yaml.ethos-tmp",
		".punt-labs/ethos/missions/.m-1.migrate-old/contract.yaml",
		".punt-labs/ethos/missions/m-1/log.jsonl",
		".punt-labs/ethos/sessions/date/audit.jsonl",
		".punt-labs/ethos/missions/m-1/artifacts/result.md",
	} {
		t.Run(rel, func(t *testing.T) {
			root := gitRepo(t)
			path := filepath.Join(root, filepath.FromSlash(rel))
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte("retained\n"), 0o600))
			// Independent physical-root assertion, not the classifier's answer.
			physical, err := filepath.EvalSymlinks(path)
			require.NoError(t, err)
			assert.Equal(t, path, physical)
			assert.FileExists(t, filepath.Join(root, filepath.FromSlash(rel)))
			_, _, recognized := layout.Classify(rel)
			require.True(t, recognized, "the old classification-only check accepts this file")
			require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".punt-labs/ethos/\n.punt-labs/local/\n"), 0o600))
			assert.Equal(t, "PASS", CheckLocalExtNotTracked(root).Status, "untracked/ignored is a separate question")
			r := CheckPhysicalPlacement(root)
			assert.Equal(t, "FAIL", r.Status, r.Detail)
			assert.Contains(t, r.Detail, rel)
			assert.Contains(t, r.Detail, "recognized legacy data found at ")
			assert.Contains(t, r.Detail, "(agent-owned artifacts are not legacy)")
			assert.NotContains(t, r.Detail, "is moved")
			assert.FileExists(t, path, "doctor must not remove legacy bytes")
		})
	}
}

func TestPhysicalPlacementAllowsPublishedTraceAndLocalData(t *testing.T) {
	root := gitRepo(t)
	for _, rel := range []string{
		".punt-labs/ethos/missions/m-1/reflections.yaml",
		".punt-labs/ethos/missions/m-1/delegations/d-1/prompt.md",
		".punt-labs/ethos/missions/m-1/log-000001-000002.jsonl",
		".punt-labs/ethos/sessions/date/audit-000001-000002.jsonl",
		".punt-labs/ethos/exported/m-1.contract.yaml",
		".punt-labs/local/ethos/sessions/session.audit.jsonl",
		".punt-labs/local/ethos/tmp/test.ethos-tmp",
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte("retained\n"), 0o600))
	}
	assert.Equal(t, "PASS", CheckPhysicalPlacement(root).Status)
}

func TestPhysicalPlacementDoesNotFollowRedirectedZones(t *testing.T) {
	for _, zone := range []string{".punt-labs", ".punt-labs/ethos", ".punt-labs/local", ".punt-labs/local/ethos"} {
		t.Run(zone, func(t *testing.T) {
			root, other := t.TempDir(), t.TempDir()
			path := filepath.Join(root, filepath.FromSlash(zone))
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.Symlink(other, path))
			r := CheckPhysicalPlacement(root)
			assert.Equal(t, "FAIL", r.Status)
			assert.Contains(t, r.Detail, zone+" is a symlink")
		})
	}
}

func TestPhysicalPlacementChecksSharedStoreAndCheckout(t *testing.T) {
	store, checkout, s, teams := currencyFixture(t)
	path := filepath.Join(store, ".punt-labs", "ethos", "sessions", "old", "audit.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("retained old tail\n"), 0o600))
	rows := RunAll(s, session.NewStore(t.TempDir()), checkout, nil, store, nil, teams)
	var placement []Result
	for _, row := range rows {
		if row.Name == "Physical storage placement" {
			placement = append(placement, row)
		}
	}
	require.Len(t, placement, 2)
	assert.Equal(t, "PASS", placement[0].Status, "C contains only legitimate sources")
	assert.Equal(t, "FAIL", placement[1].Status, "legacy runtime bytes belong to shared S")
	assert.Contains(t, placement[1].Detail, store)
	assert.FileExists(t, path)
}

func TestPhysicalPlacementFindsRecognizableRuntimeBeyondClassTable(t *testing.T) {
	for _, rel := range []string{
		".punt-labs/ethos/tmp/abandoned.md",
		".punt-labs/ethos/locks/agent.lock",
		".punt-labs/ethos/adrs/adr.yaml.lock",
		".punt-labs/ethos/missions/m-1/reflection-intent.yaml",
		".punt-labs/ethos/sessions/old/audit-1.quarantine",
	} {
		t.Run(rel, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, filepath.FromSlash(rel))
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte("retained\n"), 0o600))
			r := CheckPhysicalPlacement(root)
			assert.Equal(t, "FAIL", r.Status, r.Detail)
			assert.Contains(t, r.Detail, rel)
		})
	}
}

func TestTrackedLegacyLocalPathsAreExactAndTraceIsNotAllLocal(t *testing.T) {
	root := gitRepo(t)
	paths := []string{
		".punt-labs/ethos/identities/bwk.ext/quarry.local.yaml",
		".punt-labs/ethos/missions/m-1/contract.yaml.lock",
		".punt-labs/local/ethos/trace/private.jsonl",
	}
	trace := ".punt-labs/ethos/missions/m-1/log-000001-000002.jsonl"
	for _, rel := range append(append([]string(nil), paths...), trace) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte("retained\n"), 0o600))
	}
	out, err := testhelpers.GitCommand("-C", root, "add", ".punt-labs").CombinedOutput()
	require.NoError(t, err, string(out))
	r := CheckLocalExtNotTracked(root)
	assert.Equal(t, "FAIL", r.Status)
	for _, rel := range paths {
		assert.Contains(t, r.Detail, rel)
		assert.FileExists(t, filepath.Join(root, filepath.FromSlash(rel)))
	}
	assert.NotContains(t, r.Detail, trace)
	assert.NotContains(t, r.Detail, "git rm --cached")
}
