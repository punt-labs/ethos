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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/textscan"
)

// Register appends line to the CLAUDE.md at path when no top-level,
// terminator-insensitive match is already present, and returns whether it
// wrote. A missing file is created (mode 0644). Every byte outside the
// appended line is preserved; the appended line uses the host's EOL.
// A repository directory-sync failure returns true and an
// *layout.InstallDurabilityError: the edit applied, but durability is unconfirmed.
// A matching-file retry also confirms directory durability; it returns false
// (no new write), with the same error type if confirmation still fails.
func Register(path, line string, opts ...Option) (bool, error) {
	policies := repositoryPolicies(opts)
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
			return confirmUnchangedHost(real, policies...)
		}
		eol := textscan.DetectEOL(data)
		out := make([]byte, 0, len(data)+len(line)+2*len(eol))
		out = append(out, data...)
		if len(data) > 0 && !endsWithTerminator(data) {
			out = append(out, eol...)
		}
		out = append(out, line...)
		out = append(out, eol...)
		if err := writeAtomic(real, path, out, mode, policies...); err != nil {
			var applied *layout.InstallDurabilityError
			wrote = errors.As(err, &applied)
			return err
		}
		wrote = true
		return nil
	}, policies...)
	return wrote, err
}

// Deregister removes every top-level, terminator-insensitive match of line
// from the CLAUDE.md at path, collapsing an accidental duplicate to zero,
// and returns whether it wrote. A missing file is a no-op. Every byte
// outside the removed lines is preserved.
// As with Register, a post-install durability failure returns true and an error.
// A no-op retry returns false but still confirms an existing repository host.
func Deregister(path, line string, opts ...Option) (bool, error) {
	policies := repositoryPolicies(opts)
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
		// A host ending inside an unterminated fence is malformed: the import
		// line would be classified fenced and go unmatched, so Deregister would
		// report "already absent" and leave the line behind — breaking the
		// enabled⟺import biconditional (§2.11). Error rather than half-remove,
		// symmetric with Register.
		if fenceOpenAtEOF(data) {
			return fmt.Errorf("%s ends inside an unterminated code fence — close the fence and re-run", real)
		}
		idx := matchIndices(data, line)
		if len(idx) == 0 {
			return confirmUnchangedHost(real, policies...)
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
		if err := writeAtomic(real, path, out, mode, policies...); err != nil {
			var applied *layout.InstallDurabilityError
			wrote = errors.As(err, &applied)
			return err
		}
		wrote = true
		return nil
	}, policies...)
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

// lockPathFor returns a domain-qualified shared-local repository lock when a
// policy is supplied. Otherwise it preserves the global caller's per-user file
// named by SHA-256 of the target, under ~/.punt-labs/ethos/locks/.
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
func lockPathFor(real string, policies ...layout.Policy) (string, error) {
	if len(policies) > 0 {
		return policies[0].LockPath("claudemd", real)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir for the lock: %w", err)
	}
	sum := sha256.Sum256([]byte(real))
	dir := filepath.Join(home, ".punt-labs", "ethos", "locks")
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".lock"), nil
}

// withLock runs fn while holding the repository-local or per-user lock for
// real. Cooperating Register/Deregister calls coordinate independently of
// TMPDIR. Repository callers must supply the same shared-store policy.
func withLock(real string, fn func() error, policies ...layout.Policy) error {
	lockPath, err := lockPathFor(real, policies...)
	if err != nil {
		return err
	}
	var dirErr error
	if len(policies) > 0 {
		dirErr = layout.EnsureLocalDir(policies[0], filepath.Dir(lockPath))
	} else {
		dirErr = os.MkdirAll(filepath.Dir(lockPath), 0o700)
	}
	if err := dirErr; err != nil {
		return fmt.Errorf("creating lock dir %s: %w", filepath.Dir(lockPath), err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening lock %s: %w", lockPath, err)
	}
	defer func() { _ = f.Close() }()
	if err := flock(f); err != nil {
		return fmt.Errorf("locking %s: %w", lockPath, err)
	}
	defer func() { _ = funlock(f) }()
	return fn()
}

// createTemp uses shared-local repository staging when a policy is supplied,
// otherwise the existing global caller's adjacent staging. Its name comes from
// logical, the path the caller asked to
// write, not from real: when CLAUDE.md is a link to AGENTS.md the file is still
// classified as CLAUDE.md's staging file (DES-079).
func createTemp(real, logical string, policies ...layout.Policy) (*os.File, error) {
	if len(policies) > 0 {
		return layout.CreateLocalTemp(policies[0], filepath.Base(logical), "")
	}
	return os.CreateTemp(filepath.Dir(real), layout.DotTempPattern(filepath.Base(logical)))
}

// writeAtomic writes data to a unique temp file, then
// renames it over real. The rename is atomic on POSIX, so no reader ever
// sees a torn file. Cross-device rename is a pre-commit error, never a reason
// to fall back beside the host. logical is the caller's path; see createTemp.
func writeAtomic(real, logical string, data []byte, mode os.FileMode, policies ...layout.Policy) error {
	tmp, err := createTemp(real, logical, policies...)
	if err != nil {
		return fmt.Errorf("creating staging file for %s: %w", logical, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", name, err)
	}
	if len(policies) > 0 {
		if err := tmp.Chmod(mode); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("setting mode on %s: %w", name, err)
		}
		if err := syncHostFile(tmp); err != nil {
			return fmt.Errorf("syncing %s: %w", name, errors.Join(err, tmp.Close()))
		}
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", name, err)
	}
	if len(policies) == 0 {
		if err := os.Chmod(name, mode); err != nil {
			return fmt.Errorf("setting mode on %s: %w", name, err)
		}
	}
	if err := os.Rename(name, real); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", name, real, err)
	}
	if len(policies) > 0 {
		if err := syncHostDestination(policies[0], real); err != nil {
			return &layout.InstallDurabilityError{Path: real, Err: err}
		}
	}
	return nil
}

var syncHostFile = (*os.File).Sync
var syncHostDestination = layout.SyncRepositoryDestination

// Matching bytes do not prove that an earlier rename's directory sync completed.
// Confirm under the same host lock, without rewriting the file. A missing host
// has no installed replacement to confirm and remains Deregister's no-op.
func confirmUnchangedHost(real string, policies ...layout.Policy) error {
	if len(policies) == 0 {
		return nil
	}
	if _, err := os.Stat(real); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", real, err)
	}
	if err := syncHostDestination(policies[0], real); err != nil {
		return &layout.InstallDurabilityError{Path: real, Err: err}
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

// isFence reports whether content is a fence delimiter: after at most three
// leading spaces (and no tab) it begins with three or more backticks or
// tildes. Per tool-enable-disable §2.4, a line indented with a tab or four or
// more spaces is an INDENTED CODE line, not a fence — so an indented ``` must
// not be misread as opening a real fence (which would false-refuse a host
// ending on such a line). Indentation precedence therefore lives here.
func isFence(content string) bool {
	spaces := 0
	for spaces < len(content) && content[spaces] == ' ' {
		spaces++
	}
	if spaces > 3 {
		return false // four or more leading spaces: indented code
	}
	t := content[spaces:]
	if strings.HasPrefix(t, "\t") {
		return false // a tab in the indent: indented code
	}
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

// isIndentedCode reports whether content begins an indented code block: it
// starts with a tab or four or more spaces.
func isIndentedCode(content string) bool {
	return strings.HasPrefix(content, "\t") || strings.HasPrefix(content, "    ")
}
