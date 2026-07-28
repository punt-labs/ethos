package identity

import (
	"fmt"
	"sort"
	"strings"
)

// MissingRef names one file a repo-authoritative resolution needed but
// could not find. Path is where ethos looked in the repo layer — the
// place `ethos vendor` would put it — so the diagnostic tells the user
// what to fix, not merely that something is wrong.
type MissingRef struct {
	Kind string `json:"kind"` // "personalities", "writing-styles", "talents", "ext"
	Slug string `json:"slug"`
	Path string `json:"path"`
}

// String renders one miss as "{kind}/{slug}".
func (m MissingRef) String() string {
	return m.Kind + "/" + m.Slug
}

// ErrIncompleteRepoSet reports that a repo-only store cannot fully
// resolve a handle: the repo vendored the identity but not everything it
// references (DES-057 Part A).
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

// newIncompleteRepoSet builds the aggregate error, or nil when nothing
// is missing. Refs are sorted by kind then slug so the message is stable
// across runs — an unstable diagnostic is unusable in CI output.
func newIncompleteRepoSet(handle string, missing []MissingRef) error {
	if len(missing) == 0 {
		return nil
	}
	sorted := append([]MissingRef(nil), missing...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Kind != sorted[j].Kind {
			return sorted[i].Kind < sorted[j].Kind
		}
		return sorted[i].Slug < sorted[j].Slug
	})
	return &ErrIncompleteRepoSet{Handle: handle, Missing: sorted}
}
