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
		// --- Glob entries (ethos-qy7k round 2). Containment became
		// glob-aware; overlap has to follow or two missions can claim
		// the same tree through a glob and both be admitted.
		{
			name: "doublestar entry overlaps a file inside its root",
			a:    "docs/**",
			b:    "docs/a.md",
			want: true,
		},
		{
			name: "file inside a doublestar root overlaps it (symmetric)",
			a:    "docs/a.md",
			b:    "docs/**",
			want: true,
		},
		{
			name: "doublestar entry overlaps the directory it globs",
			a:    "docs/**",
			b:    "docs/",
			want: true,
		},
		{
			name: "two doublestar entries under one root overlap",
			a:    "docs/**",
			b:    "docs/**/adr",
			want: true,
		},
		{
			name: "doublestar entry does not overlap a sibling tree",
			a:    "docs/**",
			b:    "internal/mission",
			want: false,
		},
		{
			name: "single star entry overlaps its literal prefix",
			a:    "internal/mission/*.go",
			b:    "internal/mission/store.go",
			want: true,
		},
		{
			name: "single star entry does not overlap a sibling package",
			a:    "internal/mission/*.go",
			b:    "internal/hook",
			want: false,
		},
		{
			// A first-segment glob can match at the root, so it
			// claims everything. It must not read as "matches
			// nothing" the way an empty entry does.
			name: "leading glob claims the root and overlaps anything",
			a:    "*.go",
			b:    "internal/mission/store.go",
			want: true,
		},
		{
			name: "leading doublestar claims the root and overlaps anything",
			a:    "**/store.go",
			b:    "cmd/ethos",
			want: true,
		},
		{
			name: "leading glob against an empty entry still matches nothing",
			a:    "*.go",
			b:    "",
			want: false,
		},
		// Brackets are literal here too: a bracketed segment is not a
		// pattern, so it is compared, not truncated away.
		{
			name: "bracketed file names overlap only themselves",
			a:    "docs/[draft].md",
			b:    "docs/[draft].md",
			want: true,
		},
		{
			name: "a bracketed file does not overlap a character-class expansion",
			a:    "docs/[draft].md",
			b:    "docs/d.md",
			want: false,
		},
		{
			name: "a bracketed directory overlaps its children",
			a:    "app/[id]",
			b:    "app/[id]/page.tsx",
			want: true,
		},
		{
			name: "bracketed siblings do not overlap",
			a:    "app/[id]",
			b:    "app/[slug]",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pathsOverlap(tt.a, tt.b),
				"pathsOverlap(%q, %q)", tt.a, tt.b)
			// The relation is symmetric by contract; every case must
			// hold in both directions.
			assert.Equal(t, tt.want, pathsOverlap(tt.b, tt.a),
				"pathsOverlap(%q, %q) [reversed]", tt.b, tt.a)
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
		{
			// ethos-qy7k: a doublestar entry claims everything under
			// the directory. The literal comparison read `**` as a
			// directory name and refused every real path.
			name:  "doublestar entry contains a file one level down",
			file:  "docs/audited-delegation.md",
			entry: "docs/**",
			want:  true,
		},
		{
			name:  "doublestar entry contains a file many levels down",
			file:  "docs/design/adr/des-054.md",
			entry: "docs/**",
			want:  true,
		},
		{
			name:  "doublestar entry does not contain a sibling tree",
			file:  "internal/mission/store.go",
			entry: "docs/**",
			want:  false,
		},
		{
			name:  "doublestar in the middle spans intermediate segments",
			file:  "internal/mission/sub/pkg/store.go",
			entry: "internal/**/store.go",
			want:  true,
		},
		{
			name:  "doublestar in the middle still requires the tail to match",
			file:  "internal/mission/sub/pkg/result.go",
			entry: "internal/**/store.go",
			want:  false,
		},
		{
			name:  "single star matches within one segment only",
			file:  "internal/mission/store.go",
			entry: "internal/*",
			want:  true,
		},
		{
			name:  "single star does not match a path separator",
			file:  "internal/mission/store.go",
			entry: "*.go",
			want:  false,
		},
		{
			name:  "star suffix matches an extension within a segment",
			file:  "internal/mission/store.go",
			entry: "internal/mission/*.go",
			want:  true,
		},
		{
			name:  "star suffix rejects a different extension",
			file:  "internal/mission/store.yaml",
			entry: "internal/mission/*.go",
			want:  false,
		},
		{
			// A glob entry must not resurrect the parent-claim
			// exploit: the file still has to reach the entry's tail.
			name:  "parent of a glob entry is still refused",
			file:  "internal",
			entry: "internal/*/store.go",
			want:  false,
		},
		{
			// An unclosed bracket is not a valid pattern; path.Match
			// reports ErrBadPattern. The validator accepts the entry,
			// so it must still match the file it literally names.
			name:  "malformed pattern compares literally",
			file:  "internal/a[b/store.go",
			entry: "internal/a[b",
			want:  true,
		},
		// --- Brackets are filename characters, not character classes
		// (Bugbot on PR #415). path.Match read `[draft]` as a class, so
		// an entry naming a real file stopped matching its own path
		// while matching one nobody declared — wrong in both
		// directions, inside the containment gate.
		{
			name:  "a bracketed file name matches itself",
			file:  "docs/[draft].md",
			entry: "docs/[draft].md",
			want:  true,
		},
		{
			name:  "a bracketed route file matches itself",
			file:  "app/routes/[id].tsx",
			entry: "app/routes/[id].tsx",
			want:  true,
		},
		{
			name:  "a bracketed directory entry contains its children",
			file:  "app/[id]/page.tsx",
			entry: "app/[id]",
			want:  true,
		},
		{
			// The character-class reading admitted this. It must not:
			// the entry names one bracketed file, not every one-letter
			// file whose name is drawn from the brackets.
			name:  "a bracketed entry does not admit a character-class expansion",
			file:  "docs/d.md",
			entry: "docs/[draft].md",
			want:  false,
		},
		{
			name:  "a star still globs alongside brackets in the path",
			file:  "app/[id]/page.tsx",
			entry: "app/*/page.tsx",
			want:  true,
		},
		{
			// A segment using * with a malformed bracket cannot be
			// parsed as a pattern, so it falls back to a literal
			// comparison — which only matches its own text.
			name:  "star with a malformed bracket falls back to literal",
			file:  "internal/a[bc.go",
			entry: "internal/a[b*",
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

// TestCanonicalPath exercises the exported wrapper that external
// callers use to compare paths against write_set entries under the
// same normalization rules the validator uses. Every row declares
// two textually different inputs that must produce the same
// canonical string, or one input whose canonical form is the empty
// string.
func TestCanonicalPath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain relative path", in: "a.txt", want: "a.txt"},
		{name: "leading ./", in: "./a.txt", want: "a.txt"},
		{name: "trailing slash", in: "internal/foo/", want: "internal/foo"},
		{name: "embedded ./", in: "internal/./foo", want: "internal/foo"},
		{name: "doubled slash", in: "internal//foo", want: "internal/foo"},
		{name: "backslashes", in: `internal\foo\bar`, want: "internal/foo/bar"},
		{name: "leading whitespace", in: "  a.txt", want: "a.txt"},
		{name: "trailing whitespace", in: "a.txt  ", want: "a.txt"},
		{name: "empty", in: "", want: ""},
		{name: "whitespace only", in: "   ", want: ""},
		{name: "lone dot", in: ".", want: ""},
		{name: "dot slash", in: "./", want: ""},
		{name: "nested dots", in: "./.", want: ""},
		{name: "root slashes", in: "///", want: ""},
		{name: "multi-segment round-trip", in: "cmd/ethos/mission.go", want: "cmd/ethos/mission.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CanonicalPath(tt.in),
				"CanonicalPath(%q)", tt.in)
		})
	}
}

// TestCanonicalPath_EquivalenceClass asserts that every member of a
// canonical equivalence class maps to the same string. This is the
// invariant the --verify cross-check relies on: if two paths compare
// equal under pathContainedBy, CanonicalPath must agree.
func TestCanonicalPath_EquivalenceClass(t *testing.T) {
	classes := [][]string{
		{"a.txt", "./a.txt", "a.txt/", "./a.txt/", "  a.txt  "},
		{"internal/foo", "./internal/foo", "internal/./foo",
			"internal//foo", "internal/foo/", `internal\foo`},
		{"cmd/ethos/mission.go", "./cmd/ethos/mission.go",
			"cmd/./ethos/./mission.go", "cmd//ethos//mission.go"},
	}
	for _, class := range classes {
		want := CanonicalPath(class[0])
		assert.NotEmpty(t, want, "canonical form of %q must be non-empty", class[0])
		for _, member := range class[1:] {
			assert.Equal(t, want, CanonicalPath(member),
				"CanonicalPath(%q) must equal CanonicalPath(%q)", member, class[0])
		}
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
					Paths: []ConflictPath{
						{Path: "internal/mission/store.go", Source: ConflictSourceWriteSet},
					},
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
					Paths: []ConflictPath{
						{Path: "internal/mission/store.go", Source: ConflictSourceWriteSet},
					},
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
					Paths: []ConflictPath{
						{Path: "internal/mission/", Source: ConflictSourceWriteSet},
					},
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
					Paths: []ConflictPath{
						{Path: "internal/foo/bar.go", Source: ConflictSourceWriteSet},
					},
				},
				{
					MissionID: "m-2026-04-08-003",
					Worker:    "rmh",
					Paths: []ConflictPath{
						{Path: "cmd/ethos/serve.go", Source: ConflictSourceWriteSet},
					},
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
					Paths: []ConflictPath{
						{Path: "internal/mission/log.go", Source: ConflictSourceWriteSet},
						{Path: "internal/mission/store.go", Source: ConflictSourceWriteSet},
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
					Paths: []ConflictPath{
						{Path: "internal/mission/store.go", Source: ConflictSourceWriteSet},
					},
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
			got := findWriteSetConflicts(tt.newSet, nil, tt.existing)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestEntryPairConflicts is the row-by-row exercise of the six-rule
// table in DES-052. Each row pins one cell of the {ws-file, ws-dir,
// ei-dir} × {ws-file, ws-dir, ei-dir} matrix so a regression in any
// branch surfaces as a single named test.
//
// The kinds are encoded as (isDir, isEI):
//
//	(false, false) = ws-file
//	(true,  false) = ws-dir
//	(true,  true)  = ei-dir
func TestEntryPairConflicts(t *testing.T) {
	type kind struct {
		path  string
		isDir bool
		isEI  bool
	}
	tests := []struct {
		name string
		a, b kind
		want bool
	}{
		// Row 1: ws-file × ws-file
		{
			name: "ws-file x ws-file exact match",
			a:    kind{path: "internal/foo/bar.go"},
			b:    kind{path: "internal/foo/bar.go"},
			want: true,
		},
		{
			name: "ws-file x ws-file disjoint",
			a:    kind{path: "internal/foo/bar.go"},
			b:    kind{path: "cmd/ethos/main.go"},
			want: false,
		},
		// Row 2: ws-file × ws-dir
		{
			name: "ws-file x ws-dir prefix matches",
			a:    kind{path: "internal/foo/bar.go"},
			b:    kind{path: "internal/foo/", isDir: true},
			want: true,
		},
		{
			name: "ws-file x ws-dir file outside dir",
			a:    kind{path: "cmd/ethos/main.go"},
			b:    kind{path: "internal/foo/", isDir: true},
			want: false,
		},
		{
			name: "ws-file x ws-dir parent of file (H1 exploit)",
			// DES-032 still triggers here because pathsOverlap goes
			// both ways for ws-dir × anything when the directions agree
			// on a directory entry. The dir is "cmd"; the file is
			// "cmd/foo/bar.go" — dir is a prefix of file.
			a:    kind{path: "cmd/foo/bar.go"},
			b:    kind{path: "cmd", isDir: true},
			want: true,
		},
		// Row 3: ws-dir × ws-dir
		{
			name: "ws-dir x ws-dir nested",
			a:    kind{path: "internal/foo/", isDir: true},
			b:    kind{path: "internal/foo/bar/", isDir: true},
			want: true,
		},
		{
			name: "ws-dir x ws-dir disjoint",
			a:    kind{path: "internal/foo/", isDir: true},
			b:    kind{path: "internal/bar/", isDir: true},
			want: false,
		},
		// Row 4: ws-file × ei-dir — the new constraint
		{
			name: "ws-file x ei-dir dir is prefix of file",
			a:    kind{path: "internal/foo/bar.go"},
			b:    kind{path: "internal/foo/", isDir: true, isEI: true},
			want: true,
		},
		{
			name: "ws-file x ei-dir file outside dir",
			a:    kind{path: "cmd/ethos/main.go"},
			b:    kind{path: "internal/foo/", isDir: true, isEI: true},
			want: false,
		},
		{
			name: "ws-file x ei-dir file with sibling-prefix-substring no overlap",
			a:    kind{path: "internal/foobar/baz.go"},
			b:    kind{path: "internal/foo/", isDir: true, isEI: true},
			want: false,
		},
		// Row 5: ws-dir × ei-dir
		{
			name: "ws-dir x ei-dir overlap",
			a:    kind{path: "internal/foo/", isDir: true},
			b:    kind{path: "internal/foo/", isDir: true, isEI: true},
			want: true,
		},
		{
			name: "ws-dir x ei-dir ei is subdir of ws",
			a:    kind{path: "internal/", isDir: true},
			b:    kind{path: "internal/foo/", isDir: true, isEI: true},
			want: true,
		},
		{
			name: "ws-dir x ei-dir disjoint",
			a:    kind{path: "internal/foo/", isDir: true},
			b:    kind{path: "internal/bar/", isDir: true, isEI: true},
			want: false,
		},
		// Row 6: ei-dir × ei-dir — never
		{
			name: "ei-dir x ei-dir exact same dir never conflicts",
			a:    kind{path: "internal/foo/", isDir: true, isEI: true},
			b:    kind{path: "internal/foo/", isDir: true, isEI: true},
			want: false,
		},
		{
			name: "ei-dir x ei-dir nested never conflicts",
			a:    kind{path: "internal/", isDir: true, isEI: true},
			b:    kind{path: "internal/foo/", isDir: true, isEI: true},
			want: false,
		},
		{
			name: "ei-dir x ei-dir disjoint never conflicts",
			a:    kind{path: "internal/foo/", isDir: true, isEI: true},
			b:    kind{path: "internal/bar/", isDir: true, isEI: true},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ab := entryPairConflicts(tt.a.path, tt.a.isDir, tt.a.isEI,
				tt.b.path, tt.b.isDir, tt.b.isEI)
			ba := entryPairConflicts(tt.b.path, tt.b.isDir, tt.b.isEI,
				tt.a.path, tt.a.isDir, tt.a.isEI)
			assert.Equal(t, tt.want, ab,
				"forward direction: %#v vs %#v", tt.a, tt.b)
			assert.Equal(t, tt.want, ba,
				"symmetry violated: forward=%v reverse=%v for %#v vs %#v",
				ab, ba, tt.a, tt.b)
		})
	}
}

// TestEntryPairConflicts_UnreachableCombination pins the defensive
// guard for the (isEI=true, isDir=false) combination. Rule 17 rejects
// file-shaped extract_into entries at validate time, so this shape
// cannot reach the dispatch in production — but a caller that bypasses
// the validator must not produce a false-positive conflict against an
// entry that the schema does not allow.
func TestEntryPairConflicts_UnreachableCombination(t *testing.T) {
	tests := []struct {
		name string
		a, b struct {
			path  string
			isDir bool
			isEI  bool
		}
	}{
		{
			name: "ei flagged but not dir-shaped (a)",
			a: struct {
				path  string
				isDir bool
				isEI  bool
			}{path: "internal/foo.go", isDir: false, isEI: true},
			b: struct {
				path  string
				isDir bool
				isEI  bool
			}{path: "internal/foo.go", isDir: false, isEI: false},
		},
		{
			name: "ei flagged but not dir-shaped (b)",
			a: struct {
				path  string
				isDir bool
				isEI  bool
			}{path: "internal/foo/", isDir: true, isEI: false},
			b: struct {
				path  string
				isDir bool
				isEI  bool
			}{path: "internal/foo/bar.go", isDir: false, isEI: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.False(t, entryPairConflicts(
				tt.a.path, tt.a.isDir, tt.a.isEI,
				tt.b.path, tt.b.isDir, tt.b.isEI),
				"unreachable (ei && !dir) must short-circuit to no conflict")
			assert.False(t, entryPairConflicts(
				tt.b.path, tt.b.isDir, tt.b.isEI,
				tt.a.path, tt.a.isDir, tt.a.isEI),
				"symmetric direction must also short-circuit")
		})
	}
}

// TestFindWriteSetConflicts_ExtractInto covers the cross-mission race
// scenarios the new ei-dir axis introduces. The critical row is the
// ws-file × ei-dir race in DES-052: A declares write_set:
// internal/foo/bar.go, B declares extract_into: internal/foo/ —
// without rejection, B can create bar.go before A writes it and one
// body is lost at git merge.
func TestFindWriteSetConflicts_ExtractInto(t *testing.T) {
	makeFull := func(id, worker string, writeSet, extractInto []string) *Contract {
		return &Contract{
			MissionID:   id,
			Worker:      worker,
			WriteSet:    writeSet,
			ExtractInto: extractInto,
		}
	}

	tests := []struct {
		name        string
		newSet      []string
		newExtract  []string
		existing    []*Contract
		wantPaths   []ConflictPath
		wantBlocker string
	}{
		{
			name:   "ws-file new vs ei-dir existing rejects same-path race",
			newSet: []string{"internal/foo/bar.go"},
			existing: []*Contract{
				makeFull("m-2026-05-21-100", "rmh", nil, []string{"internal/foo/"}),
			},
			wantPaths: []ConflictPath{
				{Path: "internal/foo/bar.go", Source: ConflictSourceWriteSet},
			},
			wantBlocker: "m-2026-05-21-100",
		},
		{
			name:       "ei-dir new vs ws-file existing rejects same-path race",
			newExtract: []string{"internal/foo/"},
			existing: []*Contract{
				makeFull("m-2026-05-21-101", "rmh",
					[]string{"internal/foo/bar.go"}, nil),
			},
			wantPaths: []ConflictPath{
				{Path: "internal/foo/", Source: ConflictSourceExtractInto},
			},
			wantBlocker: "m-2026-05-21-101",
		},
		{
			name:       "two missions extracting into same dir never conflict",
			newExtract: []string{"internal/foo/"},
			existing: []*Contract{
				makeFull("m-2026-05-21-102", "rmh", nil, []string{"internal/foo/"}),
			},
			wantPaths: nil,
		},
		{
			name:       "two missions extracting into nested dirs never conflict",
			newExtract: []string{"internal/foo/bar/"},
			existing: []*Contract{
				makeFull("m-2026-05-21-103", "rmh", nil, []string{"internal/foo/"}),
			},
			wantPaths: nil,
		},
		{
			name:       "ei-dir new vs ws-dir existing conflicts on overlap",
			newExtract: []string{"internal/foo/"},
			existing: []*Contract{
				makeFull("m-2026-05-21-104", "rmh", []string{"internal/foo/"}, nil),
			},
			wantPaths: []ConflictPath{
				{Path: "internal/foo/", Source: ConflictSourceExtractInto},
			},
			wantBlocker: "m-2026-05-21-104",
		},
		{
			name:       "ei-dir new vs disjoint ws-file existing does not conflict",
			newExtract: []string{"internal/foo/"},
			existing: []*Contract{
				makeFull("m-2026-05-21-105", "rmh",
					[]string{"cmd/ethos/main.go"}, nil),
			},
			wantPaths: nil,
		},
		{
			name:   "mixed new write_set and extract_into report both hit entries",
			newSet: []string{"internal/foo/bar.go"},
			newExtract: []string{
				"docs/",
			},
			existing: []*Contract{
				makeFull("m-2026-05-21-106", "rmh",
					[]string{"internal/foo/"}, []string{"docs/"}),
			},
			// docs/ matches the existing ei-dir (ei x ei -> never), so
			// only the ws-file entry hits via the existing ws-dir.
			wantPaths: []ConflictPath{
				{Path: "internal/foo/bar.go", Source: ConflictSourceWriteSet},
			},
			wantBlocker: "m-2026-05-21-106",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findWriteSetConflicts(tt.newSet, tt.newExtract, tt.existing)
			if tt.wantPaths == nil {
				assert.Empty(t, got, "expected no conflicts")
				return
			}
			if assert.Len(t, got, 1, "expected one conflict") {
				assert.Equal(t, tt.wantBlocker, got[0].MissionID)
				assert.Equal(t, tt.wantPaths, got[0].Paths)
			}
		})
	}
}

// TestIsDirEntry locks the trailing-slash heuristic findWriteSetConflicts
// uses to dispatch ws-file vs ws-dir on the new side. Whitespace is
// trimmed first so an operator-written " internal/ " classifies as a
// directory the same as "internal/".
func TestIsDirEntry(t *testing.T) {
	tests := []struct {
		entry string
		want  bool
	}{
		{entry: "internal/foo/", want: true},
		{entry: "internal/foo", want: false},
		{entry: "  internal/foo/  ", want: true},
		{entry: "foo.go", want: false},
		{entry: "docs/architecture.md", want: false},
		{entry: "docs/", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.entry, func(t *testing.T) {
			assert.Equal(t, tt.want, isDirEntry(tt.entry))
		})
	}
}

// TestFormatConflictError asserts the operator-facing error string is
// stable, deterministic, names the source field, and includes mission
// ID, worker, and overlapping paths for every conflict — one line per
// blocker.
func TestFormatConflictError(t *testing.T) {
	t.Run("single write_set conflict", func(t *testing.T) {
		conflicts := []Conflict{
			{
				MissionID: "m-2026-04-07-002",
				Worker:    "bwk",
				Paths: []ConflictPath{
					{Path: "internal/mission/store.go", Source: ConflictSourceWriteSet},
				},
			},
		}
		err := formatConflictError(conflicts)
		want := "write_set conflict with mission m-2026-04-07-002 (worker: bwk): write_set [internal/mission/store.go]"
		assert.EqualError(t, err, want)
	})

	t.Run("single extract_into conflict names extract_into", func(t *testing.T) {
		conflicts := []Conflict{
			{
				MissionID: "m-2026-05-21-100",
				Worker:    "bwk",
				Paths: []ConflictPath{
					{Path: "internal/foo/", Source: ConflictSourceExtractInto},
				},
			},
		}
		err := formatConflictError(conflicts)
		want := "extract_into conflict with mission m-2026-05-21-100 (worker: bwk): extract_into [internal/foo/]"
		assert.EqualError(t, err, want)
	})

	t.Run("mixed sources name both fields", func(t *testing.T) {
		conflicts := []Conflict{
			{
				MissionID: "m-2026-05-21-101",
				Worker:    "bwk",
				Paths: []ConflictPath{
					{Path: "internal/foo/bar.go", Source: ConflictSourceWriteSet},
					{Path: "docs/", Source: ConflictSourceExtractInto},
				},
			},
		}
		err := formatConflictError(conflicts)
		want := "write_set + extract_into conflict with mission m-2026-05-21-101 (worker: bwk): write_set [internal/foo/bar.go] extract_into [docs/]"
		assert.EqualError(t, err, want)
	})

	t.Run("multi conflict embeds newline between blockers", func(t *testing.T) {
		conflicts := []Conflict{
			{
				MissionID: "m-2026-04-07-002",
				Worker:    "bwk",
				Paths: []ConflictPath{
					{Path: "internal/mission/store.go", Source: ConflictSourceWriteSet},
				},
			},
			{
				MissionID: "m-2026-04-07-003",
				Worker:    "rmh",
				Paths: []ConflictPath{
					{Path: "cmd/ethos/mission.go", Source: ConflictSourceWriteSet},
					{Path: "cmd/ethos/serve.go", Source: ConflictSourceWriteSet},
				},
			},
		}
		err := formatConflictError(conflicts)
		msg := err.Error()
		lines := strings.Split(msg, "\n")
		if assert.Len(t, lines, 2, "multi-conflict error must be one line per blocker") {
			assert.Equal(t,
				"write_set conflict with mission m-2026-04-07-002 (worker: bwk): write_set [internal/mission/store.go]",
				lines[0])
			assert.Equal(t,
				"write_set conflict with mission m-2026-04-07-003 (worker: rmh): write_set [cmd/ethos/mission.go cmd/ethos/serve.go]",
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

// TestSplitPathList locks the one splitter both path-list surfaces
// use — the CLI's --write-set/--extract-into flags and pipeline
// template expansion (ethos-t2lb).
func TestSplitPathList(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "empty", in: "", want: nil},
		{name: "whitespace only", in: "   \t ", want: nil},
		{name: "single path", in: "internal/mission", want: []string{"internal/mission"}},
		{name: "surrounding whitespace", in: "  internal/mission  ", want: []string{"internal/mission"}},
		{
			name: "comma separated",
			in:   "internal/mission,cmd/ethos",
			want: []string{"internal/mission", "cmd/ethos"},
		},
		{
			name: "space separated",
			in:   "internal/mission cmd/ethos",
			want: []string{"internal/mission", "cmd/ethos"},
		},
		{
			name: "comma and space separated",
			in:   "internal/mission, cmd/ethos , hooks",
			want: []string{"internal/mission", "cmd/ethos", "hooks"},
		},
		{
			name: "newline separated",
			in:   "internal/mission\ncmd/ethos",
			want: []string{"internal/mission", "cmd/ethos"},
		},
		{name: "empty fields dropped", in: ",,internal/mission,,", want: []string{"internal/mission"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SplitPathList(tt.in))
		})
	}
}

// TestWriteSetsConflict covers ethos-swoh: a handle-overlap warning
// must not fire when the two missions' write_sets don't actually
// intersect. Rows pair a false-positive shape (disjoint write_sets)
// against the true-positive shape (overlapping write_sets) so a
// regression in either direction surfaces as a named failure.
func TestWriteSetsConflict(t *testing.T) {
	tests := []struct {
		name                    string
		aWriteSet, aExtractInto []string
		bWriteSet, bExtractInto []string
		want                    bool
	}{
		{
			name:      "disjoint write_sets do not conflict (ethos-swoh false positive)",
			aWriteSet: []string{"internal/mission/conflict.go"},
			bWriteSet: []string{"cmd/ethos/serve.go"},
			want:      false,
		},
		{
			name:      "overlapping write_sets conflict",
			aWriteSet: []string{"internal/mission/conflict.go"},
			bWriteSet: []string{"internal/mission/"},
			want:      true,
		},
		{
			name:      "exact match conflicts",
			aWriteSet: []string{"internal/mission/store.go"},
			bWriteSet: []string{"internal/mission/store.go"},
			want:      true,
		},
		{
			name:      "both empty does not conflict",
			aWriteSet: nil,
			bWriteSet: nil,
			want:      false,
		},
		{
			name:         "extract_into on one side, overlapping write_set on the other, conflicts",
			aWriteSet:    []string{"internal/mission/store.go"},
			bExtractInto: []string{"internal/mission/"},
			want:         true,
		},
		{
			name:         "extract_into vs extract_into never conflicts, even when identical",
			aExtractInto: []string{"internal/mission/"},
			bExtractInto: []string{"internal/mission/"},
			want:         false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WriteSetsConflict(tt.aWriteSet, tt.aExtractInto, tt.bWriteSet, tt.bExtractInto)
			assert.Equal(t, tt.want, got)
		})
	}
}
