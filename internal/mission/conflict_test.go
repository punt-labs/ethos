//go:build !windows

package mission

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPathsOverlap covers the segment-prefix overlap rule. Each row
// describes a single comparison; symmetric cases (forward + reverse
// prefix) are tested explicitly so a unidirectional bug surfaces.
func TestPathsOverlap(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{
			name: "exact match",
			a:    "internal/foo/bar.go",
			b:    "internal/foo/bar.go",
			want: true,
		},
		{
			name: "forward prefix (A is ancestor of B)",
			a:    "internal/foo",
			b:    "internal/foo/bar.go",
			want: true,
		},
		{
			name: "reverse prefix (B is ancestor of A)",
			a:    "internal/foo/bar.go",
			b:    "internal/foo",
			want: true,
		},
		{
			name: "sibling (no overlap)",
			a:    "internal/foo",
			b:    "internal/bar",
			want: false,
		},
		{
			name: "substring of segment is not a prefix",
			a:    "internal/foo",
			b:    "internal/foobar",
			want: false,
		},
		{
			name: "trailing slash equivalence",
			a:    "internal/foo/",
			b:    "internal/foo",
			want: true,
		},
		{
			name: "trailing slash on both sides",
			a:    "internal/foo/",
			b:    "internal/foo/",
			want: true,
		},
		{
			name: "whitespace trimming equivalence",
			a:    "  internal/foo  ",
			b:    "internal/foo",
			want: true,
		},
		{
			name: "backslash normalization",
			a:    `internal\foo`,
			b:    "internal/foo",
			want: true,
		},
		{
			name: "case sensitivity (Linux)",
			a:    "Internal/foo",
			b:    "internal/foo",
			want: false,
		},
		{
			name: "empty path on left",
			a:    "",
			b:    "internal/foo",
			want: false,
		},
		{
			name: "empty path on right",
			a:    "internal/foo",
			b:    "",
			want: false,
		},
		{
			name: "both empty",
			a:    "",
			b:    "",
			want: false,
		},
		{
			name: "whitespace only on left",
			a:    "   ",
			b:    "internal/foo",
			want: false,
		},
		{
			name: "deep forward prefix",
			a:    "internal",
			b:    "internal/mission/store.go",
			want: true,
		},
		{
			name: "single segment match",
			a:    "Makefile",
			b:    "Makefile",
			want: true,
		},
		{
			name: "single segment vs nested",
			a:    "cmd",
			b:    "cmd/ethos/mission.go",
			want: true,
		},
		{
			name: "double slash in middle equivalent to single",
			a:    "internal//foo",
			b:    "internal/foo",
			want: true,
		},
		{
			name: "double slash forward prefix",
			a:    "internal/foo",
			b:    "internal//foo/bar.go",
			want: true,
		},
		{
			// `///` collapses to nil after trim+filter, so it matches
			// nothing — same convention as an empty input.
			name: "triple slash collapses to nil",
			a:    "///",
			b:    "internal/foo",
			want: false,
		},
		{
			// The per-entry validator rejects leading slashes upstream,
			// so this case cannot reach the conflict check in
			// production. The row locks the helper's behavior in case
			// the validator's coverage ever changes.
			name: "leading slash filtered",
			a:    "/internal/foo",
			b:    "internal/foo",
			want: true,
		},
		{
			name: "leading dot segment equivalent",
			a:    "./internal/foo",
			b:    "internal/foo",
			want: true,
		},
		{
			name: "interior dot segment equivalent",
			a:    "internal/./foo",
			b:    "internal/foo",
			want: true,
		},
		{
			name: "trailing dot segment equivalent",
			a:    "internal/foo/.",
			b:    "internal/foo",
			want: true,
		},
		{
			name: "multiple dot segments collapse",
			a:    "./internal/./foo/./bar",
			b:    "internal/foo/bar",
			want: true,
		},
		{
			// The per-entry validator rejects lone-dot and other
			// root-equivalent entries upstream (Bugbot finding on
			// PR #178), so this case cannot reach the conflict check
			// in production. The row locks the helper's behavior in
			// case the validator's coverage ever changes.
			name: "lone dot path matches nothing (validator rejects upstream)",
			a:    ".",
			b:    "internal/foo",
			want: false,
		},
		{
			name: "dot-only path collapses to nil",
			a:    "./",
			b:    "internal/foo",
			want: false,
		},
		{
			name: "dot segment forward prefix",
			a:    "./internal/foo",
			b:    "internal/foo/bar.go",
			want: true,
		},
		{
			name: "dot mixed with double slash",
			a:    "internal/.//foo",
			b:    "internal/foo",
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pathsOverlap(tt.a, tt.b),
				"pathsOverlap(%q, %q)", tt.a, tt.b)
		})
	}
}

// TestPathContainedBy covers the asymmetric segment-prefix helper
// Phase 3.6 uses for result containment. Unlike pathsOverlap, this
// helper answers "is file inside entry?" — a directional subset
// check that refuses a parent-prefix even if the two paths share
// segments at the front.
//
// Round 2 of Phase 3.6 added this helper after all four reviewers
// flagged the H1 bug: the symmetric pathsOverlap helper accepted a
// result claiming a parent directory of a write_set file entry.
func TestPathContainedBy(t *testing.T) {
	tests := []struct {
		name  string
		file  string
		entry string
		want  bool
	}{
		{
			name:  "exact match file",
			file:  "cmd/ethos/mission.go",
			entry: "cmd/ethos/mission.go",
			want:  true,
		},
		{
			name:  "exact match directory",
			file:  "internal/mission",
			entry: "internal/mission",
			want:  true,
		},
		{
			name:  "file inside directory entry",
			file:  "internal/mission/store.go",
			entry: "internal/mission",
			want:  true,
		},
		{
			name:  "file inside directory entry with trailing slash",
			file:  "internal/mission/store.go",
			entry: "internal/mission/",
			want:  true,
		},
		{
			// The H1 exploit: result claims a strict parent of a
			// file entry. Must be rejected. The symmetric helper
			// pathsOverlap returns true here; pathContainedBy
			// returns false because the file has fewer segments
			// than the entry.
			name:  "parent of file entry (H1 exploit)",
			file:  "cmd/ethos",
			entry: "cmd/ethos/mission.go",
			want:  false,
		},
		{
			// Top-level ancestor of a file entry.
			name:  "top-level ancestor of file entry",
			file:  "cmd",
			entry: "cmd/ethos/mission.go",
			want:  false,
		},
		{
			// Top-level ancestor of a directory entry.
			name:  "top-level ancestor of directory entry",
			file:  "internal",
			entry: "internal/mission",
			want:  false,
		},
		{
			// Parent of a directory entry.
			name:  "parent of directory entry",
			file:  "internal/mission",
			entry: "internal/mission/store.go",
			want:  false,
		},
		{
			name:  "sibling at same depth",
			file:  "internal/mission",
			entry: "internal/session",
			want:  false,
		},
		{
			name:  "substring of segment is not a prefix",
			file:  "internal/foobar",
			entry: "internal/foo",
			want:  false,
		},
		{
			// Subdirectory file inside a directory entry.
			name:  "deep file inside directory entry",
			file:  "internal/mission/sub/pkg/file.go",
			entry: "internal/mission/",
			want:  true,
		},
		{
			// Dot segment normalization equivalence.
			name:  "dot segment equivalence",
			file:  "./internal/mission/result.go",
			entry: "internal/mission",
			want:  true,
		},
		{
			// Empty segment lists match nothing — the per-entry
			// validator rejects these upstream but the helper
			// behavior is locked explicitly.
			name:  "empty file",
			file:  "",
			entry: "internal/mission",
			want:  false,
		},
		{
			name:  "empty entry",
			file:  "internal/mission/store.go",
			entry: "",
			want:  false,
		},
		{
			name:  "case sensitivity",
			file:  "Internal/mission",
			entry: "internal/mission",
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pathContainedBy(tt.file, tt.entry),
				"pathContainedBy(%q, %q)", tt.file, tt.entry)
		})
	}
}

// TestFindWriteSetConflicts exercises the cross-mission conflict
// detector against a list of existing contracts. Each row sets up a
// new contract's write_set plus zero or more existing contracts and
// asserts the returned []Conflict.
func TestFindWriteSetConflicts(t *testing.T) {
	makeContract := func(id, worker string, writeSet ...string) *Contract {
		return &Contract{
			MissionID: id,
			Worker:    worker,
			WriteSet:  writeSet,
		}
	}

	tests := []struct {
		name     string
		newSet   []string
		existing []*Contract
		want     []Conflict
	}{
		{
			name:     "empty existing returns nil",
			newSet:   []string{"internal/foo"},
			existing: nil,
			want:     nil,
		},
		{
			name:     "empty new write_set returns nil",
			newSet:   nil,
			existing: []*Contract{makeContract("m-2026-04-08-001", "bwk", "internal/foo")},
			want:     nil,
		},
		{
			name:   "no conflict with disjoint paths",
			newSet: []string{"cmd/ethos/serve.go"},
			existing: []*Contract{
				makeContract("m-2026-04-08-001", "bwk", "internal/foo/bar.go"),
			},
			want: nil,
		},
		{
			name:   "exact match conflict",
			newSet: []string{"internal/mission/store.go"},
			existing: []*Contract{
				makeContract("m-2026-04-08-001", "bwk", "internal/mission/store.go"),
			},
			want: []Conflict{
				{
					MissionID: "m-2026-04-08-001",
					Worker:    "bwk",
					Paths:     []string{"internal/mission/store.go"},
				},
			},
		},
		{
			name:   "forward prefix conflict",
			newSet: []string{"internal/mission/store.go"},
			existing: []*Contract{
				makeContract("m-2026-04-08-001", "bwk", "internal/mission/"),
			},
			want: []Conflict{
				{
					MissionID: "m-2026-04-08-001",
					Worker:    "bwk",
					Paths:     []string{"internal/mission/store.go"},
				},
			},
		},
		{
			name:   "reverse prefix conflict",
			newSet: []string{"internal/mission/"},
			existing: []*Contract{
				makeContract("m-2026-04-08-001", "bwk", "internal/mission/store.go"),
			},
			want: []Conflict{
				{
					MissionID: "m-2026-04-08-001",
					Worker:    "bwk",
					Paths:     []string{"internal/mission/"},
				},
			},
		},
		{
			name:   "multi-conflict across two existing missions, sorted by ID",
			newSet: []string{"internal/foo/bar.go", "cmd/ethos/serve.go"},
			existing: []*Contract{
				// Deliberately out of order to exercise the sort.
				makeContract("m-2026-04-08-003", "rmh", "cmd/ethos/"),
				makeContract("m-2026-04-08-002", "bwk", "internal/foo/"),
			},
			want: []Conflict{
				{
					MissionID: "m-2026-04-08-002",
					Worker:    "bwk",
					Paths:     []string{"internal/foo/bar.go"},
				},
				{
					MissionID: "m-2026-04-08-003",
					Worker:    "rmh",
					Paths:     []string{"cmd/ethos/serve.go"},
				},
			},
		},
		{
			name:   "single existing mission, multiple overlapping paths sorted",
			newSet: []string{"internal/mission/store.go", "internal/mission/log.go"},
			existing: []*Contract{
				makeContract("m-2026-04-08-001", "bwk", "internal/mission/"),
			},
			want: []Conflict{
				{
					MissionID: "m-2026-04-08-001",
					Worker:    "bwk",
					Paths: []string{
						"internal/mission/log.go",
						"internal/mission/store.go",
					},
				},
			},
		},
		{
			name:   "duplicate overlapping paths in new set are deduplicated",
			newSet: []string{"internal/mission/store.go", "internal/mission/store.go"},
			existing: []*Contract{
				makeContract("m-2026-04-08-001", "bwk", "internal/mission/store.go"),
			},
			want: []Conflict{
				{
					MissionID: "m-2026-04-08-001",
					Worker:    "bwk",
					Paths:     []string{"internal/mission/store.go"},
				},
			},
		},
		{
			name:   "sibling paths produce no conflict",
			newSet: []string{"internal/foo"},
			existing: []*Contract{
				makeContract("m-2026-04-08-001", "bwk", "internal/foobar"),
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findWriteSetConflicts(tt.newSet, tt.existing)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestFormatConflictError asserts the operator-facing error string is
// stable, deterministic, and includes mission ID, worker, and the
// overlapping paths for every conflict — one line per blocker.
func TestFormatConflictError(t *testing.T) {
	t.Run("single conflict", func(t *testing.T) {
		conflicts := []Conflict{
			{
				MissionID: "m-2026-04-07-002",
				Worker:    "bwk",
				Paths:     []string{"internal/mission/store.go"},
			},
		}
		err := formatConflictError(conflicts)
		want := "write_set conflict with mission m-2026-04-07-002 (worker: bwk): overlapping paths [internal/mission/store.go]"
		assert.EqualError(t, err, want)
	})

	t.Run("multi conflict embeds newline between blockers", func(t *testing.T) {
		conflicts := []Conflict{
			{
				MissionID: "m-2026-04-07-002",
				Worker:    "bwk",
				Paths:     []string{"internal/mission/store.go"},
			},
			{
				MissionID: "m-2026-04-07-003",
				Worker:    "rmh",
				Paths:     []string{"cmd/ethos/mission.go", "cmd/ethos/serve.go"},
			},
		}
		err := formatConflictError(conflicts)
		msg := err.Error()
		lines := strings.Split(msg, "\n")
		if assert.Len(t, lines, 2, "multi-conflict error must be one line per blocker") {
			assert.Equal(t,
				"write_set conflict with mission m-2026-04-07-002 (worker: bwk): overlapping paths [internal/mission/store.go]",
				lines[0])
			assert.Equal(t,
				"write_set conflict with mission m-2026-04-07-003 (worker: rmh): overlapping paths [cmd/ethos/mission.go cmd/ethos/serve.go]",
				lines[1])
		}
	})

	t.Run("nil returns nil error", func(t *testing.T) {
		assert.NoError(t, formatConflictError(nil))
	})

	t.Run("empty returns nil error", func(t *testing.T) {
		assert.NoError(t, formatConflictError([]Conflict{}))
	})
}
