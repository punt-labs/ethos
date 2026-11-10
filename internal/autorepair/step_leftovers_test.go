//go:build linux || darwin

package autorepair

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func staleLocks(t *testing.T, env Env) []string {
	t.Helper()
	var out []string
	for _, root := range []string{env.Policy.RecordRoot(), env.Global} {
		for _, f := range listFiles(t, root) {
			if strings.HasSuffix(f, ".lock") && !strings.HasPrefix(f, "locks/") {
				out = append(out, filepath.Join(root, f))
			}
		}
	}
	return out
}

func TestLeftoversOverCorpus(t *testing.T) {
	for _, v := range corpusVersions {
		t.Run(v, func(t *testing.T) {
			env := corpusEnv(t, v)
			before := staleLocks(t, env)
			require.NotEmpty(t, before, "corpus %s must contain lock files", v)
			out, err := runLeftovers(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, len(before), out.Items)
			assert.False(t, out.Deferred)
			assert.Empty(t, out.Residuals)
			assert.Empty(t, staleLocks(t, env))
			again, err := runLeftovers(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, Outcome{}, again, "second run changes nothing")
		})
	}
}

func TestLeftoversStagingAndTemps(t *testing.T) {
	env := corpusEnv(t, "v4.22.1")
	rec := env.Policy.RecordRoot()
	old := env.Now.Add(-3 * time.Hour)
	fresh := env.Now
	stage := filepath.Join(rec, "missions", layout.MigrateStagingPrefix("m-x")+"123")
	require.NoError(t, os.MkdirAll(filepath.Join(stage, "sub"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(stage, "sub", "f"), []byte("x"), 0o600))
	oldTmp := filepath.Join(rec, "missions", "m-x", "contract.yaml"+layout.TmpMarker)
	legacyTmp := filepath.Join(rec, "missions", "m-x", "results.yaml"+layout.LegacyTmpSuffix)
	freshTmp := filepath.Join(rec, "missions", "m-x", "reflections.yaml"+layout.TmpMarker)
	keep := filepath.Join(rec, "missions", "m-x", "artifacts", "n.ethos-tmp")
	for _, f := range []string{oldTmp, legacyTmp, freshTmp, keep} {
		require.NoError(t, os.MkdirAll(filepath.Dir(f), 0o700))
		require.NoError(t, os.WriteFile(f, []byte("x"), 0o600))
	}
	for _, f := range []string{stage, oldTmp, legacyTmp, keep} {
		require.NoError(t, os.Chtimes(f, old, old))
	}
	require.NoError(t, os.Chtimes(freshTmp, fresh, fresh))

	out, err := runLeftovers(context.Background(), env)
	require.NoError(t, err)
	assert.NoDirExists(t, stage)
	assert.NoFileExists(t, oldTmp)
	assert.NoFileExists(t, legacyTmp)
	assert.FileExists(t, freshTmp, "a fresh temp may be in flight")
	assert.FileExists(t, keep, "agent artifacts are never touched")
	assert.GreaterOrEqual(t, out.Items, 3)
	again, err := runLeftovers(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, again.Items)
}

func TestLeftoversBusyLockIsSkippedAndDeferred(t *testing.T) {
	env := corpusEnv(t, "v4.20.4")
	busy := filepath.Join(env.Policy.RecordRoot(), "missions.jsonl.lock")
	f, err := os.OpenFile(busy, os.O_RDWR, 0)
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })
	require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))

	out, err := runLeftovers(context.Background(), env)
	require.NoError(t, err)
	assert.True(t, out.Deferred)
	assert.FileExists(t, busy)
	assert.Equal(t, []string{busy}, staleLocks(t, Env{Policy: env.Policy, Global: t.TempDir()}))

	require.NoError(t, f.Close())
	out, err = runLeftovers(context.Background(), env)
	require.NoError(t, err)
	assert.False(t, out.Deferred)
	assert.NoFileExists(t, busy)
}

func TestLeftoversLeavesCurrentLocks(t *testing.T) {
	env := corpusEnv(t, "v4.22.1")
	local, err := env.Policy.LockPath("autorepair", "repository")
	require.NoError(t, err)
	global := filepath.Join(env.Global, "locks", "abc.lock")
	for _, p := range []string{local, global} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, nil, 0o600))
	}
	_, err = runLeftovers(context.Background(), env)
	require.NoError(t, err)
	assert.FileExists(t, local)
	assert.FileExists(t, global)
}
