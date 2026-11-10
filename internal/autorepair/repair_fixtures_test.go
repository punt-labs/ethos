package autorepair

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

const fixT0 = int64(1793786400000001000)

func fixEnv(t *testing.T) (Env, *recLog, audit.Stream, string, string) {
	t.Helper()
	home := fixtureHome(t)
	env, log := damageEnv(t, "corrupt-sealed-chunk")
	s := audit.Stream{Namespace: audit.SessionNS, Session: damageSession}
	files := chunkFiles(t, env)
	require.Len(t, files, 1)
	dir := filepath.Dir(files[0])
	require.NoError(t, os.Remove(files[0]))
	return env, log, s, dir, home
}

func fixCmd(ts int64, tool, cmd string) string {
	return `{"session":"` + damageSession + `","tool":"` + tool + `","tool_input":{"command":"` + cmd + `"},"ts":"` + audit.FormatLineTS(ts) + `"}` + "\n"
}

func fixChunk(dir string, first, last int64) string {
	return filepath.Join(dir, audit.ChunkName{Namespace: audit.SessionNS, First: first, Last: last}.ChunkFile())
}

// fixStoreSnap hashes the whole store except the lock files, which taking a
// stream lock creates and which carry no audit content.
func fixStoreSnap(t *testing.T, env Env) map[string]string {
	snap := treeSnapshot(t, env.Roots.Store)
	for k := range snap {
		if strings.Contains(k, "/locks/") {
			delete(snap, k)
		}
	}
	return snap
}

func fixSecondRun(t *testing.T, env Env) {
	t.Helper()
	before := fixStoreSnap(t, env)
	a := runStep(t, runCorruptChunks, env)
	b := runStep(t, runLiveTails, env)
	assert.Equal(t, Outcome{}, a, "second corrupt-chunks run")
	assert.Equal(t, Outcome{}, b, "second live-tails run")
	assert.Equal(t, before, fixStoreSnap(t, env), "second run: identical store tree")
}

func fixMarkers(t *testing.T, dir string) map[string]audit.Marker {
	t.Helper()
	out := map[string]audit.Marker{}
	m, err := filepath.Glob(filepath.Join(dir, "*.quarantine*"))
	require.NoError(t, err)
	for _, p := range m {
		mk, err := audit.ReadMarker(p)
		if err == nil {
			out[filepath.Base(p)] = mk
		}
	}
	return out
}

// (a) decodable token line + home path line around a garbage line.
func TestRepairFixtureTokenAndHomeRedacted(t *testing.T) {
	env, _, s, dir, home := fixEnv(t)
	src := fixChunk(dir, fixT0, fixT0+2000)
	body := fixCmd(fixT0, "Bash", "echo "+fakeToken) + "garbage-not-json\n" + fixCmd(fixT0+2000, "Bash", "cat "+home+"/secret.txt")
	require.NoError(t, os.WriteFile(src, []byte(body), 0o600))

	a := runStep(t, runCorruptChunks, env)
	b := runStep(t, runLiveTails, env)
	t.Logf("corrupt-chunks=%+v live-tails=%+v", a, b)
	data, err := audit.ReadStream(env.Policy, s)
	require.NoError(t, err)
	assert.Len(t, data.Lines, 2, "both decodable lines kept")
	for _, l := range data.Lines {
		t.Logf("kept: %s", l.Raw)
	}
	t.Logf("gaps=%v markers=%v", data.Gaps, fixMarkers(t, dir))
	assert.Empty(t, walkContains(t, env.Roots.Store, fakeToken, home), "no token/home anywhere in store")
	assert.Empty(t, corruptFiles(t, env.Roots.Store))
	fixSecondRun(t, env)
}

// (a2) an undecodable interior line that held a real audit entry.
func TestRepairFixtureUndecodableInteriorLineIsRecorded(t *testing.T) {
	env, _, s, dir, _ := fixEnv(t)
	src := fixChunk(dir, fixT0, fixT0+2000)
	interior := `{"session":"` + damageSession + `","tool":"Write","tool_input":{"content":"bad \q escape"},"ts":"` + audit.FormatLineTS(fixT0+1000) + `"}` + "\n"
	body := stableLine(fixT0, "A") + interior + stableLine(fixT0+2000, "C")
	require.NoError(t, os.WriteFile(src, []byte(body), 0o600))
	a := runStep(t, runCorruptChunks, env)
	t.Logf("corrupt-chunks=%+v", a)
	data, err := audit.ReadStream(env.Policy, s)
	require.NoError(t, err)
	t.Logf("lines=%d gaps=%v markers=%v", len(data.Lines), data.Gaps, fixMarkers(t, dir))
	assert.NotEmpty(t, data.Gaps, "the entry at ts T0+1000 is gone; a marker must name it")
}

// (b) a decodable line duplicating a neighbor's ts with a different payload.
func TestRepairFixtureConflictRecordedAsLost(t *testing.T) {
	env, log, s, dir, _ := fixEnv(t)
	require.NoError(t, os.WriteFile(fixChunk(dir, fixT0+1000, fixT0+1000), []byte(stableLine(fixT0+1000, "Xsurvivor")), 0o600))
	src := fixChunk(dir, fixT0, fixT0+2000)
	body := stableLine(fixT0, "A") + stableLine(fixT0+1000, "Yconflict") + "garbage\n" + stableLine(fixT0+2000, "C")
	require.NoError(t, os.WriteFile(src, []byte(body), 0o600))
	a := runStep(t, runCorruptChunks, env)
	b := runStep(t, runLiveTails, env)
	t.Logf("corrupt-chunks=%+v live-tails=%+v", a, b)
	t.Logf("log:\n%s", log.text())
	data, err := audit.ReadStream(env.Policy, s)
	require.NoError(t, err)
	t.Logf("lines=%d gaps=%v markers=%v", len(data.Lines), data.Gaps, fixMarkers(t, dir))
	t.Logf("Yconflict bytes survive in: %v", walkContains(t, env.Roots.Store, "Yconflict"))
	assert.NotEmpty(t, data.Gaps, "the refused line is a recorded loss")
	fixSecondRun(t, env)
}

// (c1) zero-byte .corrupt.
func TestRepairFixtureZeroByteCorrupt(t *testing.T) {
	env, log, s, dir, _ := fixEnv(t)
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 2000}
	c := filepath.Join(dir, audit.CorruptName(cn.Stem()))
	require.NoError(t, os.WriteFile(c, nil, 0o600))
	a := runStep(t, runCorruptChunks, env)
	t.Logf("corrupt-chunks=%+v log:\n%s", a, log.text())
	_, statErr := os.Stat(c)
	t.Logf("corrupt exists after: %v ; markers=%v", statErr == nil, fixMarkers(t, dir))
	if statErr != nil {
		mk, err := audit.ReadMarker(filepath.Join(dir, cn.MarkerFile()))
		require.NoError(t, err, "deleted only under a covering marker")
		assert.True(t, mk.HasGap())
	}
	_, err := audit.ReadStream(env.Policy, s)
	assert.NoError(t, err)
	fixSecondRun(t, env)
}

// (c2) truncated JSON retirement record with its raw bytes still held under
// quarantine/<key>/<sha>.corrupt (an interrupted older run).
func TestRepairFixtureTruncatedRetirementRecord(t *testing.T) {
	env, log, s, dir, home := fixEnv(t)
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 2000}
	raw := fixCmd(fixT0, "Bash", "echo "+fakeToken) + "garbage\n" + fixCmd(fixT0+2000, "Bash", "cat "+home+"/x")
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(s.Key())))
	rawPath := filepath.Join(env.Policy.LocalRoot(), "quarantine", key, sum+".corrupt")
	plant(t, env.Policy, rawPath, raw)
	c := filepath.Join(dir, audit.CorruptName(cn.Stem()))
	require.NoError(t, os.WriteFile(c, []byte(`{"withheld":"raw corrupt bytes retained locally","sha256":"`+sum[:20]), 0o600))
	a := runStep(t, runCorruptChunks, env)
	b := runStep(t, runLiveTails, env)
	t.Logf("corrupt-chunks=%+v live-tails=%+v log:\n%s", a, b, log.text())
	_, statErr := os.Stat(c)
	t.Logf("corrupt exists after: %v ; markers=%v", statErr == nil, fixMarkers(t, dir))
	data, err := audit.ReadStream(env.Policy, s)
	if err == nil {
		t.Logf("lines=%d gaps=%v", len(data.Lines), data.Gaps)
	}
	assert.Empty(t, walkContains(t, env.Roots.Store, fakeToken, home), "no token/home anywhere in store")
}

// (c3) interrupted QuarantineLocal whose replacement has the source's name:
// replacement installed, raw bytes held, final marker not yet written.
func TestRepairFixtureInterruptedQuarantineSameNameReplacement(t *testing.T) {
	env, log, s, dir, _ := fixEnv(t)
	require.NoError(t, os.WriteFile(fixChunk(dir, fixT0+1000, fixT0+1000), []byte(stableLine(fixT0+1000, "Xsurvivor")), 0o600))
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 2000}
	src := filepath.Join(dir, cn.ChunkFile())
	raw := stableLine(fixT0, "A") + fixCmd(fixT0+1000, "Bash", "echo "+fakeToken) + "garbage\n" + stableLine(fixT0+2000, "C")
	require.NoError(t, os.WriteFile(src, []byte(raw), 0o600))
	m, err := audit.QuarantineLocal(env.Policy, s, src, dir, cn, "fixture", lineGuard(env.Policy, audit.SessionNS))
	require.NoError(t, err)
	require.True(t, m.HasGap(), "fixture: the complete run records the conflict as a gap")
	require.FileExists(t, src, "fixture: replacement carries the source's name")
	// Crash window: replacement installed, final marker and raw deletion not done.
	require.NoError(t, os.Remove(filepath.Join(dir, cn.MarkerFile())))
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(s.Key())))
	rawPath := filepath.Join(env.Policy.LocalRoot(), "quarantine", key, sum+".corrupt")
	rec, err := os.ReadFile(filepath.Join(dir, audit.CorruptName(cn.Stem())))
	require.NoError(t, err)
	plant(t, env.Policy, rawPath, raw)
	plant(t, env.Policy, rawPath+".json", string(rec))

	a := runStep(t, runCorruptChunks, env)
	t.Logf("corrupt-chunks=%+v log:\n%s", a, log.text())
	mk, err := audit.ReadMarker(filepath.Join(dir, cn.MarkerFile()))
	require.NoError(t, err)
	t.Logf("final marker: %+v", mk)
	assert.True(t, mk.HasGap(), "the conflict loss must survive resume")
	assert.Empty(t, walkContains(t, env.Roots.Store, fakeToken), "raw bytes with the token are gone")
}

// (d) 65 orphan legacy .corrupt files in one directory: more than the 64-pass budget.
func TestRepairFixturePassBudget(t *testing.T) {
	env, log, s, dir, _ := fixEnv(t)
	var stems []audit.ChunkName
	for i := int64(0); i < 65; i++ {
		cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0 + i*100, Last: fixT0 + i*100 + 50}
		stems = append(stems, cn)
		require.NoError(t, os.WriteFile(filepath.Join(dir, audit.CorruptName(cn.Stem())), []byte("garbage\n"), 0o600))
	}
	a := runStep(t, runCorruptChunks, env)
	t.Logf("first run: Items=%d Deferred=%v Residuals=%v", a.Items, a.Deferred, a.Residuals)
	left := 0
	for _, cn := range stems {
		if _, err := os.Stat(filepath.Join(dir, audit.CorruptName(cn.Stem()))); err == nil {
			left++
			continue
		}
		mk, err := audit.ReadMarker(filepath.Join(dir, cn.MarkerFile()))
		require.NoError(t, err, "deleted %s has its marker", cn.Stem())
		assert.True(t, mk.HasGap())
	}
	t.Logf(".corrupt left after first run: %d", left)
	assert.True(t, a.Deferred, "pass budget is transient: Deferred")
	assert.Empty(t, a.Residuals, "pass budget is transient: no Residual")
	b := runStep(t, runCorruptChunks, env)
	t.Logf("second run: Items=%d Deferred=%v Residuals=%v", b.Items, b.Deferred, b.Residuals)
	_, err := audit.ReadStream(env.Policy, s)
	assert.NoError(t, err)
	_ = log
}

// (e) live-tails defers while the stream dir holds an uncovered .corrupt.
func TestRepairFixtureLiveTailsDefersThenRedacts(t *testing.T) {
	env, _, s, dir, home := fixEnv(t)
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 2000}
	require.NoError(t, os.WriteFile(filepath.Join(dir, audit.CorruptName(cn.Stem())), []byte("legacy raw\n"), 0o600))
	live := s.LivePath(env.Policy)
	plant(t, env.Policy, live, fixCmd(fixT0+9000, "Bash", "cat "+home+"/y "+fakeToken))
	before, err := os.ReadFile(live)
	require.NoError(t, err)
	b := runStep(t, runLiveTails, env)
	after, err := os.ReadFile(live)
	require.NoError(t, err)
	t.Logf("live-tails (dirty)=%+v identical=%v", b, string(before) == string(after))
	assert.True(t, b.Deferred)
	assert.Empty(t, b.Residuals)
	assert.Equal(t, before, after)
	a := runStep(t, runCorruptChunks, env)
	b = runStep(t, runLiveTails, env)
	t.Logf("corrupt-chunks=%+v live-tails=%+v", a, b)
	assert.Equal(t, 1, b.Items)
	assert.Empty(t, walkContains(t, env.Roots.Store, fakeToken, home))
	fixSecondRun(t, env)
}

// (f) stream lock held: both steps defer, touch nothing, print nothing.
func TestRepairFixtureLockHeldPrintsNothing(t *testing.T) {
	env, _, s, dir, home := fixEnv(t)
	require.NoError(t, os.WriteFile(fixChunk(dir, fixT0, fixT0+2000), []byte(stableLine(fixT0, "A")+"garbage\n"), 0o600))
	plant(t, env.Policy, s.LivePath(env.Policy), fixCmd(fixT0+9000, "Bash", "cat "+home+"/y "+fakeToken))
	before := fixStoreSnap(t, env)
	rOut, wOut, err := os.Pipe()
	require.NoError(t, err)
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = wOut, wOut
	var a, b Outcome
	lerr := audit.WithStreamLock(env.Policy, s, func() error {
		a = runStep(t, runCorruptChunks, env)
		b = runStep(t, runLiveTails, env)
		return nil
	})
	os.Stdout, os.Stderr = oldOut, oldErr
	require.NoError(t, wOut.Close())
	printed, _ := io.ReadAll(rOut)
	require.NoError(t, lerr)
	t.Logf("corrupt-chunks=%+v live-tails=%+v printed=%q", a, b, string(printed))
	assert.True(t, a.Deferred)
	assert.True(t, b.Deferred)
	assert.Empty(t, a.Residuals)
	assert.Empty(t, b.Residuals)
	assert.Empty(t, strings.TrimSpace(string(printed)))
	assert.Equal(t, before, fixStoreSnap(t, env), "nothing touched")
}

// (d2) with maxRepairPasses lowered to 3: 5 orphans.
func TestRepairFixturePassBudgetLowered(t *testing.T) {
	env, log, _, dir, _ := fixEnv(t)
	old := maxRepairPasses
	maxRepairPasses = 3
	t.Cleanup(func() { maxRepairPasses = old })
	var stems []audit.ChunkName
	for i := int64(0); i < 5; i++ {
		cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0 + i*100, Last: fixT0 + i*100 + 50}
		stems = append(stems, cn)
		require.NoError(t, os.WriteFile(filepath.Join(dir, audit.CorruptName(cn.Stem())), []byte("garbage\n"), 0o600))
	}
	a := runStep(t, runCorruptChunks, env)
	t.Logf("run: Items=%d Deferred=%v Residuals=%v\nlog:\n%s", a.Items, a.Deferred, a.Residuals, log.text())
	for _, cn := range stems {
		if _, err := os.Stat(filepath.Join(dir, audit.CorruptName(cn.Stem()))); err == nil {
			continue
		}
		_, err := audit.ReadMarker(filepath.Join(dir, cn.MarkerFile()))
		require.NoError(t, err, "deleted %s has its marker", cn.Stem())
	}
	assert.Empty(t, a.Residuals, "an unfinished pass budget is transient: it must not be a Residual")
	assert.True(t, a.Deferred)
}

// (b2) same-name replacement: the source is overwritten in place before the
// gap marker is written. Fail the marker write (a directory at its path) to
// stand in for a crash between the two.
func TestRepairFixtureMarkerAfterInPlaceReplace(t *testing.T) {
	env, log, s, dir, _ := fixEnv(t)
	require.NoError(t, os.WriteFile(fixChunk(dir, fixT0+1000, fixT0+1000), []byte(stableLine(fixT0+1000, "Xsurvivor")), 0o600))
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 2000}
	src := filepath.Join(dir, cn.ChunkFile())
	require.NoError(t, os.WriteFile(src, []byte(stableLine(fixT0, "A")+stableLine(fixT0+1000, "Yconflict")+"garbage\n"+stableLine(fixT0+2000, "C")), 0o600))
	// A read-only directory refuses the marker write (and the replacement
	// rename); the raw bytes must survive until the marker can be written.
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	a := runStep(t, runCorruptChunks, env)
	t.Logf("run1=%+v\nlog:\n%s", a, log.text())
	require.NoError(t, os.Chmod(dir, 0o700))
	raw, err := os.ReadFile(src)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "Yconflict", "source bytes intact after the failed marker write")
	b := runStep(t, runCorruptChunks, env)
	t.Logf("run2 (after the obstacle is gone)=%+v", b)
	data, err := audit.ReadStream(env.Policy, s)
	require.NoError(t, err)
	t.Logf("lines=%d gaps=%v Yconflict anywhere=%v", len(data.Lines), data.Gaps, walkContains(t, env.Roots.Store, "Yconflict"))
	assert.NotEmpty(t, data.Gaps, "the refused conflicting line must stay recorded as lost")
}

// (g) legacy raw .corrupt holding a decodable line the valid same-stem chunk
// does not hold.
func TestRepairFixtureLegacyRawBesideValidHoldsUniqueLine(t *testing.T) {
	env, log, s, dir, _ := fixEnv(t)
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 2000}
	require.NoError(t, os.WriteFile(filepath.Join(dir, cn.ChunkFile()), []byte(stableLine(fixT0, "A")+stableLine(fixT0+2000, "C")), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, audit.CorruptName(cn.Stem())), []byte(stableLine(fixT0, "A")+stableLine(fixT0+1000, "UNIQUEB")+"garbage\n"), 0o600))
	a := runStep(t, runCorruptChunks, env)
	t.Logf("run=%+v\nlog:\n%s", a, log.text())
	data, err := audit.ReadStream(env.Policy, s)
	require.NoError(t, err)
	t.Logf("lines=%d gaps=%v UNIQUEB anywhere=%v", len(data.Lines), data.Gaps, walkContains(t, env.Roots.Store, "UNIQUEB"))
	kept := false
	for _, l := range data.Lines {
		kept = kept || strings.Contains(string(l.Raw), "UNIQUEB")
	}
	assert.True(t, kept || len(data.Gaps) > 0, "the unique line is kept or its loss is recorded")
}

// Finding 8: a malformed chunk name is permanent (Residual, class reason, no
// raw path) in both steps.
func TestMalformedChunkNameIsResidual(t *testing.T) {
	env, _, s, dir, _ := fixEnv(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "audit-bad-name.jsonl"), []byte("x\n"), 0o600))
	plant(t, env.Policy, s.LivePath(env.Policy), fixCmd(fixT0+9000, "Bash", "echo hi"))
	for _, run := range []func(context.Context, Env) (Outcome, error){runCorruptChunks, runLiveTails} {
		out := runStep(t, run, env)
		assert.False(t, out.Deferred, "a permanent condition is never deferred")
		require.Len(t, out.Residuals, 1)
		assert.NotContains(t, out.Residuals[0].Reason, dir, "the reason names the class, not a path")
	}
}

// Finding 8: the classifier calls a non-regular file permanent and a
// permission error transient. (A non-regular chunk or an unreadable one stops
// audit.ListStreams first, so the step sees them only through this function.)
func TestPermanentScanReason(t *testing.T) {
	dir := t.TempDir()
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 5}
	link := filepath.Join(dir, cn.ChunkFile())
	require.NoError(t, os.Symlink("/nonexistent", link))
	_, err := audit.ScanSealedDir(dir, audit.SessionNS, "")
	reason, ok := permanentScanReason(err)
	assert.True(t, ok, "a non-regular file is permanent: %v", err)
	assert.NotContains(t, reason, dir)

	_, err = audit.ScanSealedDir(dir, audit.SessionNS, "")
	require.Error(t, err)
	require.NoError(t, os.Remove(link))
	if os.Geteuid() != 0 {
		require.NoError(t, os.WriteFile(link, []byte("x\n"), 0o000))
		_, err = audit.ScanSealedDir(dir, audit.SessionNS, "")
		require.Error(t, err)
		_, ok = permanentScanReason(err)
		assert.False(t, ok, "a permission error may pass: transient")
	}
}

// Finding 6: held raw bytes with no .corrupt at all are recovered through the
// guard and deleted only under a replacement and a marker.
func TestOrphanedHeldQuarantineIsRecovered(t *testing.T) {
	env, _, s, dir, home := fixEnv(t)
	raw := fixCmd(fixT0, "Bash", "echo "+fakeToken) + "garbage\n" + fixCmd(fixT0+2000, "Bash", "cat "+home+"/x")
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(s.Key())))
	held := filepath.Join(env.Policy.LocalRoot(), "quarantine", key, sum+".corrupt")
	plant(t, env.Policy, held, raw)
	plant(t, env.Policy, held+".json", `{"withheld":"x"`)

	out := runStep(t, runCorruptChunks, env)
	assert.Equal(t, 1, out.Items)
	assert.Empty(t, out.Residuals)
	assert.Empty(t, walkContains(t, env.Roots.Store, fakeToken, home), "no token or home path survives")
	assert.NoFileExists(t, held)
	assert.NoFileExists(t, held+".json")
	data, err := audit.ReadStream(env.Policy, s)
	require.NoError(t, err)
	assert.Len(t, data.Lines, 2)
	assert.NotEmpty(t, data.Gaps, "the garbage line is a recorded loss")
	_ = dir
	fixSecondRun(t, env)
}

// A legacy raw .corrupt that duplicates a valid chunk exactly lost nothing: its
// marker exists and records no gap.
func TestRepairFixtureLegacyDuplicateIsNoGap(t *testing.T) {
	env, log, s, dir, _ := fixEnv(t)
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 2000}
	body := stableLine(fixT0, "A") + stableLine(fixT0+2000, "C")
	require.NoError(t, os.WriteFile(filepath.Join(dir, cn.ChunkFile()), []byte(body), 0o600))
	c := filepath.Join(dir, audit.CorruptName(cn.Stem()))
	require.NoError(t, os.WriteFile(c, []byte(body), 0o600))
	a := runStep(t, runCorruptChunks, env)
	t.Logf("run1 corrupt=%+v\nlog:\n%s", a, log.text())
	assert.Empty(t, a.Residuals)
	assert.NoFileExists(t, c)
	mk, err := audit.ReadMarker(filepath.Join(dir, cn.MarkerFile()))
	require.NoError(t, err, "marker exists")
	assert.False(t, mk.HasGap(), "nothing lost")
	data, err := audit.ReadStream(env.Policy, s)
	require.NoError(t, err)
	assert.Empty(t, data.Gaps, "ReadStream reports no gap")
	assert.Contains(t, log.text(), "gap=false")
	fixSecondRun(t, env)
}

// A chunk name whose first ts is past its last names no range: malformed, so
// permanent.
func TestInvertedChunkNameIsResidual(t *testing.T) {
	env, _, s, dir, _ := fixEnv(t)
	require.NoError(t, os.WriteFile(fixChunk(dir, fixT0+5000, fixT0), []byte(stableLine(fixT0, "A")), 0o600))
	plant(t, env.Policy, s.LivePath(env.Policy), fixCmd(fixT0+9000, "Bash", "echo hi"))
	for _, run := range []func(context.Context, Env) (Outcome, error){runCorruptChunks, runLiveTails} {
		out := runStep(t, run, env)
		assert.False(t, out.Deferred, "a malformed name is never deferred")
		require.Len(t, out.Residuals, 1)
		assert.NotContains(t, out.Residuals[0].Reason, dir)
	}
}

// A non-regular or unreadable artifact under a chunk or marker name is
// classified by the step, not turned into an error by stream discovery:
// permanent for a non-regular file, transient for a permission failure.
func TestArtifactTypeIsClassifiedByTheStep(t *testing.T) {
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 5}
	cases := []struct {
		name         string
		plant        func(t *testing.T, dir string)
		wantResidual bool
	}{
		{"chunk-is-directory", func(t *testing.T, dir string) {
			require.NoError(t, os.Mkdir(filepath.Join(dir, cn.ChunkFile()), 0o700))
		}, true},
		{"marker-is-directory", func(t *testing.T, dir string) {
			require.NoError(t, os.Mkdir(filepath.Join(dir, cn.MarkerFile()), 0o700))
		}, true},
		{"chunk-mode-000", func(t *testing.T, dir string) {
			if os.Geteuid() == 0 {
				t.Skip("permission denial requires a non-root process")
			}
			p := filepath.Join(dir, cn.ChunkFile())
			require.NoError(t, os.WriteFile(p, []byte(stableLine(fixT0, "A")), 0o000))
			t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, _, s, dir, _ := fixEnv(t)
			tc.plant(t, dir)
			plant(t, env.Policy, s.LivePath(env.Policy), fixCmd(fixT0+9000, "Bash", "echo hi"))
			for _, run := range []func(context.Context, Env) (Outcome, error){runCorruptChunks, runLiveTails} {
				ctx, cancel := context.WithTimeout(context.Background(), StepBudget)
				out, err := run(ctx, env)
				cancel()
				require.NoError(t, err, "the step reports a condition, it does not fail")
				if tc.wantResidual {
					assert.False(t, out.Deferred)
					require.Len(t, out.Residuals, 1)
					assert.NotContains(t, out.Residuals[0].Reason, dir)
				} else {
					assert.True(t, out.Deferred)
					assert.Empty(t, out.Residuals)
				}
			}
		})
	}
}

// A permission failure while repairing is transient: the step defers, records
// no residual, and the next run completes once the directory is writable.
func TestReadOnlyArchiveDirDefersThenCompletes(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission denial requires a non-root process")
	}
	env, _, s, dir, _ := fixEnv(t)
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 2000}
	c := filepath.Join(dir, audit.CorruptName(cn.Stem()))
	require.NoError(t, os.WriteFile(c, nil, 0o600))
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	out := runStep(t, runCorruptChunks, env)
	t.Logf("run1=%+v", out)
	assert.True(t, out.Deferred, "a permission failure is transient")
	assert.Empty(t, out.Residuals)
	require.NoError(t, os.Chmod(dir, 0o700))
	out = runStep(t, runCorruptChunks, env)
	t.Logf("run2=%+v", out)
	assert.False(t, out.Deferred)
	assert.Empty(t, out.Residuals)
	assert.NoFileExists(t, c)
	_, err := audit.ReadStream(env.Policy, s)
	require.NoError(t, err)
	fixSecondRun(t, env)
}

// A legacy raw .corrupt spanning the same range as a valid chunk, with a line
// the chunk lacks (here carrying a token), keeps that line redacted in a chunk
// of its own, deletes the raw bytes and leaves no token behind.
func TestLegacyRawSameRangeAsValidKeepsUniqueLine(t *testing.T) {
	env, log, s, dir, home := fixEnv(t)
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 2000}
	require.NoError(t, os.WriteFile(filepath.Join(dir, cn.ChunkFile()), []byte(stableLine(fixT0, "A")+stableLine(fixT0+2000, "C")), 0o600))
	c := filepath.Join(dir, audit.CorruptName(cn.Stem()))
	require.NoError(t, os.WriteFile(c, []byte(stableLine(fixT0, "A")+fixCmd(fixT0+1000, "Bash", "echo "+fakeToken)+stableLine(fixT0+2000, "C")), 0o600))
	a := runStep(t, runCorruptChunks, env)
	t.Logf("run1=%+v\nlog:\n%s", a, log.text())
	assert.Empty(t, a.Residuals)
	assert.False(t, a.Deferred)
	assert.NoFileExists(t, c)
	assert.Empty(t, walkContains(t, env.Roots.Store, fakeToken, home), "no token survives")
	data, err := audit.ReadStream(env.Policy, s)
	require.NoError(t, err)
	t.Logf("lines=%d gaps=%v", len(data.Lines), data.Gaps)
	kept := false
	for _, l := range data.Lines {
		kept = kept || l.TS == fixT0+1000
	}
	assert.True(t, kept || len(data.Gaps) > 0, "the unique line is kept or its loss is recorded")
	fixSecondRun(t, env)
}

// Held raw bytes with no parseable ts and no sidecar are never named from the
// file's mtime: an mtime outside the range a chunk name holds must neither
// break the stream's scan nor yield a marker that reads as no gap.
func TestHeldBytesWithoutTSAreNotNamedFromMtime(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mtime time.Time
	}{
		{"epoch", time.Unix(0, 0)},
		{"1969", time.Unix(-86400, 0)},
		{"2500", time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, log, s, dir, home := fixEnv(t)
			raw := "garbage with " + fakeToken + " and " + home + "/z\n"
			sum := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
			held := filepath.Join(audit.HeldQuarantineDir(env.Policy, s), sum+".corrupt")
			plant(t, env.Policy, held, raw)
			require.NoError(t, os.Chtimes(held, tc.mtime, tc.mtime))
			a := runStep(t, runCorruptChunks, env)
			t.Logf("run1=%+v\nlog:\n%s", a, log.text())
			assert.Empty(t, a.Residuals)
			assert.NoFileExists(t, held)
			assert.Empty(t, walkContains(t, env.Roots.Store, fakeToken, home), "no token survives")
			_, err := audit.ScanSealedDir(dir, audit.SessionNS, "")
			require.NoError(t, err, "the repair's own output scans")
			data, err := audit.ReadStream(env.Policy, s)
			require.NoError(t, err)
			t.Logf("gaps=%v", data.Gaps)
			require.NotEmpty(t, data.Gaps, "the lost bytes are a recorded loss")
			for _, g := range data.Gaps {
				assert.Positive(t, g.First)
			}
			fixSecondRun(t, env)
		})
	}
}

// plantHeld puts raw bytes where QuarantineLocal holds them, named by their
// digest, with no retirement record referencing them.
func plantHeld(t *testing.T, env Env, s audit.Stream, raw string) string {
	t.Helper()
	held := filepath.Join(audit.HeldQuarantineDir(env.Policy, s), fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))+".corrupt")
	plant(t, env.Policy, held, raw)
	return held
}

// sameTSDigests returns the canonical-payload digests of two stableLine tools
// at one ts, the pair a repair log must name when it records their conflict.
func sameTSDigests(t *testing.T, ts int64, tools ...string) []string {
	t.Helper()
	var out []string
	for _, tool := range tools {
		b, err := audit.CanonicalPayload([]byte(stableLine(ts, tool)))
		require.NoError(t, err)
		out = append(out, fmt.Sprintf("%x", sha256.Sum256(b)))
	}
	return out
}

// assertSameTSRecorded checks that two payloads damaged bytes held at one ts
// are either both installed or recorded as lost with the ts, and that the
// log names both digests.
func assertSameTSRecorded(t *testing.T, env Env, log *recLog, s audit.Stream, dir string) {
	t.Helper()
	data, err := audit.ReadStream(env.Policy, s)
	require.NoError(t, err)
	x, y := false, false
	for _, l := range data.Lines {
		x = x || strings.Contains(string(l.Raw), "Xfirst__")
		y = y || strings.Contains(string(l.Raw), "Ysecond_")
	}
	if !(x && y) {
		hasGap := false
		for _, g := range data.Gaps {
			hasGap = hasGap || g.First <= fixT0+1000 && fixT0+1000 <= g.Last
		}
		assert.True(t, hasGap, "one payload is gone: a gap must cover its ts; gaps=%v", data.Gaps)
		for _, d := range sameTSDigests(t, fixT0+1000, "Xfirst__", "Ysecond_") {
			assert.Contains(t, log.text(), d, "the log names both digests")
		}
	}
}

// Two decodable damaged lines with one ts and different payloads, in a legacy
// raw .corrupt with nothing beside it: neither is silently dropped.
func TestSameTSDamagedLinesLegacyAloneAreRecorded(t *testing.T) {
	env, log, s, dir, _ := fixEnv(t)
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 2000}
	require.NoError(t, os.WriteFile(filepath.Join(dir, audit.CorruptName(cn.Stem())), []byte(stableLine(fixT0, "A")+stableLine(fixT0+1000, "Xfirst__")+stableLine(fixT0+1000, "Ysecond_")+stableLine(fixT0+2000, "C")), 0o600))
	runStep(t, runCorruptChunks, env)
	assertSameTSRecorded(t, env, log, s, dir)
	fixSecondRun(t, env)
}

// The same pair beside a valid same-stem chunk, with a third unique line.
func TestSameTSDamagedLinesBesideValidChunkAreRecorded(t *testing.T) {
	env, log, s, dir, _ := fixEnv(t)
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: fixT0, Last: fixT0 + 2000}
	require.NoError(t, os.WriteFile(filepath.Join(dir, cn.ChunkFile()), []byte(stableLine(fixT0, "A")+stableLine(fixT0+2000, "C")), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, audit.CorruptName(cn.Stem())), []byte(stableLine(fixT0, "A")+stableLine(fixT0+1000, "Xfirst__")+stableLine(fixT0+1000, "Ysecond_")+stableLine(fixT0+1500, "Zthird__")+stableLine(fixT0+2000, "C")), 0o600))
	runStep(t, runCorruptChunks, env)
	assertSameTSRecorded(t, env, log, s, dir)
	fixSecondRun(t, env)
}
