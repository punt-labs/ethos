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
)

const corpusSession = "sess-corpus-0001"

func globalAuditFile(env Env) string {
	return filepath.Join(env.Global, "sessions", corpusSession+".audit.jsonl")
}

// holdSession gives the repository a live stream for the session, the
// association signal for a global audit file.
func holdSession(t *testing.T, env Env, session string) {
	t.Helper()
	path := audit.LiveAuditPath(env.Roots.Store, session)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, nil, 0o600))
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

func TestGlobalAudit(t *testing.T) {
	env, log := corpusEnv(t, "v3.11.0")
	holdSession(t, env, corpusSession)
	home := os.Getenv("HOME")
	raw, err := os.ReadFile(globalAuditFile(env))
	require.NoError(t, err)
	require.Contains(t, string(raw), "ghp_FAKE", "the corpus line is unredacted")

	out, err := runGlobalAudit(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 1, out.Items)
	assert.Empty(t, out.Residuals)
	assert.False(t, out.Deferred)
	assert.NotEmpty(t, log.steps)

	got := archivedAudit(t, env, corpusSession)
	require.NotEmpty(t, got, "the redacted copy is in the local archive")
	assert.Equal(t, 3, strings.Count(got, "\n"))
	assert.NotContains(t, got, "ghp_FAKE", "the credential is redacted")
	assert.NotContains(t, got, home)
	_, statErr := os.Stat(globalAuditFile(env))
	assert.True(t, os.IsNotExist(statErr), "the global source is deleted once archived")

	out, err = runGlobalAudit(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items, "second run changes nothing")
	assert.Equal(t, got, archivedAudit(t, env, corpusSession))
}

func TestGlobalAuditUnassociatedStaysGlobal(t *testing.T) {
	env, _ := corpusEnv(t, "v3.11.0")
	out, err := runGlobalAudit(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items)
	assert.FileExists(t, globalAuditFile(env))
}

func TestGlobalAuditNoGlobalFiles(t *testing.T) {
	for _, v := range []string{"v3.12.0", "v4.0.0", "v4.8.0", "v4.13.0", "v4.17.0", "v4.20.4", "v4.21.0", "v4.22.1"} {
		t.Run(v, func(t *testing.T) {
			env, _ := corpusEnv(t, v)
			out, err := runGlobalAudit(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, 0, out.Items)
			assert.Empty(t, out.Residuals)
		})
	}
}

func TestGlobalAuditOutsideRepository(t *testing.T) {
	env, _ := corpusEnv(t, "v3.11.0")
	env.Roots.Store = ""
	out, err := runGlobalAudit(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items)
	assert.FileExists(t, globalAuditFile(env))
}
