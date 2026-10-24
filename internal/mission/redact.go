package mission

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/redact"
)

// PathRedactor rewrites machine-specific absolute path prefixes into
// portable tokens. $HOME/X becomes ~/X and <repoRoot>/X becomes
// <repo>/X.
//
// Every record ethos writes into a tracked tree is built from a
// PathRedactor: contracts, results, reflections, event logs, the
// missions.jsonl trace, `ethos mission export`, and the delegation
// prompt and record go through RedactRecord and Content; the PostToolUse
// audit lines (internal/hook) go through NewPathRedactor and the shared
// internal/redact passes. TestTrackedWritersAreRedacted enumerates the
// writers and fails when one is added without a redactor, so this comment
// is checked, not trusted. Absolute paths, addresses, and credentials in
// shared history leak forever, and those trees are pushed to public
// repos.
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
	s = replacePrefix(s, r.Home, "~")
	return replaceDashHome(s, r.Home)
}

// replaceDashHome rewrites the dash-encoded home directory Claude Code
// uses in project slugs (/var/home/zed becomes -var-home-zed) to -~. The
// slug carries the username exactly as the path does. /home/<name> and
// /Users/<name> are also caught without knowing the home, by
// redact.HomePaths; this covers any other home layout.
func replaceDashHome(s, home string) string {
	dash := strings.ReplaceAll(home, string(filepath.Separator), "-")
	if home == "" || dash == "" || dash == "-" || !strings.Contains(s, dash) {
		return s
	}
	re := regexp.MustCompile(`(^|[^A-Za-z0-9_.\-])` + regexp.QuoteMeta(dash) + `(-|[^A-Za-z0-9_.]|$)`)
	return re.ReplaceAllString(s, "${1}-~${2}")
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

// Content redacts free text bound for a tracked file: paths first, then
// the shared credential, email and foreign-home passes in
// internal/redact. Text alone is for values known to be paths.
func (r PathRedactor) Content(s string) string {
	return redact.Prose(r.Text(s))
}

// Path redacts a value the system acts on, such as a tool grant or a
// write-set entry: home and repo prefixes, and any user's home, but no
// credential or address pass. Those passes would rewrite the value and
// change what it enforces.
func (r PathRedactor) Path(s string) string {
	return redact.HomePaths(r.Text(s))
}

// Body redacts a file body. Returns nil for nil so an absent optional
// artifact stays absent.
func (r PathRedactor) Body(b []byte) []byte {
	if b == nil {
		return nil
	}
	return []byte(r.Content(string(b)))
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

// RedactRecord returns a copy of v with every string field passed
// through Content, except fields tagged `redact:"keep"` (IDs, hashes,
// enums, timestamps). It is the one entry point the record writers use.
//
// It fails closed: when no usable redactor can be built the error is
// returned and the caller must refuse the write.
func RedactRecord[T any](repoRoot string, v T) (T, error) {
	r, err := NewPathRedactor(repoRoot)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("redacting record: %w", err)
	}
	return redact.RecordPaths(v, r.Content, r.Path), nil
}

// RedactedYAML is RedactRecord followed by yaml.Marshal: the one way a
// record becomes the YAML bytes of a tracked file. Contracts, results,
// reflections, delegation records and `ethos mission export` all call it,
// so there is no marshal step between a record and the disk that skipped
// the redactor. An error means the caller must not write.
//
// Each check runs on the redacted record before it is marshaled, so a
// caller can refuse a record whose redacted form is no longer valid.
func RedactedYAML[T any](repoRoot string, v T, checks ...func(redacted T) error) ([]byte, error) {
	r, err := RedactRecord(repoRoot, v)
	if err != nil {
		return nil, err
	}
	for _, check := range checks {
		if err := check(r); err != nil {
			return nil, err
		}
	}
	data, err := yaml.Marshal(&r)
	if err != nil {
		return nil, fmt.Errorf("marshaling record: %w", err)
	}
	return data, nil
}

// checkPathFields refuses a redacted contract in which any path-tagged
// field differs from the path pass applied alone to the original. Those
// fields are acted on, so the credential and email passes must never have
// touched them; this holds whether or not the contract validates.
func checkPathFields(repoRoot string, orig, red Contract) error {
	r, err := NewPathRedactor(repoRoot)
	if err != nil {
		return fmt.Errorf("building path redactor: %w", err)
	}
	want := redact.RecordPaths(orig, func(s string) string { return s }, r.Path)
	if diffs := redact.PathDiffs(want, red); len(diffs) > 0 {
		return fmt.Errorf("redaction altered enforcement fields: %s", strings.Join(diffs, ", "))
	}
	return nil
}
