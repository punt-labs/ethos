//go:build linux || darwin

package autorepair

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/repair"
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

func run5(t *testing.T, env Env) Outcome {
	t.Helper()
	out, err := runRecordTreeTrace(context.Background(), env)
	require.NoError(t, err)
	return out
}

// Pre-v4.21 missions have no round_decisions, so their reflections.yaml is
// the only round authority and stays, as a residual naming step 3. Every
// other trace is archived and deleted.
func TestRecordTreeTraceOverCorpus(t *testing.T) {
	withTrace := map[string]bool{"v4.0.0": true, "v4.8.0": true, "v4.13.0": true, "v4.17.0": true, "v4.20.4": true}
	for _, v := range corpusVersions {
		t.Run(v, func(t *testing.T) {
			env := corpusEnv(t, v)
			before := traceFiles(t, env)
			var reflections []string
			for _, f := range before {
				if strings.HasSuffix(f, "/reflections.yaml") {
					reflections = append(reflections, f)
				}
			}
			out := run5(t, env)
			assert.False(t, out.Deferred)
			if withTrace[v] {
				require.NotEmpty(t, before, "corpus %s must hold trace in the record tree", v)
				require.Len(t, reflections, 1, "corpus %s holds one legacy reflections.yaml", v)
			} else {
				assert.Empty(t, before, "corpus %s holds no record-tree trace", v)
			}
			assert.Equal(t, len(before)-len(reflections), out.Items)
			require.Len(t, out.Residuals, len(reflections))
			for i, r := range reflections {
				assert.Equal(t, r, out.Residuals[i].ID)
				assert.Contains(t, out.Residuals[i].Reason, "round-decisions")
			}
			assert.Equal(t, reflections, traceFiles(t, env), "only the reflections aggregate remains")
			home := os.Getenv("HOME")
			for _, f := range listFiles(t, env.Policy.TraceArchiveRoot()) {
				b, err := os.ReadFile(filepath.Join(env.Policy.TraceArchiveRoot(), f))
				require.NoError(t, err)
				assert.NotContains(t, string(b), fakeToken, f)
				assert.NotContains(t, string(b), home, f)
			}
			streams, err := audit.ListStreams(env.Policy)
			require.NoError(t, err)
			for _, s := range streams {
				_, err := audit.ReadStream(env.Policy, s)
				assert.NoError(t, err, s.Key())
			}
			snapshot := hashFiles(t, env.Policy.RecordRoot(), env.Policy.LocalRoot())
			again := run5(t, env)
			assert.Equal(t, out.Residuals, again.Residuals, "the same residuals on every run")
			assert.Equal(t, 0, again.Items)
			assert.False(t, again.Deferred)
			assert.Equal(t, snapshot, hashFiles(t, env.Policy.RecordRoot(), env.Policy.LocalRoot()), "second run leaves every file as it was")
		})
	}
}

// A mission without round_decisions keeps reflections.yaml and the current
// reader still returns its rounds from it.
func TestRecordTreeTraceKeepsReflectionsWithoutDecisions(t *testing.T) {
	env := corpusEnv(t, "v4.8.0")
	const id = "m-2026-11-09-001"
	agg := filepath.Join(env.Policy.RecordRoot(), "missions", id, "reflections.yaml")
	require.FileExists(t, agg)
	run5(t, env)
	assert.FileExists(t, agg)
	assert.NoFileExists(t, filepath.Join(env.Policy.TraceArchiveRoot(), "missions", id, "reflection-1.yaml"))
	store := mission.NewStoreWithRoots(env.Roots.Store, env.Global)
	states, err := store.ReadReflections(id)
	require.NoError(t, err)
	require.NotEmpty(t, states)
	assert.Equal(t, "present", states[0].DetailState)
	require.NotNil(t, states[0].Reflection)
	assert.Equal(t, "round 1 finished cleanly", states[0].Reflection.Reason)
}

// Once the contract holds round_decisions the aggregate becomes per-round
// envelopes that join the contract's decision, and the aggregate goes.
func TestRecordTreeTraceArchivesReflectionsAfterDecisions(t *testing.T) {
	env := corpusEnv(t, "v4.22.1")
	const id = "m-2026-11-09-001"
	rec := env.Policy.RecordRoot()
	agg := filepath.Join(rec, "missions", id, "reflections.yaml")
	// The v4.8.0 aggregate for the same mission, whose round 1 the v4.22.1
	// contract already decided (bf96036b...).
	src := filepath.Join("testdata", "corpus", "v4.8.0", "repo", ".punt-labs", "ethos", "missions", id, "reflections.yaml")
	b, err := os.ReadFile(src)
	require.NoError(t, err)
	b = []byte(strings.ReplaceAll(string(b), "2026-11-09T23:02:27Z", "2026-11-09T23:03:22Z"))
	require.NoError(t, os.WriteFile(agg, b, 0o600))

	out := run5(t, env)
	assert.Empty(t, out.Residuals)
	assert.NoFileExists(t, agg)
	env1 := filepath.Join(env.Policy.TraceArchiveRoot(), "missions", id, "reflection-1.yaml")
	assert.FileExists(t, env1)
	store := mission.NewStoreWithRoots(env.Roots.Store, env.Global)
	states, err := store.ReadReflections(id)
	require.NoError(t, err)
	require.Len(t, states, 2)
	assert.Equal(t, "present", states[0].DecisionState)
	assert.Equal(t, "present", states[0].DetailState, "%v", states[0].Diagnostics)
	require.NotNil(t, states[0].Reflection)
	assert.Equal(t, "round 1 finished cleanly", states[0].Reflection.Reason)
}

// An aggregate whose round the contract never decided is left in place.
func TestRecordTreeTraceLeavesReflectionsForUndecidedRound(t *testing.T) {
	env := corpusEnv(t, "v4.22.1")
	const id = "m-2026-11-09-001"
	agg := filepath.Join(env.Policy.RecordRoot(), "missions", id, "reflections.yaml")
	body := "reflections:\n  - round: 2\n    created_at: \"2026-11-09T23:05:00Z\"\n    author: claude\n    converging: true\n    signals: [ok]\n    recommendation: continue\n    reason: undecided\n"
	require.NoError(t, os.WriteFile(agg, []byte(body), 0o600))
	out := run5(t, env)
	require.Len(t, out.Residuals, 1)
	assert.Contains(t, out.Residuals[0].Reason, "no shared decision")
	assert.FileExists(t, agg)
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
	run5(t, env)
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
	out := run5(t, env)
	var ids []string
	for _, r := range out.Residuals {
		ids = append(ids, r.ID)
	}
	assert.Contains(t, ids, "missions/m-2026-11-09-002/delegations/d-2026-11-09-001/prompt.md")
	assert.FileExists(t, prompt, "an unreadable file is never deleted")
}

// An existing archived copy counts only when it is byte-equal to the
// redacted payload: the original is then deleted. A different file at the
// destination is a collision: the original stays, as a residual.
func TestRecordTreeTraceExistingArchiveMustMatch(t *testing.T) {
	env := corpusEnv(t, "v4.20.4")
	const id = "m-2026-11-09-002"
	prompt := filepath.Join(env.Policy.RecordRoot(), "missions", id, "delegations", "d-2026-11-09-001", "prompt.md")
	dest := filepath.Join(env.Policy.TraceArchiveRoot(), "missions", id, "delegations", "d-2026-11-09-001", "prompt.md")
	body, err := os.ReadFile(prompt)
	require.NoError(t, err)

	t.Run("identical copy", func(t *testing.T) {
		env := corpusEnv(t, "v4.20.4")
		dest := filepath.Join(env.Policy.TraceArchiveRoot(), "missions", id, "delegations", "d-2026-11-09-001", "prompt.md")
		prompt := filepath.Join(env.Policy.RecordRoot(), "missions", id, "delegations", "d-2026-11-09-001", "prompt.md")
		require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o700))
		require.NoError(t, os.WriteFile(dest, body, 0o600))
		out := run5(t, env)
		assert.NoFileExists(t, prompt)
		for _, r := range out.Residuals {
			assert.NotContains(t, r.ID, "prompt.md")
		}
	})
	t.Run("differing copy", func(t *testing.T) {
		require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o700))
		require.NoError(t, os.WriteFile(dest, []byte("someone else's prompt\n"), 0o600))
		out := run5(t, env)
		assert.FileExists(t, prompt)
		var found bool
		for _, r := range out.Residuals {
			if strings.HasSuffix(r.ID, "prompt.md") {
				found = true
				assert.Contains(t, r.Reason, "different content")
			}
		}
		assert.True(t, found, "collision is a residual")
		b, err := os.ReadFile(dest)
		require.NoError(t, err)
		assert.Equal(t, "someone else's prompt\n", string(b), "the existing archive is never overwritten")
	})
}

// A busy mission-state lock defers that mission's trace; a busy session
// stream lock defers that session's chunk. Neither file is touched.
func TestRecordTreeTraceBusyLocksDefer(t *testing.T) {
	env := corpusEnv(t, "v4.8.0")
	rec := env.Policy.RecordRoot()
	missionLock, err := env.Policy.LockPath("mission-state", "m-2026-11-09-002")
	require.NoError(t, err)
	streamLock, err := repair.StreamLockPath(env.Policy, "sessions/2026-11-09-sess-corpus-0001/x")
	require.NoError(t, err)
	var holders []*os.File
	for _, p := range []string{missionLock, streamLock} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
		require.NoError(t, err)
		require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
		holders = append(holders, f)
	}
	t.Cleanup(func() {
		for _, f := range holders {
			f.Close()
		}
	})
	prompt := filepath.Join(rec, "missions", "m-2026-11-09-002", "delegations", "d-2026-11-09-001", "prompt.md")
	sessionChunks := filepath.Join(rec, "sessions", "2026-11-09-sess-corpus-0001")
	chunksBefore := listFiles(t, sessionChunks)
	require.NotEmpty(t, chunksBefore)

	out := run5(t, env)
	assert.True(t, out.Deferred)
	assert.FileExists(t, prompt)
	assert.Equal(t, chunksBefore, listFiles(t, sessionChunks))

	for _, f := range holders {
		require.NoError(t, f.Close())
	}
	out = run5(t, env)
	assert.False(t, out.Deferred)
	assert.NoFileExists(t, prompt)
	assert.Empty(t, listFiles(t, sessionChunks))
}

// A file that changed between the inventory and the delete is left in place.
func TestRecordTreeTraceChangedFileIsLeft(t *testing.T) {
	env := corpusEnv(t, "v4.8.0")
	prompt := filepath.Join(env.Policy.RecordRoot(), "missions", "m-2026-11-09-002", "delegations", "d-2026-11-09-001", "prompt.md")
	plan, err := repair.Inventory(context.Background(), env.Policy)
	require.NoError(t, err)
	var item *repair.Item
	for i := range plan.Items {
		if plan.Items[i].Path == prompt {
			item = &plan.Items[i]
		}
	}
	require.NotNil(t, item)
	require.NoError(t, os.WriteFile(prompt, []byte("edited after inventory\n"), 0o600))
	a := &archiver{env: env, repo: env.Roots.Store}
	a.red, err = mission.NewPathRedactor(a.repo)
	require.NoError(t, err)
	deferred, err := a.archiveAndDelete(kindPrompt, "missions/m-2026-11-09-002/delegations/d-2026-11-09-001/prompt.md", *item)
	assert.False(t, deferred)
	require.ErrorContains(t, err, "changed since the inventory")
	assert.FileExists(t, prompt)
}

// Lines are redacted by the seal's own guard: a home path becomes a token,
// tool_input_hash is recomputed over the stored tool_input, and a line the
// guard cannot clear becomes a loss marker that keeps its ts.
func TestRecordTreeTraceRedactsLinesLikeTheSeal(t *testing.T) {
	env := corpusEnv(t, "v4.8.0")
	home := os.Getenv("HOME")
	rec := env.Policy.RecordRoot()
	frozen := filepath.Join(rec, "sessions", "2026-11-09-sess-corpus-0001", "audit.jsonl")
	hashed := `{"ts":"2026-11-09T23:02:21Z","session":"sess-corpus-0001","tool":"Read","tool_input":{"file_path":"` + home + `/secret/notes.md"},"tool_input_hash":"0000000000000000000000000000000000000000000000000000000000000000"}`
	// Keys that differ only in case fold onto one struct field when a reader
	// decodes the line, so the guard replaces the whole line with a marker
	// (hook/audit_guard.go, caseCollision).
	unclearable := `{"ts":"2026-11-09T23:02:22Z","session":"sess-corpus-0001","Tool":"Bash","tool":"Read"}`
	require.NoError(t, os.WriteFile(frozen, []byte(hashed+"\n"+unclearable+"\n"), 0o600))

	out := run5(t, env)
	assert.NoFileExists(t, frozen)
	for _, r := range out.Residuals {
		assert.NotContains(t, r.ID, "audit.jsonl")
	}
	dest := filepath.Join(env.Policy.TraceArchiveRoot(), "sessions", "2026-11-09-sess-corpus-0001", "audit.jsonl")
	b, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.NotContains(t, string(b), home)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	require.Len(t, lines, 2)

	var first map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	assert.Equal(t, "2026-11-09T23:02:21Z", first["ts"])
	assert.NotEqual(t, strings.Repeat("0", 64), first["tool_input_hash"], "hash is recomputed over the stored tool_input")
	ti, _ := json.Marshal(first["tool_input"])
	assert.Contains(t, string(ti), "~/secret/notes.md")

	var second map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &second))
	assert.Equal(t, "2026-11-09T23:02:22Z", second["ts"])
	assert.NotEmpty(t, second["audit_error"], "an unclearable line is a loss marker")
}
