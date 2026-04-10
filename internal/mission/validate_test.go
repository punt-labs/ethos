package mission

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validContract returns a fully-populated Contract that passes Validate.
// Tests mutate copies of this to exercise individual failure modes.
func validContract() Contract {
	return Contract{
		MissionID: "m-2026-04-07-001",
		Status:    StatusOpen,
		CreatedAt: "2026-04-07T21:30:00Z",
		UpdatedAt: "2026-04-07T21:30:00Z",
		Leader:    "claude",
		Worker:    "bwk",
		Evaluator: Evaluator{
			Handle:   "djb",
			PinnedAt: "2026-04-07T21:30:00Z",
		},
		Inputs: Inputs{
			Bead:  "ethos-07m.5",
			Files: []string{"internal/session/store.go"},
		},
		WriteSet:        []string{"internal/mission/", "cmd/ethos/mission.go"},
		Tools:           []string{"Read", "Write", "Edit"},
		SuccessCriteria: []string{"make check passes"},
		Budget: Budget{
			Rounds:              3,
			ReflectionAfterEach: true,
		},
		CurrentRound: 1,
	}
}

func TestValidate_HappyPath(t *testing.T) {
	c := validContract()
	require.NoError(t, c.Validate())
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Contract)
		wantErr string
	}{
		// Rule 1: mission_id pattern
		{
			name:    "rule 1: missing mission_id",
			mutate:  func(c *Contract) { c.MissionID = "" },
			wantErr: "invalid mission_id",
		},
		{
			name:    "rule 1: malformed mission_id",
			mutate:  func(c *Contract) { c.MissionID = "mission-001" },
			wantErr: "invalid mission_id",
		},
		{
			name:    "rule 1: bad date in mission_id",
			mutate:  func(c *Contract) { c.MissionID = "m-26-4-7-001" },
			wantErr: "invalid mission_id",
		},
		{
			name:    "rule 1: short counter in mission_id",
			mutate:  func(c *Contract) { c.MissionID = "m-2026-04-07-1" },
			wantErr: "invalid mission_id",
		},

		// Rule 4: updated_at RFC3339
		{
			name:    "rule 4: empty updated_at",
			mutate:  func(c *Contract) { c.UpdatedAt = "" },
			wantErr: "invalid updated_at",
		},
		{
			name:    "rule 4: malformed updated_at",
			mutate:  func(c *Contract) { c.UpdatedAt = "not-a-time" },
			wantErr: "invalid updated_at",
		},

		// Rule 5: status↔closed_at invariant
		{
			name:    "rule 5: open with closed_at set",
			mutate:  func(c *Contract) { c.ClosedAt = "2026-04-07T22:00:00Z" },
			wantErr: "status is open but closed_at is set",
		},
		{
			name: "rule 5: closed without closed_at",
			mutate: func(c *Contract) {
				c.Status = StatusClosed
			},
			wantErr: "closed_at is empty",
		},
		{
			name: "rule 5: failed without closed_at",
			mutate: func(c *Contract) {
				c.Status = StatusFailed
			},
			wantErr: "closed_at is empty",
		},
		{
			name: "rule 5: closed with malformed closed_at",
			mutate: func(c *Contract) {
				c.Status = StatusClosed
				c.ClosedAt = "not-a-time"
			},
			wantErr: "invalid closed_at",
		},

		// Rule 2: status enum
		{
			name:    "rule 2: empty status",
			mutate:  func(c *Contract) { c.Status = "" },
			wantErr: "invalid status",
		},
		{
			name:    "rule 2: unknown status",
			mutate:  func(c *Contract) { c.Status = "in_progress" },
			wantErr: "invalid status",
		},

		// Rule 3: created_at RFC3339
		{
			name:    "rule 3: empty created_at",
			mutate:  func(c *Contract) { c.CreatedAt = "" },
			wantErr: "invalid created_at",
		},
		{
			name:    "rule 3: malformed created_at",
			mutate:  func(c *Contract) { c.CreatedAt = "yesterday" },
			wantErr: "invalid created_at",
		},
		{
			name:    "rule 3: missing timezone in created_at",
			mutate:  func(c *Contract) { c.CreatedAt = "2026-04-07T21:30:00" },
			wantErr: "invalid created_at",
		},

		// Rule 4: leader required
		{
			name:    "rule 4: empty leader",
			mutate:  func(c *Contract) { c.Leader = "" },
			wantErr: "leader is required",
		},
		{
			name:    "rule 4: whitespace-only leader",
			mutate:  func(c *Contract) { c.Leader = "   " },
			wantErr: "leader is required",
		},

		// Rule 5: worker required
		{
			name:    "rule 5: empty worker",
			mutate:  func(c *Contract) { c.Worker = "" },
			wantErr: "worker is required",
		},

		// Rule 6: evaluator.handle required
		{
			name:    "rule 6: empty evaluator handle",
			mutate:  func(c *Contract) { c.Evaluator.Handle = "" },
			wantErr: "evaluator.handle is required",
		},

		// Rule 7: evaluator.pinned_at RFC3339
		{
			name:    "rule 7: empty evaluator pinned_at",
			mutate:  func(c *Contract) { c.Evaluator.PinnedAt = "" },
			wantErr: "invalid evaluator.pinned_at",
		},
		{
			name:    "rule 7: malformed evaluator pinned_at",
			mutate:  func(c *Contract) { c.Evaluator.PinnedAt = "now" },
			wantErr: "invalid evaluator.pinned_at",
		},

		// Rule 8: write_set non-empty + path validation
		{
			name:    "rule 8: empty write_set",
			mutate:  func(c *Contract) { c.WriteSet = nil },
			wantErr: "write_set must contain at least one entry",
		},

		// Rule 9: budget.rounds in [1, 10]
		{
			name:    "rule 9: zero rounds",
			mutate:  func(c *Contract) { c.Budget.Rounds = 0 },
			wantErr: "budget.rounds 0 out of range",
		},
		{
			name:    "rule 9: negative rounds",
			mutate:  func(c *Contract) { c.Budget.Rounds = -1 },
			wantErr: "budget.rounds -1 out of range",
		},
		{
			name:    "rule 9: rounds above max",
			mutate:  func(c *Contract) { c.Budget.Rounds = 11 },
			wantErr: "budget.rounds 11 out of range",
		},

		// Rule 10: success_criteria non-empty
		{
			name:    "rule 10: empty success_criteria",
			mutate:  func(c *Contract) { c.SuccessCriteria = nil },
			wantErr: "success_criteria must contain at least one entry",
		},

		// Rule 13: current_round in [1, budget.rounds]. Store.Create
		// and Store.loadLocked rewrite CurrentRound == 0 to 1 before
		// calling Validate, but a caller that builds a Contract
		// directly (MCP tests, hand-rolled fixtures) bypasses those
		// paths and must still see the range rejection.
		{
			name:    "rule 13: current_round zero",
			mutate:  func(c *Contract) { c.CurrentRound = 0 },
			wantErr: "current_round 0 out of range",
		},
		{
			name: "rule 13: current_round exceeds budget",
			mutate: func(c *Contract) {
				c.CurrentRound = 4
				c.Budget.Rounds = 3
			},
			wantErr: "current_round 4 out of range",
		},
		{
			name:    "rule 13: current_round above max budget",
			mutate:  func(c *Contract) { c.CurrentRound = 11 },
			wantErr: "current_round 11 out of range",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validContract()
			tt.mutate(&c)
			err := c.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestValidate_RejectsPathTraversal asserts that the write_set rejects
// every documented path-traversal form. This is a security boundary —
// the contract is the trust point, so the rule is enforced at parse time.
func TestValidate_RejectsPathTraversal(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		wantErrMatch string
	}{
		{name: "parent escape", path: "../etc/passwd", wantErrMatch: "path traversal"},
		{name: "embedded parent escape", path: "internal/../../../tmp", wantErrMatch: "path traversal"},
		{name: "absolute unix path", path: "/etc/passwd", wantErrMatch: "relative path"},
		{name: "empty entry", path: "", wantErrMatch: "empty or whitespace"},
		{name: "whitespace-only entry", path: "   ", wantErrMatch: "empty or whitespace"},
		{name: "single dot dot", path: "..", wantErrMatch: "path traversal"},
		{name: "trailing parent", path: "internal/..", wantErrMatch: "path traversal"},
		{name: "backslash parent (windows form)", path: `internal\..\..\tmp`, wantErrMatch: "path traversal"},
		{name: "null byte", path: "internal/foo\x00/bar", wantErrMatch: "null byte"},
		{name: "null byte truncation", path: "allowed\x00../etc/passwd", wantErrMatch: "null byte"},
		{name: "newline", path: "internal/foo\nbar", wantErrMatch: "control character"},
		{name: "carriage return", path: "internal/foo\rbar", wantErrMatch: "control character"},
		{name: "escape", path: "internal/foo\x1bbar", wantErrMatch: "control character"},
		{name: "tab", path: "internal/foo\tbar", wantErrMatch: "control character"},
		{name: "DEL", path: "internal/foo\x7fbar", wantErrMatch: "control character"},
		{name: "windows drive letter upper", path: `C:\foo`, wantErrMatch: "drive letter"},
		{name: "windows drive letter lower", path: `d:\foo`, wantErrMatch: "drive letter"},
		{name: "windows drive letter with forward slash", path: "E:/foo", wantErrMatch: "drive letter"},
		{name: "UNC path backslash", path: `\\server\share\file`, wantErrMatch: "relative path"},
		{name: "UNC path forward slash", path: "//server/share/file", wantErrMatch: "relative path"},
		// Root-claim rejection: a write_set entry that normalizes to
		// "the project root" (only `.` segments and slashes) would
		// silently bypass the conflict check because pathsOverlap
		// against an empty segment list returns false. Bugbot caught
		// this on PR #178; the per-entry validator now blocks the
		// input upstream so the conflict check never sees it.
		{name: "rejects lone dot", path: ".", wantErrMatch: "claims the project root"},
		{name: "rejects dot with trailing slash", path: "./", wantErrMatch: "claims the project root"},
		{name: "rejects dot-slash-dot", path: "./.", wantErrMatch: "claims the project root"},
		{name: "rejects multiple dot segments", path: "././", wantErrMatch: "claims the project root"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validContract()
			c.WriteSet = []string{tt.path}
			err := c.Validate()
			require.Error(t, err, "expected validation error for path %q", tt.path)
			assert.Contains(t, err.Error(), tt.wantErrMatch)
		})
	}
}

// TestValidate_RejectsControlCharsInHandles asserts that Leader,
// Worker, and Evaluator.Handle reject any value containing a C0
// control character. json.Marshal escapes control characters inside
// strings, so a newline in a handle doesn't literally forge a new
// JSONL log line; the real concerns are terminal/CLI injection via
// ANSI escape sequences in display output and downstream tools that
// don't unescape JSON. Reject at the trust boundary regardless.
func TestValidate_RejectsControlCharsInHandles(t *testing.T) {
	tests := []struct {
		name   string
		field  string // for error message context
		mutate func(*Contract)
	}{
		// Leader
		{name: "leader newline", field: "leader", mutate: func(c *Contract) { c.Leader = "claude\nFAKE" }},
		{name: "leader carriage return", field: "leader", mutate: func(c *Contract) { c.Leader = "claude\rFAKE" }},
		{name: "leader escape", field: "leader", mutate: func(c *Contract) { c.Leader = "claude\x1bFAKE" }},
		{name: "leader tab", field: "leader", mutate: func(c *Contract) { c.Leader = "claude\tFAKE" }},
		{name: "leader null byte", field: "leader", mutate: func(c *Contract) { c.Leader = "claude\x00FAKE" }},
		{name: "leader DEL", field: "leader", mutate: func(c *Contract) { c.Leader = "claude\x7fFAKE" }},

		// Worker
		{name: "worker newline", field: "worker", mutate: func(c *Contract) { c.Worker = "bwk\nFAKE" }},
		{name: "worker escape", field: "worker", mutate: func(c *Contract) { c.Worker = "bwk\x1bFAKE" }},

		// Evaluator.Handle
		{name: "evaluator newline", field: "evaluator.handle", mutate: func(c *Contract) { c.Evaluator.Handle = "djb\nFAKE" }},
		{name: "evaluator escape", field: "evaluator.handle", mutate: func(c *Contract) { c.Evaluator.Handle = "djb\x1bFAKE" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validContract()
			tt.mutate(&c)
			err := c.Validate()
			require.Error(t, err, "expected validation error for %s", tt.name)
			assert.Contains(t, err.Error(), tt.field+" contains control character")
		})
	}
}

// TestValidate_WriteSetErrorsNameTheField asserts the M2 parity
// fix: the Contract.Validate path still names "write_set entry" in
// the error so the operator can locate the field they can fix. The
// result validator names "files_changed" instead — the two fields
// share one helper but surface different contexts.
func TestValidate_WriteSetErrorsNameTheField(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"traversal", "../etc/passwd"},
		{"absolute", "/etc/passwd"},
		{"null byte", "internal/\x00bad"},
		{"control character", "internal/\nbad"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validContract()
			c.WriteSet = []string{tc.path}
			err := c.Validate()
			require.Error(t, err)
			msg := err.Error()
			assert.Contains(t, msg, "write_set entry",
				"Contract.Validate must name write_set entry, got: %s", msg)
			assert.NotContains(t, msg, "files_changed",
				"Contract.Validate must not name files_changed, got: %s", msg)
		})
	}
}

// TestValidate_AcceptsSingleDotSegment asserts that `./foo` is treated
// as legitimate path syntax, not as path traversal. Single-dot segments
// are a common shell convention and do not escape the base directory.
// The reviewer suggested rejecting them; this was overridden — rejecting
// single-dot would produce false positives on legitimate paths.
func TestValidate_AcceptsSingleDotSegment(t *testing.T) {
	tests := []string{
		"./internal/mission/",
		"internal/./mission/",
		"./cmd/ethos/mission.go",
	}
	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			c := validContract()
			c.WriteSet = []string{path}
			require.NoError(t, c.Validate())
		})
	}
}

// TestValidate_RejectsZeroWidthUnicode asserts that write_set entries
// containing invisible zero-width Unicode characters are rejected.
// These runes are invisible in most terminals and editors, making them
// a vector for path confusion: two paths that appear identical to the
// operator but differ by a BOM or ZWSP would be treated as distinct
// by the filesystem. Reject at the trust boundary.
func TestValidate_RejectsZeroWidthUnicode(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "BOM prefix", path: "\uFEFFinternal/foo"},
		{name: "ZWSP embedded", path: "internal/\u200Bfoo"},
		{name: "ZWNJ embedded", path: "internal/\u200Cfoo"},
		{name: "ZWJ embedded", path: "internal/\u200Dfoo"},
		{name: "word joiner embedded", path: "internal/\u2060foo"},
		{name: "noncharacter FFFE embedded", path: "internal/\uFFFEfoo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validContract()
			c.WriteSet = []string{tt.path}
			err := c.Validate()
			require.Error(t, err, "expected validation error for path with zero-width char")
			assert.Contains(t, err.Error(), "zero-width Unicode character")
		})
	}
}

// TestValidate_ZeroWidthNoFalsePositive asserts that clean paths
// containing ordinary Unicode are not rejected by the zero-width check.
func TestValidate_ZeroWidthNoFalsePositive(t *testing.T) {
	paths := []string{
		"internal/mission/",
		"cmd/ethos/main.go",
		"docs/日本語/readme.md",
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			c := validContract()
			c.WriteSet = []string{p}
			require.NoError(t, c.Validate())
		})
	}
}

func TestValidate_NilContract(t *testing.T) {
	var c *Contract
	require.Error(t, c.Validate())
}
