package autorepair

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const fakeToken34 = "ghp_FAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE"

// scrubVersions34 are the corpus snapshots that keep mission records in the
// tracked repository tree and predate redaction (v4.20.0).
var scrubVersions34 = []string{"v4.0.0", "v4.8.0", "v4.13.0", "v4.17.0"}

// plantLeaks34 writes what a pre-redaction binary wrote: the fake token and a
// home path inside tracked mission records. It returns the planted paths.
func plantLeaks34(t *testing.T, env Env) []string {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	var planted []string
	for _, dir := range missionDirs34(t, env) {
		contract := filepath.Join(dir, "contract.yaml")
		orig := read34(t, contract)
		leaked := strings.Replace(orig, "\ncontext: ", "\ncontext: token "+fakeToken34+" in "+home+"/work/notes ", 1)
		require.NotEqual(t, orig, leaked, "%s has no context line to plant in", contract)
		write34(t, contract, leaked)
		planted = append(planted, contract)
	}
	return planted
}

// decodeYAML34 decodes a YAML file, dropping empty-string values: the
// canonical writer adds zero-valued keys such as mission: "" that carry no
// information.
func decodeYAML34(t *testing.T, path string) any {
	t.Helper()
	var v any
	require.NoError(t, yaml.Unmarshal([]byte(read34(t, path)), &v), path)
	return dropEmpty34(v)
}

func dropEmpty34(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if s, ok := e.(string); ok && s == "" {
				delete(x, k)
				continue
			}
			x[k] = dropEmpty34(e)
		}
	case []any:
		for i := range x {
			x[i] = dropEmpty34(x[i])
		}
	}
	return v
}

func recordLeaks34(t *testing.T, env Env) []string {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	var bad []string
	root := filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "missions")
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), fakeToken34) || strings.Contains(string(b), home) {
			bad = append(bad, p)
		}
		return nil
	}))
	return bad
}

func TestScrubRecords_Corpus(t *testing.T) {
	for _, v := range scrubVersions34 {
		t.Run(v, func(t *testing.T) {
			env := stage34(t, v)
			planted := plantLeaks34(t, env)
			require.NotEmpty(t, recordLeaks34(t, env), "the plant must leak before the step runs")
			before := map[string]*mission.Contract{}
			for _, p := range planted {
				c, err := mission.DecodeContractStrict([]byte(read34(t, p)), p)
				require.NoError(t, err)
				before[p] = c
			}
			resultsPath := filepath.Join(filepath.Dir(planted[0]), "results.yaml")
			resultsBefore := read34(t, resultsPath)
			reflectionsPath := filepath.Join(filepath.Dir(planted[0]), "reflections.yaml")
			reflectionsBefore := decodeYAML34(t, reflectionsPath)

			out := run34(t, "scrub-records", env)
			// Every planted contract changes; a file already clean but not in
			// canonical form (older reflections.yaml) is re-encoded too.
			assert.GreaterOrEqual(t, out.Items, len(planted), "files changed")
			assert.False(t, out.Deferred)
			assert.Empty(t, out.Residuals)
			assert.Empty(t, recordLeaks34(t, env), "no mission record may keep the token or the home path")

			for p, was := range before {
				now, err := mission.DecodeContractStrict([]byte(read34(t, p)), p)
				require.NoError(t, err)
				assert.Equal(t, was.Status, now.Status)
				assert.Equal(t, was.CurrentRound, now.CurrentRound)
				assert.Equal(t, was.Budget, now.Budget)
				assert.Equal(t, was.Evaluator, now.Evaluator)
				assert.Equal(t, was.ClosedAt, now.ClosedAt)
				assert.Equal(t, was.Inputs, now.Inputs)
				assert.NotContains(t, now.Context, fakeToken34)
				assert.Contains(t, now.Context, "corpus scenario", "text outside the secret survives")
			}
			assert.Equal(t, resultsBefore, read34(t, resultsPath), "an already clean accepted result is not rewritten")
			assert.Equal(t, reflectionsBefore, decodeYAML34(t, reflectionsPath), "a re-encoded reflections file keeps every value")

			again := run34(t, "scrub-records", env)
			assert.Zero(t, again.Items, "second run is a no-op")
		})
	}
}

func TestScrubRecords_CleanSnapshotsChangeNothing(t *testing.T) {
	for _, v := range []string{"v4.21.0", "v4.22.1"} {
		t.Run(v, func(t *testing.T) {
			env := stage34(t, v)
			out := run34(t, "scrub-records", env)
			assert.Zero(t, out.Items)
			assert.Empty(t, out.Residuals)
		})
	}
}

func TestScrubRecords_OutsideRepositoryDoesNothing(t *testing.T) {
	assert.Equal(t, Outcome{}, run34(t, "scrub-records", Env{Log: &recLog34{}}))
}

// plantAll34 plants the fake token into every kind of mission record the step
// scrubs: contract, results, reflections, a delegation record and prompt,
// missions.jsonl and corrections. It returns the planted paths.
func plantAll34(t *testing.T, env Env) []string {
	t.Helper()
	root := filepath.Join(env.Roots.Store, ".punt-labs", "ethos")
	plant := func(path, from, to string) {
		orig := read34(t, path)
		leaked := strings.Replace(orig, from, to, 1)
		require.NotEqual(t, orig, leaked, "%s: nothing to plant at %q", path, from)
		write34(t, path, leaked)
	}
	m1 := filepath.Join(root, "missions", "m-2026-11-09-001")
	plant(filepath.Join(m1, "results.yaml"), "verdict: pass", "prose: leaked "+fakeToken34+"\n      verdict: pass")
	plant(filepath.Join(m1, "reflections.yaml"), "reason: round 1 finished cleanly", "reason: leaked "+fakeToken34)
	plant(filepath.Join(root, "missions.jsonl"), `"success_criteria":["tests pass"]`, `"success_criteria":["tests pass '`+fakeToken34+`'"]`)
	write34(t, filepath.Join(m1, "corrections.yaml"), "version: 1\nmission: m-2026-11-09-001\nentries:\n    - id: 0123456789abcdef0123456789abcdef\n      created_at: \"2026-11-09T23:02:44Z\"\n      correction:\n        mission: m-2026-11-09-001\n        round: 1\n        kind: factual\n        author: claude\n        claim: leaked "+fakeToken34+"\n        corrected: fixed\n")
	planted := []string{
		filepath.Join(m1, "contract.yaml"), filepath.Join(m1, "results.yaml"), filepath.Join(m1, "reflections.yaml"),
		filepath.Join(root, "missions.jsonl"),
	}
	return planted
}

// L5: every kind of tracked record the step scrubs loses the token, not only
// contract.context, and a second run changes neither the file list nor a byte.
func TestScrubRecords_EveryRecordKind(t *testing.T) {
	env := stage34(t, "v4.8.0")
	root := filepath.Join(env.Roots.Store, ".punt-labs", "ethos")
	plantLeaks34(t, env)
	plantAll34(t, env)
	del := filepath.Join(root, "missions", "m-2026-11-09-002", "delegations", "d-2026-11-09-001")
	write34(t, filepath.Join(del, "record.yaml"), read34(t, filepath.Join(del, "record.yaml"))+"reason: leaked "+fakeToken34+"\n")
	write34(t, filepath.Join(del, "prompt.md"), read34(t, filepath.Join(del, "prompt.md"))+"\nleaked "+fakeToken34+"\n")
	// The corpus index was taken before the plant added corrections.yaml.
	cmd := testhelpers.GitCommand("add", "-A", "--", ".")
	cmd.Dir = env.Roots.Checkout
	b, err := cmd.CombinedOutput()
	require.NoError(t, err, string(b))
	require.NotEmpty(t, recordLeaks34(t, env))

	out := run34(t, "scrub-records", env)
	assert.Empty(t, out.Residuals)
	assert.False(t, out.Deferred)
	assert.Empty(t, recordLeaks34(t, env), "no record may keep the token")
	assert.NotContains(t, read34(t, filepath.Join(root, "missions.jsonl")), fakeToken34)

	before := snapshotTree34(t, root)
	again := run34(t, "scrub-records", env)
	assert.Zero(t, again.Items)
	assert.Equal(t, before, snapshotTree34(t, root), "second run changes no file and no byte")
}

// M2: the step prints nothing over the corpus, even when a directory sync fails.
func TestScrubRecords_PrintsNothing(t *testing.T) {
	for _, v := range scrubVersions34 {
		t.Run(v, func(t *testing.T) {
			env := stage34(t, v)
			plantLeaks34(t, env)
			stderr := captureStderr34(t, func() { run34(t, "scrub-records", env) })
			assert.Empty(t, stderr)
		})
	}
}

// M3: a live writer holding a mission's lock defers the step; it does not block,
// and the held record is untouched.
func TestScrubRecords_BusyLockDefers(t *testing.T) {
	env := stage34(t, "v4.17.0")
	plantLeaks34(t, env)
	id := "m-2026-11-09-001"
	contract := filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "missions", id, "contract.yaml")
	held := read34(t, contract)
	var out Outcome
	require.NoError(t, mission.WithRecordLockTry(env.Policy, id, func() error {
		out = run34(t, "scrub-records", env)
		return nil
	}))
	assert.True(t, out.Deferred)
	assert.Empty(t, out.Residuals)
	assert.Equal(t, held, read34(t, contract), "the held record is not rewritten")

	again := run34(t, "scrub-records", env)
	assert.False(t, again.Deferred, "the next run, with the lock free, finishes")
	assert.Empty(t, recordLeaks34(t, env))
}

// M4: a record that cannot be decoded is a permanent condition: one residual,
// no deferral, and the other records are still scrubbed.
func TestScrubRecords_UndecodableRecordIsResidual(t *testing.T) {
	env := stage34(t, "v4.17.0")
	plantLeaks34(t, env)
	root := filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "missions")
	write34(t, filepath.Join(root, "m-2026-11-09-001", "reflections.yaml"), "reflections: [")

	out := run34(t, "scrub-records", env)
	assert.False(t, out.Deferred)
	require.Len(t, out.Residuals, 1)
	assert.Equal(t, "tracked-records", out.Residuals[0].ID)
	assert.NotContains(t, out.Residuals[0].Reason, "reflections.yaml", "the reason names no path")
	assert.GreaterOrEqual(t, out.Items, 1, "the other records are scrubbed")
}
