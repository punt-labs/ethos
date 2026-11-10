package layout_test

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/adr"
	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/enable"
	"github.com/punt-labs/ethos/v4/internal/githook"
	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/role"
	"github.com/punt-labs/ethos/v4/internal/seed"
	"github.com/punt-labs/ethos/v4/internal/team"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/punt-labs/ethos/v4/internal/vendor"
)

// writerPaths runs the real writers against a temporary repository and returns
// every file they left in its working tree, relative to the repository root.
// Paths come from the writers and their path constructors, not from a list kept
// here, so a writer added later either appears in the result or is not
// exercised; the former fails TestEveryWriterPathIsClassified when no entry
// matches.
func writerCorpus(t *testing.T) corpus {
	t.Helper()

	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
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

	policy := writerPolicy(t, root)
	writeIdentityGraph(t, ethosDir, identity.RepositoryPolicy(policy))
	writeMissionRecord(t, root, global)
	writeAuditFiles(t, root)
	writeAgents(t, root, ethosDir, global)

	adrs, err := adr.NewRepositoryStore(policy, "")
	require.NoError(t, err)
	require.NoError(t, adrs.Create(&adr.ADR{
		ID: "DES-901", Title: "t", Status: adr.StatusProposed, Author: "bwk",
		CreatedAt: "2026-10-24T00:00:00Z", UpdatedAt: "2026-10-24T00:00:00Z",
		Context: "c", Decision: "d",
	}))
	writeVendored(t, ethosDir)
	writeMigrations(t, root, global)

	var paths []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
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

	// Hook files are classified from the directory git resolves for the repo,
	// as the doctor check does.
	hooksDir := githook.HooksDir(root).Display()
	return corpus{paths: paths, table: mustTableFor(t, root, hooksDir)}
}

// corpus is the set of files the writers left in the repository, with the table
// that classifies them: layout.Table plus the entries for in-tree hook files.
type corpus struct {
	paths []string
	table []layout.Entry
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := testhelpers.GitCommand(append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writerPolicy(t *testing.T, root string) layout.Policy {
	t.Helper()
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	return p
}

func writeIdentityGraph(t *testing.T, ethosDir string, opts ...identity.StoreOption) {
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

	ids := identity.NewStore(ethosDir, opts...)
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

	s := mission.NewRepositoryStore(writerPolicy(t, root), global).WithSessionID("sess-1")
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
	// Force optional archival to fail after the real writer accepts the shared
	// decision. Its durable per-round receipt must survive the final corpus walk.
	archive := filepath.Join(root, ".punt-labs", "local", "ethos", "trace", "missions", c.MissionID, "reflection-1.yaml")
	require.NoError(t, os.MkdirAll(archive, 0o700))
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
	require.FileExists(t, filepath.Join(root, ".punt-labs", "local", "ethos", "missions", c.MissionID, "reflection-intents", "1.yaml"))
}

func TestRetainedReflectionReceiptIsClassified(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	writeMissionRecord(t, root, t.TempDir())
	rel := ".punt-labs/local/ethos/missions/m-2026-10-24-901/reflection-intents/1.yaml"
	require.FileExists(t, filepath.Join(root, filepath.FromSlash(rel)))
	class, _, ok := layout.Classify(rel)
	require.True(t, ok)
	require.Equal(t, layout.Never, class)

	var trimmed []layout.Entry
	for _, entry := range layout.Table {
		if entry.Pattern != ".punt-labs/local/ethos/missions/*/reflection-intents/*.yaml" {
			trimmed = append(trimmed, entry)
		}
	}
	require.Equal(t, []string{rel}, unclassified(trimmed, []string{rel}), "removing the receipt entry must expose the actual retained writer output")
}

func TestPromptConsentIsNeverPublishedTrace(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	const id = "m-2026-10-24-905"
	const delegation = "d-2026-10-24-905"
	_, err = mission.WriteDelegationSkeletonWithPolicy(writerPolicy(t, root), id, delegation, mission.DelegationSkeleton{
		Tier: mission.TierB, AgentType: "bwk", Prompt: []byte("bounded work"),
	})
	require.NoError(t, err)
	rel := ".punt-labs/local/ethos/trace/missions/" + id + "/delegations/" + delegation + "/prompt-consent.yaml"
	require.FileExists(t, filepath.Join(root, filepath.FromSlash(rel)))
	class, _, ok := layout.Classify(rel)
	require.True(t, ok)
	require.Equal(t, layout.Never, class, "consent is local eligibility metadata, not shareable prompt content")

	var trimmed []layout.Entry
	for _, entry := range layout.Table {
		if entry.Pattern != ".punt-labs/local/ethos/trace/missions/*/delegations/*/prompt-consent.yaml" {
			trimmed = append(trimmed, entry)
		}
	}
	class, _, ok = layout.ClassifyIn(trimmed, rel)
	require.True(t, ok)
	require.Equal(t, layout.Trace, class, "the real writer output must expose a missing eligibility-metadata override")
}

// writeAuditFiles creates the live audit files, seals them with the real
// sealer, and adds the names the path constructors define for the artifacts a
// seal does not produce on a healthy run.
func writeAuditFiles(t *testing.T, root string) {
	t.Helper()

	const sid = "sess-1"
	live := audit.LiveAuditPath(root, sid)
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o755))
	require.NoError(t, os.WriteFile(live, []byte(`{"ts":"2026-10-24T00:00:00.000000001Z","tool":"Read","contract_id":"m-2026-10-24-903"}`+"\n"), 0o644))
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
		filepath.Join(sessionDir, sess.ChunkFile()),
		filepath.Join(sessionDir, sess.TempFile()),
		filepath.Join(sessionDir, sess.MarkerFile()),
		filepath.Join(missionDir, msn.ChunkFile()),
		filepath.Join(missionDir, msn.TempFile()),
		filepath.Join(missionDir, msn.MarkerFile()),
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, nil, 0o644))
	}

	quarantineOnce(t, root, sessionDir, sess)
	quarantineOnce(t, root, missionDir, msn)
}

// quarantineOnce exercises repeated recovery: retirement metadata and distinct
// damaged payloads remain local even when the same chunk name is damaged twice.
func quarantineOnce(t *testing.T, root, dir string, cn audit.ChunkName) {
	t.Helper()

	require.NoError(t, os.MkdirAll(dir, 0o755))
	// The classification pass above planted an empty file at the marker path.
	// An empty marker is a torn loss record, which no writer replaces, so the
	// stand-in goes before the first quarantine writes the real one.
	require.NoError(t, os.RemoveAll(filepath.Join(dir, cn.MarkerFile())))
	chunk := filepath.Join(dir, cn.ChunkFile())
	require.NoError(t, os.WriteFile(chunk, []byte("not json\n"), 0o644))
	_, err := hook.QuarantineChunk(root, chunk, "test corruption")
	require.NoError(t, err)
	require.NoFileExists(t, chunk)
	require.NoError(t, os.WriteFile(chunk, []byte("still not json\n"), 0o644))
	_, err = hook.QuarantineChunk(root, chunk, "new corruption")
	require.NoError(t, err)
	require.NoFileExists(t, chunk)
	// Neither damaged payload decoded, so neither is kept as bytes: the loss
	// is recorded in the marker, which the second quarantine merged into the
	// first (ranges only widen, never narrow), and nothing raw remains under
	// local/ for a reader to mistake for evidence.
	marker, err := audit.ReadMarker(filepath.Join(dir, cn.MarkerFile()))
	require.NoError(t, err)
	require.Equal(t, cn.Stem(), marker.Chunk)
	require.True(t, marker.HasGap(), "the undecodable bytes are recorded as a loss")
	require.LessOrEqual(t, marker.UnrecoveredFirst, cn.First)
	require.GreaterOrEqual(t, marker.UnrecoveredLast, cn.Last)
	raw, err := filepath.Glob(filepath.Join(root, ".punt-labs", "local", "ethos", "quarantine", "*", "*.corrupt"))
	require.NoError(t, err)
	require.Empty(t, raw, "no raw copy of undecodable bytes survives a completed quarantine")
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
		filepath.Join(root, ".claude", "agents"), "", "", "test", false, seed.WithRepositoryAgents(writerPolicy(t, root)))
	require.NoError(t, err)
}

// writeVendored runs `ethos vendor --team engineering --apply` from a separate
// source store into the repository: the copy writer and the .vendor.yaml
// manifest.
func writeVendored(t *testing.T, ethosDir string) {
	t.Helper()

	src, empty := t.TempDir(), t.TempDir()
	writeIdentityGraph(t, src)
	v, err := vendor.New(vendor.Sources{
		Roots:      []string{src},
		Identities: identity.NewLayeredStore(identity.NewStore(src), identity.NewStore(empty)),
		Roles:      role.NewLayeredStore(src, empty),
		Teams:      team.NewLayeredStore(src, empty),
	}, vendor.Options{Team: "engineering", Dest: ethosDir, Apply: true})
	require.NoError(t, err)
	_, err = v.Run()
	require.NoError(t, err)
}

// writeMigrations runs `ethos mission migrate` and the legacy audit migration
// against legacy global files, and leaves the staging directory a crashed
// mission migration would leave behind.
func writeMigrations(t *testing.T, root, global string) {
	t.Helper()

	const id = "m-2026-10-24-903" // referenced as contract_id in the live audit file
	legacy := filepath.Join(global, "missions")
	require.NoError(t, os.MkdirAll(legacy, 0o755))
	// The copy parses each file as the record it is, so each holds a minimal one.
	for suffix, body := range map[string]string{
		".yaml":             "mission_id: " + id + "\nstatus: open\n",
		".jsonl":            `{"ts":"2026-10-24T00:00:00Z","event":"create","actor":"bwk"}` + "\n",
		".results.yaml":     "- round: 1\n  prose: done\n",
		".reflections.yaml": "- round: 1\n  reason: why\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(legacy, id+suffix), []byte(body), 0o644))
	}
	require.NoError(t, mission.MigrateMission(global, root, root, id, false, io.Discard))

	missions := filepath.Join(root, ".punt-labs", "ethos", "missions")
	staging, err := os.MkdirTemp(missions, layout.MigrateStagingPrefix("m-2026-10-24-904"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(staging, "contract.yaml"), []byte("x: 1\n"), 0o600))

	sessions := t.TempDir()
	// Keep this migration stream independent of the intentionally damaged
	// quarantine fixtures above; their malformed marker must remain an error.
	require.NoError(t, os.WriteFile(filepath.Join(sessions, "sess-migrate.audit.jsonl"),
		[]byte(`{"ts":"2026-10-23T00:00:00Z","tool_name":"Read","tool_use_id":"t1"}`+"\n"), 0o644))
	require.NoError(t, hook.MigrateAudit(sessions, root, false, io.Discard))
}

// Enable writes through CLAUDE.md links to user files inside or outside C. Git
// must be able to prove the whole local directory excluded, so the ignore source
// is a regular file, not a symlink Git refuses to read (tested separately below).
// Classification uses the logical writer name, not the user-chosen target.
func TestSymlinkedTargetsAreClassified(t *testing.T) {
	for _, outside := range []bool{false, true} {
		t.Run(fmt.Sprintf("outside=%t", outside), func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			t.Setenv("HOME", t.TempDir())
			gitIn(t, root, "init", "-q")
			target := filepath.Join(root, "docs", "AGENTS.md")
			linkText := filepath.Join("docs", "AGENTS.md")
			if outside {
				external, err := filepath.EvalSymlinks(t.TempDir())
				require.NoError(t, err)
				target = filepath.Join(external, "AGENTS.md")
				linkText = target
			}
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
			require.NoError(t, os.WriteFile(target, []byte("# user file\n"), 0o644))
			link := filepath.Join(root, "CLAUDE.md")
			if err := os.Symlink(linkText, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			_, err = enable.Enable(root)
			require.NoError(t, err)
			covered, err := enable.LocalIgnored(root)
			require.NoError(t, err)
			require.True(t, covered, "real Git must confirm directory-level protection")
			data, err := os.ReadFile(target)
			require.NoError(t, err)
			require.Contains(t, string(data), "# user file\n")
			require.Contains(t, string(data), enable.CanonicalImport)
			gotLink, err := os.Readlink(link)
			require.NoError(t, err)
			require.Equal(t, linkText, gotLink)
			class, _, ok := layout.Classify("CLAUDE.md")
			require.True(t, ok)
			require.Equal(t, layout.Source, class)
			checkSymlinkWriterPaths(t, root, "docs/AGENTS.md")
			entries, err := os.ReadDir(filepath.Dir(target))
			require.NoError(t, err)
			require.Len(t, entries, 1, "no adjacent staging debris at either physical target")
			require.Equal(t, "AGENTS.md", entries[0].Name())
		})
	}
}

func checkSymlinkWriterPaths(t *testing.T, root string, userPaths ...string) {
	t.Helper()
	var leftovers []string
	require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == ".git" && d.IsDir() {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		for _, userPath := range userPaths {
			if filepath.ToSlash(rel) == userPath {
				return nil
			}
		}
		if _, _, ok := layout.Classify(filepath.ToSlash(rel)); !ok {
			leftovers = append(leftovers, rel)
		}
		return nil
	}))
	if len(leftovers) > 0 {
		t.Errorf("enable through symlinks left unclassified files: %v", leftovers)
	}
}

// Ignore sources refuse symlinks before reading their payload, even when the
// target is readable and holds the literal rule. Neither that rule nor selected
// filename coverage can replace Git's directory-level proof. Preserve user files/index,
// and do not mark enabled or install hooks/imports after the refusal.
func TestSymlinkedGitignoreRefusesUnconfirmedProtection(t *testing.T) {
	for _, outside := range []bool{false, true} {
		t.Run(fmt.Sprintf("outside=%t", outside), func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			t.Setenv("HOME", t.TempDir())
			gitIn(t, root, "init", "-q")
			gitIn(t, root, "config", "core.hooksPath", ".githooks")
			require.NoError(t, os.WriteFile(filepath.Join(root, "source.txt"), []byte("source\n"), 0o644))
			gitIn(t, root, "add", "source.txt")
			index := filepath.Join(root, ".git", "index")
			beforeIndex, err := os.ReadFile(index)
			require.NoError(t, err)

			target := filepath.Join(root, "config", "shared.ignore")
			linkText := filepath.Join("config", "shared.ignore")
			if outside {
				external, err := filepath.EvalSymlinks(t.TempDir())
				require.NoError(t, err)
				target = filepath.Join(external, "shared.ignore")
				linkText = target
			}
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
			original := []byte("# user file\n" + enable.LocalIgnoreRule + "\n")
			require.NoError(t, os.WriteFile(target, original, 0o644))
			link := filepath.Join(root, ".gitignore")
			if err := os.Symlink(linkText, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			claude := filepath.Join(root, "CLAUDE.md")
			require.NoError(t, os.WriteFile(claude, []byte("# user instructions\n"), 0o644))
			added, err := enable.EnsureLocalIgnored(root, writerPolicy(t, root))
			require.ErrorContains(t, err, "reading .gitignore")
			require.ErrorContains(t, err, "not a regular file (symlinks are not allowed); refusing to read")
			require.False(t, added)
			require.NoDirExists(t, filepath.Join(root, ".punt-labs"), "ignore maintenance must refuse before staging or writing")

			_, err = enable.Enable(root)
			require.ErrorContains(t, err, "reading .gitignore")
			require.ErrorContains(t, err, "not a regular file (symlinks are not allowed); refusing to read")
			require.NoFileExists(t, filepath.Join(root, ".punt-labs", "ethos", "enabled"))
			require.NoDirExists(t, filepath.Join(root, ".githooks"))
			for path, want := range map[string][]byte{target: original, claude: []byte("# user instructions\n"), index: beforeIndex} {
				got, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, want, got, "refusal must preserve %s", path)
			}
			gotLink, err := os.Readlink(link)
			require.NoError(t, err)
			require.Equal(t, linkText, gotLink)
			// Enable may deposit its guides before checking protection. Those
			// outputs still must be classified; this is not all-or-nothing enable.
			checkSymlinkWriterPaths(t, root, "config/shared.ignore", "source.txt")
			entries, err := os.ReadDir(filepath.Dir(target))
			require.NoError(t, err)
			require.Len(t, entries, 1)
			require.Equal(t, "shared.ignore", entries[0].Name())
		})
	}
}
