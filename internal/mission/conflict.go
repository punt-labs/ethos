//go:build !windows

package mission

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Conflict describes a write_set overlap between a new mission contract
// and an existing open mission. One Conflict is emitted per blocking
// existing mission, with the union of overlapping paths from the new
// contract's perspective.
//
// Paths is the set of entries from the NEW contract's write_set that
// overlap at least one entry in the existing mission's write_set,
// sorted lexicographically for deterministic error messages and tests.
type Conflict struct {
	MissionID string   // ID of the existing open mission
	Worker    string   // Worker handle of the existing open mission
	Paths     []string // Overlapping entries from the new contract's write_set
}

// findWriteSetConflicts compares newWriteSet against the write_set of
// each contract in existing. Returns one Conflict per existing
// contract that has at least one overlapping path. The caller is
// responsible for filtering existing to open missions only — this
// helper does no status filtering.
//
// Returned conflicts are sorted by MissionID for deterministic output;
// each Conflict's Paths slice is sorted and deduplicated. An empty
// newWriteSet or empty existing slice returns nil.
//
// See pathsOverlap for the segment-prefix overlap rule.
func findWriteSetConflicts(newWriteSet []string, existing []*Contract) []Conflict {
	if len(newWriteSet) == 0 || len(existing) == 0 {
		return nil
	}

	var conflicts []Conflict
	for _, ec := range existing {
		if ec == nil || len(ec.WriteSet) == 0 {
			continue
		}
		// Collect overlapping new-side entries into a set so a
		// duplicate entry in the new write_set is reported once.
		seen := make(map[string]struct{})
		for _, np := range newWriteSet {
			for _, ep := range ec.WriteSet {
				if pathsOverlap(np, ep) {
					seen[np] = struct{}{}
					break
				}
			}
		}
		if len(seen) == 0 {
			continue
		}
		paths := make([]string, 0, len(seen))
		for p := range seen {
			paths = append(paths, p)
		}
		sort.Strings(paths)
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

// splitSegments normalizes a write_set entry and splits it on the
// forward-slash separator. Normalization:
//   - trim leading/trailing whitespace
//   - replace any `\` with `/` (defense in depth — the per-entry
//     validator already rejected drive letters and UNC paths)
//   - trim a single trailing `/` so `internal/foo/` and `internal/foo`
//     produce the same segment list
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

// formatConflictError builds the operator-facing error string from
// one or more Conflicts. Each conflict is on its own line so the
// operator sees every blocker at once.
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
		lines[i] = fmt.Sprintf(
			"write_set conflict with mission %s (worker: %s): overlapping paths [%s]",
			c.MissionID, c.Worker, strings.Join(c.Paths, " "),
		)
	}
	return errors.New(strings.Join(lines, "\n"))
}
