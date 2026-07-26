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

// gitignoreMarker heads the block enable appends so the runtime patterns are
// identifiable as ethos-managed.
const gitignoreMarker = "# ethos runtime (never track) — DES-058 live zone + mission locks"

// gitignorePatterns are the runtime zones a consumer repo must never track: the
// DES-058 live-session files (audit + lock) and the mission runtime locks. The
// seal hook rewrites these continuously while a session is live, so tracking
// them deadlocks git checkout/pull and leaks them into release PRs.
var gitignorePatterns = []string{
	".punt-labs/**/local/**",
	".punt-labs/ethos/missions/**/*.lock",
}

// ensureGitignore makes the repo .gitignore cover ethos's runtime zones. It
// adds only the patterns not already present, so re-enable adds nothing
// (idempotent). When the ethos block already exists the missing patterns are
// inserted under that one marker — never a second marker block; otherwise a
// fresh marked block is appended. Existing lines are left in place and never
// reordered. A missing .gitignore is created. It returns the step action
// ("added" or "already") and a detail line for the report.
func ensureGitignore(repoRoot string) (action, detail string, err error) {
	path := filepath.Join(repoRoot, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", "", fmt.Errorf("reading .gitignore: %w", err)
	}

	// Match lines exactly, but for the comparison only: leading whitespace is
	// significant in .gitignore, so an indented "  .punt-labs/**/local/**" is a
	// different pattern — it matches paths that start with spaces, not the real
	// files — and must not count as coverage, or the real zone stays unignored.
	// A trailing \r (CRLF file) is stripped for the comparison so a CRLF repo
	// stays idempotent; leading whitespace is left intact.
	lines := strings.Split(string(data), "\n")
	present := make(map[string]bool, len(lines))
	markerIdx := -1
	for i, line := range lines {
		key := strings.TrimRight(line, "\r")
		present[key] = true
		if markerIdx < 0 && key == gitignoreMarker {
			markerIdx = i
		}
	}
	var missing []string
	for _, p := range gitignorePatterns {
		if !present[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return "already", ".gitignore already ignores ethos runtime zones", nil
	}

	// Preserve the file's existing line ending so the block we add matches.
	eol := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		eol = "\r\n"
	}
	ins := make([]string, len(missing))
	for i, m := range missing {
		ins[i] = m + strings.TrimSuffix(eol, "\n")
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
	return "added", "ignored " + strings.Join(missing, ", "), nil
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
// globs the .gitignore uses. Untracking (git rm --cached) is the operator's
// call, so the caller only warns.
func trackedRuntimeFiles(repoRoot string) ([]string, error) {
	args := []string{"-C", repoRoot, "ls-files", "-z", "--"}
	for _, p := range gitignorePatterns {
		args = append(args, ":(glob)"+p)
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
