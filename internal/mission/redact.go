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
// No method on this type returns an error: once a redactor exists, a
// caller cannot end up writing unredacted content because redaction
// failed halfway. Deciding whether a usable redactor can be built at
// all is NewPathRedactor's job, and that is the one place the
// fail-closed refusal happens.
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
func (r PathRedactor) Text(s string) string {
	s = replacePrefix(s, r.Repo, "<repo>")
	return replacePrefix(s, r.Home, "~")
}

// replacePrefix rewrites every occurrence of prefix in s to token,
// skipping matches where the text continues the same path component.
// A plain substring replacement turns /w/repo2/file into <repo>2/file
// when the prefix is /w/repo — it corrupts a sibling directory and
// hides which path was really named.
//
// A skipped repo match is not a leak: the home substitution runs
// second over the same string, so /w/repo2/file still comes out as
// ~/repo2/file when the repo sits under home, which is the case that
// carries the username.
func replacePrefix(s, prefix, token string) string {
	if prefix == "" {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, prefix)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := i + len(prefix)
		if continuesComponent(s[end:]) {
			b.WriteString(s[:end])
			s = s[end:]
			continue
		}
		b.WriteString(s[:i])
		b.WriteString(token)
		s = s[end:]
	}
}

// continuesComponent reports whether rest extends the path component
// a prefix match just ended on. Only characters that make the match a
// different directory count: letters, digits, '-', '_', and any
// non-ASCII byte, which in UTF-8 can only be part of a letter.
//
// A '.' terminates rather than continues, so a prompt ending "fix
// /w/repo." is redacted. That over-redacts a sibling named
// /w/repo.bak into <repo>.bak, which costs precision; treating '.' as
// a continuation would instead leave a full absolute path with the
// operator's username in prose, which is the defect this type exists
// to prevent. Where the two directions conflict, redact.
func continuesComponent(rest string) bool {
	if rest == "" {
		return false
	}
	switch c := rest[0]; {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '-', c == '_', c >= 0x80:
		return true
	default:
		return false
	}
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
// — still applies. <repo> is portability, not privacy, so an unusable
// repoRoot disables that one substitution instead of failing the
// write. It is also normalized: Text matches an exact prefix, so a
// repoRoot carrying a trailing slash would match nothing and quietly
// leave repo paths tagged with ~ instead of <repo>. ETHOS_REPO_ROOT is
// operator-supplied and can arrive in either shape.
func NewPathRedactor(repoRoot string) (PathRedactor, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return PathRedactor{}, fmt.Errorf("resolving home directory for path redaction: %w", err)
	}
	if !usablePrefix(home) {
		return PathRedactor{}, fmt.Errorf(
			"home directory %q is unusable for path redaction (want an absolute path below the root)", home)
	}
	repo := ""
	if usablePrefix(repoRoot) {
		repo = filepath.Clean(repoRoot)
	}
	return PathRedactor{Home: filepath.Clean(home), Repo: repo}, nil
}

// usablePrefix reports whether p can serve as a redaction prefix. It
// must be absolute — a relative prefix matches arbitrary substrings —
// and it must sit below the filesystem root, because a prefix of "/"
// rewrites the leading separator of every absolute path and turns
// /etc/hosts into ~etc/hosts. filepath.Clean("") is ".", so the empty
// string is rejected here rather than cleaned into a prefix that
// matches every "." in every string.
func usablePrefix(p string) bool {
	return p != "" && filepath.IsAbs(p) && filepath.Clean(p) != string(filepath.Separator)
}
