package mission

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PathRedactor rewrites machine-specific absolute path prefixes into
// portable tokens. $HOME/X becomes ~/X and <repoRoot>/X becomes
// <repo>/X.
//
// Every artifact ethos writes into a git-tracked tree passes through
// one of these: the PostToolUse audit lines (internal/hook), the Tier B
// delegation prompt.md, and the delegation record. Absolute paths in
// shared history leak the original operator's username and machine
// layout forever, and those trees are pushed to public repos.
//
// A zero Repo or Home disables that substitution rather than erroring.
// The redactor holds no state beyond the two prefixes, so it never
// fails — a caller cannot end up writing unredacted content because
// redaction returned an error. That is the fail-closed property: there
// is no error path to fall through.
type PathRedactor struct {
	Home string
	Repo string
}

// Text replaces every occurrence of the repo and home prefixes inside
// s with their portable tokens. Repo is checked first so a repo nested
// inside home (the common case) is tagged <repo>/X, not ~/<rel>/X.
// Both prefixes are replaced globally so one string embedding several
// paths — a Bash command, a delegation prompt — gets every one
// rewritten.
//
// The trailing-slash form is replaced first; the bare form is replaced
// second so a path that ends exactly at the root (no trailing slash,
// e.g. `cd <repoRoot>`) also gets the token.
func (r PathRedactor) Text(s string) string {
	if r.Repo != "" {
		s = strings.ReplaceAll(s, r.Repo+"/", "<repo>/")
		s = strings.ReplaceAll(s, r.Repo, "<repo>")
	}
	if r.Home != "" {
		s = strings.ReplaceAll(s, r.Home+"/", "~/")
		s = strings.ReplaceAll(s, r.Home, "~")
	}
	return s
}

// Body redacts a file body. Returns nil for nil so an absent optional
// artifact stays absent.
func (r PathRedactor) Body(b []byte) []byte {
	if b == nil {
		return nil
	}
	return []byte(r.Text(string(b)))
}

// Value redacts v recursively. Strings are rewritten via Text; maps
// and slices recurse; other types pass through unchanged. The input is
// never mutated.
func (r PathRedactor) Value(v any) any {
	switch x := v.(type) {
	case string:
		return r.Text(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = r.Value(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = r.Value(vv)
		}
		return out
	default:
		return v
	}
}

// Map redacts every string value reachable from m. Returns nil for nil
// so a caller can distinguish "no input" from "empty input".
func (r PathRedactor) Map(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out, _ := r.Value(m).(map[string]any)
	return out
}

// NewPathRedactor builds the redactor for a write into a git-tracked
// tree. Every such write — the delegation prompt and record here, the
// audit lines in internal/hook — resolves its home prefix through this
// one function so the two paths cannot disagree about what counts as
// redactable.
//
// Resolving the home directory is a precondition, not a best effort: a
// home directory ethos cannot name is one it cannot redact, and
// writing the content anyway would put the operator's username into
// shared history. Callers propagate the error and refuse the write.
//
// The value is checked, not just the error. os.UserHomeDir returns
// whatever $HOME holds without validating it, and it returns "/" on
// ios rather than an error. A relative or root home would not fail —
// it would silently produce a redactor that rewrites the leading "/"
// of every absolute path, turning /etc/hosts into ~etc/hosts. Refusing
// an unusable prefix keeps the failure at the one place that can
// report it.
//
// repoRoot may be empty at a call site that knows only the artifact's
// own path; the home substitution — the one that carries the username
// — still applies. <repo> is portability, not privacy.
func NewPathRedactor(repoRoot string) (PathRedactor, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return PathRedactor{}, fmt.Errorf("resolving home directory for path redaction: %w", err)
	}
	if !filepath.IsAbs(home) || filepath.Clean(home) == string(filepath.Separator) {
		return PathRedactor{}, fmt.Errorf(
			"home directory %q is unusable for path redaction (want an absolute path below the root)", home)
	}
	return PathRedactor{Home: filepath.Clean(home), Repo: repoRoot}, nil
}
