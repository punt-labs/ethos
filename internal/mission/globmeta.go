package mission

// This file carries NO build constraint on purpose.
//
// globMeta is shared by the path matcher in conflict.go (which is
// //go:build !windows) and the per-entry validator in validate.go
// (which is not). Defining it beside the matcher made validate.go
// depend on a !windows-only symbol, so validate.go stopped compiling
// under GOOS=windows — invisible to a darwin/linux `make check`
// (Copilot on PR #415). A constant with no platform behavior belongs
// in a file with no platform constraint.

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
