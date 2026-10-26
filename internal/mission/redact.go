package mission

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"unicode/utf8"

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
// No redaction method (Text, Path, Content) returns an error: once a redactor
// exists, a caller cannot end up writing unredacted content because redaction
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
	return untilStable(s, r.textOnce)
}

func (r PathRedactor) textOnce(s string) string {
	s = replacePrefix(s, r.Repo, "<repo>")
	s = replacePrefix(s, r.Home, "~")
	return replaceDashHome(s, r.Home)
}

// maxRedactRounds bounds untilStable. A pass that still changes its own output
// after this many rounds is not converging.
const maxRedactRounds = 8

// untilStable applies pass until the output stops changing, so that a pass
// applied to its own output is the identity: a placeholder one round writes
// can open a path the next round would read (Content of a@b.co-home-zed is
// [redacted-email]-home-zed after the first round, and the second round's Text
// writes [redacted-email]-~). If the bound is hit it
// returns the placeholder for a path that could not be redacted, not a
// half-redacted string; no redaction method (Text, Path, Content) returns an error.
func untilStable(s string, pass func(string) string) string {
	for i := 0; i < maxRedactRounds; i++ {
		t := pass(s)
		if t == s {
			return s
		}
		s = t
	}
	return unstablePath
}

// unstablePath stands for a value no redaction round settled on.
const unstablePath = "[redacted-path]"

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
	// A match consumes the dash that follows it, so a second home right after
	// the first (-home-zed-home-zed) is found only on the next round: repeat
	// until nothing changes, or a second pass over the output would still
	// rewrite. Each round that changes the text replaces at least one
	// occurrence of the dash home and creates none, so it ends.
	for {
		t := re.ReplaceAllString(s, "${1}-~${2}")
		if t == s {
			return s
		}
		s = t
	}
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
	return untilStable(s, func(x string) string { return redact.Prose(r.Text(x)) })
}

// Placeholders a path-tagged value carries after Path, and what they stand
// for. <repo>, ~ and -~ are this machine's repo root, home directory and
// dash-encoded home; Expand turns them back. redactedUser stands for
// another user's home and expands to nothing.
const (
	redactedUser     = "~[redacted-user]"
	redactedUserSlug = "-~[redacted-user]-"
)

// HasRedactedUser reports whether s holds the token that replaced another
// user's home directory. Such a value names a path on a machine this one
// cannot see, so nothing that compares paths may treat it as a match.
func HasRedactedUser(s string) bool {
	return strings.Contains(s, redactedUser)
}

// Path redacts a value the system acts on, such as a tool grant, a
// require_read entry or an input file: the repo root, the home directory
// and the dash-encoded home of this machine become <repo>, ~ and -~, and
// another user's home becomes a token that cannot be expanded. No credential
// or address pass runs: those would rewrite the value and change what it
// enforces.
//
// A prefix is rewritten where a path begins: at the start of the value,
// after a character that cannot be part of a name (a parenthesis, space, =,
// :, comma, quote), and after the first slash of a Claude Code permission
// rule's //path. It is never rewritten inside a relative path, so
// testdata/home/alice/x stays what it was: it names a different file than
// /home/alice/x, and rewriting it would change what the value authorizes.
// Expand reverses the local placeholders.
func (r PathRedactor) Path(s string) string {
	return untilStable(s, r.pathOnce)
}

func (r PathRedactor) pathOnce(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if tok, n := r.pathTokenAt(s, i); n > 0 {
			b.WriteString(tok)
			i += n
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// pathTokenAt returns the placeholder for the path that begins at s[i] and
// the number of bytes it replaces, or 0 when none begins there.
func (r PathRedactor) pathTokenAt(s string, i int) (string, int) {
	if pathBoundary(s, i, false) {
		if n := prefixLen(s, i, r.Repo); n > 0 {
			return "<repo>", n
		}
		if n := prefixLen(s, i, r.Home); n > 0 {
			return "~", n
		}
		if loc := foreignHome.FindStringIndex(s[i:]); loc != nil {
			return redactedUser, loc[1]
		}
	}
	if s[i] == '-' && pathBoundary(s, i, true) {
		if n := dashPrefixLen(s, i, r.Home); n > 0 {
			return "-~", n
		}
		if loc := foreignSlug.FindStringIndex(s[i:]); loc != nil {
			return redactedUserSlug, loc[1]
		}
	}
	return "", 0
}

// foreignHome matches /home/<name> and /Users/<name>, the homes of users
// other than the current one. foreignSlug matches their dash-encoded form
// -home-<name>-..., which Claude Code uses for a project directory.
var (
	//
	// <name> is any run of characters but a slash, whitespace, a quote or a
	// closing bracket or separator. Case is folded, and a doubled slash or
	// /./ between the parts is skipped, as the filesystem would.
	foreignHome = regexp.MustCompile("^(?i:/home|/Users)/+(?:\\./+)*[^\\s/\"'`)\\]:,;]+")
	foreignSlug = regexp.MustCompile("^-(?i:home|Users)-[^\\s/\"'`)]+")
)

// Expand reverses Path for this machine: <repo>, ~ and -~ at a path
// boundary become the repo root, the home directory and the dash-encoded
// home. The token for another user's home is left as it is. It returns an
// error when a placeholder needs a root this redactor does not have.
func (r PathRedactor) Expand(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		if pathBoundary(s, i, false) {
			switch {
			case strings.HasPrefix(s[i:], "<repo>") && endsPath(s, i+len("<repo>"), false):
				if r.Repo == "" {
					return "", fmt.Errorf("expanding %q: the repo root is unknown", s)
				}
				b.WriteString(r.Repo)
				i += len("<repo>")
				continue
			case s[i] == '~' && endsPath(s, i+1, false):
				if r.Home == "" {
					return "", fmt.Errorf("expanding %q: the home directory is unknown", s)
				}
				b.WriteString(r.Home)
				i++
				continue
			}
		}
		if s[i] == '-' && pathBoundary(s, i, true) && strings.HasPrefix(s[i:], "-~") && endsPath(s, i+2, true) {
			if r.Home == "" {
				return "", fmt.Errorf("expanding %q: the home directory is unknown", s)
			}
			b.WriteString(dashOf(r.Home))
			i += 2
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String(), nil
}

// ExpandPath is Expand for this machine, against repoRoot and the current
// home directory. The home is looked up only when s has a ~ to expand, so a
// value without placeholders never fails for want of one.
func ExpandPath(s, repoRoot string) (string, error) {
	r := PathRedactor{}
	if usablePrefix(repoRoot) {
		r.Repo = filepath.Clean(repoRoot)
	}
	if strings.Contains(s, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expanding %q: resolving home directory: %w", s, err)
		}
		if usablePrefix(home) {
			r.Home = filepath.Clean(home)
		}
	}
	return r.Expand(s)
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// isNameByte reports whether c can be part of a path component's name.
func isNameByte(c byte) bool {
	return isLetter(c) || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-'
}

// pathBoundary reports whether a path may begin at s[i]: at the start, after
// a character that is neither a name byte nor a slash, and after a run of
// slashes that itself follows such a character (//path, ///path,
// file:///path). It never begins right after a placeholder, so Path leaves
// what it wrote alone when it runs again. With slash, any slash qualifies,
// for a dash-encoded home, which is a whole path component.
func pathBoundary(s string, i int, slash bool) bool {
	if i == 0 {
		return true
	}
	if s[i-1] != '/' {
		return !isNameByte(s[i-1]) && !afterPlaceholder(s, i)
	}
	if slash {
		return true
	}
	// A longer run is not a path anyone wrote; the writer's leak check
	// refuses it if it hides a home, and the bound keeps this linear.
	j := i - 1
	for n := 0; j > 0 && s[j-1] == '/'; n++ {
		if n == maxSlashRun {
			return false
		}
		j--
	}
	return j == 0 || (!isNameByte(s[j-1]) && !afterPlaceholder(s, j))
}

// maxSlashRun bounds how far back pathBoundary looks along a run of slashes.
const maxSlashRun = 16

// afterPlaceholder reports whether s[:i] ends with a placeholder Path wrote:
// a <repo>, ~, -~ or redacted-user token that itself begins a path. A ~ in the
// middle of a word is just a character.
func afterPlaceholder(s string, i int) bool {
	for _, tok := range []string{"<repo>", "~", "-~", redactedUser} {
		if strings.HasSuffix(s[:i], tok) {
			// -~ is the dash-encoded home only where the slug form writes it,
			// before a byte that ends a path (a slash and a dash included).
			// Anywhere else it is text; a ~ that follows it still begins a path,
			// as after any non-name byte.
			if tok == "-~" && !endsPath(s, i, true) {
				continue
			}
			k := i - len(tok)
			if k == 0 || !isNameByte(s[k-1]) {
				return true
			}
		}
	}
	return false
}

// endsPath reports whether a path ends at s[i]: at the end of s or before a
// slash, ) ] : , ; whitespace or a quote. A dash-encoded home also ends before -.
func endsPath(s string, i int, dash bool) bool {
	if i >= len(s) {
		return true
	}
	switch s[i] {
	case '/', ')', ']', ':', ',', ';', ' ', '\t', '\n', '\r', '"', '\'', '`':
		return true
	}
	return dash && s[i] == '-'
}

// prefixLen is len(prefix) when s[i:] starts with prefix and the path ends
// there, else 0.
func prefixLen(s string, i int, prefix string) int {
	if prefix == "" || !hasPathPrefix(s[i:], prefix) || !endsPath(s, i+len(prefix), false) {
		return 0
	}
	return len(prefix)
}

// PathCaseFold says whether path prefixes compare without regard to case: on
// macOS, whose default filesystem treats /Users/Zed and /Users/zed as one
// directory. A variable so a test can set it on any system.
var PathCaseFold = runtime.GOOS == "darwin"

// SamePath reports whether a and b are the same path under the prefix rule:
// equal, or equal ignoring case where PathCaseFold is set.
func SamePath(a, b string) bool {
	if PathCaseFold {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// hasPathPrefix is strings.HasPrefix, folding case where PathCaseFold is set.
func hasPathPrefix(s, prefix string) bool {
	if PathCaseFold {
		return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
	}
	return strings.HasPrefix(s, prefix)
}

// dashOf is the dash-encoded form of a directory: /home/alice is
// -home-alice.
func dashOf(dir string) string {
	return strings.ReplaceAll(dir, string(filepath.Separator), "-")
}

// dashPrefixLen is prefixLen for the dash-encoded home: the slug ends before a
// dash, a byte that ends a path, or the end of s.
func dashPrefixLen(s string, i int, home string) int {
	dash := dashOf(home)
	if home == "" || dash == "" || dash == "-" || !strings.HasPrefix(s[i:], dash) || !endsPath(s, i+len(dash), true) {
		return 0
	}
	return len(dash)
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
	out, _ := r.ValueFlag(v)
	return out
}

// ValueFlag is Value also reporting whether any map key was renamed. Keys that
// pass to one name never merge: see redact.RenameKeys.
func (r PathRedactor) ValueFlag(v any) (any, bool) {
	switch x := v.(type) {
	case string:
		return r.Text(x), false
	case map[string]any:
		inner := false
		out, renamed := redact.RenameKeys(x, r.Text, false, func(_ string, vv any) any {
			o, f := r.ValueFlag(vv)
			inner = inner || f
			return o
		})
		return out, renamed || inner
	case []any:
		out := make([]any, len(x))
		changed := false
		for i, vv := range x {
			var f bool
			out[i], f = r.ValueFlag(vv)
			changed = changed || f
		}
		return out, changed
	default:
		return v, false
	}
}

// Map redacts every string value reachable from m. Returns nil for nil
// so a caller can distinguish "no input" from "empty input".
func (r PathRedactor) Map(m map[string]any) map[string]any {
	out, _ := r.MapFlag(m)
	return out
}

// MapFlag is Map also reporting whether any key, at any depth, was renamed.
func (r PathRedactor) MapFlag(m map[string]any) (map[string]any, bool) {
	if m == nil {
		return nil, false
	}
	v, renamed := r.ValueFlag(m)
	out, _ := v.(map[string]any)
	return out, renamed
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

func zeroOf[T any]() T {
	var z T
	return z
}

// safeFieldName is a field path fit for an error: quoted, or a stand-in when
// it names a home.
func safeFieldName(name string) string {
	if processLeaks(name) {
		return "(a field whose name is a home path)"
	}
	return fmt.Sprintf("%q", name)
}

// Announcement says, when called after its write has succeeded, that the
// redaction passes rewrote what was written. A write that is refused prints
// only the refusal, so the notice is not printed when the record is built.
type Announcement func()

// Say prints the notice. The zero Announcement says nothing.
func (a Announcement) Say() {
	if a != nil {
		a()
	}
}

// redactNoRename is the RenameStatus of a write that renamed no key.
var redactNoRename redact.RenameStatus

// announceRewritten returns the Announcement of a write the passes changed. It
// names the kind of file and never a value: the value is what was removed.
func announceRewritten(what string, st redact.RenameStatus) Announcement {
	suffix := ""
	switch {
	case st.Suffixed:
		suffix = "; a map key was renamed (name#N) so that no two keys merged"
	case st.Renamed:
		suffix = "; a map key was renamed"
	}
	return func() {
		fmt.Fprintf(os.Stderr, "ethos: redact: %s was rewritten before it was stored (a path, credential or address removed)%s\n", what, suffix)
	}
}

// refuseOwnDashHome refuses a repo-relative path entry holding this machine's
// dash home whose value is not already a path entry of prior (in any path
// field; prior is nil for a new contract). Such an entry is compared literally by its readers (the
// write-set walk, the containment check, the conflict scan), so it cannot be
// rewritten to -~; it must name the file without the home slug. It applies when
// a contract is created or its entries are changed, not to the records of a
// mission in flight, whose entries are rewritten to -~ as before. An absolute
// path keeps the rewrite, which Expand reverses, and so does free text.
func refuseOwnDashHome(repoRoot string, c, prior *Contract) error {
	r, err := NewPathRedactor(repoRoot)
	if err != nil {
		return nil // the writer refuses with its own error
	}
	var had []string
	if prior != nil {
		for _, fs := range redact.FieldStrings(*prior) {
			if fs.Path {
				had = append(had, fs.Value)
			}
		}
	}
	for _, fs := range redact.FieldStrings(*c) {
		if fs.Path && !slices.Contains(had, fs.Value) && redact.RepoRelative(fs.Value) && redact.HoldsOwnDashHome(fs.Value, r.Home) {
			return fmt.Errorf("redacting record: field %s holds this machine's dash-encoded home; the entry must name the file without the home slug", safeFieldName(fs.Name))
		}
	}
	return nil
}

// RedactRecord is RedactRecordNotify without the announcement: for a caller
// that inspects the redacted record and writes nothing. A writer uses
// RedactRecordNotify and says the announcement after its write.
func RedactRecord[T any](repoRoot string, v T) (T, error) {
	out, _, err := RedactRecordNotify(repoRoot, v)
	return out, err
}

// RedactRecordNotify returns a copy of v with every string field passed
// through Content, except fields tagged `redact:"keep"` (IDs, hashes,
// enums, timestamps) and fields tagged `redact:"handle"` that hold an identity
// handle (a value that is not one is passed through Content), and the
// Announcement to say once the write succeeded. It is the one entry point the
// record writers use.
//
// It fails closed: when no usable redactor can be built the error is
// returned and the caller must refuse the write.
func RedactRecordNotify[T any](repoRoot string, v T) (T, Announcement, error) {
	r, err := NewPathRedactor(repoRoot)
	if err != nil {
		return zeroOf[T](), nil, fmt.Errorf("redacting record: %w", err)
	}
	// A key or string that is not valid UTF-8 would be stored as U+FFFD by the
	// encoder: refused, not rewritten.
	for _, fs := range redact.FieldStrings(v) {
		if !utf8.ValidString(fs.Value) {
			return zeroOf[T](), nil, fmt.Errorf("redacting record: field %s is not valid UTF-8; remove or rephrase it", safeFieldName(fs.Name))
		}
	}
	out, renamed := redact.RecordPathsStatus(v, r.Content, r.Path)
	var say Announcement
	if renamed.Renamed || !reflect.DeepEqual(out, v) {
		say = announceRewritten(fmt.Sprintf("a %T record", v), renamed)
	}
	// Whatever the passes missed is refused, not written. The error names
	// the field and not the value: the value is the leak.
	for _, fs := range redact.FieldStrings(out) {
		if rule := r.LeakRuleFor(fs.Value, fs.Path); rule != "" {
			return zeroOf[T](), nil, fmt.Errorf("redacting record: field %s %s; remove or rephrase it", fs.Name, rule)
		}
	}
	return out, say, nil
}

// RedactedYAML is RedactedYAMLNotify without the announcement.
func RedactedYAML[T any](repoRoot string, v T, checks ...func(redacted T) error) ([]byte, error) {
	data, _, err := RedactedYAMLNotify(repoRoot, v, checks...)
	return data, err
}

// RedactedYAMLNotify is RedactRecordNotify followed by yaml.Marshal: the one
// way a record becomes the YAML bytes of a tracked file. Contracts, results,
// reflections, delegation records and `ethos mission export` all call it,
// so there is no marshal step between a record and the disk that skipped
// the redactor. An error means the caller must not write; the Announcement
// is said after the write succeeds.
//
// Each check runs on the redacted record before it is marshaled, so a
// caller can refuse a record whose redacted form is no longer valid.
func RedactedYAMLNotify[T any](repoRoot string, v T, checks ...func(redacted T) error) ([]byte, Announcement, error) {
	r, say, err := RedactRecordNotify(repoRoot, v)
	if err != nil {
		return nil, nil, err
	}
	for _, check := range checks {
		if err := check(r); err != nil {
			return nil, nil, err
		}
	}
	data, err := yaml.Marshal(&r)
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling record: %w", err)
	}
	return data, say, nil
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

// Leaks reports whether s still names this machine's home or any user's
// home, by the broad check every writer applies to what its passes made.
func (r PathRedactor) Leaks(s string) bool { return r.LeakRule(s) != "" }

// LeakRule is Leaks naming the rule that matched, without quoting s.
func (r PathRedactor) LeakRule(s string) string { return redact.LeakRule(s, r.Home) }

// pathKind is how a path field value relates to the checkout.
type pathKind int

const (
	pathElsewhere  pathKind = iota // not a plain file of the checkout or of this home
	pathInCheckout                 // repo-relative, or <repo>/ and a repo-relative path
	pathUnderHome                  // ~/ and a repo-relative path
)

// kindOf classifies a path field value. A value of kind pathInCheckout or
// pathUnderHome is one that neither the writer's path pass nor redact.HomePaths
// would change (so a value the writer rewrites always meets the full rule) and
// that has no absolute home glued into it (see gluedHome). app/home/page.tsx,
// @types/home/page.tsx and ~/x-wt/app/home/page.tsx qualify: under this
// machine's own home, a directory called home is not another user's home.
func (r PathRedactor) kindOf(s string) pathKind {
	kind := pathInCheckout
	rel, ok := strings.CutPrefix(s, "<repo>/")
	if !ok {
		if rest, tilde := strings.CutPrefix(s, "~/"); tilde {
			rel, kind = rest, pathUnderHome
		} else {
			rel = s
		}
	}
	if redact.RepoRelative(rel) && r.Path(s) == s && redact.HomePaths(s) == s && !gluedHome(rel) {
		return kind
	}
	return pathElsewhere
}

// gluedHome reports whether s holds a /home/ or /Users/ with a flag or glob
// byte before it, or at its start: a value beginning with - or +, or one where
// the byte before it is -, +, @ or *. -I/home/bob/inc, x-/home/bob/y, -/home/0,
// x@/home/bob/y and */home/bob/y are an absolute home with a prefix. HomePaths
// already rewrites these, so the @, * and start-of-value clauses change no
// output today; they are belt and braces against a change to HomePaths.
// @types/home/page.tsx is a file: the byte before /home/ is a letter.
func gluedHome(s string) bool {
	low := strings.ToLower(s)
	flag := low != "" && (low[0] == '-' || low[0] == '+')
	for _, h := range []string{"/home/", "/users/"} {
		for i := 0; ; {
			j := strings.Index(low[i:], h)
			if j < 0 {
				break
			}
			at := i + j
			if flag || at == 0 || strings.ContainsRune("-+@*", rune(low[at-1])) {
				return true
			}
			i = at + 1
		}
	}
	return false
}

// AuditLeakRule is the check of the audit log's sinks (the writer and the seal
// guard): redact.LeakRule, as for every string, except for a path field value
// that names a file. A repo-relative or <repo>/ value (pathInCheckout) meets
// redact.InsideRule; a ~/ value (pathUnderHome) meets redact.HomeRelativeRule.
// Both skip the mid-path /home/<name> and /Users/<name> rule and use the
// dotless-drive-letter slug-end rule; this machine's own home and slug are
// still refused.
func (r PathRedactor) AuditLeakRule(s string, path bool) string {
	if !path {
		return redact.LeakRule(s, r.Home)
	}
	switch r.kindOf(s) {
	case pathInCheckout:
		return redact.InsideRule(s, r.Home)
	case pathUnderHome:
		return redact.HomeRelativeRule(s, r.Home)
	}
	return redact.LeakRule(s, r.Home)
}

// LeakRuleFor is LeakRule for a value in a path-tagged field when path is
// true: a repo-relative value there (redact.RepoRelative) is judged by
// redact.OwnHomeRule, this machine's home and its dash-encoded slug and nothing
// else: no foreign-slug rule and no flag-or-glob rule (bead ethos-ka73). The
// audit log uses AuditLeakRule.
func (r PathRedactor) LeakRuleFor(s string, path bool) string {
	if path && redact.RepoRelative(s) {
		return redact.OwnHomeRule(s, r.Home)
	}
	return redact.LeakRule(s, r.Home)
}

// processLeaks is leaks for the current process's home directory, for the
// writers that are not handed a redactor.
func processLeaks(s string) bool { return newLeakChecker().rule(s) != "" }

// safeErr makes err fit for a refusal message: a parser error quotes the
// text it choked on, which can be a home path or a credential. The message
// goes through the content pass, and is withheld if it still names a home.
func safeErr(err error) error {
	msg := redact.Prose(err.Error())
	if processLeaks(msg) {
		msg = "details withheld: the message names a home directory"
	}
	return errors.New(msg)
}

// HasHomePlaceholder reports whether s holds a ~ or -~ that Expand would
// replace with the home directory.
func HasHomePlaceholder(s string) bool {
	const home = "\x00home"
	out, err := PathRedactor{Home: home, Repo: "\x00repo"}.Expand(s)
	return err != nil || strings.Contains(out, home)
}
