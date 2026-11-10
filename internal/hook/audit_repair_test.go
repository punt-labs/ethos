package hook

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

func TestRepositorySealAggregatesPromptAndArchiveFailures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root, checkout := t.TempDir(), t.TempDir()
	full := hookPolicy(t, root, checkout, layout.RecordFull)
	blame := hookPolicy(t, root, checkout, layout.RecordBlame)
	const mid, did = "m-2026-11-05-740", "d-2026-11-05-740"
	blocker := filepath.Join(full.CheckoutRecordRoot(), "missions")
	require.NoError(t, os.MkdirAll(filepath.Dir(blocker), 0o700))
	require.NoError(t, os.WriteFile(blocker, []byte("block publication"), 0o600))
	_, err := mission.WriteDelegationSkeletonWithPolicy(full, mid, did, mission.DelegationSkeleton{Tier: mission.TierB, Prompt: []byte("captured prompt")})
	require.ErrorContains(t, err, "pending")
	require.NoError(t, os.Remove(blocker))
	published := filepath.Join(blocker, mid, "delegations", did, "prompt.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(published), 0o700))
	require.NoError(t, os.WriteFile(published, []byte("conflicting publication"), 0o600))
	bad := audit.Stream{Namespace: audit.SessionNS, Session: "a-old"}
	dir, err := audit.ArchiveDir(blame, bad, "2026-11-04")
	require.NoError(t, err)
	require.NoError(t, audit.WriteLocalAtomic(blame, filepath.Join(dir, audit.SessionChunkFile(100, 100)), []byte("historical corruption\n")))
	policyAppend(t, blame, "Read")
	t.Setenv("PATH", t.TempDir()) // all policy inputs are resolved; no Git use
	res, err := SealRepoWithPolicy(blame, time.Now(), SealOptions{})
	require.ErrorContains(t, err, "a-old")
	var publication *PromptPublicationRetryError
	require.ErrorAs(t, err, &publication, "publication failure must not become a warn-only archival error")
	require.Contains(t, publication.Error(), "prompt")
	require.Equal(t, 1, res.LinesSealed)
	n, err := audit.UnarchivedCount(blame, audit.Stream{Namespace: audit.SessionNS, Session: "s"})
	require.NoError(t, err)
	require.Zero(t, n, "completed local archive survives the publication conflict")
	local := filepath.Join(full.LocalRoot(), "trace", "missions", mid, "delegations", did, "prompt.md")
	retained, err := os.ReadFile(local)
	require.NoError(t, err)
	require.Equal(t, "captured prompt", string(retained))
	unchanged, err := os.ReadFile(published)
	require.NoError(t, err)
	require.Equal(t, "conflicting publication", string(unchanged))
}

func TestRepositoryPublishedQuarantineCloneRetainsGap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	initGitRepo(t, root)
	p := hookPolicy(t, root, root, layout.RecordFull)
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: 100, Last: 200}
	source := filepath.Join(p.RecordRoot(), "sessions", "2026-11-04-s", cn.ChunkFile())
	require.NoError(t, audit.WriteLocalAtomic(p, source, []byte("corrupt original\n")))
	policyGit(t, root, "add", "--", source)
	policyGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "published source")
	before := policyGit(t, root, "ls-files", "--stage")
	_, err := QuarantineChunkWithPolicy(p, source, "fixture corruption")
	require.NoError(t, err)
	require.Equal(t, before, policyGit(t, root, "ls-files", "--stage"), "quarantine must not mutate the index")
	markerPath := filepath.Join(filepath.Dir(source), cn.MarkerFile())
	m, err := audit.ReadMarker(markerPath)
	require.NoError(t, err)
	require.True(t, m.HasGap())
	require.NoFileExists(t, source)
	_, err = SealRepoWithPolicy(p, time.Now(), SealOptions{})
	require.NoError(t, err)
	require.Equal(t, before, policyGit(t, root, "ls-files", "--stage"), "full seal must not silently stage an incomplete retirement set")
	policyGit(t, root, "add", "--", source, markerPath)
	policyGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "record explicit retirement")
	clone := filepath.Join(t.TempDir(), "clone")
	policyGit(t, root, "clone", "-q", "--no-local", root, clone)
	cp := hookPolicy(t, clone, clone, layout.RecordBlame)
	diag, err := CollectAuditDiagnosticsWithPolicy(cp)
	require.NoError(t, err)
	require.Len(t, diag.Gaps, 1, "fresh snapshot must distinguish unavailable history from empty success")
	require.Equal(t, int64(100), diag.Gaps[0].First)
	require.Equal(t, int64(200), diag.Gaps[0].Last)
	files, err := filepath.Glob(filepath.Join(p.LocalRoot(), "quarantine", "*", "*"))
	require.NoError(t, err)
	require.Empty(t, files, "no raw copy of the damaged bytes is kept once recovery completes")
	require.NotContains(t, policyGit(t, root, "ls-files"), ".corrupt")
}

func TestRepositoryMigrationGuardsExistingAndNewPayloads(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "immutable"}[existing], func(t *testing.T) {
			root, global := t.TempDir(), t.TempDir()
			p := hookPolicy(t, root, root, layout.RecordBlame)
			dir := repoSessionDir(t, root, "2026-11-04", "s")
			require.NoError(t, os.MkdirAll(dir, 0o700))
			secret := "ghp_" + strings.Repeat("a1B2", 9)
			line := []byte(`{"ts":"` + audit.FormatLineTS(100) + `","tool":"Read","tool_input_hash":"h","unknown":"` + secret + `"}` + "\n")
			legacy := filepath.Join(global, "s.audit.jsonl")
			require.NoError(t, os.WriteFile(legacy, line, 0o600))
			if existing {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "audit.jsonl"), line, 0o600))
			}
			err := MigrateAuditWithPolicy(p, global, false, io.Discard)
			original, readErr := os.ReadFile(legacy)
			require.NoError(t, readErr)
			require.Equal(t, line, original)
			if existing {
				require.ErrorContains(t, err, "unsafe immutable legacy")
				unchanged, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
				require.NoError(t, err)
				require.Equal(t, line, unchanged)
			} else {
				require.NoError(t, err)
				archive, err := audit.ArchiveDir(p, audit.Stream{Namespace: audit.SessionNS, Session: "s"}, "2026-11-04")
				require.NoError(t, err)
				body, err := os.ReadFile(filepath.Join(archive, "audit.jsonl"))
				require.NoError(t, err)
				require.NotContains(t, string(body), secret)
			}
		})
	}
}

func TestRepositoryGlobalLegacyConflictIsNotSuppressed(t *testing.T) {
	root, global := t.TempDir(), t.TempDir()
	p := hookPolicy(t, root, root, layout.RecordBlame)
	s := audit.Stream{Namespace: audit.SessionNS, Session: "s"}
	dir, err := audit.ArchiveDir(p, s, "2026-11-04")
	require.NoError(t, err)
	line := []byte(`{"ts":"` + audit.FormatLineTS(100) + `","tool":"Read","tool_input_hash":"h","delegation_id":"one"}` + "\n")
	require.NoError(t, audit.WriteLocalAtomic(p, filepath.Join(dir, "audit.jsonl"), line))
	require.NoError(t, os.WriteFile(filepath.Join(global, "s.audit.jsonl"), bytes.ReplaceAll(line, []byte("one"), []byte("two")), 0o600))
	_, err = QueryAuditByDelegationWithPolicy(p, global, "one")
	require.ErrorContains(t, err, "legacy audit identity conflict")
	err = MigrateAuditWithPolicy(p, global, false, io.Discard)
	require.ErrorContains(t, err, "legacy audit identity conflict")
}
