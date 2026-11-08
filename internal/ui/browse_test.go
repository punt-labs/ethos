package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/provenance"
)

func browseGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := gitenv.Output(root, "git", args...)
	require.NoError(t, err, "git %v", args)
	return strings.TrimSpace(string(out))
}

func browseWrite(t *testing.T, root, name string, data []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
	require.NoError(t, os.WriteFile(p, data, 0o600))
}

func browseRepository(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	root := t.TempDir()
	browseGit(t, root, "init", "-b", "main")
	browseGit(t, root, "config", "user.name", "Fixture")
	browseGit(t, root, "config", "user.email", "fixture@example.invalid")
	return root
}

func TestProvenanceBrowseHistoricalCoordinatesAndAmbiguity(t *testing.T) {
	root := browseRepository(t)
	browseWrite(t, root, "old.txt", []byte("known\nambiguous\nunknown\n"))
	browseGit(t, root, "add", "old.txt")
	browseGit(t, root, "commit", "-m", "source")
	head := browseGit(t, root, "rev-parse", "HEAD")
	blob := browseGit(t, root, "rev-parse", "HEAD:old.txt")
	id := "m-2026-11-04-001"
	stamp := "2026-11-04T10:00:00Z"
	m := provenance.Manifest{SchemaVersion: 1, BranchHead: head, Base: head, Missions: []string{id}, Associations: []provenance.Association{{Mission: id, Delegation: "d-a"}, {Mission: id, Delegation: "d-b"}}}
	records := map[string][]byte{}
	prefix := ".punt-labs/ethos/missions/" + id + "/"
	c := mission.Contract{MissionID: id, Status: mission.StatusOpen, CreatedAt: stamp, UpdatedAt: stamp, Leader: "lead", Worker: "bwk", Evaluator: mission.Evaluator{Handle: "rsc", PinnedAt: stamp}, WriteSet: []string{"old.txt"}, SuccessCriteria: []string{"checked"}, Budget: mission.Budget{Rounds: 1}, CurrentRound: 1, RoundDecisions: &mission.RoundDecisions{Version: 1, Entries: []mission.RoundDecision{}}}
	var err error
	records[prefix+"contract.yaml"], err = yaml.Marshal(c)
	require.NoError(t, err)
	records[prefix+"corrections.yaml"] = []byte("version: 1\nmission: " + id + "\nentries: []\n")
	for _, did := range []string{"d-a", "d-b"} {
		records[prefix+"delegations/"+did+"/record.yaml"], err = yaml.Marshal(mission.Delegation{ID: did, Mission: id, Tier: mission.TierB, AgentType: did, CreatedAt: stamp, Verdict: "pass"})
		require.NoError(t, err)
	}
	for name, data := range records {
		browseWrite(t, root, name, data)
		m.Records = append(m.Records, provenance.Record{Path: name, SHA256: provenance.Digest(data)})
	}
	evidence := provenance.Evidence{Commit: head, Path: "old.txt", Line: 1, Missions: []string{id}, Delegations: []string{"d-a"}}
	ambiguous := evidence
	ambiguous.Delegations = []string{"d-a", "d-b"}
	unknown := evidence
	unknown.Delegations = nil
	m.Sources = []provenance.Source{{Path: "old.txt", Blob: blob, Ranges: []provenance.Range{
		{Start: 1, End: 1, Candidates: m.Associations[:1], Evidence: evidence},
		{Start: 2, End: 2, Candidates: m.Associations, Evidence: ambiguous, Unknown: "multiple recorded candidates"},
		{Start: 3, End: 3, Evidence: unknown, Unknown: "no recorded tuple"},
	}}}
	name, data, err := provenance.Encode(m)
	require.NoError(t, err)
	browseWrite(t, root, name, data)
	// Create a squash-like commit introducing source and its records together.
	// git blame must name the manifest-bearing commit, not the branch source.
	browseGit(t, root, "add", ".punt-labs")
	browseGit(t, root, "commit", "--amend", "-m", "squash\n\nEthos-Provenance: "+name)
	merge := browseGit(t, root, "rev-parse", "HEAD")
	r := provenance.Repository{Root: root, Env: gitenv.Parsed()}
	_, err = r.VerifyCommit(context.Background(), merge)
	require.NoError(t, err)
	browseGit(t, root, "mv", "old.txt", "new.txt")
	browseWrite(t, root, "new.txt", []byte("later\nknown\nambiguous\nunknown\n"))
	browseGit(t, root, "add", "new.txt")
	browseGit(t, root, "commit", "-m", "insert and rename")
	s := &Server{repoRoot: root, storeRoot: t.TempDir()}
	lines, notices := s.gitBlameWithTrailers("new.txt")
	require.Len(t, lines, 4)
	assert.Empty(t, lines[0].Mission)
	assert.Equal(t, id, lines[1].Mission)
	assert.Equal(t, "d-a", lines[1].Agent)
	assert.Equal(t, "old.txt", lines[1].OriginalPath)
	assert.Equal(t, 1, lines[1].OriginalLine)
	assert.Empty(t, lines[2].Mission, "do not choose the first candidate")
	assert.Len(t, lines[2].Candidates, 2)
	assert.Contains(t, strings.Join(notices, "\n"), "overlapping mission/delegation candidates")
	assert.Contains(t, strings.Join(notices, "\n"), "no unambiguous recorded")
	assert.Empty(t, lines[3].Agent)
	// A working-tree record cannot override the historical agent.
	browseWrite(t, root, prefix+"delegations/d-a/record.yaml", []byte("agent_type: wrong\n"))
	lines, _ = s.gitBlameWithTrailers("new.txt")
	assert.Equal(t, "d-a", lines[1].Agent)
}

func TestProvenanceBrowseNoTicketOrFirstDelegationGuess(t *testing.T) {
	for _, message := range []string{"fix: ethos-123", "mission only\n\nMission: m-2026-11-04-001", "ambiguous trailers\n\nMission: m-2026-11-04-001\nDelegation: d-a\nDelegation: d-b"} {
		t.Run(strings.Split(message, "\n")[0], func(t *testing.T) {
			root := browseRepository(t)
			browseWrite(t, root, "a.txt", []byte("a\n"))
			browseGit(t, root, "add", "a.txt")
			browseGit(t, root, "commit", "-m", message)
			s := &Server{repoRoot: root, storeRoot: t.TempDir()}
			browseWrite(t, s.storeRoot, ".punt-labs/ethos/missions.jsonl", []byte("{\"id\":\"m-2026-11-04-001\",\"ticket\":\"ethos-123\"}\n"))
			browseWrite(t, s.storeRoot, ".punt-labs/ethos/missions/m-2026-11-04-001/delegations/d-a/record.yaml", []byte("agent_type: guessed\n"))
			lines, _ := s.gitBlameWithTrailers("a.txt")
			require.Len(t, lines, 1)
			assert.Empty(t, lines[0].Mission)
			assert.Empty(t, lines[0].Delegation)
			assert.Empty(t, lines[0].Agent)
		})
	}
}
