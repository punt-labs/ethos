//go:build linux || darwin

package autorepair

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

const fakeToken = "ghp_FAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE"

// traceFiles lists record-tree files this step owns.
func traceFiles(t *testing.T, env Env) []string {
	t.Helper()
	var out []string
	for _, f := range listFiles(t, env.Policy.RecordRoot()) {
		if _, ok := classifyTrace(f); ok {
			out = append(out, f)
		}
	}
	return out
}

func TestRecordTreeTraceOverCorpus(t *testing.T) {
	archived := map[string]bool{"v4.0.0": true, "v4.8.0": true, "v4.13.0": true, "v4.17.0": true, "v4.20.4": true}
	for _, v := range corpusVersions {
		t.Run(v, func(t *testing.T) {
			env := corpusEnv(t, v)
			before := traceFiles(t, env)
			out, err := runRecordTreeTrace(context.Background(), env)
			require.NoError(t, err)
			assert.False(t, out.Deferred)
			assert.Empty(t, out.Residuals)
			assert.Empty(t, traceFiles(t, env), "record tree keeps no obsolete trace")
			assert.Equal(t, len(before), out.Items)
			if archived[v] {
				require.NotEmpty(t, before, "corpus %s must hold trace in the record tree", v)
			} else {
				assert.Empty(t, before, "corpus %s holds no record-tree trace", v)
			}
			// Every archived file is free of the fake token and the home path.
			home := os.Getenv("HOME")
			for _, f := range listFiles(t, env.Policy.TraceArchiveRoot()) {
				b, err := os.ReadFile(filepath.Join(env.Policy.TraceArchiveRoot(), f))
				require.NoError(t, err)
				assert.NotContains(t, string(b), fakeToken, f)
				assert.NotContains(t, string(b), home, f)
			}
			// The current readers accept every archived stream.
			streams, err := audit.ListStreams(env.Policy)
			require.NoError(t, err)
			for _, s := range streams {
				_, err := audit.ReadStream(env.Policy, s)
				assert.NoError(t, err, s.Key())
			}
			again, err := runRecordTreeTrace(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, Outcome{}, again, "second run changes nothing")
		})
	}
}

func TestRecordTreeTraceKeepsPublicationsAndExports(t *testing.T) {
	env := corpusEnv(t, "v4.20.4")
	rec := env.Policy.RecordRoot()
	full := filepath.Join(rec, "missions", "m-full", audit.MissionChunkFile("sid", 100, 200))
	exp := filepath.Join(rec, "missions", "m-exp", "log.jsonl")
	art := filepath.Join(rec, "missions", "m-art", "artifacts", "log.jsonl")
	for path, body := range map[string]string{
		full: `{"ts":"1970-01-01T00:00:00.000000100Z","event":"x","record":"full"}` + "\n",
		exp:  `{"ts":"2026-01-01T00:00:00Z","event":"x"}` + "\n",
		art:  "agent notes\n",
		filepath.Join(rec, "missions", "m-exp.contract.yaml"): "mission_id: m-exp\n",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	}
	_, err := runRecordTreeTrace(context.Background(), env)
	require.NoError(t, err)
	assert.FileExists(t, full)
	assert.FileExists(t, exp)
	assert.FileExists(t, art)
	assert.FileExists(t, filepath.Join(rec, "missions", "m-exp.contract.yaml"))
}

func TestRecordTreeTraceUnreadableFileIsResidual(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure requires an unprivileged process")
	}
	env := corpusEnv(t, "v4.8.0")
	prompt := filepath.Join(env.Policy.RecordRoot(), "missions", "m-2026-11-09-002", "delegations", "d-2026-11-09-001", "prompt.md")
	require.NoError(t, os.Chmod(prompt, 0))
	t.Cleanup(func() { _ = os.Chmod(prompt, 0o600) })
	out, err := runRecordTreeTrace(context.Background(), env)
	require.NoError(t, err)
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, "missions/m-2026-11-09-002/delegations/d-2026-11-09-001/prompt.md", out.Residuals[0].ID)
	assert.FileExists(t, prompt, "an unreadable file is never deleted")
}
