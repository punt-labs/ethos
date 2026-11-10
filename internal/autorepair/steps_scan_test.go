package autorepair

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

// treeSnapshot maps every file under root to the SHA-256 of its bytes, so a
// second step run can be shown to change nothing (MEDIUM-11).
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return out // a stream-less repository has no local tree yet
	}
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[rel] = fmt.Sprintf("%x", sha256.Sum256(b))
		return nil
	}))
	return out
}

// assertSecondRunChangesNothing runs the step once more and asserts the
// outcome is empty and the local tree's file list and bytes are unchanged.
func assertSecondRunChangesNothing(t *testing.T, run func(context.Context, Env) (Outcome, error), env Env) {
	t.Helper()
	roots := []string{env.Policy.LocalRoot(), env.Roots.Store}
	if env.Roots.Checkout != env.Roots.Store {
		roots = append(roots, env.Roots.Checkout)
	}
	snap := func() []map[string]string {
		var out []map[string]string
		for _, r := range roots {
			out = append(out, treeSnapshot(t, r))
		}
		return out
	}
	before := snap()
	assert.Equal(t, Outcome{}, runStep(t, run, env), "second run reports nothing")
	assert.Equal(t, before, snap(), "second run changes no file in the local root, the store or the checkout")
}

func assertStreamClean(t *testing.T, env Env, s audit.Stream, dir string) {
	t.Helper()
	_, err := audit.ScanSealedDir(dir, s.Namespace, s.Session)
	assert.NoError(t, err, "ScanSealedDir is clean")
	_, err = audit.ReadStream(env.Policy, s)
	assert.NoError(t, err, "ReadStream is clean")
}

func damagedFixture(t *testing.T) (Env, *recLog, audit.Stream, string, audit.ChunkName) {
	t.Helper()
	env, log := damageEnv(t, "corrupt-sealed-chunk")
	s := audit.Stream{Namespace: audit.SessionNS, Session: damageSession}
	src := chunkFiles(t, env)[0]
	cn, kind := audit.Classify(filepath.Base(src), audit.SessionNS)
	require.Equal(t, audit.KindValid, kind)
	return env, log, s, src, cn
}

// HIGH-1 shape 1: a legacy raw .corrupt from the old git-mv quarantine, with
// no marker, is salvaged, covered by a marker, and deleted.
func TestCorruptChunksLegacyRawCorruptOrphan(t *testing.T) {
	env, _, s, src, cn := damagedFixture(t)
	dir := filepath.Dir(src)
	legacy := filepath.Join(dir, audit.CorruptName(cn.Stem()))
	require.NoError(t, os.Rename(src, legacy))
	_, err := audit.ScanSealedDir(dir, s.Namespace, "")
	var orphan *audit.OrphanCorruptError
	require.ErrorAs(t, err, &orphan, "fixture: the scan reports the orphan")

	out := runStep(t, runCorruptChunks, env)
	assert.Equal(t, 1, out.Items)
	assert.Empty(t, out.Residuals)
	assert.False(t, out.Deferred)
	assertStreamClean(t, env, s, dir)
	assert.NoFileExists(t, legacy, "the legacy raw bytes are deleted")
	m, err := audit.ReadMarker(filepath.Join(dir, cn.MarkerFile()))
	require.NoError(t, err)
	assert.True(t, m.HasGap(), "nothing in the bytes decoded; the range is a recorded loss")
	assertSecondRunChangesNothing(t, runCorruptChunks, env)
}

// HIGH-1 shape 2: a pending marker (RetiredSHA256 cleared, as QuarantineLocal
// writes before removing the source) beside a completed retirement record is
// resumed to a final marker.
func TestCorruptChunksPendingMarkerResumes(t *testing.T) {
	env, _, s, src, cn := damagedFixture(t)
	dir := filepath.Dir(src)
	m, err := audit.QuarantineLocal(env.Policy, s, src, dir, cn, "x", passGuard)
	require.NoError(t, err)
	m.RetiredSHA256 = ""
	b, err := audit.MarshalMarker(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, cn.MarkerFile()), b, 0o600))
	_, err = audit.ScanSealedDir(dir, s.Namespace, "")
	require.Error(t, err, "fixture: the pending marker leaves the scan dirty")

	out := runStep(t, runCorruptChunks, env)
	assert.Equal(t, 1, out.Items)
	assert.Empty(t, out.Residuals)
	assertStreamClean(t, env, s, dir)
	final, err := audit.ReadMarker(filepath.Join(dir, cn.MarkerFile()))
	require.NoError(t, err)
	assert.NotEmpty(t, final.RetiredSHA256, "the marker is final")
	assertSecondRunChangesNothing(t, runCorruptChunks, env)
}

// HIGH-1 shape 3: the crash window inside QuarantineLocal after the retirement
// record is written and before the damaged source is removed: damaged source
// plus retirement record, no marker.
func TestCorruptChunksDamagedSourceBesideRetirementRecord(t *testing.T) {
	env, _, s, src, cn := damagedFixture(t)
	dir := filepath.Dir(src)
	body, err := os.ReadFile(src)
	require.NoError(t, err)
	_, err = audit.QuarantineLocal(env.Policy, s, src, dir, cn, "x", passGuard)
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(dir, cn.MarkerFile())))
	require.NoError(t, os.WriteFile(src, body, 0o600))
	_, err = audit.ScanSealedDir(dir, s.Namespace, "")
	require.Error(t, err, "fixture: the scan is dirty")

	out := runStep(t, runCorruptChunks, env)
	assert.GreaterOrEqual(t, out.Items, 1)
	assert.Empty(t, out.Residuals)
	assertStreamClean(t, env, s, dir)
	assert.NoFileExists(t, src, "the damaged source is gone")
	assertSecondRunChangesNothing(t, runCorruptChunks, env)
}

// HIGH-1 shape 4: a valid chunk beside an uncovered .corrupt of the same
// stem (a hand-restored source next to a stale retirement artifact).
func TestCorruptChunksValidChunkBesideUncoveredCorrupt(t *testing.T) {
	env, _, s, src, _ := damagedFixture(t)
	dir := filepath.Dir(src)
	require.NoError(t, os.Remove(src))
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: 100, Last: 200}
	require.NoError(t, os.WriteFile(filepath.Join(dir, cn.ChunkFile()), []byte(stableLine(100, "A")+stableLine(200, "B")), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, audit.CorruptName(cn.Stem())), []byte("garbage\n"), 0o600))
	_, err := audit.ScanSealedDir(dir, s.Namespace, "")
	require.Error(t, err, "fixture: the uncovered .corrupt leaves the scan dirty")

	out := runStep(t, runCorruptChunks, env)
	assert.GreaterOrEqual(t, out.Items, 1)
	assert.Empty(t, out.Residuals)
	assertStreamClean(t, env, s, dir)
	data, err := audit.ReadStream(env.Policy, s)
	require.NoError(t, err)
	assert.Len(t, data.Lines, 2, "the valid chunk's lines are intact")
	assertSecondRunChangesNothing(t, runCorruptChunks, env)
}

// MEDIUM-8: step 6 meets a stream whose archive dir is dirty (an orphan
// .corrupt step 7 will fix). It defers that stream rather than recording a
// residual, leaves the live file untouched, and redacts it once the scan is
// clean.
func TestLiveTailsDefersDirtyScanThenRedacts(t *testing.T) {
	home := fixtureHome(t)
	env, log, s, src, cn := damagedFixture(t)
	dir := filepath.Dir(src)
	require.NoError(t, os.Rename(src, filepath.Join(dir, audit.CorruptName(cn.Stem()))))
	live := s.LivePath(env.Policy)
	plant(t, env.Policy, live, `{"session":"`+damageSession+`","tool":"Bash","tool_input":{"command":"cat `+home+`/x `+fakeToken+`"},"ts":"`+audit.FormatLineTS(1793786500000000000)+`"}`+"\n")
	before, err := os.ReadFile(live)
	require.NoError(t, err)

	out := runStep(t, runLiveTails, env)
	assert.True(t, out.Deferred, "a dirty scan is transient: step 7 repairs it")
	assert.Empty(t, out.Residuals, "not a permanent condition")
	assert.Equal(t, 0, out.Items)
	after, err := os.ReadFile(live)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a deferred stream is untouched")
	assert.Contains(t, log.text(), "defer-dirty-scan")

	require.Equal(t, 1, runStep(t, runCorruptChunks, env).Items)
	out = runStep(t, runLiveTails, env)
	assert.False(t, out.Deferred)
	assert.Equal(t, 1, out.Items)
	assert.Empty(t, walkContains(t, env.Roots.Store, home, fakeToken))
	assertSecondRunChangesNothing(t, runLiveTails, env)
}

func passGuard(raw []byte) ([]byte, string, bool) { return raw, "", false }
