package autorepair

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

// deadPID is far above any pid the kernel hands out.
const deadPID = "2147483646"

// resolvePlaceholders rewrites the corpus placeholders under dir: the fixture
// pid becomes pid (a file named @PID@ is renamed), @REPO@ the checkout and
// @HOST@ a host name.
func resolvePlaceholders(t *testing.T, dir, pid, repo string) {
	t.Helper()
	var files []string
	require.NoError(t, filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, p)
		}
		return err
	}))
	r := strings.NewReplacer("@PID@", pid, "38921", pid, "@REPO@", repo, "@HOST@", "testhost")
	for _, p := range files {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		if s := string(b); strings.Contains(s, "@") {
			require.NoError(t, os.WriteFile(p, []byte(r.Replace(s)), 0o600))
		}
		if base := filepath.Base(p); strings.Contains(base, "@PID@") || (base == "38921" && filepath.Base(filepath.Dir(p)) == "current") {
			require.NoError(t, os.Rename(p, filepath.Join(filepath.Dir(p), r.Replace(base))))
		}
	}
}

// keptForLiveAudit names the snapshots whose scenario left a live session audit
// file in the repository's local zone (v4.21.0 onward). The non-force purge
// keeps their roster: it cannot prove the unsealed lines sealed.
var keptForLiveAudit = map[string]bool{"v4.21.0": true, "v4.22.1": true}

func sessionsDirOf(env Env) string { return filepath.Join(env.Global, "sessions") }

func TestSessionsPurgesDeadState(t *testing.T) {
	for _, v := range []string{"v3.11.0", "v3.12.0", "v4.0.0", "v4.8.0", "v4.13.0", "v4.17.0", "v4.20.4", "v4.21.0", "v4.22.1"} {
		t.Run(v, func(t *testing.T) {
			env := fixtureEnv(t, "corpus/"+v)
			resolvePlaceholders(t, sessionsDirOf(env), deadPID, env.Roots.Checkout)
			live := filepath.Join(sessionsDirOf(env), "current", strconv.Itoa(os.Getpid()))
			require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
			require.NoError(t, os.WriteFile(live, []byte("sess-live\n"), 0o600))

			out, err := runSessions(context.Background(), env)
			require.NoError(t, err)
			assert.False(t, out.Deferred)
			assert.Greater(t, out.Items, 0)

			roster := filepath.Join(sessionsDirOf(env), "sess-corpus-0001.yaml")
			if keptForLiveAudit[v] {
				// The non-force purge keeps a roster whose unsealed audit lines
				// it cannot prove sealed, and says so once as a residual.
				assert.FileExists(t, roster, "a roster with live audit files is kept")
				require.Len(t, out.Residuals, 1)
				assert.Contains(t, out.Residuals[0].ID, "sess-corpus-0001")
				assert.Contains(t, out.Residuals[0].Reason, "could not be read or proven free of unsealed audit lines")
			} else if strings.Contains(readFixture(t, filepath.Join("testdata", "corpus", v, "home", ".punt-labs", "ethos", "sessions", "sess-corpus-0001.yaml")), "checkout:") {
				assert.NoFileExists(t, roster, "dead roster")
			} else {
				assert.FileExists(t, roster, "a roster without checkout ages out (DES-058)")
			}
			assert.NoFileExists(t, filepath.Join(sessionsDirOf(env), "current", deadPID), "dead current file")
			assert.FileExists(t, live, "a live pid's current file is kept")
			tombs, err := audit.ListTombstones(sessionsDirOf(env), nil)
			require.NoError(t, err)
			assert.Empty(t, tombs, "every tombstone is retired")

			again, err := runSessions(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, 0, again.Items, "second run changes nothing")
		})
	}
}

func TestSessionsKeepsLiveRoster(t *testing.T) {
	env := fixtureEnv(t, "corpus/v4.22.1")
	resolvePlaceholders(t, sessionsDirOf(env), strconv.Itoa(os.Getpid()), env.Roots.Checkout)

	for i := 1; i <= 2; i++ {
		out, err := runSessions(context.Background(), env)
		require.NoError(t, err)
		assert.Equal(t, 0, out.Items, "run %d", i)
		assert.Empty(t, out.Residuals, "run %d", i)
		assert.FileExists(t, filepath.Join(sessionsDirOf(env), "sess-corpus-0001.yaml"), "run %d", i)
		assert.FileExists(t, filepath.Join(sessionsDirOf(env), "current", strconv.Itoa(os.Getpid())), "run %d", i)
	}
}

// captureStderr returns what fn wrote to os.Stderr.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	os.Stderr = old
	require.NoError(t, w.Close())
	return <-done
}

// A step never prints: an unreadable roster is logged and left as a residual,
// on every pass.
func TestSessionsStepPrintsNothingForUnreadableRoster(t *testing.T) {
	env := fixtureEnv(t, "corpus/v4.22.1")
	resolvePlaceholders(t, sessionsDirOf(env), deadPID, env.Roots.Checkout)
	bad := filepath.Join(sessionsDirOf(env), "sess-broken.yaml")
	require.NoError(t, os.WriteFile(bad, []byte("{: not yaml ["), 0o600))

	for i := 1; i <= 2; i++ {
		var out Outcome
		stderr := captureStderr(t, func() {
			var err error
			out, err = runSessions(context.Background(), env)
			require.NoError(t, err)
		})
		assert.Empty(t, stderr, "pass %d wrote to stderr", i)
		assert.FileExists(t, bad, "an unreadable roster is kept")
		var ids []string
		for _, r := range out.Residuals {
			assert.Equal(t, stepSessions, r.Step)
			ids = append(ids, r.ID)
		}
		assert.Contains(t, ids, "sess-broken", "pass %d", i)
		// The v4.22.1 snapshot has a live audit file, so its own roster is kept
		// too, with its own residual.
		assert.Len(t, ids, 2, "pass %d", i)
	}
}

func TestSessionsLeavesRosterWithoutCheckout(t *testing.T) {
	env := fixtureEnv(t, "damage/roster-without-checkout")
	resolvePlaceholders(t, sessionsDirOf(env), deadPID, env.Roots.Checkout)

	out, err := runSessions(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 1, out.Items, "only the dead current file goes")
	assert.FileExists(t, filepath.Join(sessionsDirOf(env), "sess-damage-0001.yaml"), "DES-058 residual ages out")
	assert.NoFileExists(t, filepath.Join(sessionsDirOf(env), "current", deadPID))
}

func TestSessionsRetiresTombstonesAfterOneLogLine(t *testing.T) {
	env := fixtureEnv(t, "corpus/v4.22.1")
	dir := sessionsDirOf(env)
	require.NoError(t, audit.WriteTombstone(dir, audit.Tombstone{Session: "sess-gone-1", UnsealedLines: true}))
	require.NoError(t, audit.WriteTombstone(dir, audit.Tombstone{Session: "sess-gone-2", LiveFileGone: true}))
	resolvePlaceholders(t, dir, strconv.Itoa(os.Getpid()), env.Roots.Checkout)

	out, err := runSessions(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 2, out.Items)
	tombs, err := audit.ListTombstones(dir, nil)
	require.NoError(t, err)
	assert.Empty(t, tombs)
	assert.FileExists(t, filepath.Join(dir, "sess-gone-1.purged.acked"))

	log := env.Log.(*entryLog)
	n := 0
	for _, e := range log.Entries {
		if e.Action == "retire-tombstone" {
			n++
		}
	}
	assert.Equal(t, 2, n, "one log line per tombstone")

	again, err := runSessions(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, again.Items)
}

func TestSessionStartMatchesStep(t *testing.T) {
	env := fixtureEnv(t, "corpus/v4.20.4")
	resolvePlaceholders(t, sessionsDirOf(env), deadPID, env.Roots.Checkout)
	n, err := SessionStart(context.Background(), env)
	require.NoError(t, err)
	assert.Greater(t, n, 0)
}
