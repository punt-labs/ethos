package autorepair

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

const corpusSession = "sess-corpus-0001"

func globalAuditFile(env Env) string {
	return filepath.Join(env.Global, "sessions", corpusSession+".audit.jsonl")
}

// holdSession gives the repository a live stream for the session: one of
// the two association signals for a global audit file.
func holdSession(t *testing.T, env Env, session string) {
	t.Helper()
	path := audit.LiveAuditPath(env.Roots.Store, session)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, nil, 0o600))
}

// rosterCheckout rewrites the session's global roster so its checkout is
// dir, as every roster since v4.13.0 records it. The corpus v3.11 roster has
// no checkout field at all. The corpus host placeholder @HOST@ is not valid
// YAML (a real roster holds a hostname), so it is resolved here too.
func rosterCheckout(t *testing.T, env Env, session, dir string) {
	t.Helper()
	path := filepath.Join(env.Global, "sessions", session+".yaml")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	s := strings.Replace(string(b), "host:", "checkout: "+dir+"\nhost:", 1)
	require.NotEqual(t, string(b), s)
	s = strings.ReplaceAll(s, "@HOST@", "corpus-host")
	require.NoError(t, os.WriteFile(path, []byte(s), 0o600))
}

// realHome replaces the corpus placeholder @HOME@ in the global audit file
// with the test's real temporary HOME, so the redaction assertion has a real
// path to miss.
func realHome(t *testing.T, env Env) string {
	t.Helper()
	home := os.Getenv("HOME")
	b, err := os.ReadFile(globalAuditFile(env))
	require.NoError(t, err)
	require.Contains(t, string(b), "@HOME@", "the fixture carries the placeholder")
	require.NoError(t, os.WriteFile(globalAuditFile(env), []byte(strings.ReplaceAll(string(b), "@HOME@", home)), 0o600))
	return home
}

func archivedAudit(t *testing.T, env Env, session string) string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(env.Roots.Store, ".punt-labs", "local", "ethos", "trace", "sessions", "*-"+session, "audit.jsonl"))
	require.NoError(t, err)
	if len(m) == 0 {
		return ""
	}
	b, err := os.ReadFile(m[0])
	require.NoError(t, err)
	return string(b)
}

// TestGlobalAudit: a session whose global roster names this checkout is
// imported, redacted, and its global file deleted; the second run changes
// nothing in either tree.
func TestGlobalAudit(t *testing.T) {
	env, log := corpusEnvLogged(t, "v3.11.0")
	rosterCheckout(t, env, corpusSession, env.Roots.Checkout)
	home := realHome(t, env)
	raw, err := os.ReadFile(globalAuditFile(env))
	require.NoError(t, err)
	require.Contains(t, string(raw), "ghp_FAKE", "the corpus line is unredacted")
	require.Contains(t, string(raw), home)

	var out Outcome
	stderr := testhelpers.CaptureStderr(t, func() {
		out, err = runGlobalAudit(context.Background(), env)
	})
	require.NoError(t, err)
	assert.Empty(t, stderr, "the step logs; it never prints")
	assert.Equal(t, 1, out.Items)
	assert.Empty(t, out.Residuals)
	assert.False(t, out.Deferred)
	assert.NotEmpty(t, log.steps)

	got := archivedAudit(t, env, corpusSession)
	require.NotEmpty(t, got, "the redacted copy is in the local archive")
	assert.Equal(t, 3, strings.Count(got, "\n"))
	assert.NotContains(t, got, "ghp_FAKE", "the credential is redacted")
	assert.NotContains(t, got, home, "the real home path is redacted")
	assert.NotContains(t, got, "/Users/", "no user home of any kind survives")
	_, statErr := os.Stat(globalAuditFile(env))
	assert.True(t, os.IsNotExist(statErr), "the global source is deleted once archived")

	repoBefore, globalBefore := treeDigest(t, env.Roots.Store), treeDigest(t, env.Global)
	out, err = runGlobalAudit(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items, "second run changes nothing")
	assert.Equal(t, got, archivedAudit(t, env, corpusSession))
	assert.Equal(t, repoBefore, treeDigest(t, env.Roots.Store))
	assert.Equal(t, globalBefore, treeDigest(t, env.Global))
}

// TestGlobalAuditStreamOnlyImportsAndDeletes: a repository stream alone
// associates the session, so the file is imported; the redacted archive is
// the retained copy and the pre-redaction global file is deleted.
func TestGlobalAuditStreamOnlyImportsAndDeletes(t *testing.T) {
	env, log := corpusEnvLogged(t, "v3.11.0")
	holdSession(t, env, corpusSession)
	out, err := runGlobalAudit(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 1, out.Items, "the session is imported")
	assert.NotEmpty(t, archivedAudit(t, env, corpusSession))
	assert.NoFileExists(t, globalAuditFile(env), "the pre-redaction global copy is deleted once archived")
	assert.Empty(t, out.Residuals)
	assert.Contains(t, log.steps, "global-audit:import-and-delete")
}

// TestGlobalAuditOtherCheckoutStaysGlobal: a roster naming another checkout
// is another repository's session; nothing is imported and nothing deleted.
func TestGlobalAuditOtherCheckoutStaysGlobal(t *testing.T) {
	env, log := corpusEnvLogged(t, "v3.11.0")
	rosterCheckout(t, env, corpusSession, filepath.Join(t.TempDir(), "other"))
	out, err := runGlobalAudit(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items)
	assert.Empty(t, out.Residuals)
	assert.Empty(t, archivedAudit(t, env, corpusSession))
	assert.FileExists(t, globalAuditFile(env))
	assert.Contains(t, log.steps, "global-audit:other-checkout", "the operator has a record of the skipped session")
}

// TestGlobalAuditNoSignalIsResidual: the v3.11.0 corpus as it is. Its roster
// has no checkout field and its audit lines carry no repository, so nothing
// says whose session this is. The repository that upgrades first gains a
// redacted copy and retires the pre-redaction global file: once archived,
// obsolete unredacted data is not kept (operator ruling, DES-084).
func TestGlobalAuditNoSignalImportsAndDeletes(t *testing.T) {
	env, log := corpusEnvLogged(t, "v3.11.0")
	roster, err := os.ReadFile(filepath.Join(env.Global, "sessions", corpusSession+".yaml"))
	require.NoError(t, err)
	require.NotContains(t, string(roster), "checkout:", "the v3.11 roster names no checkout")
	home := realHome(t, env)
	before, err := os.ReadFile(globalAuditFile(env))
	require.NoError(t, err)

	out, err := runGlobalAudit(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 1, out.Items, "the unmodified v3.11 corpus imports")
	got := archivedAudit(t, env, corpusSession)
	require.NotEmpty(t, got, "the redacted copy is in this repository's local archive")
	assert.Equal(t, 3, strings.Count(got, "\n"))
	assert.NotContains(t, got, "ghp_FAKE")
	assert.NotContains(t, got, home)
	assert.NotEmpty(t, before, "fixture: the global file held the pre-redaction lines")
	assert.NoFileExists(t, globalAuditFile(env), "the pre-redaction global copy is deleted once archived")
	assert.Empty(t, out.Residuals)
	assert.Contains(t, log.steps, "global-audit:import-and-delete")

	repoBefore, globalBefore := treeDigest(t, env.Roots.Store), treeDigest(t, env.Global)
	out, err = runGlobalAudit(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items, "second run finds nothing to import")
	assert.Equal(t, repoBefore, treeDigest(t, env.Roots.Store))
	assert.Equal(t, globalBefore, treeDigest(t, env.Global))
}

func TestGlobalAuditNoGlobalFiles(t *testing.T) {
	for _, v := range []string{"v3.12.0", "v4.0.0", "v4.8.0", "v4.13.0", "v4.17.0", "v4.20.4", "v4.21.0", "v4.22.1"} {
		t.Run(v, func(t *testing.T) {
			env, _ := corpusEnvLogged(t, v)
			out, err := runGlobalAudit(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, 0, out.Items)
			assert.Empty(t, out.Residuals)
		})
	}
}

func TestGlobalAuditOutsideRepository(t *testing.T) {
	env, _ := corpusEnvLogged(t, "v3.11.0")
	env.Roots.Store = ""
	env.Policy = layout.Policy{} // the engine zeroes Roots and Policy together outside the repository pass
	out, err := runGlobalAudit(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items)
	assert.FileExists(t, globalAuditFile(env))
}

// TestGlobalAuditDefersWhenContextDone: a context that is done before the
// first file leaves every file for the next run and marks the step deferred.
func TestGlobalAuditDefersWhenContextDone(t *testing.T) {
	env, _ := corpusEnvLogged(t, "v3.11.0")
	rosterCheckout(t, env, corpusSession, env.Roots.Checkout)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := runGlobalAudit(ctx, env)
	require.NoError(t, err)
	assert.True(t, out.Deferred)
	assert.FileExists(t, globalAuditFile(env), "nothing was attempted")
}
