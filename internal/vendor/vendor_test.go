package vendor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/role"
	"github.com/punt-labs/ethos/v4/internal/team"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixture is a two-layer store — an empty repo layer and a populated
// global one — matching the situation vendor exists for: a repo that has
// nothing yet, and a user's global registry that has everything.
type fixture struct {
	repo   string
	global string
	src    Sources
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{repo: t.TempDir(), global: t.TempDir()}

	is := identity.NewLayeredStoreWithBundle(
		identity.NewStore(f.repo), nil, identity.NewStore(f.global), false)
	f.src = Sources{
		Roots:      []string{f.repo, f.global},
		Identities: is,
		Roles:      role.NewLayeredStoreWithBundle(f.repo, "", f.global, false),
		Teams:      team.NewLayeredStoreWithBundle(f.repo, "", f.global, false),
	}
	return f
}

func (f *fixture) writeGlobal(t *testing.T, rel, body string) {
	t.Helper()
	path := filepath.Join(f.global, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// seedOrg builds the shape DES-057's acceptance case describes: bwk is
// the handle a user would vendor, and claudia exists ONLY as a member of
// the team bwk belongs to — reachable through no direct reference from
// bwk at all.
func (f *fixture) seedOrg(t *testing.T) {
	t.Helper()
	f.writeGlobal(t, "identities/bwk.yaml",
		"name: Brian K\nhandle: bwk\nkind: agent\npersonality: kernighan\n"+
			"writing_style: kernighan-prose\ntalents:\n  - engineering\n")
	f.writeGlobal(t, "identities/claudia.yaml",
		"name: Claudia\nhandle: claudia\nkind: agent\npersonality: prose\n")
	f.writeGlobal(t, "identities/unrelated.yaml",
		"name: Nobody\nhandle: unrelated\nkind: human\n")

	f.writeGlobal(t, "personalities/kernighan.md", "# Kernighan\n\nSimplicity.\n")
	f.writeGlobal(t, "personalities/prose.md", "# Prose\n\nCuration.\n")
	f.writeGlobal(t, "writing-styles/kernighan-prose.md", "# Kernighan Prose\n\nTerse.\n")
	f.writeGlobal(t, "talents/engineering.md", "# Engineering\n\nGo.\n")

	f.writeGlobal(t, "roles/go-specialist.yaml", "name: go-specialist\nresponsibilities:\n  - Go\n")
	f.writeGlobal(t, "roles/writer.yaml", "name: writer\nresponsibilities:\n  - Prose\n")

	f.writeGlobal(t, "teams/engineering.yaml",
		"name: engineering\nmembers:\n"+
			"  - identity: bwk\n    role: go-specialist\n"+
			"  - identity: claudia\n    role: writer\n")
}

func (f *fixture) run(t *testing.T, opts Options) (*Plan, error) {
	t.Helper()
	if opts.Dest == "" {
		opts.Dest = f.repo
	}
	v, err := New(f.src, opts)
	require.NoError(t, err)
	return v.Run()
}

// The load-bearing reverse edge. Vendoring bwk must pull claudia, who is
// reachable only as a fellow member of bwk's team — without her the
// vendored team file names an identity the set does not contain, which
// is exactly the incomplete "complete" snapshot vendor exists to prevent.
func TestClosurePullsTeamOnlyMember(t *testing.T) {
	f := newFixture(t)
	f.seedOrg(t)

	p, err := f.run(t, Options{Handles: []string{"bwk"}})
	require.NoError(t, err)

	assert.Equal(t, []string{"bwk"}, p.Seeds)
	assert.Equal(t, []string{"bwk", "claudia"}, p.Identities,
		"claudia is reachable only through team membership")
	assert.Equal(t, []string{"kernighan", "prose"}, p.Personalities,
		"and her personality comes with her")
	assert.Equal(t, []string{"go-specialist", "writer"}, p.Roles)
	assert.Equal(t, []string{"engineering"}, p.Teams)
	assert.NotContains(t, p.Identities, "unrelated",
		"the closure is the connected component, not the whole store")
}

// Termination: roles are leaves and there is no team→team edge, so a
// second run over a store the first run already covered reaches the same
// fixed point.
func TestClosureIsAFixedPoint(t *testing.T) {
	f := newFixture(t)
	f.seedOrg(t)

	first, err := f.run(t, Options{Handles: []string{"bwk"}})
	require.NoError(t, err)
	second, err := f.run(t, Options{Handles: []string{"bwk", "claudia"}})
	require.NoError(t, err)

	assert.Equal(t, first.Identities, second.Identities)
	assert.Equal(t, first.Teams, second.Teams)
}

// The default is a plan. Vendor writes into git-tracked space, so a bare
// invocation must not put bytes there.
func TestPlanWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.seedOrg(t)

	p, err := f.run(t, Options{Handles: []string{"bwk"}})
	require.NoError(t, err)

	assert.False(t, p.Applied)
	assert.Zero(t, p.FilesWritten)
	assert.NoFileExists(t, filepath.Join(f.repo, "identities", "bwk.yaml"))
	assert.NoFileExists(t, ManifestPath(f.repo))
}

func TestApplyWritesAResolvableSet(t *testing.T) {
	f := newFixture(t)
	f.seedOrg(t)

	p, err := f.run(t, Options{Handles: []string{"bwk"}, Apply: true})
	require.NoError(t, err)
	assert.True(t, p.Applied)

	for _, rel := range []string{
		"identities/bwk.yaml", "identities/claudia.yaml",
		"personalities/kernighan.md", "personalities/prose.md",
		"writing-styles/kernighan-prose.md", "talents/engineering.md",
		"roles/go-specialist.yaml", "roles/writer.yaml",
		"teams/engineering.yaml", ManifestName,
	} {
		assert.FileExists(t, filepath.Join(f.repo, rel))
	}

	// The postcondition Run already enforced, asserted directly: the
	// snapshot resolves with no global layer at all.
	require.NoError(t, Verify(f.repo))
}

func TestApplyRecordsExtInTheManifest(t *testing.T) {
	f := newFixture(t)
	f.seedOrg(t)
	f.writeGlobal(t, "identities/bwk.ext/quarry.yaml", "memory_collection: bwk-mem\n")
	f.writeGlobal(t, "identities/bwk.ext/quarry.local.yaml", "api_token: s3cret\n")

	_, err := f.run(t, Options{Handles: []string{"bwk"}, Apply: true})
	require.NoError(t, err)

	assert.FileExists(t, filepath.Join(f.repo, "identities", "bwk.ext", "quarry.yaml"))
	assert.NoFileExists(t, filepath.Join(f.repo, "identities", "bwk.ext", "quarry.local.yaml"),
		".local is excluded structurally, by name — not scrubbed")

	m, err := LoadManifest(f.repo)
	require.NoError(t, err)
	require.NotNil(t, m)
	assert.Equal(t, map[string][]string{"bwk": {"quarry.yaml"}, "claudia": nil}, m.RequiredExt())
}

// A symlink named `quarry.yaml` would slip past the name-based .local
// skip if vendor followed it. The check is lstat and the answer is a
// refusal, not a silent skip that leaves the set quietly incomplete.
func TestApplyRefusesSymlinkedExt(t *testing.T) {
	f := newFixture(t)
	f.seedOrg(t)
	f.writeGlobal(t, "identities/bwk.ext/quarry.local.yaml", "api_token: s3cret\n")
	require.NoError(t, os.Symlink(
		filepath.Join(f.global, "identities", "bwk.ext", "quarry.local.yaml"),
		filepath.Join(f.global, "identities", "bwk.ext", "quarry.yaml")))

	_, err := f.run(t, Options{Handles: []string{"bwk"}, Apply: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")
}

// Fail-closed at the git-writing boundary, and closed on a dry run too:
// a warning that fires after the bytes are in the working tree is one
// `git add` from being committed and is invisible in CI.
func TestCredentialGuardBlocks(t *testing.T) {
	for _, apply := range []bool{false, true} {
		name := "plan"
		if apply {
			name = "apply"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.seedOrg(t)
			f.writeGlobal(t, "identities/bwk.ext/quarry.yaml",
				"memory_collection: bwk-mem\napi_token: s3cret\n")

			_, err := f.run(t, Options{Handles: []string{"bwk"}, Apply: apply})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "bwk quarry/api_token")
			assert.Contains(t, err.Error(), "--allow-ext-key quarry/api_token")
			assert.NoFileExists(t, filepath.Join(f.repo, "identities", "bwk.ext", "quarry.yaml"))
		})
	}
}

func TestCredentialGuardPerKeyOverride(t *testing.T) {
	f := newFixture(t)
	f.seedOrg(t)
	f.writeGlobal(t, "identities/bwk.ext/quarry.yaml",
		"memory_collection: bwk-mem\napi_token: s3cret\n")

	p, err := f.run(t, Options{
		Handles: []string{"bwk"}, Apply: true,
		AllowExtKeys: []string{"quarry/api_token"},
	})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(f.repo, "identities", "bwk.ext", "quarry.yaml"))
	// The exemption stays visible in the run's output rather than
	// disappearing once granted.
	require.Len(t, p.Warnings, 1)
	assert.Equal(t, "bwk quarry/api_token", p.Warnings[0].String())
}

// The override is per key. Allowing one credential must not open the
// door for a second one in the same namespace.
func TestCredentialGuardOverrideIsPerKey(t *testing.T) {
	f := newFixture(t)
	f.seedOrg(t)
	f.writeGlobal(t, "identities/bwk.ext/quarry.yaml",
		"api_token: s3cret\nclient_secret: other\n")

	_, err := f.run(t, Options{
		Handles: []string{"bwk"}, Apply: true,
		AllowExtKeys: []string{"quarry/api_token"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bwk quarry/client_secret")
	assert.NotContains(t, err.Error(), "quarry/api_token (")
}

func TestSeedFromTeam(t *testing.T) {
	f := newFixture(t)
	f.seedOrg(t)

	p, err := f.run(t, Options{Team: "engineering"})
	require.NoError(t, err)
	assert.Equal(t, []string{"bwk", "claudia"}, p.Seeds)
	assert.Equal(t, []string{"bwk", "claudia"}, p.Identities)
}

// --team and explicit handles are not mutually exclusive: the command's
// own usage line is `ethos vendor [handle...] [flags]`, so a positional
// handle alongside --team must add to the team, not be dropped.
func TestSeedFromTeamPlusExplicitHandlesIsAUnion(t *testing.T) {
	f := newFixture(t)
	f.seedOrg(t)

	p, err := f.run(t, Options{Team: "engineering", Handles: []string{"unrelated"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"bwk", "claudia", "unrelated"}, p.Seeds)
	assert.Equal(t, []string{"bwk", "claudia", "unrelated"}, p.Identities)
}

// The union deduplicates: naming a handle that the team already contains
// must not produce a repeated seed.
func TestSeedFromTeamPlusExplicitHandlesDeduplicates(t *testing.T) {
	f := newFixture(t)
	f.seedOrg(t)

	p, err := f.run(t, Options{Team: "engineering", Handles: []string{"bwk"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"bwk", "claudia"}, p.Seeds)
}

func TestSeedFromAll(t *testing.T) {
	f := newFixture(t)
	f.seedOrg(t)

	p, err := f.run(t, Options{All: true})
	require.NoError(t, err)
	assert.Contains(t, p.Identities, "unrelated")
}

func TestNoSeedsIsAnError(t *testing.T) {
	f := newFixture(t)
	f.seedOrg(t)

	_, err := f.run(t, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no identities selected")
}

// Prune reports before it removes, and never touches a .local companion:
// that is untracked machine-local state vendor does not own.
func TestPrune(t *testing.T) {
	f := newFixture(t)
	f.seedOrg(t)
	_, err := f.run(t, Options{Handles: []string{"bwk"}, Apply: true})
	require.NoError(t, err)

	stale := filepath.Join(f.repo, "identities", "stale.yaml")
	require.NoError(t, os.WriteFile(stale, []byte("name: Stale\nhandle: stale\nkind: human\n"), 0o644))
	local := filepath.Join(f.repo, "identities", "bwk.ext", "quarry.local.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(local), 0o755))
	require.NoError(t, os.WriteFile(local, []byte("api_token: s3cret\n"), 0o644))

	plan, err := f.run(t, Options{Handles: []string{"bwk"}})
	require.NoError(t, err)
	assert.Equal(t, []string{stale}, plan.Pruned)
	assert.FileExists(t, stale, "a plan removes nothing")

	_, err = f.run(t, Options{Handles: []string{"bwk"}, Apply: true, Prune: true})
	require.NoError(t, err)
	assert.NoFileExists(t, stale)
	assert.FileExists(t, local, ".local is machine-local, not vendor's to delete")
}

// team.Load's structural validation does not check that a member's role
// exists, so a hand-edited team file can name one that does not. Catch it
// while planning — discovering it during the copy would leave a
// half-written set behind.
func TestClosureRejectsAMissingRoleAtPlanTime(t *testing.T) {
	f := newFixture(t)
	f.seedOrg(t)
	require.NoError(t, os.Remove(filepath.Join(f.global, "roles", "writer.yaml")))

	_, err := f.run(t, Options{Handles: []string{"bwk"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `role "writer"`)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestNewRejectsMissingInputs(t *testing.T) {
	f := newFixture(t)
	_, err := New(Sources{}, Options{Dest: f.repo})
	require.Error(t, err)

	_, err = New(f.src, Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "destination")

	_, err = New(f.src, Options{Dest: f.repo, AllowExtKeys: []string{"bogus"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--allow-ext-key")
}
