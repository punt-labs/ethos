package enable

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

// gitignoreMarker heads the block enable appends so the patterns are
// identifiable as ethos-managed.
const gitignoreMarker = "# ethos: shared machine-local zone (never track)"

const legacyGitignoreMarker = "# ethos runtime (never track) — DES-058 live zone + mission locks"

// LocalIgnoreRule excludes the shared machine-local zone. Source-tree local
// overlays, locks, staging, traces and artifacts must be relocated by their
// writers, not hidden by additional ignore rules. Setup and vendor share this
// spelling; equivalent repository-owned coverage is also accepted.
const LocalIgnoreRule = ".punt-labs/local/"

// LocalIgnoreNote labels the canonical rule for setup and vendor callers.
const LocalIgnoreNote = "# ethos: shared machine-local zone (never track)"

// localIgnoreProbes names the directory, not a sample of possible filenames.
// Kept for the path-exclusion tests; protection uses gitLocalDirectoryExcluded.
var localIgnoreProbes = []string{".punt-labs/local"}

// ignoreRule is one pattern enable ensures the repo excludes: the pattern
// itself, the comment written above it, and whether it names a runtime zone.
type ignoreRule struct {
	pattern string
	note    string
	runtime bool
}

// gitignoreRules contains only the shared local boundary, independent of record
// mode. Nothing under the record/source tree gets a new ignore exception.
var gitignoreRules = []ignoreRule{
	machineLocalRule,
}

// machineLocalRule is the machine-local entry of gitignoreRules, named so
// LocalIgnored and enable cannot drift onto different boundaries.
var machineLocalRule = ignoreRule{
	pattern: LocalIgnoreRule,
	runtime: true,
}

// ensureGitignore makes the repo .gitignore cover the shared machine-local
// zone. It adds only the patterns the repo does not already
// exclude, so re-enable adds nothing (idempotent). When the ethos block already
// exists the missing patterns are inserted under that one marker — never a
// second marker block; otherwise a fresh marked block is appended. Existing
// user lines are left in place and never reordered. Recognized rules in the
// old ethos block are retired; a missing .gitignore is created.
//
// It returns the step action and a detail line for the report: "added" when it
// wrote, "already" when the repo was covered, and "attention" when a pattern's
// line is in the file but git does not honour it. Attention always includes an
// error: a defeated rule must not permit enablement or report success.
func ensureGitignore(repoRoot string, p layout.Policy) (action, detail string, err error) {
	return ensureGitignoreWithIO(repoRoot, p, defaultGitignoreIO())
}

func ensureGitignoreWithIO(repoRoot string, p layout.Policy, io gitignoreIO) (action, detail string, err error) {
	path := filepath.Join(repoRoot, ".gitignore")
	data, err := readIgnoreSource(repoRoot, ".gitignore")
	if err != nil && !os.IsNotExist(err) {
		return "", "", fmt.Errorf("reading .gitignore: %w", err)
	}

	original := data
	data, retired := removeLegacyManagedRules(data)
	if err := preflightIgnoreRetirement(repoRoot, retired); err != nil {
		return "", "", err
	}
	present := presentLines(data)
	markerIdx := -1
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.TrimRight(line, "\r") == gitignoreMarker {
			markerIdx = i
			break
		}
	}
	var missing []ignoreRule
	var patterns []string
	for _, r := range gitignoreRules {
		covered, err := LocalIgnored(repoRoot)
		if err != nil {
			return "", "", err
		}
		// Git still sees the old on-disk block until publication. Do not let
		// a rule we are retiring suppress the new shared-zone exclusion.
		if covered && (bytes.Equal(original, data) || present[r.pattern]) {
			continue
		}
		// The pattern is uncovered but its literal line is already in the
		// file: a later negation or an override defeats it, and appending a
		// second copy of the same line would not change git's answer — it
		// would just grow the file on every run. Report it instead; only a
		// human can resolve the conflict.
		if present[r.pattern] {
			continue
		}
		missing = append(missing, r)
		patterns = append(patterns, r.pattern)
	}
	if len(missing) == 0 {
		if !bytes.Equal(original, data) {
			if err := writeGitignoreWithIO(path, data, p, io); err != nil {
				return "", "", err
			}
		}
		if err := verifyLocalProtection(repoRoot, p, io); err != nil {
			return "attention", err.Error(), err
		}
		if !bytes.Equal(original, data) {
			return "updated", "retired recognized ethos-managed legacy ignore rules", nil
		}
		return "already", ".gitignore already ignores the shared machine-local zone", nil
	}

	// Preserve the file's existing line ending so the block we add matches.
	eol := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		eol = "\r\n"
	}
	cr := strings.TrimSuffix(eol, "\n")
	var ins []string
	for _, r := range missing {
		if r.note != "" {
			ins = append(ins, r.note+cr)
		}
		ins = append(ins, r.pattern+cr)
	}

	var out string
	if markerIdx >= 0 {
		// Add the missing patterns under the existing marker — one block, no
		// duplicate comment. Existing lines keep their \r; joining on \n
		// reproduces the file's original line endings.
		merged := make([]string, 0, len(lines)+len(ins))
		merged = append(merged, lines[:markerIdx+1]...)
		merged = append(merged, ins...)
		merged = append(merged, lines[markerIdx+1:]...)
		out = strings.Join(merged, "\n")
	} else {
		var buf strings.Builder
		buf.Write(data)
		// Separate the appended block from existing content with a blank line,
		// and terminate an existing final line that lacks its newline.
		if len(data) > 0 {
			if !bytes.HasSuffix(data, []byte("\n")) {
				buf.WriteString(eol)
			}
			buf.WriteString(eol)
		}
		buf.WriteString(gitignoreMarker + strings.TrimSuffix(eol, "\n"))
		for _, line := range ins {
			buf.WriteString("\n" + line)
		}
		buf.WriteString("\n")
		out = buf.String()
	}

	if err := writeGitignoreWithIO(path, []byte(out), p, io); err != nil {
		return "", "", err
	}
	detail = "ignored " + strings.Join(patterns, ", ")
	if err := verifyLocalProtection(repoRoot, p, io); err != nil {
		return "attention", detail + "; " + err.Error(), err
	}
	return "added", detail, nil
}

// removeLegacyManagedRules retires only the exact rules in the contiguous block
// headed by our former marker. Identical rules elsewhere may be user-owned and
// are left alone. Unknown comments/patterns terminate the recognized block;
// broader legacy ignore cleanup and data migration require operator review.
func removeLegacyManagedRules(data []byte) ([]byte, []string) {
	lines := strings.SplitAfter(string(data), "\n")
	managed := false
	var retired []string
	var out strings.Builder
	for _, line := range lines {
		text := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if text == legacyGitignoreMarker {
			managed = true
			eol := "\n"
			if strings.HasSuffix(line, "\r\n") {
				eol = "\r\n"
			}
			out.WriteString(gitignoreMarker + eol)
			continue
		}
		if managed {
			switch text {
			case ".punt-labs/**/local/**", ".punt-labs/ethos/missions/**/*.lock",
				".punt-labs/**/*.ethos-tmp*", ".punt-labs/**/*.local.*",
				".punt-labs/ethos/**/*.local.yaml",
				".punt-labs/ethos/missions/*/delegations/*/prompt.md",
				".punt-labs/ethos/missions/*/reflections.yaml",
				".punt-labs/ethos/missions/*/log.jsonl",
				".punt-labs/ethos/missions/*/log-*.jsonl",
				".punt-labs/ethos/missions/*/log-*.quarantine",
				".punt-labs/ethos/missions/*/log-*.jsonl.corrupt*",
				".punt-labs/ethos/missions/*/artifacts/",
				".punt-labs/ethos/missions/*/artifacts/**",
				".punt-labs/ethos/sessions/*/audit.jsonl",
				".punt-labs/ethos/sessions/*/audit-*.jsonl",
				".punt-labs/ethos/sessions/*/audit-*.quarantine",
				".punt-labs/ethos/sessions/*/audit-*.jsonl.corrupt*":
				retired = append(retired, text)
				continue
			case "# ethos: machine-local files (.local.*) — may hold secrets, never track":
				continue
			default:
				managed = false
			}
		}
		out.WriteString(line)
	}
	return []byte(out.String()), retired
}

// preflightLegacyIgnores checks each host before EnableTo changes either ignore
// file. A linked checkout must not be updated before the shared store refuses.
func preflightLegacyIgnores(repoRoot string) error {
	data, err := readIgnoreSource(repoRoot, ".gitignore")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading .gitignore in %s: %w", repoRoot, err)
	}
	_, retired := removeLegacyManagedRules(data)
	return preflightIgnoreRetirement(repoRoot, retired)
}

// preflightIgnoreRetirement refuses while any existing path outside the new
// local zone matches a retiring rule. Git evaluates the old patterns, not a
// second glob implementation. Do not use --exclude-standard: an independent
// user rule must not conceal data still depending on the old layout, including
// other products' local files. This check reads names only, never private bytes.
// Old writers must be quiesced for migration; it cannot fence an old process
// creating another misplaced file after the scan.
func preflightIgnoreRetirement(repoRoot string, retired []string) error {
	if len(retired) == 0 {
		return nil
	}
	args := []string{"ls-files", "-z", "--cached", "--others", "--ignored"}
	for _, pattern := range retired {
		args = append(args, "--exclude="+pattern)
	}
	args = append(args, "--", ".punt-labs/")
	var stderr bytes.Buffer
	out, err := gitenv.Run(gitenv.Spec{
		Git: "git", Dir: repoRoot, Env: gitenv.Parsed(), Args: args, Stderr: &stderr,
	})
	if err != nil {
		return fmt.Errorf("checking legacy ignore protections in %s: %w: %s; refusing to retire rules", repoRoot, err, strings.TrimSpace(stderr.String()))
	}
	if stderr.Len() > 0 {
		// ls-files can exit zero after an unreadable-directory warning. That
		// is an incomplete scan, not proof that retirement is safe.
		return fmt.Errorf("checking legacy ignore protections in %s: %s; refusing to retire rules after an incomplete scan", repoRoot, strings.TrimSpace(stderr.String()))
	}
	seen := make(map[string]bool)
	var paths []string
	localChecked, localSafe := false, false
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" || seen[rel] {
			continue
		}
		if strings.HasPrefix(rel, LocalIgnoreRule) {
			if !localChecked {
				localSafe, err = unnegatedLocalIgnore(repoRoot)
				if err != nil {
					return err
				}
				localChecked = true
			}
			if localSafe {
				continue
			}
		}
		path := filepath.Join(repoRoot, filepath.FromSlash(rel))
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue // a tracked deletion is not existing private data
		} else if err != nil {
			return fmt.Errorf("examining legacy protected path %q: %w; refusing to retire rules", path, err)
		}
		seen[rel] = true
		paths = append(paths, fmt.Sprintf("%q", path))
	}
	if len(paths) == 0 {
		return nil
	}
	sort.Strings(paths)
	return fmt.Errorf("refusing to retire legacy ignore rules: existing protected paths require migration or operator cleanup (quiesce old writers, copy and verify before removal): %s; ignore files unchanged", strings.Join(paths, ", "))
}

// unnegatedLocalIgnore proves the replacement directory rule is sufficient for
// existing canonical-local files. A parent .gitignore can override that rule;
// conservatively refuse on any negation there rather than guessing its scope.
// Once the local directory itself is excluded, rules inside it cannot reinclude
// its children. This does not relax ensureGitignore's Git-based coverage check.
func unnegatedLocalIgnore(repoRoot string) (bool, error) {
	for _, rel := range []string{".gitignore", ".punt-labs/.gitignore"} {
		path := filepath.Join(repoRoot, filepath.FromSlash(rel))
		data, err := readIgnoreSource(repoRoot, rel)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("checking local ignore overrides in %q: %w; refusing to retire rules", path, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "!") {
				return false, nil
			}
		}
	}
	return true, nil
}

// readIgnoreSource reads a fixed repository-owned ignore source only when it is
// absent or a regular file. Lstat refuses links (including dangling links), FIFOs
// and other nonregular leaves before opening their payload. Internal ancestors
// must be physical directories; repoRoot is the physical root supplied by the
// caller, not an internal component to resolve. Like the vendored preflight,
// this assumes stable paths, not hostile concurrent filesystem replacement.
func readIgnoreSource(repoRoot, rel string) ([]byte, error) {
	parts := strings.Split(rel, "/")
	path := repoRoot
	for i, part := range parts {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("examining ignore source %s: %w", path, err)
		}
		if i < len(parts)-1 {
			if !info.IsDir() {
				return nil, fmt.Errorf("ignore source ancestor %s is not a physical directory; refusing to read", path)
			}
		} else if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("ignore source %s is not a regular file (symlinks are not allowed); refusing to read", path)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading ignore source %s: %w", path, err)
	}
	return data, nil
}

// presentLines indexes the .gitignore's lines for an exact-match lookup.
// Leading whitespace is significant in .gitignore, so an indented
// "  .punt-labs/**/local/**" is a different pattern — it matches paths that
// start with spaces, not the real files — and is not folded into the
// unindented key. A trailing \r (CRLF file) is stripped so a CRLF repo still
// matches.
func presentLines(data []byte) map[string]bool {
	lines := strings.Split(string(data), "\n")
	present := make(map[string]bool, len(lines))
	for _, line := range lines {
		present[strings.TrimRight(line, "\r")] = true
	}
	return present
}

// gitLocalDirectoryExcluded proves the directory itself (or an ancestor) is
// excluded. Git then cannot reinclude any child, whatever its name or extension.
// A finite list of filenames cannot establish that property.
//
// Ask for the bare name as well as its directory spelling: an absent directory
// will not match a directory-only rule without the slash. Conversely Git can
// match a trailing slash against synthetic contents. Even the same globstar rule
// can win for both queries while a directory-only negation leaves future children
// stageable. Only a literal final pattern component establishes that Git matched
// a named directory or ancestor, not synthetic contents. For a non-directory-only
// rule, both spellings must additionally agree on the winning rule's identity.
// This works without creating directories or probing sample filenames.
func gitLocalDirectoryExcluded(repoRoot string) (bool, error) {
	dir := strings.TrimSuffix(LocalIgnoreRule, "/")
	matches, err := gitIgnoreMatches(repoRoot, []string{dir, dir + "/"})
	if err != nil {
		return false, err
	}
	return gitDirectoryMatchesExclude(matches[dir], matches[dir+"/"]), nil
}

func gitDirectoryMatchesExclude(bare, directory gitIgnoreMatch) bool {
	if !directory.excludes() {
		return false
	}
	// Apply this criterion BEFORE the same-rule shortcut: trailing ** and
	// **/* can produce identical positive matches without excluding local.
	// Rather than interpret wildcard or escape syntax ourselves, require a
	// nonempty literal final component. It cannot match the slash query's
	// synthetic empty child. Git must have excluded a real named component;
	// once that directory is excluded, children cannot be reincluded. Wildcards
	// earlier in the pattern are harmless to this argument (e.g. **/local/).
	// Ambiguous alternatives get the canonical rule, not a guessed proof.
	pattern := strings.TrimSuffix(directory.pattern, "/")
	component := pattern[strings.LastIndex(pattern, "/")+1:]
	if component == "" || strings.ContainsAny(component, "*?[\\") {
		return false
	}
	return strings.HasSuffix(directory.pattern, "/") || directory == bare
}

// gitIgnoreMatch keeps the winning rule's identity, including negations and
// machine-only sources. The caller must check excludes before trusting a match.
// Pattern text alone is not identity: identical patterns at different source
// paths or line numbers can have different effects on directory spellings.
type gitIgnoreMatch struct {
	source  string
	line    int
	pattern string
}

func (m gitIgnoreMatch) excludes() bool {
	return m.pattern != "" && !strings.HasPrefix(m.pattern, "!") && travelsWithRepo(m.source)
}

// gitExcludes reports whether the repo's own ignore rules exclude every path
// in probes. Every one: a rule that excludes some of them is not coverage for
// a pattern that spans all of them.
//
// The matching rule must also live in the work tree. `git check-ignore` counts
// .git/info/exclude and core.excludesFile, and neither travels with the repo —
// a per-clone exclude on the operator's machine would suppress writing the
// committed rule, and the fresh clone that CI or a colleague makes would stage
// the secret (djb, review of PR #423). A negated pattern is not coverage
// either: git reports the match that applies, and a `!` match means the path
// is not ignored at all.
//
// core.excludesFile is disabled at the source rather than filtered out of the
// answer. Git prints the source as the configured path, so a relative
// core.excludesFile prints "myexcludes" — indistinguishable from a nested
// .gitignore in the work tree, and travelsWithRepo passed it (Copilot, review
// of PR #423). Pointing the setting at the null device makes git find no
// patterns there at all, whatever the spelling, and -c overrides the system,
// global, and repo config alike. travelsWithRepo still handles
// .git/info/exclude, which no config setting can turn off.
//
// --no-index keeps the answer about the rules alone, so a path that is somehow
// in the index does not read as "not ignored". check-ignore exits 1 when no
// path matches; any other failure means git could not answer and is returned
// as an error, never as permission to trust a literal pattern.
func gitExcludes(repoRoot string, probes []string) (bool, error) {
	matches, err := gitIgnoreMatches(repoRoot, probes)
	if err != nil {
		return false, err
	}
	for _, path := range probes {
		if !matches[path].excludes() {
			return false, nil
		}
	}
	return true, nil
}

// gitIgnoreMatches returns Git's winning rules for the requested paths. A missing
// Git is an error, not permission to trust a literal line despite overrides.
func gitIgnoreMatches(repoRoot string, paths []string) (map[string]gitIgnoreMatch, error) {
	var stdout, stderr bytes.Buffer
	_, err := gitenv.Run(gitenv.Spec{
		Git: "git", Dir: repoRoot,
		Args: []string{"-c", "core.excludesFile=" + os.DevNull,
			"check-ignore", "-v", "-z", "--no-index", "--stdin"},
		// The caller's environment, in the C locale: the "not a git
		// repository" message below is matched in English.
		Env:    gitenv.Parsed(),
		Stdin:  strings.NewReader(strings.Join(paths, "\x00") + "\x00"),
		Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil {
		var ee *exec.ExitError
		// Exit 1 is "no path matched" — an answer, and the answer is no.
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			if gitenv.IsNotARepo(stderr.String()) {
				return nil, nil // nothing in a non-repository is covered
			}
			return nil, fmt.Errorf("git check-ignore: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
	}
	if stderr.Len() > 0 {
		return nil, fmt.Errorf("git check-ignore: %s; cannot confirm local-zone protection", strings.TrimSpace(stderr.String()))
	}
	return parseGitIgnoreMatches(stdout.String())
}

func parseGitIgnoreMatches(output string) (map[string]gitIgnoreMatch, error) {
	// -v -z prints four NUL-terminated fields per match: source, line number,
	// pattern, pathname. Unmatched paths print nothing.
	fields := strings.Split(output, "\x00")
	if (len(fields)-1)%4 != 0 || fields[len(fields)-1] != "" {
		return nil, fmt.Errorf("git check-ignore: malformed response; cannot confirm local-zone protection")
	}
	matched := make(map[string]gitIgnoreMatch, len(fields)/4)
	for i := 0; i+3 < len(fields); i += 4 {
		source, number, pattern, path := fields[i], fields[i+1], fields[i+2], fields[i+3]
		line, err := strconv.Atoi(number)
		if err != nil || line <= 0 || source == "" || pattern == "" || path == "" {
			return nil, fmt.Errorf("git check-ignore: malformed rule; cannot confirm local-zone protection")
		}
		if _, duplicate := matched[path]; duplicate {
			return nil, fmt.Errorf("git check-ignore: duplicate path %q; cannot confirm local-zone protection", path)
		}
		matched[path] = gitIgnoreMatch{source: source, line: line, pattern: pattern}
	}
	return matched, nil
}

// travelsWithRepo reports whether an ignore source is part of the repo every
// clone gets. git prints the source relative to the repo root for a .gitignore
// inside the work tree — ".gitignore", ".punt-labs/.gitignore" — and the
// rejects here are the sources local to one machine: anything under a .git
// directory (info/exclude), which no config setting can disable, and any
// absolute path. The absolute case is belt and braces: gitExcludes already
// points core.excludesFile at the null device, because judging that setting by
// the path git prints misses a relative one.
//
// It does not require the file to be committed yet. The .gitignore ethos just
// wrote is untracked until the operator commits it, and demanding tracked
// status would make the very next run append the rule a second time.
func travelsWithRepo(source string) bool {
	if source == "" || filepath.IsAbs(source) {
		return false
	}
	for _, seg := range strings.Split(filepath.ToSlash(filepath.Clean(source)), "/") {
		if seg == ".git" || seg == ".." {
			return false
		}
	}
	return true
}

// LocalIgnored reports whether repository-owned rules exclude the shared local
// directory or an ancestor, not just particular filenames. Setup, vendor, enable
// and doctor use this same predicate. It neither creates paths nor trusts a
// literal rule when Git cannot confirm it.
func LocalIgnored(repoRoot string) (bool, error) {
	_, err := readIgnoreSource(repoRoot, ".gitignore")
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("reading .gitignore: %w", err)
	}
	return gitLocalDirectoryExcluded(repoRoot)
}

// EnsureLocalIgnored maintains only the shared local exclusion, preserving all
// legacy/user rules. Setup and vendor use it without installing hooks, retiring
// old rules, or changing enablement state. Staging always belongs to p's S, even
// when repoRoot is C. The result reports a write even if its durability or
// effective protection could not subsequently be confirmed.
func EnsureLocalIgnored(repoRoot string, p layout.Policy) (added bool, err error) {
	return ensureLocalIgnoredWithIO(repoRoot, p, defaultGitignoreIO())
}

func ensureLocalIgnoredWithIO(repoRoot string, p layout.Policy, io gitignoreIO) (bool, error) {
	covered, err := LocalIgnored(repoRoot)
	if err != nil {
		return false, err
	}
	if covered {
		return false, confirmGitignoreProtection(repoRoot, p, io)
	}
	path := filepath.Join(repoRoot, ".gitignore")
	data, err := readIgnoreSource(repoRoot, ".gitignore")
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	if presentLines(data)[LocalIgnoreRule] {
		return false, localProtectionError(repoRoot)
	}
	eol := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		eol = "\r\n"
	}
	var b strings.Builder
	b.Write(data)
	if len(data) > 0 {
		if !bytes.HasSuffix(data, []byte("\n")) {
			b.WriteString(eol)
		}
		b.WriteString(eol)
	}
	b.WriteString(LocalIgnoreNote + eol + LocalIgnoreRule + eol)
	if err := writeGitignoreWithIO(path, []byte(b.String()), p, io); err != nil {
		var applied *layout.InstallDurabilityError
		return errors.As(err, &applied), err
	}
	return true, verifyLocalProtection(repoRoot, p, io)
}

func localProtectionError(repoRoot string) error {
	return fmt.Errorf("%s: rule %s is present but not honoured by git (a negation or override); local files are still stageable; repair conflicting ignore rules",
		filepath.Join(repoRoot, ".gitignore"), LocalIgnoreRule)
}

func verifyLocalProtection(repoRoot string, p layout.Policy, io gitignoreIO) error {
	covered, err := LocalIgnored(repoRoot)
	if err != nil {
		return fmt.Errorf("verifying local-zone protection in %s: %w", repoRoot, err)
	}
	if !covered {
		return localProtectionError(repoRoot)
	}
	return confirmGitignoreProtection(repoRoot, p, io)
}

// Equal/no-write retries must reconfirm directory durability, including a nested
// repository ignore source and the physical target of a symlinked host file.
func confirmGitignoreProtection(repoRoot string, p layout.Policy, io gitignoreIO) error {
	for _, rel := range []string{".gitignore", ".punt-labs/.gitignore"} {
		path := filepath.Join(repoRoot, filepath.FromSlash(rel))
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return fmt.Errorf("examining local-zone ignore source %s: %w", path, err)
		}
		target, err := gitignoreTarget(path)
		if err != nil {
			return err
		}
		if err := io.syncDestination(p, target); err != nil {
			return &layout.InstallDurabilityError{Path: target, Err: err}
		}
	}
	return nil
}

// gitignoreIO is per call, so fault injection never mutates process-wide hooks
// or races another enable/setup/vendor request.
type gitignoreIO struct {
	syncFile        func(*os.File) error
	rename          func(string, string) error
	syncDestination func(layout.Policy, string) error
}

func defaultGitignoreIO() gitignoreIO {
	return gitignoreIO{(*os.File).Sync, os.Rename, layout.SyncRepositoryDestination}
}

// createGitignoreTemp stages in the canonical shared-local zone. Its name comes
// from the logical host, even when the final write follows a symlink elsewhere.
func createGitignoreTemp(logical string, p layout.Policy) (*os.File, error) {
	return layout.CreateLocalTemp(p, filepath.Base(logical), "")
}

// writeGitignore replaces the .gitignore at path atomically and durably: it
// writes to a temp file in the shared local zone, fsyncs it, and renames over
// the target, so a crash or power loss leaves the operator's file as either the
// old content or the new — never a truncated hybrid. A symlinked .gitignore (a
// dotfile manager like stow or chezmoi) is resolved so the real target is
// rewritten and the symlink itself preserved. An existing file's mode is kept;
// a new file gets 0o644. Cross-device publication fails with the old target
// intact; it never falls back to adjacent staging.
func writeGitignore(path string, data []byte, p layout.Policy) error {
	return writeGitignoreWithIO(path, data, p, defaultGitignoreIO())
}

func gitignoreTarget(path string) (string, error) {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", fmt.Errorf("resolving .gitignore symlink %s: %w", path, err)
		}
		return resolved, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("examining .gitignore %s: %w", path, err)
	}
	return path, nil
}

func writeGitignoreWithIO(path string, data []byte, p layout.Policy, io gitignoreIO) error {
	target, err := gitignoreTarget(path)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(target); err == nil {
		mode = fi.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("examining .gitignore target %s: %w", target, err)
	}

	tmp, err := createGitignoreTemp(path, p)
	if err != nil {
		return fmt.Errorf("creating staging file for .gitignore: %w", err)
	}
	name := tmp.Name()
	if n, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("writing %s: %w", name, err)
	} else if n < len(data) {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("short write to %s: %d of %d bytes", name, n, len(data))
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("setting mode on %s: %w", name, err)
	}
	if err := io.syncFile(tmp); err != nil {
		err = errors.Join(err, tmp.Close())
		_ = os.Remove(name)
		return fmt.Errorf("syncing %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("closing %s: %w", name, err)
	}
	if err := io.rename(name, target); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("renaming %s to %s: %w", name, target, err)
	}
	if err := io.syncDestination(p, target); err != nil {
		return &layout.InstallDurabilityError{Path: target, Err: err}
	}
	return nil
}

// trackedRuntimeFiles returns the repo-relative paths git already tracks that
// fall in ethos's runtime zones. The .gitignore only stops FUTURE tracking; a
// repo that committed these files before enabling still tracks them, and the
// live seal hook rewriting them keeps deadlocking git checkout/pull with no
// operator signal. The :(glob) pathspec magic makes git evaluate the same
// globs the .gitignore uses. A tracked machine-local file is a different
// fault — a secret in git, which `ethos doctor` FAILs on — so it is not
// folded into this warning. Untracking (git rm --cached) is the operator's
// call, so the caller only warns.
func trackedRuntimeFiles(repoRoot string) ([]string, error) {
	args := []string{"ls-files", "-z", "--"}
	for _, r := range gitignoreRules {
		if r.runtime {
			args = append(args, ":(glob)"+r.pattern)
		}
	}
	out, err := gitenv.OutputEnv(repoRoot, "git", nil, args...)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("git ls-files: %w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// trackedRuntimeWarning is the loud remedy line for files that are already
// tracked despite the .gitignore. It names the count, the files, and a
// copy-pasteable command; enable does not run the removal itself. The command
// uses the -- separator and shell-quotes each path, so paths with spaces or
// special characters survive the copy-paste.
func trackedRuntimeWarning(files []string) string {
	quoted := make([]string, len(files))
	for i, f := range files {
		quoted[i] = shellQuote(f)
	}
	return fmt.Sprintf(
		"%d ethos runtime file(s) are already git-tracked (%s); the .gitignore does not untrack them — run: git rm -r --cached -- %s  then commit",
		len(files), strings.Join(files, ", "), strings.Join(quoted, " "))
}

// shellQuote wraps s in single quotes for POSIX shells. Each embedded single
// quote is replaced by the sequence
//
//	'\''
//
// (close-quote, escaped quote, reopen-quote), so the result is a single shell
// word safe to paste into a command line regardless of spaces or
// metacharacters.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
