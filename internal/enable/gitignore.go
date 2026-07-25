package enable

import (
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

	lines := strings.Split(string(data), "\n")
	present := make(map[string]bool, len(lines))
	markerIdx := -1
	for i, line := range lines {
		t := strings.TrimSpace(line)
		present[t] = true
		if markerIdx < 0 && t == gitignoreMarker {
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

	var out string
	if markerIdx >= 0 {
		// Add the missing patterns under the existing marker — one block, no
		// duplicate comment.
		merged := make([]string, 0, len(lines)+len(missing))
		merged = append(merged, lines[:markerIdx+1]...)
		merged = append(merged, missing...)
		merged = append(merged, lines[markerIdx+1:]...)
		out = strings.Join(merged, "\n")
	} else {
		var buf strings.Builder
		buf.Write(data)
		// Separate the appended block from existing content with a blank line,
		// and terminate an existing final line that lacks its newline.
		if len(data) > 0 {
			if !strings.HasSuffix(string(data), "\n") {
				buf.WriteByte('\n')
			}
			buf.WriteByte('\n')
		}
		buf.WriteString(gitignoreMarker)
		buf.WriteByte('\n')
		buf.WriteString(strings.Join(missing, "\n"))
		buf.WriteByte('\n')
		out = buf.String()
	}

	if err := writeGitignore(path, []byte(out)); err != nil {
		return "", "", err
	}
	return "added", "ignored " + strings.Join(missing, ", "), nil
}

// writeGitignore replaces path atomically: it writes to a temp file in the same
// directory and renames over the target, so a crash mid-update never leaves a
// partially written .gitignore — the operator's file is either the old content
// or the new, never a truncated hybrid. An existing file's mode is preserved; a
// new file gets 0o644.
func writeGitignore(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".gitignore.*")
	if err != nil {
		return fmt.Errorf("creating temp .gitignore in %s: %w", dir, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", name, err)
	}
	if err := os.Chmod(name, mode); err != nil {
		return fmt.Errorf("setting mode on %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("renaming %s to .gitignore: %w", name, err)
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

// shellQuote wraps s in single quotes for POSIX shells, escaping any embedded
// single quote as '\''. The result is a single shell word safe to paste into a
// command line regardless of spaces or metacharacters.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
