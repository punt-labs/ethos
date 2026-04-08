package mission

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// validResult returns a fully-populated Result that passes Validate.
// Tests mutate copies to exercise individual failure modes.
func validResult() Result {
	return Result{
		Mission:    "m-2026-04-07-001",
		Round:      1,
		CreatedAt:  "2026-04-08T08:00:00Z",
		Author:     "bwk",
		Verdict:    VerdictPass,
		Confidence: 0.9,
		FilesChanged: []FileChange{
			{Path: "internal/mission/result.go", Added: 120, Removed: 0},
			{Path: "internal/mission/result_test.go", Added: 60, Removed: 0},
		},
		Evidence: []EvidenceCheck{
			{Name: "go test ./internal/mission/... -race", Status: EvidenceStatusPass},
			{Name: "make check", Status: EvidenceStatusPass},
		},
		OpenQuestions: []string{"Should the helper be promoted to shared code?"},
		Prose:         "Round 1 delivered the typed Result artifact and the close gate.",
	}
}

func TestResult_Validate_HappyPath(t *testing.T) {
	r := validResult()
	require.NoError(t, r.Validate())
}

func TestResult_Validate_NilReceiver(t *testing.T) {
	var r *Result
	err := r.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

func TestResult_Validate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Result)
		wantErr string
	}{
		{
			name:    "missing mission",
			mutate:  func(r *Result) { r.Mission = "" },
			wantErr: "invalid mission",
		},
		{
			name:    "malformed mission ID",
			mutate:  func(r *Result) { r.Mission = "not-a-mission" },
			wantErr: "invalid mission",
		},
		{
			name:    "round zero",
			mutate:  func(r *Result) { r.Round = 0 },
			wantErr: "invalid round 0",
		},
		{
			name:    "round above max",
			mutate:  func(r *Result) { r.Round = 99 },
			wantErr: "invalid round 99",
		},
		{
			name:    "empty author",
			mutate:  func(r *Result) { r.Author = "" },
			wantErr: "author is required",
		},
		{
			name:    "whitespace author",
			mutate:  func(r *Result) { r.Author = "   " },
			wantErr: "author is required",
		},
		{
			name:    "author control character",
			mutate:  func(r *Result) { r.Author = "bwk\nFAKE" },
			wantErr: "author contains control character",
		},
		{
			name:    "missing verdict",
			mutate:  func(r *Result) { r.Verdict = "" },
			wantErr: "invalid verdict",
		},
		{
			name:    "unknown verdict",
			mutate:  func(r *Result) { r.Verdict = "maybe" },
			wantErr: "invalid verdict",
		},
		{
			name:    "confidence below range",
			mutate:  func(r *Result) { r.Confidence = -0.01 },
			wantErr: "invalid confidence",
		},
		{
			name:    "confidence above range",
			mutate:  func(r *Result) { r.Confidence = 1.01 },
			wantErr: "invalid confidence",
		},
		{
			name:    "files_changed absolute path",
			mutate:  func(r *Result) { r.FilesChanged[0].Path = "/etc/passwd" },
			wantErr: "relative path",
		},
		{
			name:    "files_changed traversal",
			mutate:  func(r *Result) { r.FilesChanged[0].Path = "../escape" },
			wantErr: "path traversal",
		},
		{
			name:    "files_changed empty path",
			mutate:  func(r *Result) { r.FilesChanged[0].Path = "" },
			wantErr: "cannot be empty",
		},
		{
			name:    "files_changed control character",
			mutate:  func(r *Result) { r.FilesChanged[0].Path = "foo\nbar" },
			wantErr: "control character",
		},
		{
			name:    "files_changed root claim",
			mutate:  func(r *Result) { r.FilesChanged[0].Path = "." },
			wantErr: "project root",
		},
		{
			name:    "files_changed negative added",
			mutate:  func(r *Result) { r.FilesChanged[0].Added = -1 },
			wantErr: "added -1",
		},
		{
			name:    "files_changed negative removed",
			mutate:  func(r *Result) { r.FilesChanged[0].Removed = -1 },
			wantErr: "removed -1",
		},
		{
			name:    "empty evidence",
			mutate:  func(r *Result) { r.Evidence = nil },
			wantErr: "evidence must contain at least one entry",
		},
		{
			name:    "evidence empty name",
			mutate:  func(r *Result) { r.Evidence[0].Name = "" },
			wantErr: "name cannot be empty",
		},
		{
			name:    "evidence control character in name",
			mutate:  func(r *Result) { r.Evidence[0].Name = "line1\nline2" },
			wantErr: "name contains control character",
		},
		{
			name:    "evidence unknown status",
			mutate:  func(r *Result) { r.Evidence[0].Status = "unknown" },
			wantErr: "invalid status",
		},
		{
			name:    "malformed created_at",
			mutate:  func(r *Result) { r.CreatedAt = "yesterday" },
			wantErr: "invalid created_at",
		},
		{
			name:    "open_questions empty entry",
			mutate:  func(r *Result) { r.OpenQuestions = []string{"valid", ""} },
			wantErr: "open_questions[1]",
		},
		{
			name:    "open_questions control character",
			mutate:  func(r *Result) { r.OpenQuestions = []string{"bad\x00entry"} },
			wantErr: "control character",
		},
		{
			name: "prose control character",
			mutate: func(r *Result) {
				// ESC (0x1B) is not \n, \r, or \t — prose rejects it.
				r.Prose = "normal text\x1bANSI smuggle"
			},
			wantErr: "prose contains control character",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validResult()
			tt.mutate(&r)
			err := r.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestResult_Validate_AllVerdicts asserts every verdict in the closed
// enum passes Validate with an otherwise well-formed result.
func TestResult_Validate_AllVerdicts(t *testing.T) {
	for _, v := range []string{VerdictPass, VerdictFail, VerdictEscalate} {
		t.Run(v, func(t *testing.T) {
			r := validResult()
			r.Verdict = v
			require.NoError(t, r.Validate())
		})
	}
}

// TestResult_Validate_AllEvidenceStatuses asserts every evidence
// status in the closed enum passes Validate.
func TestResult_Validate_AllEvidenceStatuses(t *testing.T) {
	for _, s := range []string{EvidenceStatusPass, EvidenceStatusFail, EvidenceStatusSkip} {
		t.Run(s, func(t *testing.T) {
			r := validResult()
			r.Evidence[0].Status = s
			require.NoError(t, r.Validate())
		})
	}
}

// TestResult_Validate_ConfidenceBoundaries asserts the closed interval
// [0.0, 1.0] boundary values are accepted.
func TestResult_Validate_ConfidenceBoundaries(t *testing.T) {
	for _, c := range []float64{0.0, 0.5, 1.0} {
		r := validResult()
		r.Confidence = c
		require.NoError(t, r.Validate(), "confidence %v must be accepted", c)
	}
}

// TestResult_Validate_EmptyFilesChangedAllowed asserts that a result
// with no file changes is valid. A round that only inspected code
// without writing is a legitimate outcome — the worker verified
// something the code already does correctly.
func TestResult_Validate_EmptyFilesChangedAllowed(t *testing.T) {
	r := validResult()
	r.FilesChanged = nil
	require.NoError(t, r.Validate())
}

// TestResult_Validate_ProseAcceptsMultiLine asserts that prose
// accepts newlines, tabs, and carriage returns. Prose is multi-line
// narrative by design; the control-character check uses the relaxed
// variant.
func TestResult_Validate_ProseAcceptsMultiLine(t *testing.T) {
	r := validResult()
	r.Prose = "Line 1\nLine 2\n\tIndented\r\nCRLF line"
	require.NoError(t, r.Validate())
}

func TestResult_YAMLRoundTrip(t *testing.T) {
	r := validResult()
	data, err := yaml.Marshal(&r)
	require.NoError(t, err)

	parsed, err := DecodeResultStrict(data, "test")
	require.NoError(t, err)

	assert.Equal(t, r.Mission, parsed.Mission)
	assert.Equal(t, r.Round, parsed.Round)
	assert.Equal(t, r.Author, parsed.Author)
	assert.Equal(t, r.Verdict, parsed.Verdict)
	assert.Equal(t, r.Confidence, parsed.Confidence)
	assert.Equal(t, r.FilesChanged, parsed.FilesChanged)
	assert.Equal(t, r.Evidence, parsed.Evidence)
	assert.Equal(t, r.OpenQuestions, parsed.OpenQuestions)
	assert.Equal(t, r.Prose, parsed.Prose)
}

func TestDecodeResultStrict_RejectsUnknownField(t *testing.T) {
	body := []byte(`mission: m-2026-04-07-001
round: 1
author: bwk
verdict: pass
confidence: 0.9
evidence:
  - name: make check
    status: pass
bogus: smuggled
`)
	_, err := DecodeResultStrict(body, "test.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field bogus not found")
	assert.Contains(t, err.Error(), "test.yaml")
}

func TestDecodeResultStrict_RejectsUnknownNestedField(t *testing.T) {
	body := []byte(`mission: m-2026-04-07-001
round: 1
author: bwk
verdict: pass
confidence: 0.9
files_changed:
  - path: internal/foo.go
    added: 1
    removed: 0
    smuggled: gotcha
evidence:
  - name: make check
    status: pass
`)
	_, err := DecodeResultStrict(body, "test.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "smuggled")
}

func TestDecodeResultStrict_RejectsMultipleDocuments(t *testing.T) {
	r := validResult()
	first, err := yaml.Marshal(&r)
	require.NoError(t, err)
	combined := append(first, []byte("---\nmission: m-2026-04-07-002\n")...)
	_, err = DecodeResultStrict(combined, "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple YAML documents")
}

func TestDecodeResultStrict_RejectsTrailingContent(t *testing.T) {
	r := validResult()
	first, err := yaml.Marshal(&r)
	require.NoError(t, err)
	combined := append(first, []byte("---\nextra_scalar\n")...)
	_, err = DecodeResultStrict(combined, "test")
	require.Error(t, err)
	msg := err.Error()
	assert.True(t,
		strings.Contains(msg, "multiple YAML documents") || strings.Contains(msg, "trailing content"),
		"expected multi-doc or trailing-content error, got: %s", msg)
}
