package layout_test

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/adr"
	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/enable"
	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/role"
	"github.com/punt-labs/ethos/v4/internal/seed"
	"github.com/punt-labs/ethos/v4/internal/team"
	"github.com/punt-labs/ethos/v4/internal/vendor"
)

// writerPaths runs the real writers against a temporary repository and returns
// every file they left in its working tree, relative to the repository root.
// Paths come from the writers and their path constructors, not from a list kept
// here, so a writer added later either appears in the result or is not
// exercised; the former fails TestEveryWriterPathIsClassified when no entry
// matches.
func writerPaths(t *testing.T) []string {
	t.Helper()

	root := t.TempDir()
	t.Setenv("HOME", t.TempDir()) // claudemd and counters lock under $HOME
	global := t.TempDir()
	ethosDir := filepath.Join(root, ".punt-labs", "ethos")

	gitIn(t, root, "init", "-q")
	// An in-tree hooks directory, so `ethos enable` writes hook files into the
	// working tree.
	gitIn(t, root, "config", "core.hooksPath", ".githooks")

	if _, err := enable.Enable(root); err != nil {
		t.Fatalf("enable: %v", err)
	}

	writeIdentityGraph(t, ethosDir)
	writeMissionRecord(t, root, global)
	writeAuditFiles(t, root)
	writeAgents(t, root, ethosDir, global)

	// The ADR store takes its directory from the caller; DES-079 puts it at
	// <repo>/.punt-labs/ethos/adrs. The vendor manifest is written by the vendor
	// command; its constructor is the source here.
	require.NoError(t, adr.NewStore(filepath.Join(ethosDir, "adrs")).Create(&adr.ADR{
		ID: "DES-901", Title: "t", Status: adr.StatusProposed, Author: "bwk",
		CreatedAt: "2026-10-24T00:00:00Z", UpdatedAt: "2026-10-24T00:00:00Z",
		Context: "c", Decision: "d",
	}))
	require.NoError(t, os.WriteFile(vendor.ManifestPath(ethosDir), []byte("{}\n"), 0o644))

	var paths []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		switch {
		case rel == ".git" && d.IsDir():
			return filepath.SkipDir // git's own state; hooks there are out of scope
		case d.IsDir():
			return nil
		case rel == "go.mod":
			return nil // fixture for the agent generator, not an ethos write
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	require.NoError(t, err)
	sort.Strings(paths)
	return paths
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeIdentityGraph(t *testing.T, ethosDir string) {
	t.Helper()

	for _, a := range []struct {
		kind attribute.Kind
		slug string
	}{
		{attribute.Personalities, "kernighan"},
		{attribute.WritingStyles, "kernighan-prose"},
		{attribute.Talents, "engineering"},
	} {
		require.NoError(t, attribute.NewStore(ethosDir, a.kind).Save(
			&attribute.Attribute{Slug: a.slug, Content: "# " + a.slug + "\n\nBody.\n"}))
	}

	ids := identity.NewStore(ethosDir)
	require.NoError(t, ids.Save(&identity.Identity{Name: "Brian K", Handle: "bwk", Kind: "agent",
		Personality: "kernighan", WritingStyle: "kernighan-prose", Talents: []string{"engineering"}}))
	require.NoError(t, ids.ExtSet("bwk", "quarry", "collection", "memory-bwk"))
	require.NoError(t, ids.ExtSet("bwk", "quarry", "token", "secret", identity.Local(true)))

	require.NoError(t, role.NewStore(ethosDir).Save(&role.Role{
		Name:  "go-specialist",
		Tools: []string{"Read", "Write", "Edit", "Bash"},
	}))
	yes := func(string) bool { return true }
	// "claude" is the main agent in .punt-labs/ethos.yaml; bwk is the member
	// the generator writes an agent file for.
	require.NoError(t, ids.Save(&identity.Identity{Name: "Claude", Handle: "claude", Kind: "human"}))
	require.NoError(t, team.NewStore(ethosDir).Save(&team.Team{
		Name: "engineering",
		Members: []team.Member{
			{Identity: "claude", Role: "go-specialist"},
			{Identity: "bwk", Role: "go-specialist"},
		},
	}, yes, yes))
}

func writeMissionRecord(t *testing.T, root, global string) {
	t.Helper()

	s := mission.NewStoreWithRoots(root, global).WithSessionID("sess-1")
	c := &mission.Contract{
		MissionID: "m-2026-10-24-901",
		Status:    mission.StatusOpen,
		CreatedAt: "2026-10-24T00:00:00Z",
		UpdatedAt: "2026-10-24T00:00:00Z",
		Leader:    "claude",
		Worker:    "bwk",
		Evaluator: mission.Evaluator{Handle: "djb", PinnedAt: "2026-10-24T00:00:00Z"},
		Inputs:    mission.Inputs{Ticket: "ethos-g3g3"},
		WriteSet:  []string{"internal/layout/"},
		Tools:     []string{"Read", "Write"},
		SuccessCriteria: []string{
			"make check passes",
		},
		Budget:       mission.Budget{Rounds: 2, ReflectionAfterEach: true},
		CurrentRound: 1,
	}
	require.NoError(t, s.Create(c))
	require.NoError(t, s.AppendReflection(c.MissionID, &mission.Reflection{
		Mission: c.MissionID, Round: 1, Author: "claude", Converging: true,
		Signals: []string{"green"}, Recommendation: mission.RecommendationContinue, Reason: "ok",
	}))
	require.NoError(t, s.AppendResult(c.MissionID, &mission.Result{
		Mission: c.MissionID, Round: 1, Author: "bwk", Verdict: mission.VerdictPass, Confidence: 0.8,
		Evidence: []mission.EvidenceCheck{{Name: "go test", Status: mission.EvidenceStatusPass}},
	}))
	_, err := mission.WriteDelegationSkeleton(root, c.MissionID, "d-2026-10-24-901", mission.DelegationSkeleton{
		Tier: mission.TierB, AgentType: "bwk", Prompt: []byte("do the work"),
	})
	require.NoError(t, err)
	_, err = s.Close(c.MissionID, mission.StatusClosed)
	require.NoError(t, err)
}

// writeAuditFiles creates the live audit files, seals them with the real
// sealer, and adds the names the path constructors define for the artifacts a
// seal does not produce on a healthy run.
func writeAuditFiles(t *testing.T, root string) {
	t.Helper()

	const sid = "sess-1"
	live := audit.LiveAuditPath(root, sid)
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o755))
	require.NoError(t, os.WriteFile(live, []byte(`{"ts":"2026-10-24T00:00:00.000000001Z","tool":"Read"}`+"\n"), 0o644))
	_, err := hook.SealRepo(root, time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC), hook.SealOptions{})
	require.NoError(t, err)

	const mid = "m-2026-10-24-902"
	sessionDir := filepath.Join(audit.SealedSessionsBase(root), "2026-10-24-"+sid)
	missionDir := audit.SealedMissionDir(root, mid)
	sess := audit.ChunkName{Namespace: audit.SessionNS, First: 1, Last: 2}
	msn := audit.ChunkName{Namespace: audit.MissionNS, Session: sid, First: 1, Last: 2}

	for _, p := range []string{
		audit.LiveAuditLockPath(root, sid),
		audit.LiveMissionLockPath(root, mid, sid),
		audit.LiveMissionLogPath(root, mid, sid),
		audit.MissionResiduePath(root, mid),
		audit.MissionLegacyLogPath(root, mid),
		filepath.Join(sessionDir, "audit.jsonl"),
		filepath.Join(sessionDir, sess.ChunkFile()),
		filepath.Join(sessionDir, sess.TempFile()),
		filepath.Join(sessionDir, sess.MarkerFile()),
		filepath.Join(sessionDir, sess.Stem()+".jsonl.corrupt"),
		filepath.Join(sessionDir, sess.Stem()+".jsonl.corrupt-abc123"),
		filepath.Join(missionDir, msn.ChunkFile()),
		filepath.Join(missionDir, msn.TempFile()),
		filepath.Join(missionDir, msn.MarkerFile()),
		filepath.Join(missionDir, msn.Stem()+".jsonl.corrupt"),
		filepath.Join(missionDir, msn.Stem()+".jsonl.corrupt-abc123"),
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, nil, 0o644))
	}
}

// writeAgents runs the three writers of .claude/agents/<name>.md: the
// SessionStart generator, the installer, and the seeder.
func writeAgents(t *testing.T, root, ethosDir, global string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/t\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos.yaml"),
		[]byte("agent: claude\nteam: engineering\n"), 0o644))
	ids := identity.NewLayeredStore(identity.NewStore(ethosDir), identity.NewStore(global))
	teams := team.NewLayeredStore(ethosDir, global)
	roles := role.NewLayeredStore(ethosDir, global)
	require.NoError(t, hook.GenerateAgentFiles(root, ids, teams, roles))

	require.NoError(t, os.MkdirAll(filepath.Join(ethosDir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ethosDir, "agents", "stub.md"), []byte("# stub\n"), 0o644))
	t.Chdir(root)
	_, err := hook.InstallAgentDefinitions(ethosDir, map[string]bool{})
	require.NoError(t, err)

	_, err = seed.SeedVersionWithBundleDir(filepath.Join(global, "seed"), filepath.Join(global, "skills"),
		filepath.Join(root, ".claude", "agents"), "", "", "test", false)
	require.NoError(t, err)
}
