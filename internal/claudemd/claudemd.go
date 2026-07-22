// Package claudemd registers and removes a single canonical @-import line
// in a user-owned CLAUDE.md, under the tool-enable-disable §2.4 write
// contract: exclusive lock, atomic temp+rename, byte-preserving with the
// host's EOL convention, terminator-insensitive idempotent matching,
// code-block exclusion, symlink resolution, and mode preservation.
//
// It is host-file-agnostic: the caller supplies both the target path and
// the canonical line, so the package owns the write correctness and knows
// nothing about ethos's canonical string.
package claudemd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/internal/textscan"
	"golang.org/x/sys/unix"
)

// Register appends line to the CLAUDE.md at path when no top-level,
// terminator-insensitive match is already present, and returns whether it
// wrote. A missing file is created (mode 0644). Every byte outside the
// appended line is preserved; the appended line uses the host's EOL.
func Register(path, line string) (bool, error) {
	real, mode, err := resolveTarget(path)
	if err != nil {
		return false, err
	}
	wrote := false
	err = withLock(real, func() error {
		data, err := readIfExists(real)
		if err != nil {
			return err
		}
		// A host ending inside an unterminated fence is malformed: an appended
		// import would land inside the open fence (dead — Claude Code ignores
		// fenced imports) and never match on re-run, so enable would append
		// duplicates forever. Error rather than repositioning the user's
		// content; the user closes the fence and re-runs.
		if fenceOpenAtEOF(data) {
			return fmt.Errorf("%s ends inside an unterminated code fence — close the fence and re-run", real)
		}
		if len(matchIndices(data, line)) > 0 {
			return nil
		}
		eol := textscan.DetectEOL(data)
		out := make([]byte, 0, len(data)+len(line)+2*len(eol))
		out = append(out, data...)
		if len(data) > 0 && !endsWithTerminator(data) {
			out = append(out, eol...)
		}
		out = append(out, line...)
		out = append(out, eol...)
		if err := writeAtomic(real, out, mode); err != nil {
			return err
		}
		wrote = true
		return nil
	})
	return wrote, err
}

// Deregister removes every top-level, terminator-insensitive match of line
// from the CLAUDE.md at path, collapsing an accidental duplicate to zero,
// and returns whether it wrote. A missing file is a no-op. Every byte
// outside the removed lines is preserved.
func Deregister(path, line string) (bool, error) {
	real, mode, err := resolveTarget(path)
	if err != nil {
		return false, err
	}
	wrote := false
	err = withLock(real, func() error {
		data, err := readIfExists(real)
		if err != nil {
			return err
		}
		idx := matchIndices(data, line)
		if len(idx) == 0 {
			return nil
		}
		drop := make(map[int]bool, len(idx))
		for _, i := range idx {
			drop[i] = true
		}
		lines := textscan.SplitKeepEnds(data)
		out := make([]byte, 0, len(data))
		for i, l := range lines {
			if drop[i] {
				continue
			}
			out = append(out, l...)
		}
		if err := writeAtomic(real, out, mode); err != nil {
			return err
		}
		wrote = true
		return nil
	})
	return wrote, err
}

// resolveTarget returns the real path to operate on (following one symlink
// so a dotfile-manager link survives the rename), the mode to preserve
// (0644 for a new file), and any resolution error.
func resolveTarget(path string) (string, os.FileMode, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", 0, fmt.Errorf("resolving path %s: %w", path, err)
	}
	li, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return abs, 0o644, nil
		}
		return "", 0, fmt.Errorf("stat %s: %w", abs, err)
	}
	if li.Mode()&os.ModeSymlink != 0 {
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return "", 0, fmt.Errorf("resolving symlink %s: %w", abs, err)
		}
		fi, err := os.Stat(real)
		if err != nil {
			return "", 0, fmt.Errorf("stat symlink target %s: %w", real, err)
		}
		return real, fi.Mode().Perm(), nil
	}
	return abs, li.Mode().Perm(), nil
}

// readIfExists returns the file's bytes, or nil when it does not exist.
func readIfExists(real string) ([]byte, error) {
	data, err := os.ReadFile(real)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", real, err)
	}
	return data, nil
}

// lockPathFor returns the lock file for real: a per-user file named by the
// SHA-256 of the resolved target path, under ~/.punt-labs/ethos/locks/.
//
// It is deliberately NOT under os.TempDir() (this org sets TMPDIR per-repo via
// direnv, so a temp-dir lock would put a direnv-shell writer and an env-less
// writer — a hook, a daemon — on different lock files for the same target, a
// lost update) and NOT a sibling in the work tree (that litters the repo with
// an untracked .CLAUDE.md.lock that survives disable). Keying on the resolved
// target alone makes it deterministic across TMPDIR, direnv, and callers,
// env-independent, and leaves zero work-tree litter.
//
// Scope: same-user exclusion. Two different users sharing one checkout are out
// of scope — they already collide on file permissions and are not a case §2.4
// targets.
func lockPathFor(real string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir for the lock: %w", err)
	}
	sum := sha256.Sum256([]byte(real))
	dir := filepath.Join(home, ".punt-labs", "ethos", "locks")
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".lock"), nil
}

// withLock runs fn while holding an exclusive lock on the per-user lock file
// for real, so two concurrent Register/Deregister calls (in-process
// goroutines or separate processes, same user) coordinate regardless of
// TMPDIR — without leaving a lock file in the work tree.
func withLock(real string, fn func() error) error {
	lockPath, err := lockPathFor(real)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return fmt.Errorf("creating lock dir %s: %w", filepath.Dir(lockPath), err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening lock %s: %w", lockPath, err)
	}
	defer func() { _ = f.Close() }()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("locking %s: %w", lockPath, err)
	}
	defer func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }()
	return fn()
}

// writeAtomic writes data to a temp file in real's own directory, then
// renames it over real. The rename is atomic on POSIX, so no reader ever
// sees a torn file.
func writeAtomic(real string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(real)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(real)+".tmp*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
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
	if err := os.Rename(name, real); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", name, real, err)
	}
	return nil
}

// matchIndices returns the indices, into splitKeepEnds(data), of the lines
// that equal line net of their terminator and sit at the markdown top level
// (not inside a fenced or indented code block).
func matchIndices(data []byte, line string) []int {
	lines := textscan.SplitKeepEnds(data)
	var out []int
	fenceOpen := false
	for i, raw := range lines {
		content := textscan.StripTerminator(raw)
		if isFence(content) {
			fenceOpen = !fenceOpen
			continue
		}
		if fenceOpen || isIndentedCode(content) {
			continue
		}
		// Tolerate trailing whitespace on the host line: a hand-edited import
		// with trailing spaces/tabs must still match, or enable appends a
		// duplicate beside it (audit AC4) and disable leaves an ill-formed
		// line behind (AC6). The written line stays exactly canonical.
		if strings.TrimRight(content, " \t") == line {
			out = append(out, i)
		}
	}
	return out
}

// fenceOpenAtEOF reports whether data ends with an open (unterminated) code
// fence — an odd number of fence delimiters. Such a host is malformed: an
// appended top-level import would fall inside the open fence.
func fenceOpenAtEOF(data []byte) bool {
	open := false
	for _, raw := range textscan.SplitKeepEnds(data) {
		if isFence(textscan.StripTerminator(raw)) {
			open = !open
		}
	}
	return open
}

// endsWithTerminator reports whether data's last byte is a line terminator.
func endsWithTerminator(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	last := data[len(data)-1]
	return last == '\n' || last == '\r'
}

// isFence reports whether content is a fence delimiter: its first
// non-whitespace characters are three or more backticks or tildes.
func isFence(content string) bool {
	t := strings.TrimLeft(content, " \t")
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

// isIndentedCode reports whether content begins an indented code block: it
// starts with a tab or four or more spaces.
func isIndentedCode(content string) bool {
	return strings.HasPrefix(content, "\t") || strings.HasPrefix(content, "    ")
}
