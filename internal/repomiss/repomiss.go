// Package repomiss holds the types describing what a repo-authoritative
// identity set is missing (DES-057 Part A).
//
// It is a dependency-free leaf so that the four layered stores, which
// PRODUCE misses during live resolution, and internal/vendor and
// internal/doctor, which VERIFY completeness, can all share one type.
// The design (DESIGN.md DES-057) puts these types in internal/vendor
// alongside the closure walk; that arrangement does not compile, because
// vendor must import the four stores to walk the closure and they would
// have to import it back. Splitting the types out preserves the property
// the design is after — one type, one predicate, producer and consumer
// cannot drift — with no behavior change.
package repomiss

import (
	"fmt"
	"sort"
	"strings"
)

// Kinds of missing reference. Attribute, identity, role, and team kinds
// match the store directory name, so a ref reads as the path it names.
const (
	KindPersonality  = "personalities"
	KindWritingStyle = "writing-styles"
	KindTalent       = "talents"
	KindIdentity     = "identities"
	KindRole         = "roles"
	KindTeam         = "teams"
	KindExt          = "ext"
)

// MissingRef names one file a repo-authoritative resolution needed but
// could not find. Path is where ethos looked in the repo layer — the
// place `ethos vendor` would put it — so the diagnostic tells the user
// what to fix, not merely that something is wrong.
type MissingRef struct {
	Kind string `json:"kind" yaml:"kind"`
	Slug string `json:"slug" yaml:"slug"`
	Path string `json:"path" yaml:"path"`
}

// String renders one miss as "{kind}/{slug}".
func (m MissingRef) String() string {
	return m.Kind + "/" + m.Slug
}

// ErrIncompleteRepoSet reports that a repo-only store cannot fully
// resolve a handle: the repo vendored the identity but not everything it
// references.
//
// It aggregates EVERY miss rather than stopping at the first, so one
// `ethos vendor` round closes the whole gap instead of the user
// discovering the set one failed resolution at a time.
//
// In layered mode these same misses are soft warnings, because the
// global layer is expected to supply the tail. Under repo-only there is
// no tail, so they are hard errors.
type ErrIncompleteRepoSet struct {
	Handle  string
	Missing []MissingRef
}

func (e *ErrIncompleteRepoSet) Error() string {
	refs := make([]string, 0, len(e.Missing))
	for _, m := range e.Missing {
		refs = append(refs, fmt.Sprintf("%s (looked in %s)", m, m.Path))
	}
	return fmt.Sprintf(
		"identity %q is incomplete under resolution: repo-only — missing %s; run `ethos vendor %s` to complete the set",
		e.Handle, strings.Join(refs, ", "), e.Handle)
}

// New builds the aggregate error, or nil when nothing is missing. Refs
// are sorted so the message is stable across runs — an unstable
// diagnostic is unusable in CI output.
func New(handle string, missing []MissingRef) error {
	if len(missing) == 0 {
		return nil
	}
	return &ErrIncompleteRepoSet{Handle: handle, Missing: Sorted(missing)}
}

// Sorted returns a copy of refs ordered by kind then slug.
func Sorted(refs []MissingRef) []MissingRef {
	out := append([]MissingRef(nil), refs...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Slug < out[j].Slug
	})
	return out
}
