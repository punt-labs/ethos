package autorepair

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// text is every line recLog recorded, newline-joined.
func (l *recLog) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.steps, "\n")
}

// damageEnv copies testdata/damage/<name>/repo into a scratch store and returns
// the Env a step sees there.
func damageEnv(t *testing.T, name string) (Env, *recLog) {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join("testdata", "damage", name, "repo")
	require.NoError(t, filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(root, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	}))
	root, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	log := &recLog{}
	return Env{Roots: resolve.Located{Store: root, Checkout: root}, Policy: p, Now: time.Now(), Log: log}, log
}

func plant(t *testing.T, p layout.Policy, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

// runStepT runs one step function under the step budget and requires no error.
func runStepT(t *testing.T, run func(context.Context, Env) (Outcome, error), env Env) Outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), StepBudget)
	defer cancel()
	out, err := run(ctx, env)
	require.NoError(t, err)
	return out
}

// walkContains returns the files under dir whose bytes contain any needle.
func walkContains(t *testing.T, dir string, needles ...string) []string {
	t.Helper()
	var hits []string
	require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, n := range needles {
			if strings.Contains(string(b), n) {
				hits = append(hits, path)
				break
			}
		}
		return nil
	}))
	return hits
}

// fixtureHome points HOME at a scratch directory and returns it, so the
// @HOME@ placeholder in a damage fixture names this machine's home.
func fixtureHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// resolveHome rewrites the @HOME@ placeholder in every file under dir.
func resolveHome(t *testing.T, dir, home string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(path, []byte(strings.ReplaceAll(string(b), "@HOME@", home)), 0o600)
	}))
}

// stableLine is a session audit line the guard leaves as it is.
func stableLine(ts int64, tool string) string {
	return `{"path_format":2,"record":"blame","redacted":true,"session":"sess-damage-0001","tool":"` + tool + `","ts":"` + audit.FormatLineTS(ts) + `"}` + "\n"
}

const damageSession = "sess-damage-0001"

func TestLiveTailsRedactsPreRedactionLine(t *testing.T) {
	home := fixtureHome(t)
	env, log := damageEnv(t, "live-audit-pre-redaction")
	resolveHome(t, env.Roots.Store, home)
	require.NotEmpty(t, walkContains(t, env.Roots.Store, home, fakeToken), "fixture: the live file holds the home path and the token")
	s := audit.Stream{Namespace: audit.SessionNS, Session: damageSession}
	before, err := os.ReadFile(s.LivePath(env.Policy))
	require.NoError(t, err)
	cleanLine := strings.Split(string(before), "\n")[1] + "\n"

	out := runStepT(t, runLiveTails, env)
	assert.Equal(t, 1, out.Items)
	assert.False(t, out.Deferred)
	assert.Empty(t, out.Residuals)
	assert.Empty(t, walkContains(t, env.Roots.Store, home, fakeToken), "no file keeps the home path or the token")
	assert.Contains(t, log.text(), "live-tails:redact-live lines=1")
	assert.NotContains(t, log.text(), fakeToken, "the log names paths and counts, never content")

	live, err := os.ReadFile(s.LivePath(env.Policy))
	require.NoError(t, err)
	assert.Contains(t, string(live), cleanLine, "the clean line is untouched")
	assert.NotContains(t, string(live), `"tool_input_hash":"0000"`)

	assertSecondRunChangesNothing(t, runLiveTails, env)
}

func TestLiveTailsRecomputesToolInputHash(t *testing.T) {
	home := fixtureHome(t)
	env, _ := damageEnv(t, "live-audit-pre-redaction")
	s := audit.Stream{Namespace: audit.SessionNS, Session: damageSession}
	plant(t, env.Policy, s.LivePath(env.Policy), `{"session":"`+damageSession+`","tool":"Bash","tool_input":{"command":"cat `+home+`/x"},"tool_input_hash":"0000","ts":"`+audit.FormatLineTS(1000)+`"}`+"\n")
	runStepT(t, runLiveTails, env)
	b, err := os.ReadFile(s.LivePath(env.Policy))
	require.NoError(t, err)
	assert.NotContains(t, string(b), `"tool_input_hash":"0000"`, "the hash is recomputed over the redacted input")
	assert.NotContains(t, string(b), home)
}

func TestLiveTailsMissionLog(t *testing.T) {
	home := fixtureHome(t)
	env, _ := damageEnv(t, "live-audit-pre-redaction")
	require.NoError(t, os.Remove(audit.Stream{Namespace: audit.SessionNS, Session: damageSession}.LivePath(env.Policy)))
	s := audit.Stream{Namespace: audit.MissionNS, Mission: "m-2026-07-01-001", Session: "sess-m"}
	plant(t, env.Policy, s.LivePath(env.Policy), `{"ts":"`+audit.FormatLineTS(1000)+`","event":"update","actor":"a","details":{"note":"see `+home+`/x","key":"`+fakeToken+`"}}`+"\n")
	out := runStepT(t, runLiveTails, env)
	assert.Equal(t, 1, out.Items)
	b, err := os.ReadFile(s.LivePath(env.Policy))
	require.NoError(t, err)
	assert.NotContains(t, string(b), home)
	assert.NotContains(t, string(b), fakeToken)
	assert.Contains(t, string(b), `"event":"update"`, "it stays a mission event")
	assertSecondRunChangesNothing(t, runLiveTails, env)
}

func TestLiveTailsArchivedTSConflictDropsLiveLine(t *testing.T) {
	for _, ns := range []audit.Namespace{audit.SessionNS, audit.MissionNS} {
		name := "session"
		s := audit.Stream{Namespace: ns, Session: "sess-c"}
		if ns == audit.MissionNS {
			name, s.Mission = "mission", "m-2026-07-01-002"
		}
		t.Run(name, func(t *testing.T) {
			fixtureHome(t)
			env, log := damageEnv(t, "live-audit-pre-redaction")
			dir, err := audit.ArchiveDir(env.Policy, s, "2026-07-01")
			require.NoError(t, err)
			cn := audit.ChunkName{Namespace: ns, First: 100, Last: 100}
			if ns == audit.MissionNS {
				cn.Session = s.Session
			}
			archived := `{"ts":"` + audit.FormatLineTS(100) + `","session":"sess-c","tool":"Read"}` + "\n"
			require.NoError(t, audit.WriteLocalAtomic(env.Policy, filepath.Join(dir, cn.ChunkFile()), []byte(archived)))
			plant(t, env.Policy, s.LivePath(env.Policy), `{"ts":"`+audit.FormatLineTS(100)+`","session":"sess-c","tool":"Bash"}`+"\n")
			// The fixture's own leaky session line is also in this repository.
			require.NoError(t, os.Remove(audit.Stream{Namespace: audit.SessionNS, Session: damageSession}.LivePath(env.Policy)))
			_, err = audit.ReadStream(env.Policy, s)
			require.ErrorContains(t, err, "identity conflict", "fixture: the stream is refused before the step")

			out := runStepT(t, runLiveTails, env)
			assert.Equal(t, 1, out.Items)
			assert.Regexp(t, `drop-live-line ts=100 archive-sha256=[0-9a-f]{64} live-sha256=[0-9a-f]{64}`, log.text())
			_, err = audit.ReadStream(env.Policy, s)
			require.NoError(t, err)
			b, err := os.ReadFile(s.LivePath(env.Policy))
			require.NoError(t, err)
			assert.NotContains(t, string(b), `"tool":"Bash"`)
			assertSecondRunChangesNothing(t, runLiveTails, env)
		})
	}
}

func TestLiveTailsDefersOnHeldStreamLock(t *testing.T) {
	home := fixtureHome(t)
	env, _ := damageEnv(t, "live-audit-pre-redaction")
	resolveHome(t, env.Roots.Store, home)
	s := audit.Stream{Namespace: audit.SessionNS, Session: damageSession}
	require.NoError(t, audit.WithStreamLock(env.Policy, s, func() error {
		out := runStepT(t, runLiveTails, env)
		assert.True(t, out.Deferred)
		assert.Equal(t, 0, out.Items)
		return nil
	}))
	assert.NotEmpty(t, walkContains(t, env.Roots.Store, home), "a deferred step changed nothing")
	out := runStepT(t, runLiveTails, env)
	assert.False(t, out.Deferred)
	assert.Equal(t, 1, out.Items)
	assertSecondRunChangesNothing(t, runLiveTails, env)
}

func TestLiveTailsOutsideRepositoryAndNoStreams(t *testing.T) {
	out, err := runLiveTails(context.Background(), Env{})
	require.NoError(t, err)
	assert.Equal(t, Outcome{}, out)
	env, _ := damageEnv(t, "corrupt-sealed-chunk")
	assert.Equal(t, Outcome{}, runStepT(t, runLiveTails, env), "no live file, nothing to do")
}

func TestLiveTailsHonoursCancelledContext(t *testing.T) {
	home := fixtureHome(t)
	env, _ := damageEnv(t, "live-audit-pre-redaction")
	resolveHome(t, env.Roots.Store, home)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := runLiveTails(ctx, env)
	require.NoError(t, err)
	assert.True(t, out.Deferred)
	assert.NotEmpty(t, walkContains(t, env.Roots.Store, home))
}

func corruptFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.Contains(d.Name(), ".corrupt") {
			out = append(out, path)
		}
		return err
	}))
	return out
}

// chunkFiles lists the sealed chunk files of the fixture session.
func chunkFiles(t *testing.T, env Env) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(env.Policy.LocalRoot(), "trace", "sessions", "*", "audit-*.jsonl"))
	require.NoError(t, err)
	return m
}

func TestCorruptChunksCorruptSealedChunk(t *testing.T) {
	env, log := damageEnv(t, "corrupt-sealed-chunk")
	s := audit.Stream{Namespace: audit.SessionNS, Session: damageSession}
	_, err := audit.ReadStream(env.Policy, s)
	require.Error(t, err, "fixture: the chunk set is unreadable")
	require.Len(t, chunkFiles(t, env), 1)

	out := runStepT(t, runCorruptChunks, env)
	assert.Equal(t, 1, out.Items)
	assert.Empty(t, out.Residuals)
	data, err := audit.ReadStream(env.Policy, s)
	require.NoError(t, err, "the chunk set reads cleanly")
	assert.Empty(t, data.Lines, "nothing in the chunk decoded, so nothing is invented")
	assert.Len(t, data.Gaps, 1, "the loss is recorded as a gap")
	assert.Empty(t, chunkFiles(t, env), "the damaged bytes are deleted")
	assert.Empty(t, corruptFiles(t, env.Roots.Store), "no .corrupt copy is kept")
	assert.Contains(t, log.text(), "corrupt-chunks:replace-chunk salvaged=0 gap=true")
	assertSecondRunChangesNothing(t, runCorruptChunks, env)
}

// A live line inside the damaged chunk's range is recovered, redacted.
func TestCorruptChunksRecoversRedactedLiveLine(t *testing.T) {
	home := fixtureHome(t)
	env, _ := damageEnv(t, "corrupt-sealed-chunk")
	s := audit.Stream{Namespace: audit.SessionNS, Session: damageSession}
	ts := int64(1793786400000001500)
	plant(t, env.Policy, s.LivePath(env.Policy), `{"session":"`+damageSession+`","tool":"Bash","tool_input":{"command":"cat `+home+`/n --token `+fakeToken+`"},"tool_input_hash":"0000","ts":"`+audit.FormatLineTS(ts)+`"}`+"\n")

	// The engine runs live-tails (step 6) before corrupt-chunks (step 7).
	assert.Equal(t, 1, runStepT(t, runLiveTails, env).Items)
	out := runStepT(t, runCorruptChunks, env)
	assert.Equal(t, 1, out.Items)
	data, err := audit.ReadStream(env.Policy, s)
	require.NoError(t, err)
	assert.Len(t, data.Lines, 1)
	assert.Empty(t, walkContains(t, env.Policy.LocalRoot()+"/trace", home, fakeToken), "the replacement chunk holds the redacted form")
	assert.Len(t, chunkFiles(t, env), 1)
	assertSecondRunChangesNothing(t, runCorruptChunks, env)
}

func TestCorruptChunksInvalidEscapeLines(t *testing.T) {
	home := fixtureHome(t)
	env, _ := damageEnv(t, "invalid-escape-lines")
	s := audit.Stream{Namespace: audit.SessionNS, Session: damageSession}
	files := chunkFiles(t, env)
	require.Len(t, files, 1)
	f, err := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(`{"session":"` + damageSession + `","tool":"Bash","tool_input":{"command":"cat ` + home + `/n ` + fakeToken + `"},"ts":"2026-11-04T10:00:02.000001Z"}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	_, err = audit.ReadStream(env.Policy, s)
	require.Error(t, err, "fixture: the invalid escape makes the chunk unreadable")

	out := runStepT(t, runCorruptChunks, env)
	assert.Equal(t, 1, out.Items)
	data, err := audit.ReadStream(env.Policy, s)
	require.NoError(t, err)
	assert.Len(t, data.Lines, 2, "the two decodable lines survive")
	assert.Len(t, data.Gaps, 1, "the invalid-escape line held an entry that is gone; its range is a recorded loss")
	assert.Empty(t, walkContains(t, env.Roots.Store, home, fakeToken), "the pre-redaction token and path are gone")
	assert.Empty(t, corruptFiles(t, env.Roots.Store))
	assertSecondRunChangesNothing(t, runCorruptChunks, env)
}

func TestCorruptChunksConflictingRecoveredLine(t *testing.T) {
	env, log := damageEnv(t, "conflicting-recovered-line")
	s := audit.Stream{Namespace: audit.SessionNS, Session: damageSession}
	_, err := audit.ReadStream(env.Policy, s)
	require.Error(t, err)
	require.Len(t, chunkFiles(t, env), 2)

	out := runStepT(t, runCorruptChunks, env)
	assert.Equal(t, 1, out.Items)
	assert.Regexp(t, `refuse-conflicting-line ts=1793786400000001000 surviving-sha256=[0-9a-f]{64} recovered-sha256=[0-9a-f]{64}`, log.text())
	data, err := audit.ReadStream(env.Policy, s)
	require.NoError(t, err, "the chunk set reads without an identity conflict")
	require.Len(t, data.Lines, 1)
	assert.Contains(t, string(data.Lines[0].Raw), "echo one", "the surviving line wins")
	assert.Empty(t, walkContains(t, env.Roots.Store, "ONE-DIFFERENT"), "the conflicting bytes are deleted")
	assert.Len(t, chunkFiles(t, env), 1)
	assert.Empty(t, corruptFiles(t, env.Roots.Store))
	assertSecondRunChangesNothing(t, runCorruptChunks, env)
}

func TestCorruptChunksHealthyAndOutside(t *testing.T) {
	env, _ := damageEnv(t, "live-audit-pre-redaction")
	s := audit.Stream{Namespace: audit.SessionNS, Session: damageSession}
	dir, err := audit.ArchiveDir(env.Policy, s, "2026-11-04")
	require.NoError(t, err)
	name := audit.SessionChunkFile(100, 100)
	require.NoError(t, audit.WriteLocalAtomic(env.Policy, filepath.Join(dir, name), []byte(stableLine(100, "Read"))))
	assert.Equal(t, Outcome{}, runStepT(t, runCorruptChunks, env), "a readable chunk is left alone")
	out, err := runCorruptChunks(context.Background(), Env{})
	require.NoError(t, err)
	assert.Equal(t, Outcome{}, out)
}

func TestCorruptChunksDefersOnHeldStreamLock(t *testing.T) {
	env, _ := damageEnv(t, "corrupt-sealed-chunk")
	s := audit.Stream{Namespace: audit.SessionNS, Session: damageSession}
	require.NoError(t, audit.WithStreamLock(env.Policy, s, func() error {
		out := runStepT(t, runCorruptChunks, env)
		assert.True(t, out.Deferred)
		assert.Equal(t, 0, out.Items)
		return nil
	}))
	assert.Len(t, chunkFiles(t, env), 1, "a deferred step left the chunk alone")
	assert.Equal(t, 1, runStepT(t, runCorruptChunks, env).Items)
	assertSecondRunChangesNothing(t, runCorruptChunks, env)
}

var auditCorpusVersions = []string{"v3.11.0", "v3.12.0", "v4.0.0", "v4.8.0", "v4.13.0", "v4.17.0", "v4.20.4", "v4.21.0", "v4.22.1"}

// corpusAuditEnv copies a corpus snapshot's repo into a scratch store with
// HOME pointed at a scratch directory, so the redactor has a home to judge by.
func corpusAuditEnv(t *testing.T, version string) (Env, *recLog) {
	t.Helper()
	fixtureHome(t)
	root := t.TempDir()
	src := filepath.Join("testdata", "corpus", version, "repo")
	require.NoError(t, filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(root, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	}))
	root, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	log := &recLog{}
	return Env{Roots: resolve.Located{Store: root, Checkout: root}, Policy: p, Now: time.Now(), Log: log}, log
}

// Over every released layout, steps 6 and 7 run without error or residual,
// leave every stream readable, and change nothing on a second run.
func TestAuditStepsOverCorpusAreIdempotent(t *testing.T) {
	for _, v := range auditCorpusVersions {
		t.Run(v, func(t *testing.T) {
			env, _ := corpusAuditEnv(t, v)
			for _, step := range []struct {
				name string
				run  func(context.Context, Env) (Outcome, error)
			}{{stepLiveTails, runLiveTails}, {stepCorruptChunks, runCorruptChunks}} {
				first := runStepT(t, step.run, env)
				assert.False(t, first.Deferred, "%s deferred", step.name)
				assert.Empty(t, first.Residuals, "%s residuals", step.name)
				assertSecondRunChangesNothing(t, step.run, env)
			}
			streams, err := audit.ListStreams(env.Policy)
			require.NoError(t, err)
			for _, s := range streams {
				_, err := audit.ReadStream(env.Policy, s)
				assert.NoError(t, err, "stream %s reads after the audit steps", s.Key())
			}
		})
	}
}

// A v4.17.0 corpus live tail holds pre-redaction lines whose redacted form the
// local archive already carries (the restored corpus keeps the local trace a
// real upgrade has). Step 6 brings each stale live line to the archived form
// rather than dropping it as a conflict, and the token survives nowhere under
// local/.
func TestLiveTailsAfterRecordTreeRedactionDropsStaleCorpusLines(t *testing.T) {
	const v = "v4.17.0"
	env, log := corpusAuditEnv(t, v)
	s := audit.Stream{Namespace: audit.SessionNS, Session: "sess-corpus-0001"}
	live := s.LivePath(env.Policy)
	require.NotEmpty(t, walkContains(t, live, fakeToken), "fixture: the live tail holds the token")

	out := runStepT(t, runLiveTails, env)
	assert.Equal(t, 3, out.Items, "every stale live line is rewritten")
	assert.Contains(t, log.text(), "redact-live lines=3")
	assert.NotContains(t, log.text(), "drop-live-line")
	_, err := audit.ReadStream(env.Policy, s)
	require.NoError(t, err)
	assert.Empty(t, walkContains(t, env.Policy.LocalRoot(), fakeToken), "the token survives nowhere under local/")
	assertSecondRunChangesNothing(t, runLiveTails, env)
}

// An orphan .corrupt (a retirement an interrupted old-style quarantine left
// without its covering marker) is completed by step 7: the stream reads
// cleanly afterwards, the marker is final, and the raw bytes are gone.
func TestCorruptChunksCompletesOrphanCorrupt(t *testing.T) {
	env, log := damageEnv(t, "corrupt-sealed-chunk")
	s := audit.Stream{Namespace: audit.SessionNS, Session: damageSession}
	files := chunkFiles(t, env)
	require.Len(t, files, 1)
	source := files[0]
	dir := filepath.Dir(source)
	cn, kind := audit.Classify(filepath.Base(source), audit.SessionNS)
	require.Equal(t, audit.KindValid, kind)
	// Reproduce an interrupted quarantine: retire the chunk, then lose its
	// marker before the recovery completed.
	_, err := audit.QuarantineLocal(env.Policy, s, source, dir, cn, "fixture", func(raw []byte) ([]byte, string, bool) { return raw, "", false })
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(dir, cn.MarkerFile())))
	// An older binary kept the raw bytes beside their metadata under
	// quarantine/<stream key>/<sha>.corrupt; re-create that pair.
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(s.Key())))
	rec, err := os.ReadFile(filepath.Join(dir, audit.CorruptName(cn.Stem())))
	require.NoError(t, err)
	var meta struct {
		SHA256 string `json:"sha256"`
	}
	require.NoError(t, json.Unmarshal(rec, &meta))
	raw := filepath.Join(env.Policy.LocalRoot(), "quarantine", key, meta.SHA256+".corrupt")
	plant(t, env.Policy, raw, "this is not json\n{\"ts\":\n")
	plant(t, env.Policy, raw+".json", string(rec))
	_, err = audit.ScanSealedDir(dir, audit.SessionNS, "")
	var orphan *audit.OrphanCorruptError
	require.ErrorAs(t, err, &orphan, "fixture: the scan reports the orphan before the step")

	out := runStepT(t, runCorruptChunks, env)
	assert.Equal(t, 1, out.Items)
	assert.Empty(t, out.Residuals)
	_, err = audit.ScanSealedDir(dir, audit.SessionNS, "")
	require.NoError(t, err, "the scan is clean after the step")
	_, err = audit.ReadStream(env.Policy, s)
	require.NoError(t, err)
	m, err := audit.ReadMarker(filepath.Join(dir, cn.MarkerFile()))
	require.NoError(t, err)
	assert.True(t, m.HasGap())
	assert.NoFileExists(t, raw, "the raw bytes are deleted")
	assert.NoFileExists(t, raw+".json")
	assert.FileExists(t, filepath.Join(dir, audit.CorruptName(cn.Stem())), "the retirement record stays beside its final marker")
	assert.Contains(t, log.text(), "complete-orphan-corrupt")
	assertSecondRunChangesNothing(t, runCorruptChunks, env)
}
