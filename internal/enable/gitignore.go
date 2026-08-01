package enable

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// gitignoreMarker heads the block enable appends so the patterns are
// identifiable as ethos-managed.
const gitignoreMarker = "# ethos runtime (never track) — DES-058 live zone + mission locks"

// LocalIgnoreRule is the one canonical pattern that keeps every tool's
// machine-local file out of git: any `.local.<ext>` file anywhere under
// `.punt-labs/`. enable, setup, and vendor all write this spelling. They do not
// require it: coverage is decided by asking git, so a repo that excludes the
// same paths another way is left alone.
const LocalIgnoreRule = ".punt-labs/**/*.local.*"

// LocalIgnoreNote labels the canonical rule for whoever reads the .gitignore
// next. The runtime zones churn and must stay untracked for mechanical
// reasons; this one covers files that may hold secrets, and a shared label
// would mislabel one of them.
const LocalIgnoreNote = "# ethos: machine-local files (.local.*) — may hold secrets, never track"

// localIgnoreProbes are the machine-local paths a covering rule must exclude —
// every one of them, or the rule is not coverage.
//
// One probe under .punt-labs/ethos/ was not enough. The narrow
// `.punt-labs/ethos/**/*.local.yaml` that every shipped `ethos doctor` told
// operators to add satisfies such a probe while leaving `.punt-labs/vox/`,
// `.punt-labs/beadle/`, and every non-yaml variant stageable; ethos then
// reported the repo covered and wrote nothing, and `git add -A` staged the
// secret (djb, review of PR #423). So the set spans what the canonical rule
// spans: a non-ethos subtree, a non-yaml extension, and the zero-directory
// case directly under .punt-labs/.
//
// They name no real files: git check-ignore reports a path already in the
// index as not ignored, so a probe must be one git has never seen.
var localIgnoreProbes = []string{
	".punt-labs/ethos/identities/probe.ext/probe.local.yaml",
	".punt-labs/vox/probe.local.md",
	".punt-labs/beadle/probe.local.json",
	".punt-labs/probe.local.env",
}

// ignoreRule is one pattern enable ensures the repo excludes: the pattern
// itself, the probe paths that decide whether it is already covered, the
// comment written above it, and whether it names a runtime zone.
type ignoreRule struct {
	pattern string
	probes  []string
	note    string
	runtime bool
}

// gitignoreRules are the patterns a consumer repo must never track. The
// runtime zones are the DES-058 live-session files (audit + lock) and the
// mission locks: the seal hook rewrites them continuously while a session is
// live, so tracking them deadlocks git checkout/pull and leaks them into
// release PRs. The last rule is the machine-local half of any tool namespace,
// which may hold secrets. Each rule's probes span the breadth of its pattern,
// so a narrower rule that covers only part of it does not pass for coverage.
var gitignoreRules = []ignoreRule{
	{
		pattern: ".punt-labs/**/local/**",
		// The first probe is the zero-directory case, `.punt-labs/local/`
		// itself. `/**/` spans zero or more directories, so the canonical
		// rule covers it, but a narrower `.punt-labs/*/local/**` does not —
		// and with only the nested probes that narrow rule passed for
		// coverage while the top-level DES-058 live zone stayed stageable
		// (Bugbot, review of PR #423).
		probes: []string{
			".punt-labs/local/probe/probe.jsonl",
			".punt-labs/ethos/local/probe/probe.jsonl",
			".punt-labs/vox/local/probe.json",
		},
		runtime: true,
	},
	{
		pattern: ".punt-labs/ethos/missions/**/*.lock",
		probes: []string{
			".punt-labs/ethos/missions/m-probe/probe.lock",
			".punt-labs/ethos/missions/m-probe/delegations/d-probe/probe.lock",
		},
		runtime: true,
	},
	machineLocalRule,
}

// machineLocalRule is the machine-local entry of gitignoreRules, named so
// LocalIgnored and enable cannot drift onto different probe sets.
var machineLocalRule = ignoreRule{
	pattern: LocalIgnoreRule,
	probes:  localIgnoreProbes,
	note:    LocalIgnoreNote,
}

// ensureGitignore makes the repo .gitignore cover ethos's runtime zones and
// the machine-local files. It adds only the patterns the repo does not already
// exclude, so re-enable adds nothing (idempotent). When the ethos block already
// exists the missing patterns are inserted under that one marker — never a
// second marker block; otherwise a fresh marked block is appended. Existing
// lines are left in place and never reordered. A missing .gitignore is created.
//
// It returns the step action and a detail line for the report: "added" when it
// wrote, "already" when the repo was covered, and "attention" when a pattern's
// line is in the file but git does not honour it — the one case ethos cannot
// fix by writing, because the line it would write is already there.
func ensureGitignore(repoRoot string) (action, detail string, err error) {
	path := filepath.Join(repoRoot, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", "", fmt.Errorf("reading .gitignore: %w", err)
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
	var patterns, defeated []string
	for _, r := range gitignoreRules {
		if covers(repoRoot, r, present) {
			continue
		}
		// The pattern is uncovered but its literal line is already in the
		// file: a later negation or an override defeats it, and appending a
		// second copy of the same line would not change git's answer — it
		// would just grow the file on every run. Report it instead; only a
		// human can resolve the conflict.
		if present[r.pattern] {
			defeated = append(defeated, r.pattern)
			continue
		}
		missing = append(missing, r)
		patterns = append(patterns, r.pattern)
	}
	if len(missing) == 0 {
		if len(defeated) > 0 {
			return "attention", strings.Join(defeated, ", ") +
				" present in .gitignore but not honoured by git (a later negation or an override) — those files are still stageable", nil
		}
		return "already", ".gitignore already ignores ethos runtime zones and machine-local files", nil
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

	if err := writeGitignore(path, []byte(out)); err != nil {
		return "", "", err
	}
	detail = "ignored " + strings.Join(patterns, ", ")
	if len(defeated) > 0 {
		detail += "; " + strings.Join(defeated, ", ") +
			" present but not honoured by git (a later negation or an override) — those files are still stageable"
	}
	return "added", detail, nil
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

// covers reports whether the repo's own committed ignore rules exclude every
// one of the rule's probes.
//
// git is the authority on what a rule matches: `git check-ignore` reads the
// canonical `.punt-labs/**/*.local.*` as readily as any other spelling, so a
// repo that already excludes the paths is left alone rather than having a
// redundant near-duplicate appended on every setup and vendor --apply (Bugbot,
// PR #422). Two things git is not the authority on, and both are decided here
// in gitExcludes: which rules travel with the repo, and whether one probe
// standing in for a whole pattern is enough.
//
// It answers false on any doubt. A rule that covers part of the pattern, a
// match that comes from an untraveling source, and a git that cannot answer at
// all all mean "write the rule" — a redundant line costs nothing, a secret
// staged by a repo that reported itself protected costs everything.
//
// The literal pattern is consulted only when git cannot answer (no git in
// PATH, not a work tree). It is a fallback, not a second opinion: when git
// answers "not excluded" for a pattern whose line is right there in the file,
// something overrides it, and taking the line's word for it is the fail-open
// this predicate exists to prevent.
func covers(repoRoot string, r ignoreRule, present map[string]bool) bool {
	excluded, err := gitExcludes(repoRoot, r.probes)
	if err != nil {
		return present[r.pattern]
	}
	return excluded
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
// as an error for the caller to fall back on.
func gitExcludes(repoRoot string, probes []string) (bool, error) {
	cmd := exec.Command("git", "-C", repoRoot, "-c", "core.excludesFile="+os.DevNull,
		"check-ignore", "-v", "-z", "--no-index", "--stdin")
	cmd.Stdin = strings.NewReader(strings.Join(probes, "\x00") + "\x00")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		// Exit 1 is "no path matched" — an answer, and the answer is no.
		if !errors.As(err, &ee) || ee.ExitCode() != 1 {
			return false, fmt.Errorf("git check-ignore: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
	}

	// -v -z prints four NUL-terminated fields per match: source, line number,
	// pattern, pathname. Unmatched paths print nothing.
	matched := make(map[string]bool, len(probes))
	fields := strings.Split(stdout.String(), "\x00")
	for i := 0; i+3 < len(fields); i += 4 {
		source, pattern, path := fields[i], fields[i+2], fields[i+3]
		if strings.HasPrefix(pattern, "!") || !travelsWithRepo(source) {
			continue
		}
		matched[path] = true
	}
	for _, p := range probes {
		if !matched[p] {
			return false, nil
		}
	}
	return true, nil
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

// LocalIgnored reports whether the repo's own committed rules keep every kind
// of machine-local `.local.*` file out of git, by the same predicate enable
// uses. Setup, vendor, and doctor call it, so all four surfaces answer one
// question one way.
func LocalIgnored(repoRoot string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, ".gitignore"))
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("reading .gitignore: %w", err)
	}
	return covers(repoRoot, machineLocalRule, presentLines(data)), nil
}

// writeGitignore replaces the .gitignore at path atomically and durably: it
// writes to a temp file in the target's directory, fsyncs it, and renames over
// the target, so a crash or power loss leaves the operator's file as either the
// old content or the new — never a truncated hybrid. A symlinked .gitignore (a
// dotfile manager like stow or chezmoi) is resolved so the real target is
// rewritten and the symlink itself preserved. An existing file's mode is kept;
// a new file gets 0o644.
func writeGitignore(path string, data []byte) error {
	target := path
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("resolving .gitignore symlink: %w", err)
		}
		target = resolved
	}

	mode := os.FileMode(0o644)
	if fi, err := os.Stat(target); err == nil {
		mode = fi.Mode().Perm()
	}

	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".gitignore.*")
	if err != nil {
		return fmt.Errorf("creating temp .gitignore in %s: %w", dir, err)
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
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("syncing %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("closing %s: %w", name, err)
	}
	if err := os.Rename(name, target); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("renaming %s to %s: %w", name, target, err)
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
	args := []string{"-C", repoRoot, "ls-files", "-z", "--"}
	for _, r := range gitignoreRules {
		if r.runtime {
			args = append(args, ":(glob)"+r.pattern)
		}
	}
	out, err := exec.Command("git", args...).Output()
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
