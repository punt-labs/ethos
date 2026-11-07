package adr

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestRepositoryStore_DirectorySyncOrderingAndUncertainty(t *testing.T) {
	t.Setenv("HOME", canonicalTempDir(t))
	for _, mode := range []layout.RecordMode{layout.RecordBlame, layout.RecordFull} {
		for _, operation := range []string{"create", "update", "copy-on-write", "migrate"} {
			// Each owned ancestor may be new, including .punt-labs created by
			// acquiring the local lock. Fail each sync, not only the leaf sync.
			for failAt := -1; failAt < 4; failAt++ {
				t.Run(fmt.Sprintf("%d/%s/fail-%d", mode, operation, failAt), func(t *testing.T) {
					s, legacy, p := repositoryStore(t, mode)
					a := sampleADR()
					a.Context = "installed content"
					var source []byte
					if operation == "update" {
						a.Context = "old content"
						require.NoError(t, s.Create(a))
					} else if operation != "create" {
						require.NoError(t, legacy.Create(a))
						var err error
						source, err = os.ReadFile(legacy.path(a.ID))
						require.NoError(t, err)
					}
					dest := s.path("DES-001")
					var events []string
					commit := func(src, target string) error {
						assert.Equal(t, p.TempDir(), filepath.Dir(src))
						assert.Equal(t, dest, target)
						data, err := os.ReadFile(src)
						require.NoError(t, err)
						staged, err := decodeStrict(data, "staging")
						require.NoError(t, err, "complete validated bytes precede publication")
						assert.Equal(t, "installed content", staged.Context)
						events = append(events, "publish")
						if operation == "update" {
							return os.Rename(src, target)
						}
						return os.Link(src, target)
					}
					s.link, s.rename = commit, commit
					parents := []string{s.dir, p.RecordRoot(), filepath.Dir(p.RecordRoot()), filepath.Dir(filepath.Dir(p.RecordRoot()))}
					syncs := 0
					s.syncDir = func(dir string) error {
						require.NotEmpty(t, events)
						assert.Equal(t, "publish", events[0], "sync must follow link/rename")
						stored, err := s.loadOwn("DES-001")
						require.NoError(t, err)
						assert.Equal(t, "installed content", stored.Context, "no restoration after publication")
						require.Less(t, syncs, len(parents))
						assert.Equal(t, parents[syncs], dir, "sync bottom-up through canonical store, never checkout")
						events = append(events, dir)
						syncs++
						if syncs-1 == failAt {
							return syscall.EIO
						}
						return syncDirectory(dir)
					}
					var err error
					switch operation {
					case "create":
						err = s.Create(a)
						assert.Equal(t, "DES-001", a.ID, "committed Create exposes its allocated ID even on uncertainty")
						assert.NotEmpty(t, a.CreatedAt)
					case "migrate":
						_, err = s.Migrate([]string{a.ID}, true)
					default:
						err = s.Update(a.ID, func(a *ADR) { a.Context = "installed content" })
					}
					if failAt < 0 {
						require.NoError(t, err)
						assert.Equal(t, append([]string{"publish"}, parents...), events)
					} else {
						var uncertain *DurabilityError
						require.ErrorAs(t, err, &uncertain)
						assert.Equal(t, "DES-001", uncertain.ID)
						assert.ErrorIs(t, err, syscall.EIO)
						assert.Contains(t, err.Error(), "installed; crash durability unconfirmed")
						assert.Equal(t, append([]string{"publish"}, parents[:failAt+1]...), events)
					}
					stored, loadErr := s.loadOwn("DES-001")
					require.NoError(t, loadErr)
					assert.Equal(t, "installed content", stored.Context)
					if source != nil {
						data, err := os.ReadFile(legacy.path(a.ID))
						require.NoError(t, err)
						assert.Equal(t, source, data)
					}
					entries, readErr := os.ReadDir(p.TempDir())
					require.NoError(t, readErr)
					assert.Empty(t, entries, "cleanup removes only local staging, never installed destination")
					assertOnlyADRs(t, p.RecordRoot())
				})
			}
		}
	}
}

func TestRepositoryStore_PrecommitFailureNeverSyncsOrReportsInstalled(t *testing.T) {
	t.Setenv("HOME", canonicalTempDir(t))
	for _, operation := range []string{"create", "update"} {
		t.Run(operation, func(t *testing.T) {
			s, _, _ := repositoryStore(t, layout.RecordBlame)
			a := sampleADR()
			var prior []byte
			if operation == "update" {
				require.NoError(t, s.Create(a))
				var err error
				prior, err = os.ReadFile(s.path(a.ID))
				require.NoError(t, err)
			}
			before := clone(a)
			fail := func(string, string) error { return syscall.EXDEV }
			s.link, s.rename = fail, fail
			s.syncDir = func(string) error {
				t.Error("directory sync must not run on a failed publication")
				return nil
			}
			var err error
			if operation == "create" {
				err = s.Create(a)
			} else {
				err = s.Update(a.ID, func(a *ADR) { a.Context = "not installed" })
			}
			require.ErrorIs(t, err, syscall.EXDEV)
			var uncertain *DurabilityError
			assert.False(t, errors.As(err, &uncertain))
			assert.Equal(t, before, a)
			if prior != nil {
				data, err := os.ReadFile(s.path(a.ID))
				require.NoError(t, err)
				assert.Equal(t, prior, data)
			} else {
				assert.NoFileExists(t, s.path("DES-001"))
			}
		})
	}
}

func TestMigrate_DurabilityUncertaintyRetrySyncsIdenticalCopy(t *testing.T) {
	t.Setenv("HOME", canonicalTempDir(t))
	s, legacy, _ := repositoryStore(t, layout.RecordBlame)
	a := sampleADR()
	require.NoError(t, legacy.Create(a))
	s.syncDir = func(string) error { return syscall.EIO }
	_, err := s.Migrate([]string{a.ID}, true)
	var uncertain *DurabilityError
	require.ErrorAs(t, err, &uncertain)
	assert.FileExists(t, s.path(a.ID))
	// Idempotence is not a substitute for fsync: an unchanged apply retry must
	// still report uncertainty while syncing fails, without republishing.
	s.link = func(string, string) error {
		t.Error("identical migration retry must not republish")
		return syscall.EEXIST
	}
	_, err = s.Migrate([]string{a.ID}, true)
	require.ErrorAs(t, err, &uncertain)
	syncs := 0
	s.syncDir = func(dir string) error {
		syncs++
		return syncDirectory(dir)
	}
	results, err := s.Migrate([]string{a.ID}, true)
	require.NoError(t, err)
	assert.Equal(t, 4, syncs)
	assert.Equal(t, []Migration{{ID: a.ID, Action: "unchanged"}}, results)
}
