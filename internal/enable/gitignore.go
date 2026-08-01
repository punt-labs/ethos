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

// localIgnoreProbe is a representative machine-local path a covering rule must
// exclude. It names no real file: git check-ignore reports a path already in
// the index as not ignored, so a probe must be one git has never seen.
const localIgnoreProbe = ".punt-labs/ethos/identities/probe.ext/probe.local.yaml"

// ignoreRule is one pattern enable ensures the repo excludes: the pattern
// itself, the probe path that decides whether it is already covered, the
// comment written above it, and whether it names a runtime zone.
type ignoreRule struct {
	pattern string
	probe   string
	note    string
	runtime bool
}

// gitignoreRules are the patterns a consumer repo must never track. The
// runtime zones are the DES-058 live-session files (audit + lock) and the
// mission locks: the seal hook rewrites them continuously while a session is
// live, so tracking them deadlocks git checkout/pull and leaks them into
// release PRs. The last rule is the machine-local half of any tool namespace,
// which may hold secrets.
var gitignoreRules = []ignoreRule{
	{
		pattern: ".punt-labs/**/local/**",
		probe:   ".punt-labs/ethos/local/probe/probe.jsonl",
		runtime: true,
	},
	{
		pattern: ".punt-labs/ethos/missions/**/*.lock",
		probe:   ".punt-labs/ethos/missions/m-probe/probe.lock",
		runtime: true,
	},
	{
		pattern: LocalIgnoreRule,
		probe:   localIgnoreProbe,
		note:    LocalIgnoreNote,
	},
}

// ensureGitignore makes the repo .gitignore cover ethos's runtime zones and
// the machine-local files. It adds only the patterns the repo does not already
// exclude, so re-enable adds nothing (idempotent). When the ethos block already
// exists the missing patterns are inserted under that one marker — never a
// second marker block; otherwise a fresh marked block is appended. Existing
// lines are left in place and never reordered. A missing .gitignore is created.
// It returns the step action ("added" or "already") and a detail line for the
// report.
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
	var patterns []string
	for _, r := range gitignoreRules {
		if covers(repoRoot, r.pattern, r.probe, present) {
			continue
		}
		missing = append(missing, r)
		patterns = append(patterns, r.pattern)
	}
	if len(missing) == 0 {
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
	return "added", "ignored " + strings.Join(patterns, ", "), nil
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

// covers reports whether the repo already excludes probe from git.
//
// git is the authority. `git check-ignore` sees any spelling that matches the
// path — the canonical `.punt-labs/**/*.local.*` as readily as a narrower rule
// — plus rules in .git/info/exclude and in a parent directory's .gitignore. A
// repo that already excludes the path is left alone; string-matching one
// blessed spelling instead would re-append a redundant narrow rule on every
// setup and vendor --apply, dirtying the tree (Bugbot, PR #422).
//
// The literal pattern still counts on its own, for two reasons: when git
// cannot answer (no git in PATH, not a work tree, a bare directory in a test)
// it is all there is to go on, and it keeps a re-run from appending a
// duplicate even if the probe is a poor witness for the pattern.
func covers(repoRoot, pattern, probe string, present map[string]bool) bool {
	if present[pattern] {
		return true
	}
	ignored, err := gitIgnores(repoRoot, probe)
	return err == nil && ignored
}

// gitIgnores reports whether git's ignore rules exclude the repo-relative path
// rel. --no-index makes the answer about the rules alone, so a path that is
// somehow tracked does not read as "not ignored". check-ignore exits 1 when no
// path is ignored; any other failure means git could not answer and is
// returned as an error for the caller to fall back on.
func gitIgnores(repoRoot, rel string) (bool, error) {
	cmd := exec.Command("git", "-C", repoRoot, "check-ignore", "-q", "--no-index", "--", rel)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git check-ignore %s: %w", rel, err)
}

// LocalIgnored reports whether the repo already keeps machine-local
// `.local.*` files out of git, by the same predicate enable uses. Setup and
// vendor call it before writing LocalIgnoreRule, so the three commands agree
// with each other and with what `ethos doctor` asks git.
func LocalIgnored(repoRoot string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, ".gitignore"))
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("reading .gitignore: %w", err)
	}
	return covers(repoRoot, LocalIgnoreRule, localIgnoreProbe, presentLines(data)), nil
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
