//go:build linux

package mission

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

// Syscall tracing observes even a temporary file immediately renamed away.
// Final tree walks cannot establish transient placement.
func assertScrubTempTrace(t *testing.T, trace, tempDir string, count int) {
	t.Helper()
	data, err := os.ReadFile(trace)
	require.NoError(t, err)
	seen := 0
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.Contains(line, layout.TmpMarker) && strings.Contains(line, "O_CREAT|O_EXCL") {
			seen++
			t.Logf("observed staging syscall: %s", line)
			assert.Contains(t, line, `"`+tempDir+string(filepath.Separator), "actual temporary-file creation")
		}
	}
	assert.Equal(t, count, seen, "all rewrites must create a traced staging file")
}

func scrubPlacementFiles() map[string]string {
	const m = ".punt-labs/ethos/missions/m-2026-01-01-001/"
	return map[string]string{
		".punt-labs/ethos/missions.jsonl":               "{\"id\":\"m-2026-01-01-001\",\"success_criteria\":[\"a@b.org\"]}\n",
		m + "contract.yaml":                             "mission_id: m-2026-01-01-001\ncontext: a@b.org\nsuccess_criteria: [works]\n",
		m + "results.yaml":                              "results:\n  - prose: a@b.org\n",
		m + "reflections.yaml":                          "reflections:\n  - reason: a@b.org\n",
		m + "delegations/d-1/record.yaml":               "id: d-1\nsession: a@b.org\n",
		m + "delegations/d-1/prompt.md":                 "read a@b.org\n",
		".punt-labs/ethos/missions/plain.contract.yaml": "mission_id: m-2026-01-01-002\ncontext: a@b.org\nsuccess_criteria: [works]\n",
		".punt-labs/ethos/missions/plain.result.yaml":   "prose: a@b.org\n",
	}
}

func TestScrubConvenienceTransientPlacement(t *testing.T) {
	if root := os.Getenv("ETHOS_TEST_SCRUB_ROOT"); root != "" {
		var rels []string
		for rel := range scrubPlacementFiles() {
			rels = append(rels, rel)
		}
		res, err := ScrubFiles(root, os.Getenv("ETHOS_TEST_SCRUB_GLOBAL"), rels, false)
		require.NoError(t, err)
		require.Len(t, res.Changed, len(rels))
		return
	}
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	files := scrubPlacementFiles()
	writeTree(t, root, files)
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	require.NoError(t, layout.EnsureLocalDir(p, p.TempDir()))
	trace := filepath.Join(t.TempDir(), "scrub.strace")
	self, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command("strace", "-f", "-qq", "-e", "trace=openat", "-o", trace, self, "-test.run=^TestScrubConvenienceTransientPlacement$")
	cmd.Env = append(os.Environ(), "ETHOS_TEST_SCRUB_ROOT="+root, "ETHOS_TEST_SCRUB_GLOBAL="+t.TempDir())
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	assertScrubTempTrace(t, trace, p.TempDir(), len(files))
}

func scrubPolicyGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := testhelpers.GitCommand(args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return strings.TrimSpace(string(out))
}

func scrubPolicyFixture(t *testing.T, linked bool) (layout.Policy, map[string]string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	store := t.TempDir()
	scrubPolicyGit(t, store, "init", "-q")
	files := scrubPlacementFiles()
	writeTree(t, store, files)
	scrubPolicyGit(t, store, "add", ".punt-labs")
	scrubPolicyGit(t, store, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-qm", "scrub fixture")
	checkout := store
	if linked {
		checkout = filepath.Join(t.TempDir(), "linked")
		scrubPolicyGit(t, store, "worktree", "add", "-qb", "scrub-policy", checkout)
	}
	p, err := layout.NewRepository(store, checkout, layout.RecordFull)
	require.NoError(t, err)
	return p, files
}

func TestScrubPolicyTransientPlacement(t *testing.T) {
	if store := os.Getenv("ETHOS_TEST_SCRUB_STORE"); store != "" {
		p, err := layout.NewRepository(store, os.Getenv("ETHOS_TEST_SCRUB_CHECKOUT"), layout.RecordFull)
		require.NoError(t, err)
		rels, err := TrackedRecordFiles(os.Getenv("ETHOS_TEST_SCRUB_CHECKOUT"))
		require.NoError(t, err)
		res, err := ScrubFilesWithPolicy(p, os.Getenv("ETHOS_TEST_SCRUB_GLOBAL"), rels, false)
		require.NoError(t, err)
		require.Len(t, res.Changed, len(scrubPlacementFiles()))
		return
	}
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "linked"}[linked], func(t *testing.T) {
			p, files := scrubPolicyFixture(t, linked)
			store := filepath.Dir(filepath.Dir(p.RecordRoot()))
			checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
			index := scrubPolicyGit(t, checkout, "rev-parse", "--path-format=absolute", "--git-path", "index")
			before, err := os.ReadFile(index)
			require.NoError(t, err)
			self, err := os.Executable()
			require.NoError(t, err)
			trace := filepath.Join(t.TempDir(), "scrub-policy.strace")
			cmd := exec.Command("strace", "-f", "-qq", "-e", "trace=openat", "-o", trace, self, "-test.run=^TestScrubPolicyTransientPlacement$")
			cmd.Env = append(os.Environ(), "ETHOS_TEST_SCRUB_STORE="+store, "ETHOS_TEST_SCRUB_CHECKOUT="+checkout, "ETHOS_TEST_SCRUB_GLOBAL="+t.TempDir())
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			assertScrubTempTrace(t, trace, p.TempDir(), len(files))
			for rel, raw := range files {
				assert.NotContains(t, readFileRel(t, checkout, rel), "a@b.org")
				if linked {
					assert.Equal(t, raw, readFileRel(t, store, rel), "only C is rewritten")
				}
			}
			after, err := os.ReadFile(index)
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

func TestScrubPolicyRefusesStagingObstruction(t *testing.T) {
	for _, linked := range []bool{false, true} {
		for _, fault := range []string{"file", "symlink", "EXDEV"} {
			t.Run(map[bool]string{false: "ordinary", true: "linked"}[linked]+"/"+fault, func(t *testing.T) {
				p, files := scrubPolicyFixture(t, linked)
				checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
				rels, err := TrackedRecordFiles(checkout)
				require.NoError(t, err)
				index := scrubPolicyGit(t, checkout, "rev-parse", "--path-format=absolute", "--git-path", "index")
				before, err := os.ReadFile(index)
				require.NoError(t, err)
				require.NoError(t, os.MkdirAll(filepath.Dir(p.TempDir()), 0o700))
				switch fault {
				case "file":
					require.NoError(t, os.WriteFile(p.TempDir(), []byte("obstruction"), 0o600))
				case "symlink":
					require.NoError(t, os.Symlink(t.TempDir(), p.TempDir()))
				case "EXDEV":
					real := scrubWrite
					t.Cleanup(func() { scrubWrite = real })
					scrubWrite = func(dir, pattern, dest string, data []byte) error {
						assert.Equal(t, p.TempDir(), dir)
						return &os.LinkError{Op: "rename", Old: filepath.Join(dir, pattern), New: dest, Err: syscall.EXDEV}
					}
				}
				res, err := ScrubFilesWithPolicy(p, t.TempDir(), rels, false)
				require.Error(t, err)
				assert.Empty(t, res.Changed)
				if fault == "EXDEV" {
					assert.ErrorIs(t, err, syscall.EXDEV)
				}
				for rel, raw := range files {
					assert.Equal(t, raw, readFileRel(t, checkout, rel))
				}
				after, err := os.ReadFile(index)
				require.NoError(t, err)
				assert.Equal(t, before, after)
				if linked {
					assert.NoDirExists(t, filepath.Join(checkout, ".punt-labs", "local"), "no C-local fallback")
				}
			})
		}
	}
}

func TestScrubPolicyValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, root := range []string{"", ".", "relative", "bad\x00root"} {
		_, err := ScrubFiles(root, t.TempDir(), nil, true)
		var invalid *layout.ValidationError
		assert.ErrorAs(t, err, &invalid, "single-root compatibility must not bypass validation")
	}
	_, err := ScrubFilesWithPolicy(layout.Policy{}, t.TempDir(), nil, true)
	var invalid *layout.ValidationError
	assert.ErrorAs(t, err, &invalid)
}

func TestScrubPolicyHoldsCanonicalLocks(t *testing.T) {
	p, _ := scrubPolicyFixture(t, true)
	const mid = "m-2026-01-01-001"
	tests := []struct{ rel, domain, key string }{
		{".punt-labs/ethos/missions.jsonl", "mission-summary", "missions"},
		{".punt-labs/ethos/missions/" + mid + "/contract.yaml", "mission-state", mid},
		{".punt-labs/ethos/missions/" + mid + "/delegations/d-1/record.yaml", "mission-delegations", mid},
		{".punt-labs/ethos/missions/" + mid + "/delegations/d-1/prompt.md", "delegation", "d-1"},
	}
	real := scrubWrite
	t.Cleanup(func() { scrubWrite = real })
	for _, tt := range tests {
		t.Run(tt.domain, func(t *testing.T) {
			held := false
			scrubWrite = func(dir, pattern, dest string, data []byte) error {
				lock, err := p.LockPath(tt.domain, tt.key)
				require.NoError(t, err)
				f, err := os.OpenFile(lock, os.O_RDWR, 0)
				require.NoError(t, err)
				defer func() { assert.NoError(t, f.Close()) }()
				err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
				if err == nil {
					require.NoError(t, unix.Flock(int(f.Fd()), unix.LOCK_UN))
				}
				held = errors.Is(err, unix.EWOULDBLOCK)
				return real(dir, pattern, dest, data)
			}
			_, err := ScrubFilesWithPolicy(p, t.TempDir(), []string{tt.rel}, false)
			require.NoError(t, err)
			assert.True(t, held, "canonical writer lock must cover the read-to-install interval")
		})
	}
}

func TestScrubPolicyMalformedAndDryRun(t *testing.T) {
	for _, tt := range []struct{ name, rel, body string }{
		{"invalid JSONL", ".punt-labs/ethos/missions.jsonl", "not JSON a@b.org\n"},
		{"invalid YAML", ".punt-labs/ethos/missions/m-2026-01-01-001/results.yaml", "results: [\n"},
		{"unknown field", ".punt-labs/ethos/missions/m-2026-01-01-001/results.yaml", "future_field: a@b.org\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, files := scrubPolicyFixture(t, true)
			checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
			writeTree(t, checkout, map[string]string{tt.rel: tt.body})
			_, err := ScrubFilesWithPolicy(p, t.TempDir(), []string{tt.rel}, false)
			require.Error(t, err)
			assert.Equal(t, tt.body, readFileRel(t, checkout, tt.rel))
			assert.NoDirExists(t, p.TempDir(), "refused parse must never stage bytes")
			// A dry run still reports other valid records, even when local
			// staging is obstructed; it creates neither locks nor staging.
			require.NoError(t, os.MkdirAll(filepath.Dir(p.TempDir()), 0o700))
			require.NoError(t, os.WriteFile(p.TempDir(), []byte("blocked"), 0o600))
			const plain = ".punt-labs/ethos/missions/plain.result.yaml"
			res, err := ScrubFilesWithPolicy(p, t.TempDir(), []string{plain}, true)
			require.NoError(t, err)
			assert.Equal(t, []string{plain}, res.Changed)
			assert.Equal(t, files[plain], readFileRel(t, checkout, plain))
			assert.NoDirExists(t, filepath.Join(checkout, ".punt-labs", "local"))
		})
	}
}

func TestScrubPolicyRetainsGlobalAuthorityOwnerLock(t *testing.T) {
	p, _ := scrubPolicyFixture(t, true)
	const mid = "m-2026-01-01-001"
	const rel = ".punt-labs/ethos/missions/" + mid + "/contract.yaml"
	// C still contains its tracked export, but the mutable authority exists
	// only in the global fallback layer. S's selection fence does not replace
	// the G authority lock; scrub must retain both in the store's lock order.
	require.NoError(t, os.RemoveAll(filepath.Join(p.RecordRoot(), "missions", mid)))
	global := NewStore(t.TempDir())
	require.NoError(t, global.Create(newContract(mid)))
	before, err := os.ReadFile(filepath.Join(global.Root(), "missions", mid+".yaml"))
	require.NoError(t, err)
	real := scrubWrite
	t.Cleanup(func() { scrubWrite = real })
	held := false
	scrubWrite = func(dir, pattern, dest string, data []byte) error {
		f, err := os.OpenFile(global.MissionLockPath(mid), os.O_RDWR, 0)
		require.NoError(t, err)
		defer func() { assert.NoError(t, f.Close()) }()
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			require.NoError(t, unix.Flock(int(f.Fd()), unix.LOCK_UN))
		}
		held = errors.Is(err, unix.EWOULDBLOCK)
		return real(dir, pattern, dest, data)
	}
	res, err := ScrubFilesWithPolicy(p, global.Root(), []string{rel}, false)
	require.NoError(t, err)
	assert.Equal(t, []string{rel}, res.Changed)
	assert.True(t, held, "the actual authority owner must serialize the scrub")
	after, err := os.ReadFile(filepath.Join(global.Root(), "missions", mid+".yaml"))
	require.NoError(t, err)
	assert.Equal(t, before, after, "the target is C, not the global authority")
}
