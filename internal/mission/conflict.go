//go:build !windows

package mission

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode"
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
//
// A glob entry is compared by the literal prefix that precedes its
// first glob segment: `docs/**` and `docs/*.md` both overlap as
// `docs`, and an entry whose FIRST segment globs claims from the root
// and therefore overlaps everything. Truncation only ever shortens
// the compared list, so it can only turn a non-conflict into a
// conflict — admission control refuses more, never less.
//
// This is the composed-system half of ethos-qy7k. Making containment
// glob-aware without this left two open missions free to claim
// `docs/**` and `docs/a.md` with no conflict reported — precisely the
// silent corruption DES-032 exists to refuse.
func pathsOverlap(a, b string) bool {
	as, aRoot := overlapSegments(a)
	bs, bRoot := overlapSegments(b)
	// An entry that normalizes to nothing and does not glob from the
	// root matches no other path — the empty-entry rule, unchanged.
	if (len(as) == 0 && !aRoot) || (len(bs) == 0 && !bRoot) {
		return false
	}
	// A leading glob matches at any depth from the root, so it
	// intersects every other claim.
	if aRoot || bRoot {
		return true
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

// overlapSegments returns the literal segment prefix of an entry for
// the symmetric overlap comparison, plus a flag marking an entry that
// globs from its FIRST segment.
//
// Truncation at the first glob segment is the conservative reading of
// a glob claim: `docs/**` and `docs/*.md` both cover at least
// everything under `docs`, so comparing them as `docs` reports every
// real intersection and some that are only potential. Over-reporting
// a conflict costs the leader one edit; under-reporting costs two
// workers their work.
//
// A first-segment glob (`*.go`, `**/x.md`) has no literal prefix at
// all — it can match at the root — so it is reported as a root claim
// rather than as an empty (matches-nothing) entry, which is how the
// hole in ethos-qy7k's first round would otherwise reopen.
func overlapSegments(p string) (segs []string, rootGlob bool) {
	all := splitSegments(p)
	for i, s := range all {
		if strings.ContainsAny(s, globMeta) {
			return all[:i], i == 0
		}
	}
	return all, false
}

// PathContainedBy reports whether file lives inside the write_set
// entry. It is the exported form of pathContainedBy for callers
// outside the package — the PreToolUse verifier allowlist — so the
// allowlist admits exactly what the result-containment check admits.
// A second implementation in the hook package would drift the moment
// the entry semantics change.
func PathContainedBy(file, entry string) bool {
	return pathContainedBy(file, entry)
}

// pathContainedBy reports whether a file path lives inside a
// write_set entry. The predicate is asymmetric: the entry's
// normalized segment list must match a prefix of the file's
// normalized segment list, AND the file must have at least as many
// segments as the entry consumes. "Equal" counts as contained.
//
// Entry segments carrying a glob metacharacter (`*` or `?`) match by
// pattern rather than by literal equality: `*` and `?` match within
// one segment (path.Match), and a `**` segment matches any number of
// segments, including none. Brackets are ordinary filename characters
// here, not character classes — see globMeta. A write_set that declares
// `docs/**` claims every path under docs/, so a result reporting
// `docs/audited-delegation.md` is inside it (ethos-qy7k — the
// literal comparison read `**` as a directory named `**` and refused
// every real path under the declared entry).
//
// Globs only ever widen what an entry contains, and only for entries
// that declare one. A literal entry compares exactly as before, so
// the parent-claim refusal below is untouched.
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
	// A file path that still climbs out of the tree is inside nothing.
	// Entries are validator-checked for `..` upstream, but FILES are
	// not: the PreToolUse allowlist matches a caller-supplied tool
	// target. A `**` segment happily matched `..`, so an entry like
	// `**/notes.go` admitted `../notes.go` — a write outside the repo
	// (Copilot on PR #415). The literal matcher this replaced could
	// not express that, so the guard arrives with the glob.
	for _, seg := range fs {
		if seg == ".." {
			return false
		}
	}
	return segmentsContain(fs, es)
}

// segmentsContain reports whether the entry segments match a leading
// run of the file segments. Literal and single-segment glob entries
// consume one file segment each; a `**` entry consumes any number,
// which is what makes the match a backtracking walk rather than a
// straight loop. star records the last `**` position and mark the
// file offset it was matched at, so a failure downstream retries with
// `**` swallowing one more segment.
//
// The entry running out is success — everything below the matched
// prefix is inside the entry. The file running out with entry
// segments still to match is failure: that is the parent-claim shape
// (file `cmd`, entry `cmd/ethos/serve.go`) the asymmetry exists to
// refuse.
func segmentsContain(fs, es []string) bool {
	i, j := 0, 0
	star, mark := -1, 0
	for {
		if j == len(es) {
			return true
		}
		if es[j] == "**" {
			star, mark = j, i
			j++
			continue
		}
		if i < len(fs) && segmentMatches(es[j], fs[i]) {
			i++
			j++
			continue
		}
		if star >= 0 && mark < len(fs) {
			mark++
			i = mark
			j = star + 1
			continue
		}
		return false
	}
}

// segmentMatches reports whether one entry segment matches one file
// segment. A segment with no glob metacharacter compares literally —
// the common case, and the one that keeps every non-glob write_set
// entry byte-identical in behavior. That now includes every segment
// whose only unusual characters are brackets, so a file named
// `[draft].md` matches the entry that names it.
//
// A segment that DOES use `*` or `?` alongside a malformed bracket
// makes path.Match return ErrBadPattern; that falls back to literal
// comparison rather than matching nothing. The per-entry validator
// accepts such a path, so it names a real file, and a literal
// comparison is the reading that cannot over-admit.
func segmentMatches(entrySeg, fileSeg string) bool {
	if !strings.ContainsAny(entrySeg, globMeta) {
		return entrySeg == fileSeg
	}
	ok, err := path.Match(entrySeg, fileSeg)
	if err != nil {
		return entrySeg == fileSeg
	}
	return ok
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

// SplitPathList splits one write_set or extract_into value into its
// individual entries. Commas and whitespace both separate entries, so
// `internal/mission,cmd/ethos` and `internal/mission cmd/ethos` yield
// the same two paths.
//
// One splitter serves both surfaces that build a path list from text:
// the CLI's `--write-set` / `--extract-into` flags and pipeline
// template expansion. `ethos mission pipeline instantiate --var
// target="docs/a.md docs/b.md"` used to substitute the whole value
// into one write_set entry, producing a single path that names no
// file — every real edit then fell outside the write_set (ethos-t2lb).
//
// The cost is that a path containing a space cannot be expressed in
// either surface. That is the right trade: a space-separated list is
// the common shape, and a collapsed entry fails late and obscurely,
// at the first edit, rather than at admission.
func SplitPathList(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
	if len(parts) == 0 {
		return nil
	}
	return parts
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
