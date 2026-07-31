package mission

import (
	"strings"
	"unicode"
)

// Path text handling that has no platform behavior, and therefore NO
// build constraint.
//
// The matcher and the conflict scan live in conflict.go, which is
// //go:build !windows. Anything defined there is invisible to an
// untagged file under GOOS=windows, so putting a platform-neutral
// helper beside them silently makes every untagged caller
// unbuildable on that platform — a break a darwin/linux `make check`
// cannot see. globMeta hit exactly that (validate.go), and
// SplitPathList would have hit it next: pipeline.go calls it, and
// pipeline.go carries no tag (Copilot on PR #415).
//
// The rule this file exists to hold: a constant or a function with no
// platform behavior belongs in a file with no platform constraint.

// globMeta is the set of characters that make a write_set segment a
// pattern rather than a literal name. One definition serves the
// matcher and the validator: a validator that recognized fewer
// characters would admit an entry the matcher then reads as a
// wildcard.
//
// Brackets are NOT in the set. path.Match reads `[draft]` as a
// character class, so an entry naming a real file with brackets in it
// — docs/[draft].md, or a Next.js-style app/[id].tsx — stopped
// matching its own path while matching docs/d.md, a file nobody
// declared. Wrong in both directions, in the containment gate
// (Bugbot on PR #415). The write_set glob vocabulary is `**`, `*`, and
// `?` for path wildcards; character classes were never part of it, and
// nobody writes a character-class write_set. Brackets are ordinary
// filename characters.
const globMeta = "*?"

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
