package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/internal/mission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	entry := auditEntry{
		Ts:        now.Format(time.RFC3339),
		Session:   sessionID,
		Tool:      "Read",
		ToolInput: map[string]any{"file_path": readPath},
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
// precedence over the fallback.
func TestEnvRepoRoot_PrefersEnvVar(t *testing.T) {
	t.Setenv("ETHOS_REPO_ROOT", "/explicit/root")
	got := envRepoRoot()
	assert.Equal(t, "/explicit/root", got)
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
