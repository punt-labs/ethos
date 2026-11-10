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

// obsoleteRecordLocks lists the record-tree locks step 15 owns.
func obsoleteRecordLocks(t *testing.T, env Env) []string {
	t.Helper()
	var out []string
	for _, f := range listFiles(t, env.Policy.RecordRoot()) {
		if isObsoleteRecordLock(f) {
			out = append(out, filepath.Join(env.Policy.RecordRoot(), f))
		}
	}
	return out
}

// globalLocks lists every lock under the global store: all of them are live.
func globalLocks(t *testing.T, env Env) []string {
	t.Helper()
	var out []string
	for _, f := range listFiles(t, env.Global) {
		if strings.HasSuffix(f, ".lock") {
			out = append(out, f)
		}
	}
	return out
}

func TestLeftoversOverCorpus(t *testing.T) {
	for _, v := range corpusVersions {
		t.Run(v, func(t *testing.T) {
			env := corpusEnv(t, v)
			obsolete := obsoleteRecordLocks(t, env)
			live := globalLocks(t, env)
			require.NotEmpty(t, live, "corpus %s must hold live global locks", v)
			out, err := runLeftovers(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, len(obsolete), out.Items)
			assert.False(t, out.Deferred)
			assert.Empty(t, out.Residuals)
			assert.Empty(t, obsoleteRecordLocks(t, env))
			assert.Equal(t, live, globalLocks(t, env), "every global lock is live and untouched")
			snapshot := hashFiles(t, env.Policy.RecordRoot(), env.Global)
			again, err := runLeftovers(context.Background(), env)
			require.NoError(t, err)
			assert.Equal(t, Outcome{}, again, "second run changes nothing")
			assert.Equal(t, snapshot, hashFiles(t, env.Policy.RecordRoot(), env.Global), "second run leaves every file as it was")
		})
	}
}

// Every lock a current writer opens by path, idle, survives the step: the
// global store's session, counter, ADR, mission, delegation and
// dispatch-pending locks, and everything under the local lock directory.
func TestLeftoversNeverDeletesLiveLocks(t *testing.T) {
	env := corpusEnv(t, "v4.22.1")
	local, err := env.Policy.LockPath("autorepair", "repository")
	require.NoError(t, err)
	live := []string{
		local,
		filepath.Join(env.Global, "locks", "abc.lock"),
		filepath.Join(env.Global, "sessions", "sess-live.lock"),
		filepath.Join(env.Global, "sessions", "sess-live", "dispatch-pending", ".lock"),
		filepath.Join(env.Global, "counters", "missions-2026-11-09.lock"),
		filepath.Join(env.Global, "adrs", ".adr.lock"),
		filepath.Join(env.Global, "missions", "m-2026-11-09-001.lock"),
		filepath.Join(env.Global, "missions", ".counter-2026-11-09.lock"),
		filepath.Join(env.Global, "delegations", "d-2026-11-09-001.lock"),
	}
	for _, p := range live {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, nil, 0o600))
	}
	out, err := runLeftovers(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 0, out.Items)
	for _, p := range live {
		assert.FileExists(t, p)
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

func TestLeftoversBusyObsoleteLockIsSkippedAndDeferred(t *testing.T) {
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

	require.NoError(t, f.Close())
	out, err = runLeftovers(context.Background(), env)
	require.NoError(t, err)
	assert.False(t, out.Deferred)
	assert.NoFileExists(t, busy)
}

func TestLeftoversDeletesEveryObsoleteRecordLockShape(t *testing.T) {
	env := corpusEnv(t, "v4.22.1")
	rec := env.Policy.RecordRoot()
	obsolete := []string{
		filepath.Join(rec, "missions.jsonl.lock"),
		filepath.Join(rec, "missions", ".create.lock"),
		filepath.Join(rec, "missions", "m-2026-11-09-001", ".lock"),
		filepath.Join(rec, "identities", "claude.yaml.lock"),
	}
	notOurs := filepath.Join(rec, "missions", "m-2026-11-09-001", "delegations", "d-1", ".lock")
	for _, p := range append(obsolete, notOurs) {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, nil, 0o600))
	}
	out, err := runLeftovers(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, len(obsolete), out.Items)
	for _, p := range obsolete {
		assert.NoFileExists(t, p)
	}
	assert.FileExists(t, notOurs, "an unknown lock shape is not claimed")
}
