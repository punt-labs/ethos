package mission

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// validReflection returns a fully-populated Reflection that passes
// Validate. Tests mutate copies to exercise individual failure modes.
func validReflection() Reflection {
	return Reflection{
		Round:          1,
		CreatedAt:      "2026-04-08T08:00:00Z",
		Author:         "claude",
		Converging:     true,
		Signals:        []string{"all tests passing", "no new lint findings"},
		Recommendation: RecommendationContinue,
		Reason:         "round 1 finished cleanly; round 2 will tackle the integration tests",
	}
}

func TestReflection_Validate_HappyPath(t *testing.T) {
	r := validReflection()
	require.NoError(t, r.Validate())
}

func TestReflection_Validate_NilReceiver(t *testing.T) {
	var r *Reflection
	err := r.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

func TestReflection_Validate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Reflection)
		wantErr string
	}{
		{
			name:    "round zero",
			mutate:  func(r *Reflection) { r.Round = 0 },
			wantErr: "invalid round 0",
		},
		{
			name:    "round negative",
			mutate:  func(r *Reflection) { r.Round = -1 },
			wantErr: "invalid round -1",
		},
		{
			name:    "round above max",
			mutate:  func(r *Reflection) { r.Round = 11 },
			wantErr: "invalid round 11",
		},
		{
			name:    "empty author",
			mutate:  func(r *Reflection) { r.Author = "" },
			wantErr: "author is required",
		},
		{
			name:    "whitespace author",
			mutate:  func(r *Reflection) { r.Author = "   " },
			wantErr: "author is required",
		},
		{
			name:    "author control character",
			mutate:  func(r *Reflection) { r.Author = "claude\nFAKE" },
			wantErr: "author contains control character",
		},
		{
			name:    "unknown recommendation",
			mutate:  func(r *Reflection) { r.Recommendation = "yolo" },
			wantErr: "invalid recommendation",
		},
		{
			name:    "empty recommendation",
			mutate:  func(r *Reflection) { r.Recommendation = "" },
			wantErr: "invalid recommendation",
		},
		{
			name:    "no signals",
			mutate:  func(r *Reflection) { r.Signals = nil },
			wantErr: "signals must contain at least one entry",
		},
		{
			name:    "empty signal",
			mutate:  func(r *Reflection) { r.Signals = []string{"valid", ""} },
			wantErr: "signals[1]",
		},
		{
			name:    "whitespace signal",
			mutate:  func(r *Reflection) { r.Signals = []string{"  "} },
			wantErr: "empty or whitespace",
		},
		{
			name:    "signal control character",
			mutate:  func(r *Reflection) { r.Signals = []string{"line1\nline2"} },
			wantErr: "control character",
		},
		{
			name: "stop without reason",
			mutate: func(r *Reflection) {
				r.Recommendation = RecommendationStop
				r.Reason = ""
			},
			wantErr: `reason is required when recommendation is "stop"`,
		},
		{
			name: "escalate without reason",
			mutate: func(r *Reflection) {
				r.Recommendation = RecommendationEscalate
				r.Reason = "   "
			},
			wantErr: `reason is required when recommendation is "escalate"`,
		},
		{
			name:    "malformed created_at",
			mutate:  func(r *Reflection) { r.CreatedAt = "yesterday" },
			wantErr: "invalid created_at",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := validReflection()
			tt.mutate(&r)
			err := r.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestReflection_Validate_AcceptsContinueWithoutReason asserts that
// the reason-required rule fires only for terminal recommendations.
// Continue and pivot are advance-permitting; the leader can leave
// the reason empty if the signals are self-explanatory.
func TestReflection_Validate_AcceptsContinueWithoutReason(t *testing.T) {
	r := validReflection()
	r.Recommendation = RecommendationContinue
	r.Reason = ""
	require.NoError(t, r.Validate())

	r.Recommendation = RecommendationPivot
	require.NoError(t, r.Validate())
}

// TestReflection_Validate_AcceptsAllRecommendations confirms the
// happy path for every enum value (with a reason where required).
func TestReflection_Validate_AcceptsAllRecommendations(t *testing.T) {
	for _, rec := range []string{
		RecommendationContinue,
		RecommendationPivot,
		RecommendationStop,
		RecommendationEscalate,
	} {
		t.Run(rec, func(t *testing.T) {
			r := validReflection()
			r.Recommendation = rec
			r.Reason = "explicit reason for the test"
			require.NoError(t, r.Validate())
		})
	}
}

// TestReflection_YAMLRoundTrip asserts that a valid reflection
// marshals to YAML and parses back via DecodeReflectionStrict to a
// byte-equivalent struct. This is success criterion 1: a well-formed
// reflection round-trips through YAML.
func TestReflection_YAMLRoundTrip(t *testing.T) {
	r := validReflection()
	data, err := yaml.Marshal(&r)
	require.NoError(t, err)

	parsed, err := DecodeReflectionStrict(data, "test")
	require.NoError(t, err)

	assert.Equal(t, r.Round, parsed.Round)
	assert.Equal(t, r.CreatedAt, parsed.CreatedAt)
	assert.Equal(t, r.Author, parsed.Author)
	assert.Equal(t, r.Converging, parsed.Converging)
	assert.Equal(t, r.Signals, parsed.Signals)
	assert.Equal(t, r.Recommendation, parsed.Recommendation)
	assert.Equal(t, r.Reason, parsed.Reason)
}

func TestDecodeReflectionStrict_RejectsUnknownField(t *testing.T) {
	body := []byte(`round: 1
author: claude
converging: true
signals:
  - first signal
recommendation: continue
reason: ok
bogus: smuggled
`)
	_, err := DecodeReflectionStrict(body, "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field bogus not found")
}

func TestDecodeReflectionStrict_RejectsMultipleDocuments(t *testing.T) {
	r := validReflection()
	first, err := yaml.Marshal(&r)
	require.NoError(t, err)
	combined := append(first, []byte("---\nround: 2\n")...)
	_, err = DecodeReflectionStrict(combined, "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple YAML documents")
}

func TestDecodeReflectionStrict_RejectsTrailingContent(t *testing.T) {
	r := validReflection()
	first, err := yaml.Marshal(&r)
	require.NoError(t, err)
	combined := append(first, []byte("---\nextra_scalar\n")...)
	_, err = DecodeReflectionStrict(combined, "test")
	require.Error(t, err)
	msg := err.Error()
	assert.True(t,
		strings.Contains(msg, "multiple YAML documents") || strings.Contains(msg, "trailing content"),
		"expected multi-doc or trailing-content error, got: %s", msg)
}

// TestIsTerminalRecommendation asserts the enum partition: stop and
// escalate are advance-blocking; continue and pivot are not.
func TestIsTerminalRecommendation(t *testing.T) {
	assert.True(t, IsTerminalRecommendation(RecommendationStop))
	assert.True(t, IsTerminalRecommendation(RecommendationEscalate))
	assert.False(t, IsTerminalRecommendation(RecommendationContinue))
	assert.False(t, IsTerminalRecommendation(RecommendationPivot))
	assert.False(t, IsTerminalRecommendation(""))
	assert.False(t, IsTerminalRecommendation("yolo"))
}
