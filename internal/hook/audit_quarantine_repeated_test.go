package hook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/stretchr/testify/require"
)

func TestRepositoryPublishedRepeatedRetirementClonePreservesFloor(t *testing.T) {
	for _, keepLocal := range []bool{false, true} {
		name := "published-marker-only"
		if keepLocal {
			name = "local-and-published-markers"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			root := t.TempDir()
			initGitRepo(t, root)
			p := hookPolicy(t, root, root, layout.RecordBlame)
			s := audit.Stream{Namespace: audit.SessionNS, Session: "s"}
			cn := audit.ChunkName{Namespace: audit.SessionNS, First: 100, Last: 200}
			rel := filepath.Join(".punt-labs", "ethos", "sessions", "2026-11-04-s")
			source := filepath.Join(root, rel, cn.ChunkFile())
			first := []byte(`{"ts":"` + audit.FormatLineTS(300) + `","tool":"Read"}` + "\ncorruption\n")
			require.NoError(t, audit.WriteLocalAtomic(p, source, first))
			policyGit(t, root, "add", "--", source)
			policyGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "fixture published damage")
			_, err := QuarantineChunkWithPolicy(p, source, "first retirement")
			require.NoError(t, err)
			markerPath := filepath.Join(root, rel, cn.MarkerFile())
			firstMarker, err := audit.ReadMarker(markerPath)
			require.NoError(t, err)
			require.Equal(t, int64(300), firstMarker.VerifiedLast)
			policyGit(t, root, "add", "--", source, markerPath)
			policyGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "fixture first retirement")
			if !keepLocal {
				clone := filepath.Join(t.TempDir(), "clone")
				policyGit(t, root, "clone", "-q", "--no-local", root, clone)
				root = clone
				p = hookPolicy(t, root, root, layout.RecordBlame)
				source = filepath.Join(root, rel, cn.ChunkFile())
				markerPath = filepath.Join(root, rel, cn.MarkerFile())
			}
			require.NoError(t, audit.WriteLocalAtomic(p, source, []byte("second damage has no timestamp\n")))
			before := policyGit(t, root, "ls-files", "--stage")
			_, err = QuarantineChunkWithPolicy(p, source, "second retirement")
			require.NoError(t, err)
			require.Equal(t, before, policyGit(t, root, "ls-files", "--stage"), "repeated retirement must not stage")
			secondMarker, err := audit.ReadMarker(markerPath)
			require.NoError(t, err)
			require.Empty(t, secondMarker.RetiredSHA256, "published availability must not claim local completion")
			require.GreaterOrEqual(t, secondMarker.VerifiedLast, firstMarker.VerifiedLast)
			require.Equal(t, firstMarker.UnrecoveredFirst, secondMarker.UnrecoveredFirst)
			require.Equal(t, firstMarker.UnrecoveredLast, secondMarker.UnrecoveredLast)
			policyGit(t, root, "add", "--", markerPath)
			policyGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "fixture repeated retirement")
			clone := filepath.Join(t.TempDir(), "clone")
			policyGit(t, root, "clone", "-q", "--no-local", root, clone)
			cp := hookPolicy(t, clone, clone, layout.RecordBlame)
			data, err := audit.ReadStream(cp, s)
			require.NoError(t, err)
			require.Equal(t, int64(300), data.Floor, "a fresh clone must retain the verified floor without S-local evidence")
			require.Len(t, data.Gaps, 1)
			require.Equal(t, int64(300), data.Gaps[0].Last)
			_, err = os.Stat(cp.LocalRoot())
			require.True(t, os.IsNotExist(err), "the clone assertion must not depend on retained local files")
		})
	}
}
