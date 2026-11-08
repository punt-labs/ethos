package audit

import (
	"bytes"
	"crypto/sha1"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

func TestPublicationDurabilityPersistentFailureAndRetry(t *testing.T) {
	for _, kind := range []string{
		"ordinary", "alternate-local", "alternate-external", "alternate-relative-external",
		"linked-main", "linked-bare",
	} {
		t.Run(kind, func(t *testing.T) {
			f := newPublicationDurabilityFixture(t, kind)
			checkout := filepath.Dir(filepath.Dir(f.policy.CheckoutRecordRoot()))
			before, err := os.ReadFile(f.index)
			require.NoError(t, err)
			payload, err := os.ReadFile(f.publication)
			require.NoError(t, err)
			// A competing add would change the staged source. Publication must
			// preserve its old staged blob, not refresh unrelated working files.
			require.NoError(t, os.WriteFile(filepath.Join(checkout, "source.txt"), []byte("unstaged source\n"), 0o600))

			oldSync := syncAuditDirectory
			t.Cleanup(func() { syncAuditDirectory = oldSync })
			fault := errors.New("persistent publication index directory sync fault")
			failing := true
			calls := 0
			var installed []byte
			syncAuditDirectory = func(dir string) error {
				calls++
				require.Equal(t, filepath.Dir(f.index), dir, "confirm the actual index parent, not a policy-root walk")
				lock, err := os.ReadFile(f.index + ".lock")
				require.NoError(t, err, "retain ownership of Git's index lock through confirmation")
				assert.Empty(t, lock)
				data, err := os.ReadFile(f.index)
				require.NoError(t, err)
				if installed == nil {
					installed = data
				} else {
					assert.Equal(t, installed, data, "identical retry must retain exact valid index bytes")
				}
				// A real independent Git writer must still be excluded at the
				// applied-but-not-confirmed boundary, including alternate indexes.
				cmd := testhelpers.GitCommand("add", "--", "source.txt")
				cmd.Dir = checkout
				cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+f.index)
				out, err := cmd.CombinedOutput()
				require.Error(t, err, "competing Git writer acquired the publication lock")
				assert.Contains(t, string(out), ".lock")
				if failing {
					return fault
				}
				return oldSync(dir)
			}

			for attempt := 1; attempt <= 3; attempt++ {
				if attempt == 3 {
					failing = false
				}
				err := StagePublication(f.policy, []string{f.publication})
				if failing {
					require.ErrorIs(t, err, fault)
					var applied *layout.InstallDurabilityError
					require.ErrorAs(t, err, &applied)
					assert.Equal(t, f.index, applied.Path)
					assert.Contains(t, err.Error(), "audit publication index")
					assert.Contains(t, err.Error(), "change applied")
					assert.Contains(t, err.Error(), "crash durability unconfirmed")
					assert.NotContains(t, err.Error(), "actual index unchanged")
				} else {
					require.NoError(t, err, "cleared fault must allow durable confirmation")
				}
				require.Equal(t, attempt, calls, "every retry must re-confirm the destination")
				after, err := os.ReadFile(f.index)
				require.NoError(t, err)
				assert.False(t, bytes.Equal(before, after), "publication was not installed")
				assert.Equal(t, installed, after, "an applied error must not restore the old index")
				require.GreaterOrEqual(t, len(after), 12+sha1.Size)
				assert.Equal(t, "DIRC", string(after[:4]))
				checksum := sha1.Sum(after[:len(after)-sha1.Size])
				assert.Equal(t, checksum[:], after[len(after)-sha1.Size:], "invalid Git index checksum")
				for path, want := range map[string][]byte{
					filepath.Join(checkout, "source.txt"): []byte("staged source\n"),
					f.publication:                         payload,
				} {
					data, present, err := IndexBlob(checkout, path)
					require.NoError(t, err, "Git must read the installed index")
					assert.True(t, present)
					assert.Equal(t, want, data)
				}
				_, err = os.Stat(f.index + ".lock")
				assert.ErrorIs(t, err, os.ErrNotExist, "release only our lock after confirmation")
				entries, err := os.ReadDir(f.policy.TempDir())
				require.NoError(t, err)
				assert.Empty(t, entries, "canonical candidate or candidate lock debris")
				if f.otherIndex != "" {
					data, err := os.ReadFile(f.otherIndex)
					require.NoError(t, err)
					assert.Equal(t, f.otherBefore, data, "another checkout's/default index changed")
				}
			}
		})
	}
}

type publicationDurabilityFixture struct {
	policy      layout.Policy
	publication string
	index       string
	otherIndex  string
	otherBefore []byte
}

func newPublicationDurabilityFixture(t *testing.T, kind string) publicationDurabilityFixture {
	t.Helper()
	// Keep checksum assertions independent of the operator's default hash.
	t.Setenv("GIT_DEFAULT_HASH", "sha1")
	p, path, index := publicationFixture(t)
	f := publicationDurabilityFixture{policy: p, publication: path, index: index}
	checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	if kind == "ordinary" {
		return f
	}
	if strings.HasPrefix(kind, "alternate-") {
		f.otherIndex = index
		var err error
		f.otherBefore, err = os.ReadFile(index)
		require.NoError(t, err)
		f.index = filepath.Join(t.TempDir(), "alternate-index")
		if kind == "alternate-local" {
			f.index = filepath.Join(p.LocalRoot(), "alternate-index")
		}
		require.NoError(t, os.WriteFile(f.index, f.otherBefore, 0o600))
		selected := f.index
		if kind == "alternate-relative-external" {
			selected, err = filepath.Rel(checkout, selected)
			require.NoError(t, err)
		}
		t.Setenv("GIT_INDEX_FILE", selected)
		return f
	}

	store := checkout
	common := filepath.Join(checkout, ".git")
	checkout = t.TempDir()
	if kind == "linked-bare" {
		common = filepath.Join(t.TempDir(), "shared.git")
		runGit(t, filepath.Dir(common), "init", "--bare", "--quiet", common)
		store = checkout // The established bare-common-directory ownership rule.
	} else {
		f.otherIndex = index
		var err error
		f.otherBefore, err = os.ReadFile(index)
		require.NoError(t, err)
	}
	// Register an unborn linked checkout: no fixture commit or branch writes
	// are needed to exercise Git's real worktree/index discovery and locking.
	admin := filepath.Join(common, "worktrees", "publication")
	require.NoError(t, os.MkdirAll(admin, 0o700))
	for name, content := range map[string]string{
		filepath.Join(checkout, ".git"):   "gitdir: " + admin + "\n",
		filepath.Join(admin, "HEAD"):      "ref: refs/heads/publication\n",
		filepath.Join(admin, "commondir"): "../..\n",
		filepath.Join(admin, "gitdir"):    filepath.Join(checkout, ".git") + "\n",
	} {
		require.NoError(t, os.WriteFile(name, []byte(content), 0o600))
	}
	var err error
	f.policy, err = layout.NewRepository(store, checkout, layout.RecordFull)
	require.NoError(t, err)
	f.index = filepath.Join(admin, "index")
	assert.Equal(t, f.index, strings.TrimSpace(runGit(t, checkout, "rev-parse", "--git-path", "index")))
	assert.Equal(t, common, strings.TrimSpace(runGit(t, checkout, "rev-parse", "--git-common-dir")))
	require.NoError(t, os.WriteFile(filepath.Join(checkout, "source.txt"), []byte("staged source\n"), 0o600))
	runGit(t, checkout, "add", "--", "source.txt")
	f.publication = filepath.Join(f.policy.CheckoutRecordRoot(), "sessions", "2026-11-04-s", SessionChunkFile(100, 100))
	require.NoError(t, WriteLocalAtomic(f.policy, f.publication, []byte(`{"ts":"`+FormatLineTS(100)+`","record":"full"}`+"\n")))
	return f
}

func TestPublicationDurabilityPreInstallFailureDoesNotConfirm(t *testing.T) {
	for _, kind := range []string{"foreign-lock", "ignored", "malformed-index", "outside-record-tree", "empty-index-variable"} {
		t.Run(kind, func(t *testing.T) {
			p, path, index := publicationFixture(t)
			checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
			switch kind {
			case "foreign-lock":
				require.NoError(t, os.WriteFile(index+".lock", []byte("foreign lock"), 0o600))
			case "ignored":
				require.NoError(t, os.WriteFile(filepath.Join(checkout, ".gitignore"), []byte(".punt-labs/ethos/sessions/\n"), 0o600))
			case "malformed-index":
				require.NoError(t, os.WriteFile(index, []byte("invalid Git index\n"), 0o600))
			case "outside-record-tree":
				path = filepath.Join(checkout, "source.txt")
			case "empty-index-variable":
				t.Setenv("GIT_INDEX_FILE", "")
			}
			before, err := os.ReadFile(index)
			require.NoError(t, err)
			oldSync := syncAuditDirectory
			t.Cleanup(func() { syncAuditDirectory = oldSync })
			calls := 0
			syncAuditDirectory = func(string) error {
				calls++
				return errors.New("unexpected pre-install confirmation")
			}
			err = StagePublication(p, []string{path})
			require.Error(t, err)
			var applied *layout.InstallDurabilityError
			assert.False(t, errors.As(err, &applied), "pre-install failure reported as applied")
			assert.Zero(t, calls)
			after, err := os.ReadFile(index)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			if kind == "foreign-lock" {
				data, err := os.ReadFile(index + ".lock")
				require.NoError(t, err)
				assert.Equal(t, "foreign lock", string(data))
			} else {
				_, err := os.Stat(index + ".lock")
				assert.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}
