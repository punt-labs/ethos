package mission

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/redact"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
}

func readFileRel(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	require.NoError(t, err)
	return string(b)
}

func TestScrubFiles(t *testing.T) {
	newLeakFixture(t)
	root := t.TempDir()
	const mid = "m-2026-01-01-001"
	m := ".punt-labs/ethos/missions/" + mid + "/"
	clean := `{"id":"m-2026-01-01-001","ticket":"t"}`
	leaky := `{"id":"m-2026-01-02-001","repo":"at /home/alice/work a@b.org"}`
	files := map[string]string{
		m + "contract.yaml":                       "mission_id: " + mid + "\ncontext: \"see /Users/bob/x and a@b.org\"\nsuccess_criteria:\n    - make check passes\n",
		m + "results.yaml":                        "results:\n    - prose: untouched\n",
		m + "reflections.yaml":                    "reflections:\n    - reason: \"at /home/alice/y\"\n",
		m + "delegations/d-1/record.yaml":         "id: d-1\nsession: /home/alice/s\n",
		m + "delegations/d-1/prompt.md":           "read /home/alice/notes.md\n",
		".punt-labs/ethos/missions.jsonl":         clean + "\n" + leaky + "\n",
		m + "log.jsonl":                           leaky + "\n",
		m + "log-s-1-2.jsonl":                     leaky + "\n",
		m + "artifacts/review.md":                 "/home/alice/x\n",
		m + "local/live.jsonl":                    leaky + "\n",
		m + "contract.yaml.tmp":                   "context: a@b.org\n",
		".punt-labs/ethos/sessions/s/audit.jsonl": leaky + "\n",
		".punt-labs/ethos/identities/jane.yaml":   "email: jane@example.com\n",
	}
	writeTree(t, root, files)
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	want := []string{
		m + "contract.yaml", m + "reflections.yaml", m + "delegations/d-1/record.yaml",
		m + "delegations/d-1/prompt.md", ".punt-labs/ethos/missions.jsonl",
	}

	// Dry run reports and writes nothing.
	res, err := ScrubFiles(root, t.TempDir(), rels, true)
	require.NoError(t, err)
	assert.ElementsMatch(t, want, res.Changed)
	for rel, body := range files {
		assert.Equal(t, body, readFileRel(t, root, rel), "dry run must not write %s", rel)
	}

	res, err = ScrubFiles(root, t.TempDir(), rels, false)
	require.NoError(t, err)
	assert.ElementsMatch(t, want, res.Changed)

	assert.Contains(t, readFileRel(t, root, m+"contract.yaml"), "see ~/x and [redacted-email]")
	assert.Contains(t, readFileRel(t, root, m+"reflections.yaml"), "at ~/y")
	assert.Contains(t, readFileRel(t, root, m+"delegations/d-1/record.yaml"), "session: ~/s")
	assert.Equal(t, "read ~/notes.md\n", readFileRel(t, root, m+"delegations/d-1/prompt.md"))
	assert.Equal(t, files[m+"results.yaml"], readFileRel(t, root, m+"results.yaml"), "an unaffected file keeps its bytes")

	trace := strings.Split(readFileRel(t, root, ".punt-labs/ethos/missions.jsonl"), "\n")
	assert.Equal(t, clean, trace[0], "a clean line keeps its original bytes")
	assert.Equal(t, `{"id":"m-2026-01-02-001","repo":"at ~/work [redacted-email]"}`, trace[1])

	for _, rel := range []string{m + "log.jsonl", m + "log-s-1-2.jsonl", m + "artifacts/review.md", m + "local/live.jsonl",
		m + "contract.yaml.tmp", ".punt-labs/ethos/sessions/s/audit.jsonl", ".punt-labs/ethos/identities/jane.yaml"} {
		assert.Equal(t, files[rel], readFileRel(t, root, rel), "%s is not a mutable mission record", rel)
	}

	res, err = ScrubFiles(root, t.TempDir(), rels, false)
	require.NoError(t, err)
	assert.Empty(t, res.Changed, "a second run changes nothing")
}

func TestScrubFiles_RefusesUnparseableRecord(t *testing.T) {
	newLeakFixture(t)
	root := t.TempDir()
	rel := ".punt-labs/ethos/missions.jsonl"
	writeTree(t, root, map[string]string{rel: "not json at all a@b.org\n"})
	_, err := ScrubFiles(root, t.TempDir(), []string{rel}, false)
	require.Error(t, err)
	assert.Equal(t, "not json at all a@b.org\n", readFileRel(t, root, rel), "an unparseable file is left untouched")
}

func TestScrubFiles_FailsClosedWithoutUsableHome(t *testing.T) {
	root := t.TempDir()
	rel := ".punt-labs/ethos/missions.jsonl"
	writeTree(t, root, map[string]string{rel: `{"a":"/home/alice/x"}` + "\n"})
	t.Setenv("HOME", "/")
	_, err := ScrubFiles(root, t.TempDir(), []string{rel}, false)
	require.Error(t, err)
}

func TestRedactJSONLines_KeepsOrderAndLiterals(t *testing.T) {
	in := `{"z":1,"a":"mail a@b.org","big":12345678901234567890,"n":null,"t":true,"f":1.50,"list":["x","/home/al/y"],"password":"p","sub":{"b":2,"a":"ok"}}` + "\n\n" +
		`{"clean":"line"}`
	out, changed, err := redactJSONLines(redact.Prose, []byte(in))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t,
		`{"z":1,"a":"mail [redacted-email]","big":12345678901234567890,"n":null,"t":true,"f":1.50,"list":["x","~/y"],"password":"[redacted-secret]","sub":{"b":2,"a":"ok"}}`+"\n\n"+`{"clean":"line"}`,
		string(out))

	out, changed, err = redactJSONLines(redact.Prose, out)
	require.NoError(t, err)
	assert.False(t, changed, "second pass changes nothing")
	_ = out

	_, _, err = redactJSONLines(redact.Prose, []byte(`{"a":1} {"b":2}`))
	assert.Error(t, err, "two values on one line is not JSONL")
}

// TestScrubFiles_LeavesSealedChunksAlone: sealed chunks are immutable
// (DES-058), whatever they contain.
func TestScrubFiles_LeavesSealedChunksAlone(t *testing.T) {
	newLeakFixture(t)
	root := t.TempDir()
	const mid = "m-2026-01-01-001"
	leaky := `{"ts":"2026-01-01T00:00:00Z","tool_input":{"command":"cd /home/alice && TOKEN=abc"}}` + "\n"
	files := map[string]string{
		".punt-labs/ethos/sessions/2026-01-01-s/audit-1-2.jsonl":         leaky,
		".punt-labs/ethos/sessions/2026-01-01-s/audit-1-2.jsonl.corrupt": leaky,
		".punt-labs/ethos/sessions/2026-01-01-s/audit.jsonl":             leaky,
		".punt-labs/ethos/missions/" + mid + "/log-s-1-2.jsonl":          leaky,
		".punt-labs/ethos/missions/" + mid + "/log.jsonl":                leaky,
		".punt-labs/ethos/missions/" + mid + "/artifacts/review.md":      "/home/alice/x\n",
	}
	writeTree(t, root, files)
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	res, err := ScrubFiles(root, t.TempDir(), rels, false)
	require.NoError(t, err)
	assert.Empty(t, res.Changed)
	for rel, body := range files {
		assert.Equal(t, body, readFileRel(t, root, rel), rel)
	}
}

func TestRedactYAMLDoc_KeyNamePass(t *testing.T) {
	in := "password: hunter2\nnote: fine\ntoken_count: 3\napi_key:\n    - a\n    - b\nsecret: true\nnested:\n    access_key: 12345\n    ok: 1\nempty_token: \"\"\n"
	out, changed, err := redactYAMLDoc(redact.Prose, redact.Prose, nil, []byte(in))
	require.NoError(t, err)
	assert.True(t, changed)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(out, &got), "output must parse")
	assert.Equal(t, "[redacted-secret]", got["password"])
	assert.Equal(t, "fine", got["note"])
	assert.Equal(t, "[redacted-secret]", got["token_count"])
	assert.Equal(t, "[redacted-secret]", got["api_key"])
	assert.Equal(t, true, got["secret"], "a boolean is not a credential")
	assert.Equal(t, "[redacted-secret]", got["nested"].(map[string]any)["access_key"])
	assert.Equal(t, 1, got["nested"].(map[string]any)["ok"])
	assert.Equal(t, "", got["empty_token"])

	again, changed, err := redactYAMLDoc(redact.Prose, redact.Prose, nil, out)
	require.NoError(t, err)
	assert.False(t, changed, "idempotent")
	assert.Equal(t, string(out), string(again))
}

func TestRedactJSONLines_NumbersAndArraysUnderSecretKeys(t *testing.T) {
	in := `{"pin_token":42,"api_key":["a","b"],"password":null,"flag":true,"n":7,"empty_secret":[]}`
	out, changed, err := redactJSONLines(redact.Prose, []byte(in))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, `{"pin_token":"[redacted-secret]","api_key":"[redacted-secret]","password":null,"flag":true,"n":7,"empty_secret":[]}`, string(out))
}

// TestScrubFiles_ProcessesEveryFile: a bad file and a deleted file do not
// stop the run; the good files are rewritten and the failure is reported.
func TestScrubFiles_ProcessesEveryFile(t *testing.T) {
	newLeakFixture(t)
	root := t.TempDir()
	const mid = "m-2026-01-01-001"
	m := ".punt-labs/ethos/missions/" + mid + "/"
	files := map[string]string{
		m + "contract.yaml":                          "a: [\n",
		m + "reflections.yaml":                       "reflections:\n    - reason: \"at /home/alice/y\"\n",
		m + "delegations/d-1/prompt.md":              "read /home/alice/notes.md\n",
		".punt-labs/ethos/missions.jsonl":            "not json\n",
		".punt-labs/ethos/missions/gone.result.yaml": "x: /home/alice\n",
	}
	writeTree(t, root, files)
	require.NoError(t, os.Remove(filepath.Join(root, ".punt-labs/ethos/missions/gone.result.yaml")))
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}

	res, err := ScrubFiles(root, t.TempDir(), rels, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "2 rewritten, 2 failed")
	assert.Contains(t, err.Error(), "contract.yaml")
	assert.Contains(t, err.Error(), "missions.jsonl")
	assert.ElementsMatch(t, []string{m + "reflections.yaml", m + "delegations/d-1/prompt.md"}, res.Changed)
	assert.Equal(t, []string{".punt-labs/ethos/missions/gone.result.yaml"}, res.Skipped)
	assert.Equal(t, "read ~/notes.md\n", readFileRel(t, root, m+"delegations/d-1/prompt.md"))
}

func TestScrubFiles_CoversPlainExportOutput(t *testing.T) {
	newLeakFixture(t)
	root := t.TempDir()
	dir := ".punt-labs/ethos/missions/"
	files := map[string]string{
		dir + "m-2026-01-01-001.contract.yaml": "context: at /home/alice/x\n",
		dir + "m-2026-01-01-001.result.yaml":   "prose: at /home/alice/y\n",
		dir + "notes.yaml":                     "prose: at /home/alice/z\n",
	}
	writeTree(t, root, files)
	rels := []string{dir + "m-2026-01-01-001.contract.yaml", dir + "m-2026-01-01-001.result.yaml", dir + "notes.yaml"}
	res, err := ScrubFiles(root, t.TempDir(), rels, false)
	require.NoError(t, err)
	assert.ElementsMatch(t, rels[:2], res.Changed)
	assert.Equal(t, files[dir+"notes.yaml"], readFileRel(t, root, dir+"notes.yaml"))
}

// TestScrubFiles_ContractEnforcementFieldsKeepTheirValue: the scrub applies
// the same path-only rule the contract writer does.
func TestScrubFiles_ContractEnforcementFieldsKeepTheirValue(t *testing.T) {
	newLeakFixture(t)
	root := t.TempDir()
	rel := ".punt-labs/ethos/missions/m-2026-01-01-001/contract.yaml"
	writeTree(t, root, map[string]string{rel: "tools:\n    - Bash(gh auth token:*)\ncontext: \"GITHUB_TOKEN=abc12345 at /home/alice/x\"\n"})
	res, err := ScrubFiles(root, t.TempDir(), []string{rel}, false)
	require.NoError(t, err)
	assert.Equal(t, []string{rel}, res.Changed)
	got := readFileRel(t, root, rel)
	assert.Contains(t, got, "Bash(gh auth token:*)")
	assert.Contains(t, got, "GITHUB_TOKEN="+redact.SecretToken)
}

func TestTrackedRecordFiles_ErrorCarriesGitStderr(t *testing.T) {
	// TMPDIR may sit inside a checkout; stop git's upward search above the
	// scratch directory so it is not a repository.
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	_, err := TrackedRecordFiles(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a git repository")
}

// TestScrubFiles_WriteNotExistIsNotASkip pins that a not-exist error from
// the write step (for example the directory vanishing mid-scrub) is a
// failure: only a missing file at read time is a skip.
func TestScrubFiles_WriteNotExistIsNotASkip(t *testing.T) {
	newLeakFixture(t)
	root := t.TempDir()
	rel := ".punt-labs/ethos/missions/m-2026-01-01-001/contract.yaml"
	writeTree(t, root, map[string]string{rel: "context: a@b.org\n"})
	orig := scrubWrite
	t.Cleanup(func() { scrubWrite = orig })
	scrubWrite = func(dir, pattern, dest string, data []byte) error {
		return &fs.PathError{Op: "rename", Path: dest, Err: fs.ErrNotExist}
	}

	res, err := ScrubFiles(root, t.TempDir(), []string{rel}, false)
	require.Error(t, err)
	assert.Empty(t, res.Skipped)
	assert.Empty(t, res.Changed)
}

// TestScrubFiles_PermissionFailureIsNotASkip: a file that exists but cannot
// be rewritten because of permissions is a failure.
func TestScrubFiles_PermissionFailureIsNotASkip(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	newLeakFixture(t)
	root := t.TempDir()
	rel := ".punt-labs/ethos/missions/m-2026-01-01-001/contract.yaml"
	writeTree(t, root, map[string]string{rel: "context: a@b.org\n"})
	dir := filepath.Dir(filepath.Join(root, filepath.FromSlash(rel)))
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	res, err := ScrubFiles(root, t.TempDir(), []string{rel}, false)
	require.Error(t, err)
	assert.Empty(t, res.Skipped)
	assert.Empty(t, res.Changed)
}

// TestRedactYAMLDoc_Comments: head, line and foot comments are free text
// and get the content pass, also beside a path key: yaml.v3 hangs a trailing
// comment on the preceding key.
func TestRedactYAMLDoc_Comments(t *testing.T) {
	in := "# head GITHUB_TOKEN=abc12345 from /home/alice/x\n" +
		"context: fine # line a@b.org\n" +
		"write_set:\n" +
		"    # grant cfg/token=abc and /home/alice/w\n" +
		"    - src/a.go\n" +
		"# foot password: hunter2\n"
	out, changed, err := redactYAMLDoc(redact.Prose, func(s string) string { return redact.HomePaths(s) },
		reflect.TypeOf(Contract{}), []byte(in))
	require.NoError(t, err)
	assert.True(t, changed, "a comment-only leak is a change")
	got := string(out)
	assert.Contains(t, got, "GITHUB_TOKEN="+redact.SecretToken)
	assert.Contains(t, got, "from ~/x")
	assert.Contains(t, got, redact.EmailToken)
	assert.Contains(t, got, "password: "+redact.SecretToken)
	assert.Contains(t, got, "grant cfg/token="+redact.SecretToken+" and ~/w", "a comment beside a path key is still prose")
	for _, leak := range []string{"abc12345", "a@b.org", "hunter2", "/home/alice"} {
		assert.NotContains(t, got, leak)
	}

	again, changed, err := redactYAMLDoc(redact.Prose, func(s string) string { return redact.HomePaths(s) },
		reflect.TypeOf(Contract{}), out)
	require.NoError(t, err)
	assert.False(t, changed, "idempotent")
	assert.Equal(t, got, string(again))
}

func TestRedactYAMLDoc_OnlyACommentLeaks(t *testing.T) {
	in := "context: fine\n# token=abc12345\n"
	out, changed, err := redactYAMLDoc(redact.Prose, redact.Prose, nil, []byte(in))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.NotContains(t, string(out), "abc12345")
}

// TestScrubFiles_KeepFieldsAreSchemaDriven: a pipeline ID that merely looks
// like an API key is kept, because the Contract type tags it keep; the
// contract still validates after the scrub.
func TestScrubFiles_KeepFieldsAreSchemaDriven(t *testing.T) {
	newLeakFixture(t)
	root := t.TempDir()
	rel := ".punt-labs/ethos/missions/m-2026-04-07-001/contract.yaml"
	c := validContract()
	c.Pipeline = "sk-project-implementation-workflow"
	c.Context = "GITHUB_TOKEN=abc12345"
	raw, err := yaml.Marshal(&c)
	require.NoError(t, err)
	writeTree(t, root, map[string]string{rel: string(raw)})

	res, err := ScrubFiles(root, t.TempDir(), []string{rel}, false)
	require.NoError(t, err)
	assert.Equal(t, []string{rel}, res.Changed)
	var got Contract
	require.NoError(t, yaml.Unmarshal([]byte(readFileRel(t, root, rel)), &got))
	assert.Equal(t, "sk-project-implementation-workflow", got.Pipeline)
	assert.Equal(t, "GITHUB_TOKEN="+redact.SecretToken, got.Context)
	require.NoError(t, got.Validate())
}

func TestScrubFiles_KeepFieldsInResultsAndRecords(t *testing.T) {
	newLeakFixture(t)
	root := t.TempDir()
	m := ".punt-labs/ethos/missions/m-2026-04-07-001/"
	files := map[string]string{
		m + "results.yaml":                "results:\n    - mission: m-2026-04-07-001\n      verdict: pass\n      files_changed:\n        - path: src/user@example.com\n      prose: mail a@b.org\n",
		m + "delegations/d-1/record.yaml": "id: sk-project-implementation-workflow\nsession: /home/alice/s\n",
	}
	writeTree(t, root, files)
	rels := []string{m + "results.yaml", m + "delegations/d-1/record.yaml"}
	_, err := ScrubFiles(root, t.TempDir(), rels, false)
	require.NoError(t, err)
	res := readFileRel(t, root, m+"results.yaml")
	assert.Contains(t, res, "path: src/user@example.com")
	assert.Contains(t, res, redact.EmailToken)
	rec := readFileRel(t, root, m+"delegations/d-1/record.yaml")
	assert.Contains(t, rec, "id: sk-project-implementation-workflow")
	assert.Contains(t, rec, "session: ~/s")
}

// TestCheckScrubbedContract: a redacted contract that no longer validates
// is refused when the original validated; one that never validated is not
// judged.
func TestCheckScrubbedContract(t *testing.T) {
	c := validContract()
	orig, err := yaml.Marshal(&c)
	require.NoError(t, err)
	require.NoError(t, checkScrubbedContract(orig, orig))

	bad := c
	bad.WriteSet = []string{"a:b"}
	red, err := yaml.Marshal(&bad)
	require.NoError(t, err)
	err = checkScrubbedContract(orig, red)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write_set[0]")

	assert.NoError(t, checkScrubbedContract(red, red), "an already-invalid contract is not judged")
	assert.NoError(t, checkScrubbedContract([]byte("a: ["), red), "an unparseable original is not judged")
	assert.Error(t, checkScrubbedContract(orig, []byte("a: [")))
}

// TestScrubFiles_HoldsTheWritersLock: a writer that holds the lock a file's
// writers take blocks the scrub; what the writer appends meanwhile is read
// by the scrub, not overwritten by it.
func TestScrubFiles_HoldsTheWritersLock(t *testing.T) {
	const mid = "m-2026-04-07-001"
	m := ".punt-labs/ethos/missions/" + mid + "/"
	tests := []struct {
		name string
		rel  string
		hold func(t *testing.T, repo, global string) func()
	}{
		{"trace", ".punt-labs/ethos/missions.jsonl", func(t *testing.T, repo, global string) func() {
			return holdLock(t, func(fn func() error) error { return withTraceLock(RepoStatePath(repo), fn) })
		}},
		{"mission", m + "reflections.yaml", func(t *testing.T, repo, global string) func() {
			s := NewStoreWithRoots(repo, global)
			return holdLock(t, func(fn func() error) error { return s.withLock(mid, fn) })
		}},
		{"delegation", m + "delegations/d-1/prompt.md", func(t *testing.T, repo, global string) func() {
			release, err := AcquireDelegationLock(global, "d-1")
			require.NoError(t, err)
			return release
		}},
	}
	leak := map[string]string{
		".punt-labs/ethos/missions.jsonl": `{"repo":"/home/alice/a"}` + "\n",
		m + "reflections.yaml":            "reflections:\n    - reason: /home/alice/a\n",
		m + "delegations/d-1/prompt.md":   "/home/alice/a\n",
	}
	appended := map[string]string{
		".punt-labs/ethos/missions.jsonl": `{"repo":"/home/alice/b"}` + "\n",
		m + "reflections.yaml":            "    - reason: /home/alice/b\n",
		m + "delegations/d-1/prompt.md":   "/home/alice/b\n",
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newLeakFixture(t)
			repo, global := t.TempDir(), t.TempDir()
			writeTree(t, repo, map[string]string{tt.rel: leak[tt.rel]})
			release := tt.hold(t, repo, global)

			done := make(chan error, 1)
			go func() {
				_, err := ScrubFiles(repo, global, []string{tt.rel}, false)
				done <- err
			}()
			select {
			case err := <-done:
				release()
				t.Fatalf("scrub finished while the writer held its lock: %v", err)
			case <-time.After(150 * time.Millisecond):
			}

			// The concurrent writer's append lands before the lock is released.
			f, err := os.OpenFile(filepath.Join(repo, filepath.FromSlash(tt.rel)), os.O_APPEND|os.O_WRONLY, 0o644)
			require.NoError(t, err)
			_, err = f.WriteString(appended[tt.rel])
			require.NoError(t, err)
			require.NoError(t, f.Close())
			release()
			require.NoError(t, <-done)

			got := readFileRel(t, repo, tt.rel)
			assert.Contains(t, got, "~/a")
			assert.Contains(t, got, "~/b", "the append made under the lock must survive the scrub")
			assert.NotContains(t, got, "/home/alice")
		})
	}
}

// holdLock runs lock(fn) in a goroutine whose fn blocks until the returned
// release is called, and returns once the lock is held.
func holdLock(t *testing.T, lock func(fn func() error) error) (release func()) {
	t.Helper()
	held, stop, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- lock(func() error {
			close(held)
			<-stop
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-done:
		t.Fatalf("lock not acquired: %v", err)
	}
	var once sync.Once
	return func() {
		once.Do(func() { close(stop) })
		require.NoError(t, <-done)
	}
}

func TestScrubFiles_RequiresGlobalRoot(t *testing.T) {
	newLeakFixture(t)
	_, err := ScrubFiles(t.TempDir(), "", nil, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "global store root")
}

// TestScrubFiles_RefusesSymlinks: neither a symlinked artifact nor a
// symlinked containing directory is followed; the target is untouched.
func TestScrubFiles_RefusesSymlinks(t *testing.T) {
	newLeakFixture(t)
	const mid = "m-2026-04-07-001"
	rel := ".punt-labs/ethos/missions/" + mid + "/reflections.yaml"
	body := "reflections:\n    - reason: /home/alice/a\n"

	t.Run("artifact", func(t *testing.T) {
		repo, outside := t.TempDir(), t.TempDir()
		target := filepath.Join(outside, "r.yaml")
		require.NoError(t, os.WriteFile(target, []byte(body), 0o644))
		link := filepath.Join(repo, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
		require.NoError(t, os.Symlink(target, link))

		_, err := ScrubFiles(repo, t.TempDir(), []string{rel}, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "refusing to follow symlink")
		got, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, body, string(got))
	})

	t.Run("directory", func(t *testing.T) {
		repo, outside := t.TempDir(), t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(outside, "reflections.yaml"), []byte(body), 0o644))
		missions := filepath.Join(repo, ".punt-labs", "ethos", "missions")
		require.NoError(t, os.MkdirAll(missions, 0o755))
		require.NoError(t, os.Symlink(outside, filepath.Join(missions, mid)))

		_, err := ScrubFiles(repo, t.TempDir(), []string{rel}, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "refusing to follow symlink")
		got, err := os.ReadFile(filepath.Join(outside, "reflections.yaml"))
		require.NoError(t, err)
		assert.Equal(t, body, string(got))
	})
}
