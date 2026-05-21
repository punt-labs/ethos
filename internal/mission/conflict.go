//go:build !windows

package mission

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ConflictSource identifies which field of the new contract an
// overlapping entry came from. Stored as a string for stable
// serialization across the CLI and MCP surfaces.
const (
	ConflictSourceWriteSet    = "write_set"
	ConflictSourceExtractInto = "extract_into"
)

// ConflictPath is one overlapping entry from the new contract — the
// path text plus the field it came from. Operators need to know which
// field to edit when admission control blocks a mission, so the
// per-entry source travels with the path through the error message.
type ConflictPath struct {
	Path   string
	Source string // ConflictSourceWriteSet or ConflictSourceExtractInto
}

// Conflict describes an overlap between a new mission contract and an
// existing open mission. One Conflict is emitted per blocking
// existing mission, with the union of overlapping entries from the
// new contract's perspective.
//
// Paths is the set of entries from the NEW contract (drawn from
// write_set and extract_into) that overlap at least one entry on the
// existing side. Each entry carries its source field. The slice is
// sorted by (Source, Path) for deterministic error messages — write_set
// entries appear before extract_into entries, with each group sorted
// lexicographically by path.
type Conflict struct {
	MissionID string         // ID of the existing open mission
	Worker    string         // Worker handle of the existing open mission
	Paths     []ConflictPath // Overlapping entries from the new contract
}

// findWriteSetConflicts compares the new contract's write_set and
// extract_into against the write_set and extract_into of each contract
// in existing. Returns one Conflict per existing contract that has at
// least one overlapping path. The caller is responsible for filtering
// existing to open missions only — this helper does no status filtering.
//
// Returned conflicts are sorted by MissionID for deterministic output;
// each Conflict's Paths slice is sorted and deduplicated. An empty new
// write_set AND empty new extract_into, or empty existing slice,
// returns nil.
//
// The relation is the closed six-rule form over the entry-kind
// taxonomy {ws-file, ws-dir, ei-dir} per DES-052:
//
//	ws-file × ws-file  -> conflict iff pathsOverlap
//	ws-file × ws-dir   -> conflict iff dir is prefix of file
//	ws-dir  × ws-dir   -> conflict iff pathsOverlap
//	ws-file × ei-dir   -> conflict iff dir is prefix of file
//	ws-dir  × ei-dir   -> conflict iff pathsOverlap
//	ei-dir  × ei-dir   -> never
//
// The relation is symmetric over the unordered mission pair. The
// reported Paths list names the NEW-side entries that hit at least
// one existing-side entry; the leader sees what they wrote, not what
// the other mission wrote.
func findWriteSetConflicts(newWriteSet, newExtractInto []string, existing []*Contract) []Conflict {
	if len(newWriteSet) == 0 && len(newExtractInto) == 0 {
		return nil
	}
	if len(existing) == 0 {
		return nil
	}

	var conflicts []Conflict
	for _, ec := range existing {
		if ec == nil {
			continue
		}
		if len(ec.WriteSet) == 0 && len(ec.ExtractInto) == 0 {
			continue
		}
		// Collect overlapping new-side entries into a set keyed by
		// (Source, Path) so a duplicate entry in the new contract is
		// reported once and a path that appears in both write_set and
		// extract_into surfaces under each source.
		seen := make(map[ConflictPath]struct{})
		for _, np := range newWriteSet {
			if anyEntryConflicts(np, false, ec) {
				seen[ConflictPath{Path: np, Source: ConflictSourceWriteSet}] = struct{}{}
			}
		}
		for _, np := range newExtractInto {
			if anyEntryConflicts(np, true, ec) {
				seen[ConflictPath{Path: np, Source: ConflictSourceExtractInto}] = struct{}{}
			}
		}
		if len(seen) == 0 {
			continue
		}
		paths := make([]ConflictPath, 0, len(seen))
		for p := range seen {
			paths = append(paths, p)
		}
		sortConflictPaths(paths)
		conflicts = append(conflicts, Conflict{
			MissionID: ec.MissionID,
			Worker:    ec.Worker,
			Paths:     paths,
		})
	}

	sort.Slice(conflicts, func(i, j int) bool {
		return conflicts[i].MissionID < conflicts[j].MissionID
	})
	return conflicts
}

// sortConflictPaths orders ConflictPath values primarily by Source —
// write_set first, then extract_into — and within each group by Path
// lexicographically. The grouping keeps the error message readable:
// the leader sees every write_set hit, then every extract_into hit,
// rather than the two interleaved.
func sortConflictPaths(paths []ConflictPath) {
	sort.Slice(paths, func(i, j int) bool {
		if paths[i].Source != paths[j].Source {
			// write_set sorts before extract_into deterministically.
			return paths[i].Source == ConflictSourceWriteSet
		}
		return paths[i].Path < paths[j].Path
	})
}

// anyEntryConflicts reports whether newEntry (from the new contract)
// conflicts with any entry of ec under the six-rule form. newIsEI
// flags whether newEntry comes from extract_into (ei-dir) or
// write_set (ws-file or ws-dir, distinguished by isDirEntry).
func anyEntryConflicts(newEntry string, newIsEI bool, ec *Contract) bool {
	newDir := newIsEI || isDirEntry(newEntry)
	for _, ep := range ec.WriteSet {
		if entryPairConflicts(newEntry, newDir, newIsEI, ep, isDirEntry(ep), false) {
			return true
		}
	}
	for _, ep := range ec.ExtractInto {
		// extract_into entries are directory-shaped by per-entry
		// validation (rule 17), so pass true for both isDir and isEI.
		if entryPairConflicts(newEntry, newDir, newIsEI, ep, true, true) {
			return true
		}
	}
	return false
}

// entryPairConflicts answers the six-rule question for one new-side
// entry against one existing-side entry. The (isDir, isEI) tuple
// fully encodes the entry kind for the dispatch:
//
//	(false, false) = ws-file
//	(true,  false) = ws-dir
//	(true,  true)  = ei-dir
//
// The (false, true) combination is unreachable — extract_into entries
// are always directory-shaped — but the function guards it so a
// future schema change cannot silently misclassify.
func entryPairConflicts(a string, aIsDir, aIsEI bool, b string, bIsDir, bIsEI bool) bool {
	// Defensive guard: an extract_into entry that is not
	// directory-shaped is unreachable in production — rule 17 rejects
	// file-shaped extract_into entries at validate time. Treat the
	// impossible (ei, !dir) combination as a no-conflict so a future
	// caller that bypasses the validator cannot trigger an
	// undefined-by-design branch in the dispatch below.
	if (aIsEI && !aIsDir) || (bIsEI && !bIsDir) {
		return false
	}
	// Per DES-052, ei-dir × ei-dir never conflicts. Two missions may
	// extract into the same directory or one into a subdir of the
	// other; same-filename collisions are the leader's responsibility,
	// not admission control's.
	if aIsEI && bIsEI {
		return false
	}
	// ws-file × ws-dir or ws-file × ei-dir: conflict iff the directory
	// is a prefix of the file. pathContainedBy(file, dir) answers
	// exactly that. Apply in whichever direction the file/dir lands.
	switch {
	case !aIsDir && bIsDir:
		return pathContainedBy(a, b)
	case aIsDir && !bIsDir:
		return pathContainedBy(b, a)
	default:
		// Both directories (ws-dir × ws-dir, ws-dir × ei-dir) or both
		// files (ws-file × ws-file): the segment-prefix overlap rule
		// covers every remaining row.
		return pathsOverlap(a, b)
	}
}

// isDirEntry reports whether a write_set entry is directory-shaped
// for the conflict check. The conflict check treats any entry ending
// in a slash as a directory marker; everything else is treated as a
// file claim. This matches the existing trailing-slash heuristic that
// archetype_enforce.go uses for the same dispatch.
func isDirEntry(entry string) bool {
	return strings.HasSuffix(strings.TrimSpace(entry), "/")
}

// pathsOverlap reports whether two write_set entries describe
// overlapping write territory by segment-prefix comparison.
//
// Two paths overlap when, after normalization (trim whitespace, trim
// trailing slash, replace backslashes with forward slashes), one
// path's segment list is a prefix of the other's segment list. An
// empty segment list matches no other path.
//
// Comparison is case-sensitive: POSIX filesystems treat "Foo" and
// "foo" as distinct files. macOS case-insensitive HFS+ is a known
// divergence and not handled here.
//
// The per-entry validator has already rejected `..`, absolute paths,
// control characters, drive letters, and UNC paths upstream — this
// helper does no defense-in-depth re-validation.
//
// pathsOverlap is symmetric: it answers "do these two paths intersect
// in either direction?" That is the right primitive for Phase 3.2's
// cross-mission conflict check (two workers declaring `internal/` and
// `internal/mission/store.go` are in conflict regardless of which
// side is the ancestor). For the Phase 3.6 result containment check —
// "is the result's reported file inside the contract's write_set?" —
// use pathContainedBy, which is directional.
func pathsOverlap(a, b string) bool {
	as := splitSegments(a)
	bs := splitSegments(b)
	if len(as) == 0 || len(bs) == 0 {
		return false
	}
	// One segment list is a prefix of the other (where "prefix"
	// includes "equal"). Iterate up to the shorter length and require
	// every leading segment to match exactly.
	n := len(as)
	if len(bs) < n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// pathContainedBy reports whether a file path lives inside a
// write_set entry. The predicate is asymmetric: the entry's
// normalized segment list must be a prefix of the file's normalized
// segment list, AND the file must have at least as many segments as
// the entry. "Equal" counts as contained.
//
// This is the right primitive for Phase 3.6 result containment —
// "is the result's files_changed path inside the contract's
// write_set?" A contract declaring `cmd/ethos/serve.go` and a
// result claiming `cmd` must be refused: the result would otherwise
// quietly claim authority over every file under `cmd/`, not just
// the one file the contract allowed. pathsOverlap answers the wrong
// question here — it would accept both directions.
//
// Round 2 of Phase 3.6 added this helper after all four reviewers
// independently flagged the symmetric check as the load-bearing bug.
// See m-2026-04-08-005-round2.md for the exploit table.
//
// An empty segment list on either side matches nothing. The per-
// entry validator has already rejected the malformed forms upstream.
func pathContainedBy(file, entry string) bool {
	fs := splitSegments(file)
	es := splitSegments(entry)
	if len(fs) == 0 || len(es) == 0 {
		return false
	}
	if len(fs) < len(es) {
		return false
	}
	for i, seg := range es {
		if fs[i] != seg {
			return false
		}
	}
	return true
}

// splitSegments normalizes a write_set entry and splits it on the
// forward-slash separator. Normalization:
//   - trim leading/trailing whitespace
//   - replace any `\` with `/` (defense in depth — the per-entry
//     validator already rejected drive letters and UNC paths)
//   - trim trailing `/` characters so `internal/foo/` and
//     `internal/foo` produce the same segment list
//   - drop empty segments produced by doubled slashes, so
//     `internal//foo` and `internal/foo` compare equal
//   - drop `.` segments so `./internal/foo`, `internal/./foo`, and
//     `internal/foo` compare equal
//
// The per-entry validator (see validate.go and
// TestValidate_AcceptsSingleDotSegment) deliberately accepts `.` as
// legitimate path syntax: it is a shell convention for "current
// directory" and is not a traversal segment — only `..` escapes the
// base. DES-031 recorded "Single-dot (`.`) segment rejection in
// write_set" as a rejected alternative. The conflict check therefore
// must normalize `.` segments away here, otherwise two logically
// overlapping missions (e.g. `./internal/foo` vs `internal/foo`) fall
// on opposite sides of the segment-prefix comparison and both are
// admitted — the exact silent-conflict scenario Phase 3.2 exists to
// prevent.
//
// Likewise, the per-entry validator does not reject double slashes,
// so the conflict check must normalize empty middle segments here for
// the same reason.
//
// An empty or whitespace-only input — or an input like `///` or `./.`
// that collapses to nothing after filtering — produces a nil segment
// list, which signals "matches nothing" to pathsOverlap.
func splitSegments(p string) []string {
	p = strings.TrimSpace(p)
	if p == "" {
		return nil
	}
	p = strings.ReplaceAll(p, `\`, "/")
	p = strings.TrimRight(p, "/")
	if p == "" {
		return nil
	}
	raw := strings.Split(p, "/")
	segs := raw[:0]
	for _, s := range raw {
		if s == "" || s == "." {
			continue
		}
		segs = append(segs, s)
	}
	if len(segs) == 0 {
		return nil
	}
	return segs
}

// CanonicalPath returns a canonical string form of p that matches the
// write_set containment rules used by pathContainedBy. Two inputs that
// pathContainedBy would treat as "the same file" produce the same
// canonical string; inputs that normalize to nothing (empty, `.`,
// `./`, `///`) return the empty string.
//
// This is the shared primitive for any code outside the mission
// package that needs to compare a file path against another path
// using the same semantics the validator applies to write_set entries
// and files_changed. The CLI --verify cross-check uses it so a worker
// who declares `./a.txt` in files_changed — which the validator
// accepts because `./a.txt` and `a.txt` normalize equal — is not
// falsely rejected against `git diff --numstat`, which emits the
// canonical form.
//
// The helper is an exported wrapper over splitSegments so there is
// exactly one implementation of path canonicalization in the package;
// a parallel normalizer in the CLI would drift the moment the
// validator's rules change.
func CanonicalPath(p string) string {
	segs := splitSegments(p)
	if len(segs) == 0 {
		return ""
	}
	return strings.Join(segs, "/")
}

// formatConflictError builds the operator-facing error string from
// one or more Conflicts. Each conflict is on its own line so the
// operator sees every blocker at once.
//
// The header names every contributing source ("write_set conflict",
// "extract_into conflict", or "write_set + extract_into conflict")
// so the leader knows which field to edit. Per-source path lists
// follow so the leader sees exactly which entries hit.
//
// Returns nil for an empty input slice — the caller is expected to
// only call this when there is at least one conflict, but the empty
// case is handled defensively so a refactor cannot accidentally
// produce a non-nil error with no content.
func formatConflictError(conflicts []Conflict) error {
	if len(conflicts) == 0 {
		return nil
	}
	lines := make([]string, len(conflicts))
	for i, c := range conflicts {
		lines[i] = formatConflictLine(c)
	}
	return errors.New(strings.Join(lines, "\n"))
}

// formatConflictLine renders a single Conflict into its operator-facing
// line. Sources are reported separately so the leader can locate the
// offending field; when both sources contributed, the line names them
// in declaration order (write_set first, then extract_into).
func formatConflictLine(c Conflict) string {
	var wsPaths, eiPaths []string
	for _, p := range c.Paths {
		switch p.Source {
		case ConflictSourceExtractInto:
			eiPaths = append(eiPaths, p.Path)
		default:
			wsPaths = append(wsPaths, p.Path)
		}
	}
	var header string
	switch {
	case len(wsPaths) > 0 && len(eiPaths) > 0:
		header = "write_set + extract_into conflict"
	case len(eiPaths) > 0:
		header = "extract_into conflict"
	default:
		header = "write_set conflict"
	}
	var parts []string
	if len(wsPaths) > 0 {
		parts = append(parts, fmt.Sprintf("write_set [%s]", strings.Join(wsPaths, " ")))
	}
	if len(eiPaths) > 0 {
		parts = append(parts, fmt.Sprintf("extract_into [%s]", strings.Join(eiPaths, " ")))
	}
	return fmt.Sprintf("%s with mission %s (worker: %s): %s",
		header, c.MissionID, c.Worker, strings.Join(parts, " "))
}
