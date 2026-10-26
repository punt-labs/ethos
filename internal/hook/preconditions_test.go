package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// makeContractWithPreconditions builds a Contract whose only
// non-default fields are the ones the precondition evaluator reads.
// Validate is not run here — most tests want to exercise the
// evaluator on contracts the validator would reject (empty Form,
// malformed Inputs) without staging a full mission file.
func makeContractWithPreconditions(preconditions []mission.Precondition, inputs mission.Inputs, strict *bool) *mission.Contract {
	return &mission.Contract{
		Preconditions:       preconditions,
		Inputs:              inputs,
		StrictPreconditions: strict,
	}
}

// writeSessionReadAudit appends a Read auditEntry under the per-
// repo session directory layout (resolveRepoSessionDir). Returns the
// full path the entry was written to.
func writeSessionReadAudit(t *testing.T, repoRoot, sessionID, readPath string) string {
	t.Helper()
	now := time.Now().UTC()
	dir, err := resolveRepoSessionDir(repoRoot, sessionID, now)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	// A Read reaches the log redacted, as HandleAuditLog stores it.
	stored, _ := redactToolInput(map[string]any{"tool_input": map[string]any{"file_path": readPath}}, "Read", repoRoot)
	entry := auditEntry{
		Ts:        now.Format(time.RFC3339),
		Session:   sessionID,
		Tool:      "Read",
		ToolInput: stored,
	}
	path := filepath.Join(dir, "audit.jsonl")
	require.NoError(t, writeAuditEntry(path, entry))
	return path
}

func TestEvaluatePreconditions_NilContractAllows(t *testing.T) {
	reason, deny, err := EvaluatePreconditions(nil, "Write",
		map[string]any{"file_path": "x.go"}, "sess-x", t.TempDir())
	require.NoError(t, err)
	assert.False(t, deny)
	assert.Empty(t, reason)
}

func TestEvaluatePreconditions_EmptyListAllows(t *testing.T) {
	c := makeContractWithPreconditions(nil, mission.Inputs{}, nil)
	reason, deny, err := EvaluatePreconditions(c, "Write",
		map[string]any{"file_path": "x.go"}, "sess-x", t.TempDir())
	require.NoError(t, err)
	assert.False(t, deny)
	assert.Empty(t, reason)
}

func TestEvaluatePreconditions_ImplicitUnreadDenies(t *testing.T) {
	dir := t.TempDir()
	c := makeContractWithPreconditions(
		[]mission.Precondition{{Form: mission.PreconditionFormImplicit, Message: "read before write"}},
		mission.Inputs{}, nil,
	)
	reason, deny, err := EvaluatePreconditions(c, "Write",
		map[string]any{"file_path": "/abs/path/to/store.go"}, "sess-1", dir)
	require.NoError(t, err)
	assert.True(t, deny)
	assert.Equal(t, "read before write", reason)
}

func TestEvaluatePreconditions_ImplicitReadAllows(t *testing.T) {
	dir := t.TempDir()
	target := "/abs/path/to/store.go"
	writeSessionReadAudit(t, dir, "sess-2", target)

	c := makeContractWithPreconditions(
		[]mission.Precondition{{Form: mission.PreconditionFormImplicit, Message: "read before write"}},
		mission.Inputs{}, nil,
	)
	reason, deny, err := EvaluatePreconditions(c, "Write",
		map[string]any{"file_path": target}, "sess-2", dir)
	require.NoError(t, err, "Read in audit log must satisfy implicit form")
	assert.False(t, deny)
	assert.Empty(t, reason)
}

func TestEvaluatePreconditions_ImplicitMultiplePathsOneUnread(t *testing.T) {
	dir := t.TempDir()
	writeSessionReadAudit(t, dir, "sess-3", "a.go")
	// "b.go" not read

	c := makeContractWithPreconditions(
		[]mission.Precondition{{Form: mission.PreconditionFormImplicit, Message: "read all listed files"}},
		mission.Inputs{}, nil,
	)
	reason, deny, err := EvaluatePreconditions(c, "Edit",
		map[string]any{"files": []any{"a.go", "b.go"}}, "sess-3", dir)
	require.NoError(t, err)
	assert.True(t, deny, "one unread file in a files[] array must deny")
	assert.Equal(t, "read all listed files", reason)
}

func TestEvaluatePreconditions_ImplicitNoPathsAllows(t *testing.T) {
	// Implicit precondition + tool input has no path-shaped key —
	// nothing to check; allow.
	dir := t.TempDir()
	c := makeContractWithPreconditions(
		[]mission.Precondition{{Form: mission.PreconditionFormImplicit, Message: "read"}},
		mission.Inputs{}, nil,
	)
	reason, deny, err := EvaluatePreconditions(c, "Bash",
		map[string]any{"command": "ls"}, "sess-x", dir)
	require.NoError(t, err)
	assert.False(t, deny, "path-free tool input must satisfy implicit form")
	assert.Empty(t, reason)
}

func TestEvaluatePreconditions_ExplicitUnreadDenies(t *testing.T) {
	dir := t.TempDir()
	c := makeContractWithPreconditions(
		[]mission.Precondition{
			{
				Form:        mission.PreconditionFormExplicit,
				RequireRead: []string{"internal/mission/store.go"},
				Message:     "read store.go first",
			},
		},
		mission.Inputs{}, nil,
	)
	reason, deny, err := EvaluatePreconditions(c, "Write",
		map[string]any{"file_path": "internal/mission/store.go"}, "sess-x", dir)
	require.NoError(t, err)
	assert.True(t, deny)
	assert.Equal(t, "read store.go first", reason)
}

func TestEvaluatePreconditions_ExplicitReadAllows(t *testing.T) {
	dir := t.TempDir()
	writeSessionReadAudit(t, dir, "sess-y", "internal/mission/store.go")

	c := makeContractWithPreconditions(
		[]mission.Precondition{
			{
				Form:        mission.PreconditionFormExplicit,
				RequireRead: []string{"internal/mission/store.go"},
				Message:     "read store.go first",
			},
		},
		mission.Inputs{}, nil,
	)
	reason, deny, err := EvaluatePreconditions(c, "Write",
		map[string]any{"file_path": "internal/mission/store.go"}, "sess-y", dir)
	require.NoError(t, err)
	assert.False(t, deny)
	assert.Empty(t, reason)
}

func TestEvaluatePreconditions_ExplicitInputsTicketSubstitution(t *testing.T) {
	dir := t.TempDir()
	writeSessionReadAudit(t, dir, "sess-z", "internal/foo/bar.go")

	c := makeContractWithPreconditions(
		[]mission.Precondition{
			{
				Form:        mission.PreconditionFormExplicit,
				RequireRead: []string{"${inputs.ticket}"},
				Message:     "read the ticket file",
			},
		},
		mission.Inputs{Ticket: "internal/foo/bar.go"},
		nil,
	)
	reason, deny, err := EvaluatePreconditions(c, "Write",
		map[string]any{"file_path": "internal/foo/bar.go"}, "sess-z", dir)
	require.NoError(t, err, "substituted path that was read must allow")
	assert.False(t, deny)
	assert.Empty(t, reason)
}

func TestEvaluatePreconditions_ExplicitInputsFilesIndexed(t *testing.T) {
	dir := t.TempDir()
	writeSessionReadAudit(t, dir, "sess-i", "internal/a.go")
	writeSessionReadAudit(t, dir, "sess-i", "internal/b.go")

	c := makeContractWithPreconditions(
		[]mission.Precondition{
			{
				Form:        mission.PreconditionFormExplicit,
				RequireRead: []string{"${inputs.files.0}", "${inputs.files.1}"},
				Message:     "read all input files",
			},
		},
		mission.Inputs{Files: []string{"internal/a.go", "internal/b.go"}},
		nil,
	)
	reason, deny, err := EvaluatePreconditions(c, "Edit",
		map[string]any{"file_path": "internal/a.go"}, "sess-i", dir)
	require.NoError(t, err)
	assert.False(t, deny)
	assert.Empty(t, reason)
}

func TestEvaluatePreconditions_MalformedSubstitutionErrors(t *testing.T) {
	dir := t.TempDir()
	c := makeContractWithPreconditions(
		[]mission.Precondition{
			{
				Form:        mission.PreconditionFormExplicit,
				RequireRead: []string{"${inputs.unknown}"},
				Message:     "read the input file",
			},
		},
		mission.Inputs{Ticket: "some/path"},
		nil,
	)
	reason, deny, err := EvaluatePreconditions(c, "Write",
		map[string]any{"file_path": "x.go"}, "sess-m", dir)
	require.Error(t, err, "unknown input key must surface as an unevaluable error")
	assert.True(t, deny)
	assert.Equal(t, "read the input file", reason,
		"unevaluable predicate must surface the contract's Message")
}

func TestEvaluatePreconditions_FilesOutOfRangeErrors(t *testing.T) {
	dir := t.TempDir()
	c := makeContractWithPreconditions(
		[]mission.Precondition{
			{
				Form:        mission.PreconditionFormExplicit,
				RequireRead: []string{"${inputs.files.5}"},
				Message:     "read the 6th input file",
			},
		},
		mission.Inputs{Files: []string{"a", "b"}},
		nil,
	)
	_, deny, err := EvaluatePreconditions(c, "Write",
		map[string]any{"file_path": "x.go"}, "sess-r", dir)
	require.Error(t, err)
	assert.True(t, deny)
}

func TestEvaluatePreconditions_FilesNonIntegerErrors(t *testing.T) {
	dir := t.TempDir()
	c := makeContractWithPreconditions(
		[]mission.Precondition{
			{
				Form:        mission.PreconditionFormExplicit,
				RequireRead: []string{"${inputs.files.abc}"},
				Message:     "read the file",
			},
		},
		mission.Inputs{Files: []string{"a"}},
		nil,
	)
	_, deny, err := EvaluatePreconditions(c, "Write",
		map[string]any{"file_path": "x.go"}, "sess-n", dir)
	require.Error(t, err)
	assert.True(t, deny)
}

func TestEvaluatePreconditions_UnknownFormErrors(t *testing.T) {
	dir := t.TempDir()
	c := makeContractWithPreconditions(
		[]mission.Precondition{{Form: "synth", Message: "bad form"}},
		mission.Inputs{}, nil,
	)
	_, deny, err := EvaluatePreconditions(c, "Write",
		map[string]any{"file_path": "x.go"}, "sess-u", dir)
	require.Error(t, err)
	assert.True(t, deny)
}

func TestEvaluatePreconditions_BlankMessageFallback(t *testing.T) {
	// Validate rejects blank Message at the trust boundary, but the
	// evaluator stays defensive — an in-memory mutation that bypassed
	// the validator must still surface a non-blank reason.
	dir := t.TempDir()
	c := makeContractWithPreconditions(
		[]mission.Precondition{{Form: mission.PreconditionFormImplicit, Message: ""}},
		mission.Inputs{}, nil,
	)
	reason, deny, err := EvaluatePreconditions(c, "Write",
		map[string]any{"file_path": "/x.go"}, "sess-b", dir)
	require.NoError(t, err)
	assert.True(t, deny)
	assert.Contains(t, reason, "preconditions[0] failed")
}

func TestEvaluatePreconditions_PathCleanedMatch(t *testing.T) {
	dir := t.TempDir()
	// Read recorded under cleaned form
	writeSessionReadAudit(t, dir, "sess-c", "internal/hook/store.go")

	c := makeContractWithPreconditions(
		[]mission.Precondition{{Form: mission.PreconditionFormImplicit, Message: "read"}},
		mission.Inputs{}, nil,
	)
	// Tool input uses dot-slash + extra slashes
	reason, deny, err := EvaluatePreconditions(c, "Write",
		map[string]any{"file_path": "./internal/hook//store.go"}, "sess-c", dir)
	require.NoError(t, err)
	assert.False(t, deny, "cleaned form must match: %s", reason)
}

func TestEvaluatePreconditions_NotebookPathExtracted(t *testing.T) {
	dir := t.TempDir()
	writeSessionReadAudit(t, dir, "sess-nb", "notebook.ipynb")

	c := makeContractWithPreconditions(
		[]mission.Precondition{{Form: mission.PreconditionFormImplicit, Message: "read"}},
		mission.Inputs{}, nil,
	)
	reason, deny, err := EvaluatePreconditions(c, "NotebookEdit",
		map[string]any{"notebook_path": "notebook.ipynb"}, "sess-nb", dir)
	require.NoError(t, err)
	assert.False(t, deny, "notebook_path must satisfy implicit form: %s", reason)
}

// TestEvaluatePreconditions_AuditLogReadFailure simulates a session
// dir whose audit.jsonl is a directory, not a file. readAuditEntries
// returns an error and the evaluator surfaces it as an unevaluable
// predicate so the strict-fail-mode policy can decide.
func TestEvaluatePreconditions_AuditLogReadFailure(t *testing.T) {
	dir := t.TempDir()
	sessDir, err := resolveRepoSessionDir(dir, "sess-broken", time.Now().UTC())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(sessDir, 0o700))
	// Put a directory where the JSONL file should be.
	require.NoError(t, os.MkdirAll(filepath.Join(sessDir, "audit.jsonl"), 0o700))

	c := makeContractWithPreconditions(
		[]mission.Precondition{{Form: mission.PreconditionFormImplicit, Message: "blocked"}},
		mission.Inputs{}, nil,
	)
	reason, deny, evalErr := EvaluatePreconditions(c, "Write",
		map[string]any{"file_path": "x.go"}, "sess-broken", dir)
	require.Error(t, evalErr, "unreadable audit log must surface as unevaluable")
	assert.True(t, deny)
	assert.Equal(t, "blocked", reason)
}

// TestExtractToolInputPaths covers the path-extraction table for
// each supported tool input shape.
func TestExtractToolInputPaths(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]any
		want  []string
	}{
		{"nil input", nil, nil},
		{"file_path", map[string]any{"file_path": "a.go"}, []string{"a.go"}},
		{"notebook_path", map[string]any{"notebook_path": "x.ipynb"}, []string{"x.ipynb"}},
		{
			"files array",
			map[string]any{"files": []any{"a.go", "b.go"}},
			[]string{"a.go", "b.go"},
		},
		{
			"paths array",
			map[string]any{"paths": []any{"a", "b"}},
			[]string{"a", "b"},
		},
		{
			"file_path + files",
			map[string]any{"file_path": "x.go", "files": []any{"a.go"}},
			[]string{"x.go", "a.go"},
		},
		{"empty file_path skipped", map[string]any{"file_path": ""}, nil},
		{"non-string in files array skipped",
			map[string]any{"files": []any{"a.go", 42, "b.go"}},
			[]string{"a.go", "b.go"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractToolInputPaths("Write", tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSubstituteInputs(t *testing.T) {
	c := &mission.Contract{
		Inputs: mission.Inputs{
			Ticket:     "ticket-path.go",
			Files:      []string{"a.go", "b.go"},
			References: []string{"r0.md"},
		},
	}
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{"no markers", "internal/x.go", "internal/x.go", ""},
		{"ticket scalar", "${inputs.ticket}", "ticket-path.go", ""},
		{"files indexed", "${inputs.files.0}", "a.go", ""},
		{"files second", "${inputs.files.1}", "b.go", ""},
		{"references indexed", "${inputs.references.0}", "r0.md", ""},
		{"prefix + marker", "dir/${inputs.ticket}", "dir/ticket-path.go", ""},
		{"unknown key", "${inputs.target}", "", "unknown input key"},
		{"out of range", "${inputs.files.5}", "", "out of range"},
		{"non-integer index", "${inputs.files.abc}", "", "not an integer"},
		{"non-inputs substitution", "${env.HOME}", "", "unsupported substitution"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := substituteInputs(tt.in, c)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSubstituteInputs_NilContract(t *testing.T) {
	_, err := substituteInputs("${inputs.ticket}", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contract is nil")
}

// TestSubstituteInputs_EmptyTicket pins the unevaluable diagnostic
// when the substitution resolves to an empty Inputs field — the
// evaluator must surface this rather than silently substitute the
// empty string and produce a false-positive deny.
func TestSubstituteInputs_EmptyTicket(t *testing.T) {
	c := &mission.Contract{Inputs: mission.Inputs{Ticket: ""}}
	_, err := substituteInputs("${inputs.ticket}", c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not set")
}

// TestEvaluatePreconditions_StrictModeIndependent confirms the
// evaluator returns the same (deny, err) shape regardless of the
// contract's StrictPreconditions field — the fail-open vs fail-closed
// decision is the caller's concern, not the evaluator's.
func TestEvaluatePreconditions_StrictModeIndependent(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name   string
		strict *bool
	}{
		{"nil pointer (default strict)", nil},
		{"explicit true", boolPtr(true)},
		{"explicit false (escape hatch)", boolPtr(false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := makeContractWithPreconditions(
				[]mission.Precondition{
					{
						Form:        mission.PreconditionFormExplicit,
						RequireRead: []string{"${inputs.unknown}"},
						Message:     "blocked",
					},
				},
				mission.Inputs{}, tc.strict,
			)
			reason, deny, err := EvaluatePreconditions(c, "Write",
				map[string]any{"file_path": "x.go"}, "sess-s", dir)
			require.Error(t, err)
			assert.True(t, deny)
			assert.Equal(t, "blocked", reason)
		})
	}
}

func boolPtr(v bool) *bool { return &v }

// TestEvaluatePreconditions_EmptySessionID treats no session as no
// session-read history. Implicit form denies because no Read has
// been recorded.
func TestEvaluatePreconditions_EmptySessionID(t *testing.T) {
	dir := t.TempDir()
	c := makeContractWithPreconditions(
		[]mission.Precondition{{Form: mission.PreconditionFormImplicit, Message: "no session"}},
		mission.Inputs{}, nil,
	)
	reason, deny, err := EvaluatePreconditions(c, "Write",
		map[string]any{"file_path": "x.go"}, "", dir)
	require.NoError(t, err, "empty session is treated as empty audit log, not unevaluable")
	assert.True(t, deny)
	assert.Equal(t, "no session", reason)
}

// TestLoadSessionReads_MissingFileEmpty verifies the not-exists
// audit log returns the empty set with no error so the evaluator
// can report a clean deny rather than an unevaluable error.
func TestLoadSessionReads_MissingFileEmpty(t *testing.T) {
	dir := t.TempDir()
	reads, err := loadSessionReads(dir, "sess-none")
	require.NoError(t, err)
	assert.Empty(t, reads)
}

// TestLoadSessionReads_ContainsReadEntries persists a session audit
// log via writeAuditEntry + the resolved repo session dir, then
// confirms loadSessionReads pulls every Read into the returned set.
func TestLoadSessionReads_ContainsReadEntries(t *testing.T) {
	dir := t.TempDir()
	writeSessionReadAudit(t, dir, "sess-r", "a.go")
	writeSessionReadAudit(t, dir, "sess-r", "b.go")

	reads, err := loadSessionReads(dir, "sess-r")
	require.NoError(t, err)
	require.Len(t, reads, 2)
	assert.Contains(t, reads, "a.go")
	assert.Contains(t, reads, "b.go")
}

// TestReadsContain covers the absolute / relative matching pair.
func TestReadsContain(t *testing.T) {
	abs, err := filepath.Abs("relative/path.go")
	require.NoError(t, err)

	tests := []struct {
		name  string
		reads map[string]struct{}
		path  string
		want  bool
	}{
		{"empty set never contains", map[string]struct{}{}, "x.go", false},
		{
			"exact match",
			map[string]struct{}{"a.go": {}},
			"a.go", true,
		},
		{
			"cleaned target",
			map[string]struct{}{"a/b.go": {}},
			"./a//b.go", true,
		},
		{
			"absolute in reads, relative in candidate",
			map[string]struct{}{abs: {}},
			"relative/path.go", true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := readsContain(tt.reads, tt.path, "")
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestEnvRepoRoot_PrefersEnvVar pins that ETHOS_REPO_ROOT takes
// precedence over the fallback. The override must name an existing
// directory — a nonexistent one is refused (ethos-yofr SFH F1) — so the
// test points it at a real temp dir.
func TestEnvRepoRoot_PrefersEnvVar(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ETHOS_REPO_ROOT", dir)
	got := envRepoRoot()
	assert.Equal(t, dir, got)
}

// TestEnvRepoRoot_FallbackUsedWhenEnvUnset confirms the helper
// falls back to tierBRepoRoot when the env var is not set. The
// concrete fallback value depends on the test cwd; we only assert
// non-empty.
func TestEnvRepoRoot_FallbackUsedWhenEnvUnset(t *testing.T) {
	t.Setenv("ETHOS_REPO_ROOT", "")
	got := envRepoRoot()
	assert.NotEmpty(t, got)
}

// TestEvaluatePreconditions_SessionIDLooksLikePath defends against a
// SessionID that contains slashes — filepath.Base in audit_paths.go
// normalizes the suffix so the directory lookup stays sane. The
// evaluator must produce a useful result either way (no panic, no
// path-traversal escape from the repo root).
func TestEvaluatePreconditions_SessionIDLooksLikePath(t *testing.T) {
	dir := t.TempDir()
	c := makeContractWithPreconditions(
		[]mission.Precondition{{Form: mission.PreconditionFormImplicit, Message: "x"}},
		mission.Inputs{}, nil,
	)
	// No panic and a clean deny — no audit log exists for this session.
	_, deny, err := EvaluatePreconditions(c, "Write",
		map[string]any{"file_path": "x.go"}, "../escape/sess", dir)
	require.NoError(t, err)
	assert.True(t, deny)
	// Sanity: the session dir we resolved is under dir, not escaped.
	resolved, _ := resolveRepoSessionDir(dir, "../escape/sess", time.Now().UTC())
	assert.True(t, strings.HasPrefix(resolved, dir),
		"session dir resolution must stay under repo root: %s", resolved)
}

// TestEvaluatePreconditions_HandleAuditLogIntegration writes an
// audit log entry via the HandleAuditLog public surface and
// verifies the precondition evaluator sees it. This guards against
// drift between the writer (HandleAuditLog) and the reader
// (loadSessionReads) — if either side renames a key or changes the
// per-session directory shape, this test fails.
func TestEvaluatePreconditions_HandleAuditLogIntegration(t *testing.T) {
	dir := t.TempDir()
	payload := map[string]any{
		"session_id": "sess-int",
		"tool_name":  "Read",
		"tool_input": map[string]any{"file_path": "internal/mission/store.go"},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NoError(t, HandleAuditLog(bytes.NewReader(data), dir, ""))

	c := makeContractWithPreconditions(
		[]mission.Precondition{{Form: mission.PreconditionFormImplicit, Message: "read first"}},
		mission.Inputs{}, nil,
	)
	reason, deny, err := EvaluatePreconditions(c, "Write",
		map[string]any{"file_path": "internal/mission/store.go"}, "sess-int", dir)
	require.NoError(t, err)
	assert.False(t, deny, "audit log written via HandleAuditLog must satisfy precondition: %s", reason)
}

// TestEvaluatePreconditions_ReadOfCredentialShapedPathClears: a Read of a
// file whose name looks like a credential assignment is recorded as it was
// read, so the require_read entry that names it is satisfied. Without the
// audited Read the write stays blocked.
func TestEvaluatePreconditions_ReadOfCredentialShapedPathClears(t *testing.T) {
	repo, global := t.TempDir(), t.TempDir()
	const sess = "sess-cred"
	c := makeContractWithPreconditions(
		[]mission.Precondition{{
			Form:        mission.PreconditionFormExplicit,
			RequireRead: []string{"cfg/token=abc"},
			Message:     "read the config first",
		}},
		mission.Inputs{}, nil,
	)
	write := map[string]any{"file_path": "internal/x.go"}

	_, deny, err := EvaluatePreconditions(c, "Write", write, sess, repo)
	require.NoError(t, err)
	require.True(t, deny, "no Read yet: blocked")

	payload, err := json.Marshal(map[string]any{
		"session_id": sess,
		"tool_name":  "Read",
		"tool_input": map[string]any{"file_path": "cfg/token=abc"},
	})
	require.NoError(t, err)
	require.NoError(t, HandleAuditLog(bytes.NewReader(payload), repo, global))

	reason, deny, err := EvaluatePreconditions(c, "Write", write, sess, repo)
	require.NoError(t, err)
	assert.False(t, deny, "the recorded Read must clear the precondition")
	assert.Empty(t, reason)
}

// TestEvaluatePreconditions_StoredRepoPlaceholderMatchesRelativeRead: the
// store writes an absolute input reference under the repo as <repo>/X. A
// relative Read of X must still satisfy ${inputs.references.0} after the
// contract is created and loaded back.
func TestEvaluatePreconditions_StoredRepoPlaceholderMatchesRelativeRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	store := mission.NewStoreWithRoots(repo, filepath.Join(home, ".punt-labs", "ethos"))
	const id = "m-2026-05-22-001"
	c := &mission.Contract{
		MissionID: id,
		Status:    mission.StatusOpen,
		CreatedAt: "2026-05-22T21:30:00Z",
		UpdatedAt: "2026-05-22T21:30:00Z",
		Leader:    "claude",
		Worker:    "bwk",
		Evaluator: mission.Evaluator{Handle: "djb", PinnedAt: "2026-05-22T21:30:00Z"},
		Inputs: mission.Inputs{
			Ticket:     "ethos-7i29",
			References: []string{filepath.Join(repo, "cfg", "config.yaml")},
		},
		WriteSet:        []string{"internal/hook/"},
		Tools:           []string{"Read", "Write", "Edit"},
		SuccessCriteria: []string{"make check passes"},
		Budget:          mission.Budget{Rounds: 3, ReflectionAfterEach: true},
		CurrentRound:    1,
		Preconditions: []mission.Precondition{{
			Form:        mission.PreconditionFormExplicit,
			RequireRead: []string{"${inputs.references.0}"},
			Message:     "read the config first",
		}},
	}
	require.NoError(t, store.Create(c))
	loaded, err := store.Load(id)
	require.NoError(t, err)
	require.Equal(t, "<repo>/cfg/config.yaml", loaded.Inputs.References[0])

	for _, read := range []string{"cfg/config.yaml", filepath.Join(repo, "cfg", "config.yaml")} {
		sess := "sess-" + strings.NewReplacer("/", "-", ".", "-").Replace(read)
		writeSessionReadAudit(t, repo, sess, read)
		reason, deny, err := EvaluatePreconditions(loaded, "Write",
			map[string]any{"file_path": "x.go"}, sess, repo)
		require.NoError(t, err)
		assert.False(t, deny, "a Read of %q satisfies the stored reference: %s", read, reason)
	}

	writeSessionReadAudit(t, repo, "sess-other", "cfg/other.yaml")
	_, deny, err := EvaluatePreconditions(loaded, "Write",
		map[string]any{"file_path": "x.go"}, "sess-other", repo)
	require.NoError(t, err)
	assert.True(t, deny, "a Read of another file does not")
}

// auditRead records a Read of path through the real audit writer.
func auditRead(t *testing.T, repo, global, sess, path string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"session_id": sess,
		"tool_name":  "Read",
		"tool_input": map[string]any{"file_path": path},
	})
	require.NoError(t, err)
	require.NoError(t, HandleAuditLog(bytes.NewReader(payload), repo, global))
}

// redactedRequireRead builds the contract the store would write for a
// require_read entry: the entry goes through the record redactor.
func redactedRequireRead(t *testing.T, repo, entry string) *mission.Contract {
	t.Helper()
	c := mission.Contract{Preconditions: []mission.Precondition{{
		Form:        mission.PreconditionFormExplicit,
		RequireRead: []string{entry},
		Message:     "read it first",
	}}}
	red, err := mission.RedactRecord(repo, c)
	require.NoError(t, err)
	return &red
}

// TestEvaluatePreconditions_RedactedEntryMatchesRecordedRead: a Read recorded
// by the audit writer satisfies the redacted entry that names the same file,
// for a repo path, a home path and a project-slug path under the home.
func TestEvaluatePreconditions_RedactedEntryMatchesRecordedRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo, global := t.TempDir(), t.TempDir()
	slug := strings.ReplaceAll(home, string(filepath.Separator), "-")
	paths := map[string]string{
		"repo": filepath.Join(repo, "cfg", "config.yaml"),
		"home": filepath.Join(home, "notes", "n.md"),
		"slug": filepath.Join(home, ".claude", "projects", slug+"-Coding-x", "memory", "MEMORY.md"),
	}
	for name, abs := range paths {
		t.Run(name, func(t *testing.T) {
			c := redactedRequireRead(t, repo, abs)
			require.NotEqual(t, abs, c.Preconditions[0].RequireRead[0], "the stored entry is redacted")
			write := map[string]any{"file_path": "x.go"}
			sess := "sess-" + name

			_, deny, err := EvaluatePreconditions(c, "Write", write, sess, repo)
			require.NoError(t, err)
			require.True(t, deny, "no Read yet")

			auditRead(t, repo, global, sess, abs)
			reason, deny, err := EvaluatePreconditions(c, "Write", write, sess, repo)
			require.NoError(t, err)
			assert.False(t, deny, "the recorded Read satisfies the entry: %s", reason)
		})
	}
}

// TestEvaluatePreconditions_ForeignHomeNeverMatches: an entry redacted from
// another user's home names a file this machine cannot identify. It never
// matches, not even a recorded Read that was redacted to the same token, and
// the block says why.
func TestEvaluatePreconditions_ForeignHomeNeverMatches(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, global := t.TempDir(), t.TempDir()
	c := redactedRequireRead(t, repo, "/home/bob/notes.md")
	require.True(t, mission.HasRedactedUser(c.Preconditions[0].RequireRead[0]))
	auditRead(t, repo, global, "sess-f", "/home/bob/notes.md")

	reason, deny, err := EvaluatePreconditions(c, "Write",
		map[string]any{"file_path": "x.go"}, "sess-f", repo)
	require.NoError(t, err)
	assert.True(t, deny)
	assert.Contains(t, reason, "read it first")
	assert.Contains(t, reason, "another user's home")
}

// TestEvaluatePreconditions_UnknownHomeIsUnevaluable: a ~ entry cannot be
// compared when the home directory cannot be resolved, and the error says so.
func TestEvaluatePreconditions_UnknownHomeIsUnevaluable(t *testing.T) {
	repo := t.TempDir()
	writeSessionReadAudit(t, repo, "sess-h", "notes/n.md")
	c := makeContractWithPreconditions(
		[]mission.Precondition{{
			Form:        mission.PreconditionFormExplicit,
			RequireRead: []string{"~/notes/n.md"},
			Message:     "read it first",
		}},
		mission.Inputs{}, nil,
	)
	t.Setenv("HOME", "")
	reason, deny, err := EvaluatePreconditions(c, "Write",
		map[string]any{"file_path": "x.go"}, "sess-h", repo)
	require.Error(t, err)
	assert.True(t, deny)
	assert.Equal(t, "read it first", reason)
	assert.Contains(t, err.Error(), "home directory")
}

// TestLoadSessionReads_HomePlaceholderNeedsTheFormatMarker: before the path
// format existed, another user's home was written as ~ too, so a ~ in an
// unmarked line cannot be trusted to be this machine's home.
func TestLoadSessionReads_HomePlaceholderNeedsTheFormatMarker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	for _, tt := range []struct {
		name   string
		format int
		want   bool
	}{{"unmarked", 0, false}, {"marked", auditPathFormat, true}} {
		t.Run(tt.name, func(t *testing.T) {
			sess := "sess-" + tt.name
			now := time.Now().UTC()
			dir, err := resolveRepoSessionDir(repo, sess, now)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(dir, 0o700))
			require.NoError(t, writeAuditEntry(filepath.Join(dir, "audit.jsonl"), auditEntry{
				Ts: now.Format(time.RFC3339), Session: sess, Tool: "Read", PathFormat: tt.format,
				ToolInput: map[string]any{"file_path": "~/notes/n.md"},
			}))
			reads, err := loadSessionReads(repo, sess)
			require.NoError(t, err)
			assert.Equal(t, tt.want, readsContain(reads, filepath.Join(home, "notes", "n.md"), repo))
		})
	}
}

// TestEvaluatePreconditions_ImplicitForeignHomeSaysWhy: a Write under another
// user's home needs a Read that cannot be recorded in a form that can be
// verified, and the block says so rather than just "read it first".
func TestEvaluatePreconditions_ImplicitForeignHomeSaysWhy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, global := t.TempDir(), t.TempDir()
	auditRead(t, repo, global, "sess-i", "/home/bob/x.go")
	c := makeContractWithPreconditions(
		[]mission.Precondition{{Form: mission.PreconditionFormImplicit, Message: "read first"}},
		mission.Inputs{}, nil,
	)
	reason, deny, err := EvaluatePreconditions(c, "Write",
		map[string]any{"file_path": "/home/bob/x.go"}, "sess-i", repo)
	require.NoError(t, err)
	assert.True(t, deny)
	assert.Contains(t, reason, "read first")
	assert.Contains(t, reason, "another user's home")
}

// TestPreconditionEntryUnderRepoHomeDirIsRefused: the leak check knows nothing
// of where a path begins, so an input reference under a top-level home/<x>/
// directory of the repo is refused, an accepted false refusal. (A write_set
// entry such as app/home/page.tsx is a repo-relative path-field value and is
// stored; see TestRedactRecord_RepoRelativePathFieldsMayNameAHomeDir.)
func TestPreconditionEntryUnderRepoHomeDirIsRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo, global := t.TempDir(), filepath.Join(home, ".punt-labs", "ethos")
	target := filepath.Join(repo, "home", "alice", "x.go")
	const id = "m-2026-05-22-009"

	legacy := mission.Contract{
		MissionID: id, Status: mission.StatusOpen,
		CreatedAt: "2026-05-22T21:30:00Z", UpdatedAt: "2026-05-22T21:30:00Z",
		Leader: "claude", Worker: "bwk",
		Evaluator:       mission.Evaluator{Handle: "djb", PinnedAt: "2026-05-22T21:30:00Z"},
		WriteSet:        []string{"internal/hook/"},
		Tools:           []string{"Read"},
		SuccessCriteria: []string{"make check passes"},
		Budget:          mission.Budget{Rounds: 3, ReflectionAfterEach: true},
		CurrentRound:    1,
		Inputs:          mission.Inputs{References: []string{target}},
		Preconditions: []mission.Precondition{{
			Form: mission.PreconditionFormExplicit, RequireRead: []string{"${inputs.references.0}"}, Message: "read it first",
		}},
	}
	raw, err := yaml.Marshal(&legacy)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(global, "missions"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(global, "missions", id+".yaml"), raw, 0o600))
	auditDir := filepath.Join(repo, ".punt-labs", "ethos", "sessions", "2026-05-22-sess-m")
	require.NoError(t, os.MkdirAll(auditDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(auditDir, "audit.jsonl"),
		[]byte(`{"ts":"2026-05-22T10:00:00Z","session":"sess-m","tool":"Agent","contract_id":"`+id+`"}`+"\n"), 0o600))

	var out bytes.Buffer
	err = mission.MigrateMission(global, repo, repo, "", false, &out)
	require.Error(t, err, "the reference holds /home/<x>, which the leak check refuses")
	assert.NotContains(t, err.Error(), "alice")
	assert.NotContains(t, out.String(), "alice")
	_, statErr := os.Stat(filepath.Join(repo, ".punt-labs", "ethos", "missions", id, "contract.yaml"))
	assert.True(t, os.IsNotExist(statErr), "nothing was written")
}

// TestRedactLegacyLine_NeverStampsThePathFormat: the marker vouches that a ~
// is this machine's home. A migrated line may hold a ~ an older version wrote
// for another user's home, so migration rewrites paths but never stamps.
func TestRedactLegacyLine_NeverStampsThePathFormat(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	repo := t.TempDir()
	r, err := mission.NewPathRedactor(repo)
	require.NoError(t, err)
	raw := []byte(`{"ts":"2026-05-22T10:00:00Z","session":"s","tool":"Read","tool_input":{"file_path":"/home/zed/x"}}`)
	out, changed, err := redactLegacyLine(raw, repo, r)
	require.NoError(t, err)
	require.True(t, changed)
	assert.NotContains(t, string(out), "path_format")
	assert.Contains(t, string(out), `"file_path":"~/x"`)
}

// TestMigratedLegacyLine_HomePlaceholderStaysUntrusted: a legacy ~/notes.md
// next to an address that migration rewrites is still unmarked afterwards, so
// a require_read of ~/notes.md stays unsatisfied.
func TestMigratedLegacyLine_HomePlaceholderStaysUntrusted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	r, err := mission.NewPathRedactor(repo)
	require.NoError(t, err)
	raw := []byte(`{"ts":"2026-05-22T10:00:00Z","session":"s","tool":"Read","tool_input":{"file_path":"~/notes.md","description":"mail bob@example.com"}}`)
	out, changed, err := redactLegacyLine(raw, repo, r)
	require.NoError(t, err)
	require.True(t, changed)
	e, err := decodeAuditLine(out)
	require.NoError(t, err)
	assert.Zero(t, e.PathFormat)

	now := time.Now().UTC()
	dir, err := resolveRepoSessionDir(repo, "s", now)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "audit.jsonl"), append(out, '\n'), 0o600))
	reads, err := loadSessionReads(repo, "s")
	require.NoError(t, err)
	assert.False(t, readsContain(reads, filepath.Join(home, "notes.md"), repo))
}
