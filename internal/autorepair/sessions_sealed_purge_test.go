package autorepair_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/autorepair"
	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

// A dead session whose live audit lines were all sealed by the real seal path
// (chunks land under trace/, DES-079) has nothing unsealed: the purge removes
// its roster and records no residual.
func TestSessionsPurgesRosterAfterRealSeal(t *testing.T) {
	env := autorepair.FixtureEnv(t, "corpus/v4.22.1")
	sessions := filepath.Join(env.Global, "sessions")
	autorepair.ResolvePlaceholders(t, sessions, "2147483646", env.Roots.Checkout)
	_, err := hook.SealRepoWithPolicy(env.Policy, time.Now(), hook.SealOptions{})
	require.NoError(t, err)
	chunks, err := filepath.Glob(filepath.Join(env.Policy.LocalRoot(), "trace", "sessions", "*-sess-corpus-0001", "audit-*.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, chunks, "fixture: sealed chunks sit under trace/sessions")

	out, err := autorepair.RunSessions(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, out.Residuals, "a fully sealed session is not a residual")
	assert.NoFileExists(t, filepath.Join(sessions, "sess-corpus-0001.yaml"))
}

// The sealed chunks live under the canonical store S; the invoking checkout C
// is a different directory (a linked worktree). The roster's live files are in
// S, its recorded checkout. The purge must probe S's trace archive, not C's.
func TestSessionsPurgesRosterSealedInStoreFromOtherCheckout(t *testing.T) {
	env := autorepair.FixtureEnv(t, "corpus/v4.22.1")
	sessions := filepath.Join(env.Global, "sessions")
	autorepair.ResolvePlaceholders(t, sessions, "2147483646", env.Roots.Checkout)
	env.Roots.Checkout = t.TempDir() // C: no sealed chunks of its own

	out, err := autorepair.RunSessions(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, out.Residuals, "chunks sealed under S count for a purge run from C")
	assert.NoFileExists(t, filepath.Join(sessions, "sess-corpus-0001.yaml"))
}

// A dead session with unsealed live lines in a checkout that exists is sealed
// by the step itself, then purged: nothing is lost and nothing is left over.
func TestSessionsSealsDeadSessionThenPurges(t *testing.T) {
	env := autorepair.FixtureEnv(t, "corpus/v4.22.1")
	sessions := filepath.Join(env.Global, "sessions")
	autorepair.ResolvePlaceholders(t, sessions, "2147483646", env.Roots.Checkout)
	live := filepath.Join(env.Policy.LocalRoot(), "sessions", "sess-corpus-0001.audit.jsonl")
	require.FileExists(t, live)
	// A line one tick past the newest sealed chunk: nothing has sealed it.
	f, err := os.OpenFile(live, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(`{"ts":"` + audit.FormatLineTS(1794265403460920001) + `","session":"sess-corpus-0001","tool":"Read","path_format":2,"record":"blame","redacted":true}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	before, err := os.ReadFile(live)
	require.NoError(t, err)
	n, err := audit.SessionUnsealedCountAcross(env.Roots.Store, env.Roots.Store, env.Roots.Store, "sess-corpus-0001")
	require.NoError(t, err)
	require.Positive(t, n, "fixture: the session holds unsealed lines")

	out, err := autorepair.RunSessions(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, out.Residuals)
	assert.NoFileExists(t, filepath.Join(sessions, "sess-corpus-0001.yaml"), "the roster is purged")
	chunks, err := filepath.Glob(filepath.Join(env.Policy.LocalRoot(), "trace", "sessions", "*-sess-corpus-0001", "audit-*.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, chunks, "the lines are sealed under trace/")
	var sealed []byte
	for _, c := range chunks {
		b, err := os.ReadFile(c)
		require.NoError(t, err)
		sealed = append(sealed, b...)
	}
	for _, l := range strings.Split(strings.TrimSpace(string(before)), "\n") {
		assert.Contains(t, string(sealed), l, "no live line is lost")
	}
}

// With the writer's checkout gone there is no live zone to seal: the step
// attempts no seal and leaves the existing behavior.
func TestSessionsWriterCheckoutMissingAttemptsNoSeal(t *testing.T) {
	env := autorepair.FixtureEnv(t, "corpus/v4.22.1")
	sessions := filepath.Join(env.Global, "sessions")
	gone := filepath.Join(t.TempDir(), "gone")
	autorepair.ResolvePlaceholders(t, sessions, "2147483646", gone)
	calls := 0
	orig := autorepair.SealSession
	autorepair.SealSession = func(layout.Policy, string, string, time.Time) (int, error) { calls++; return 0, nil }
	t.Cleanup(func() { autorepair.SealSession = orig })

	_, err := autorepair.RunSessions(context.Background(), env)
	require.NoError(t, err)
	assert.Zero(t, calls)
}
