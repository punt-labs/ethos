package provenance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
)

func TestProvenanceSquashFreshCloneAndLaterRenameInsertion(t *testing.T) {
	p, base := repositoryTest(t)
	r := p.Repository
	branchHead := gitTest(t, r, "rev-parse", "HEAD")
	receipt, err := p.Publish(context.Background(), selectedTest(t), base)
	require.NoError(t, err)
	gitTest(t, r, "commit", "-m", "publication")
	body, head, err := r.SquashMessage(context.Background(), "HEAD")
	require.NoError(t, err)
	assert.Equal(t, gitTest(t, r, "rev-parse", "HEAD"), head)
	assert.Contains(t, body, "Ethos-Provenance: "+receipt.Manifest)
	gitTest(t, r, "checkout", "main")
	gitTest(t, r, "merge", "--squash", "feature")
	gitTest(t, r, "commit", "-m", body)
	merge := gitTest(t, r, "rev-parse", "HEAD")
	gitTest(t, r, "branch", "-D", "feature")
	clonePath := filepath.Join(t.TempDir(), "fresh")
	gitTest(t, r, "clone", "--no-local", "--single-branch", "-b", "main", r.Root, clonePath)
	clone := Repository{Root: clonePath, Env: gitenv.Parsed()}
	_, err = clone.git(context.Background(), "cat-file", "-e", branchHead)
	require.Error(t, err, "documentary branch commits are not required reachable objects")
	verification, err := clone.VerifyCommit(context.Background(), merge)
	require.NoError(t, err)
	assert.Equal(t, []string{receipt.Manifest}, verification.Manifests)
	for _, tc := range []struct {
		line int
		want []Association
	}{
		{1, []Association{{missionA, "d-a"}}},
		{2, []Association{{missionB, "d-c"}}},
		{3, []Association{{missionA, "d-a"}, {missionA, "d-b"}}},
		{4, nil},
	} {
		pairs, err := clone.AtLine(context.Background(), merge, "source.txt", tc.line)
		require.NoError(t, err)
		assert.Equal(t, tc.want, pairs)
	}
	agent, err := clone.AgentAt(context.Background(), merge, Association{missionA, "d-b"})
	require.NoError(t, err)
	assert.Equal(t, "d-b", agent)
	gitTest(t, clone, "config", "user.name", "Later")
	gitTest(t, clone, "config", "user.email", "later@example.invalid")
	gitTest(t, clone, "mv", "source.txt", "renamed.txt")
	writeTest(t, clone.Root, "renamed.txt", []byte("inserted later\nknown A\nknown B\nambiguous\nbase four\n"))
	gitTest(t, clone, "add", "renamed.txt")
	gitTest(t, clone, "commit", "-m", "later insertion and rename")
	porcelain, err := clone.git(context.Background(), "blame", "--line-porcelain", "HEAD", "--", "renamed.txt")
	require.NoError(t, err)
	lines, err := ParseBlame(porcelain)
	require.NoError(t, err)
	require.Len(t, lines, 5)
	assert.Equal(t, "source.txt", lines[1].Path)
	assert.Equal(t, 1, lines[1].Original)
	assert.Equal(t, 2, lines[1].Final)
	pairs, err := clone.AtLine(context.Background(), lines[1].Commit, lines[1].Path, lines[1].Original)
	require.NoError(t, err)
	assert.Equal(t, []Association{{missionA, "d-a"}}, pairs)
	// Historical reads use the snapshot even when today's record is changed.
	writeTest(t, clone.Root, pairPath(Association{missionA, "d-a"}), []byte("mutable store is not authority"))
	agent, err = clone.AgentAt(context.Background(), merge, Association{missionA, "d-a"})
	require.NoError(t, err)
	assert.Equal(t, "d-a", agent)
}

func TestProvenanceIndexAndCommitRejectMissingAlteredUnknown(t *testing.T) {
	for _, kind := range []string{"missing", "altered", "unknown", "unknown-log", "unknown-delegation", "source", "manifest", "reference", "incomplete-results", "missing-decisions"} {
		t.Run(kind, func(t *testing.T) {
			p, base := repositoryTest(t)
			r := p.Repository
			receipt, err := p.Publish(context.Background(), selectedTest(t), base)
			require.NoError(t, err)
			switch kind {
			case "missing":
				gitTest(t, r, "update-index", "--force-remove", pairPath(Association{missionA, "d-a"}))
			case "altered":
				require.NoError(t, r.stageBytes(context.Background(), pairPath(Association{missionA, "d-a"}), []byte("altered\n")))
			case "unknown":
				require.NoError(t, r.stageBytes(context.Background(), ".punt-labs/ethos/missions/"+missionA+"/future.yaml", []byte("future: true\n")))
			case "unknown-log":
				require.NoError(t, r.stageBytes(context.Background(), ".punt-labs/ethos/missions/"+missionA+"/log-future.yaml", []byte("future: true\n")))
			case "unknown-delegation":
				require.NoError(t, r.stageBytes(context.Background(), ".punt-labs/ethos/missions/"+missionA+"/delegations/d-a/future/prompt.md", []byte("not a delegation prompt\n")))
			case "source":
				require.NoError(t, r.stageBytes(context.Background(), "source.txt", []byte("changed source\n")))
			case "manifest":
				require.NoError(t, r.stageBytes(context.Background(), receipt.Manifest, []byte("bad manifest\n")))
			case "incomplete-results", "missing-decisions":
				m, err := r.LoadManifest(context.Background(), ":", receipt.Manifest)
				require.NoError(t, err)
				name := ".punt-labs/ethos/missions/" + missionA + "/results.yaml"
				data := []byte("results: []\n")
				if kind == "missing-decisions" {
					name = ".punt-labs/ethos/missions/" + missionA + "/contract.yaml"
					data, err = r.read(context.Background(), ":", name)
					require.NoError(t, err)
					data = []byte(strings.Split(string(data), "round_decisions:")[0])
				}
				require.NoError(t, r.stageBytes(context.Background(), name, data))
				for i := range m.Records {
					if m.Records[i].Path == name {
						m.Records[i].SHA256 = Digest(data)
					}
				}
				newName, manifestBytes, err := Encode(*m)
				require.NoError(t, err)
				require.NoError(t, r.stageBytes(context.Background(), newName, manifestBytes))
				receipt.Manifest = newName
			}
			if kind != "reference" {
				require.Error(t, r.VerifyIndex(context.Background(), []string{receipt.Manifest}))
			}
			body := "squash\n\nEthos-Provenance: " + receipt.Manifest
			if kind == "reference" {
				body = "squash without reference"
			}
			gitTest(t, r, "commit", "-m", body)
			_, err = r.VerifyCommit(context.Background(), "HEAD")
			require.Error(t, err)
		})
	}
}

func TestProvenanceMessageRequiresFinalPublicationAndActualTree(t *testing.T) {
	p, base := repositoryTest(t)
	r := p.Repository
	_, _, err := r.SquashMessage(context.Background(), "HEAD")
	require.Error(t, err)
	_, err = p.Publish(context.Background(), selectedTest(t), base)
	require.NoError(t, err)
	// A path-limited commit that omits publication cannot generate a body.
	writeTest(t, r.Root, "only.txt", []byte("only\n"))
	gitTest(t, r, "add", "only.txt")
	gitTest(t, r, "commit", "--only", "-m", "omits publication", "--", "only.txt")
	_, _, err = r.SquashMessage(context.Background(), "HEAD")
	require.Error(t, err)
	gitTest(t, r, "commit", "-m", "publication")
	_, _, err = r.SquashMessage(context.Background(), "HEAD")
	require.NoError(t, err)
}

func TestProvenanceManifestSymlinkRecordCannotVerify(t *testing.T) {
	p, base := repositoryTest(t)
	r := p.Repository
	receipt, err := p.Publish(context.Background(), selectedTest(t), base)
	require.NoError(t, err)
	name := pairPath(Association{missionA, "d-a"})
	data, err := r.read(context.Background(), ":", name)
	require.NoError(t, err)
	blob := gitTest(t, r, "rev-parse", ":"+name)
	gitTest(t, r, "update-index", "--cacheinfo", "120000,"+blob+","+name)
	// The content hash alone is unchanged, but its tree type is not a record.
	assert.NotEmpty(t, data)
	assert.Error(t, r.VerifyIndex(context.Background(), []string{receipt.Manifest}))
	assert.NoFileExists(t, filepath.Join(r.Root, ".git", "index.lock"))
	_, err = os.Stat(filepath.Join(r.Root, "source.txt"))
	require.NoError(t, err)
}
