package mission

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestPathField_RefusesTheOwnDashHome: when a contract is created or its path
// entries change, a repo-relative path entry that holds this machine's
// dash-encoded home is refused with an error that names the field, rather than
// rewritten to -~, which its readers would compare literally. Every other
// writer rewrites the slug to -~, and so does free text.
func TestPathField_RefusesTheOwnDashHome(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	for _, p := range []string{
		"cache/-home-zed-project/config.json",
		".tmp/claude-1000/-home-zed-proj/x",
	} {
		s := NewStoreWithRoots(t.TempDir(), t.TempDir())
		c := newContract("m-2026-08-06-150")
		c.WriteSet = []string{p}
		err := s.Create(c)
		require.Error(t, err, p)
		assert.Contains(t, err.Error(), "WriteSet")
		assert.Contains(t, err.Error(), "without the home slug")
	}
	s := NewStoreWithRoots(t.TempDir(), t.TempDir())
	c := newContract("m-2026-08-06-151")
	c.Context = "see cache/-home-zed-project/config.json"
	require.NoError(t, s.Create(c))
	got, err := s.Load(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, "see cache/-~-project/config.json", got.Context)
}

// TestPathField_OrdinaryFileNamesAreStoredAsWritten: a file name that holds
// -users- or -home- is a name in a mission path field, where there is no
// foreign-slug rule. A worker that edited
// it can submit its result, and the mission can close.
func TestPathField_OrdinaryFileNamesAreStoredAsWritten(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	names := []string{
		"web/src/components/admin-users-list-view.tsx",
		"docs/the-home-screen-redesign.md",
		"app/my-home-page-v2.tsx",
		"web/user-home-tab-icon.svg",
		"docs/new-users-onboarding-guide.md",
		// BEM and double-dash names: the dash after a word is not a slug.
		"src/styles/nav--home-page.scss",
		"src/components/card--users-list.tsx",
		"web/icons/btn--home-active.svg",
		"docs/2024-01--users-guide.md",
		"src/__tests__/nav__item--home-link.test.tsx",
		"lib/v2_-users-api.go",
		// From the second review.
		"a/b--home-page.scss", "x/a--users-list-view.tsx", "x/Z--Users-Panel.tsx",
		"x/-my-home-page.tsx", "x/_-home-page.tsx", "x/.-home-rc",
		"x/e2e/--users-flow.spec.ts",
		// A -, + or @ byte next to a /home/ or /Users/ segment is a name in a
		// mission path field; only the audit log reads it as a path start. (A *
		// glob before /home/ is rewritten, as at ddefb51: bead ethos-ka73.)
		"@types/users/index.d.ts", "@scope/home/x.ts", "+x/home/y", "x/a-/home/b", "-/home/page.tsx",
	}
	for _, n := range names {
		assert.Empty(t, LeakRuleFor(n, true), n)
	}
	s := NewStoreWithRoots(t.TempDir(), t.TempDir())
	c := newContract("m-2026-08-06-152")
	c.WriteSet = names
	require.NoError(t, s.Create(c))
	got, err := s.Load(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, names, got.WriteSet)
	r := resultFor(got, VerdictPass)
	for _, n := range names {
		r.FilesChanged = append(r.FilesChanged, FileChange{Path: n, Added: 1})
	}
	require.NoError(t, s.AppendResult(c.MissionID, r))
	// A repo-relative value in a mission path field is judged by this
	// machine's own home only (OwnHomeRule): its home and its dash-encoded slug
	// anywhere are refused when a contract is created or its path entries
	// change (every other writer rewrites the slug to -~), and there is no
	// foreign-slug rule (bead ethos-ka73),
	// nor a rule about a flag or glob byte beside /home/. The audit log's path
	// fields apply broader rules of their own.
	assert.NotEmpty(t, LeakRuleFor("@-home-zed/pkg", true), "the own slug is refused")
	c2 := newContract("m-2026-08-06-154")
	c2.WriteSet = []string{"cache/-home-zed-project/x"}
	assert.Error(t, s.Create(c2))
}

// TestPathField_InFlightMissionWithADoubleDashNameCanFinish: a mission stored
// before the slug rule, with a double-dash file name in its write_set, can
// still submit its result and close.
func TestPathField_InFlightMissionWithADoubleDashNameCanFinish(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	names := []string{
		"src/styles/nav--home-page.scss", "docs/2024-01--users-guide.md", "lib/v2_-users-api.go",
		"@types/users/index.d.ts", "@scope/home/x.ts",
	}
	s := NewStoreWithRoots(t.TempDir(), t.TempDir())
	c := newContract("m-2026-08-06-153")
	require.NoError(t, s.Create(c))
	// The contract as an older ethos left it: the names written raw.
	stored, err := s.Load(c.MissionID)
	require.NoError(t, err)
	stored.WriteSet = names
	raw, err := yaml.Marshal(stored)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(mustContractPath(t, s, c.MissionID), raw, 0o600))

	got, err := s.Load(c.MissionID)
	require.NoError(t, err)
	r := resultFor(got, VerdictPass)
	for _, n := range names {
		r.FilesChanged = append(r.FilesChanged, FileChange{Path: n, Added: 1})
	}
	require.NoError(t, s.AppendResult(c.MissionID, r))
	_, err = s.Close(c.MissionID, StatusClosed)
	require.NoError(t, err)
}

// TestPathField_InFlightMissionWithTheOwnSlugCanFinish: a mission written by an
// older ethos with this machine's dash slug in write_set can still submit a
// result that names the stored entry and close, as at ddefb51, where both were
// rewritten to -~. The own-slug refusal applies when a contract is created or
// its path entries are changed, not to the records of a mission in flight.
func TestPathField_InFlightMissionWithTheOwnSlugCanFinish(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	const entry = ".claude/projects/-home-zed-proj/x.md"
	s := NewStoreWithRoots(t.TempDir(), t.TempDir())
	c := newContract("m-2026-08-06-155")
	require.NoError(t, s.Create(c))
	stored, err := s.Load(c.MissionID)
	require.NoError(t, err)
	stored.WriteSet = []string{entry}
	raw, err := yaml.Marshal(stored)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(mustContractPath(t, s, c.MissionID), raw, 0o600))

	got, err := s.Load(c.MissionID)
	require.NoError(t, err)
	r := resultFor(got, VerdictPass)
	r.FilesChanged = []FileChange{{Path: entry, Added: 1}}
	require.NoError(t, s.AppendResult(c.MissionID, r))
	_, err = s.Close(c.MissionID, StatusClosed)
	require.NoError(t, err)

	// An update that leaves the stored slug entry unchanged and edits only
	// Context succeeds: the entry is compared with the prior contract's.
	c3 := newContract("m-2026-08-06-157")
	require.NoError(t, s.Create(c3))
	inflight, err := s.Load(c3.MissionID)
	require.NoError(t, err)
	inflight.WriteSet = []string{entry}
	raw3, err := yaml.Marshal(inflight)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(mustContractPath(t, s, c3.MissionID), raw3, 0o600))
	cur, err := s.Load(c3.MissionID)
	require.NoError(t, err)
	cur.Context = "edited while in flight"
	require.NoError(t, s.Update(cur), "an unchanged slug entry is not a new entry")

	// A new entry is still refused when a contract is changed.
	c2 := newContract("m-2026-08-06-156")
	require.NoError(t, s.Create(c2))
	up, err := s.Load(c2.MissionID)
	require.NoError(t, err)
	up.WriteSet = []string{entry}
	assert.Error(t, s.Update(up))
}
