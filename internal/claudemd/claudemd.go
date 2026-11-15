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

// ErrUnclosedFence reports a CLAUDE.md that ends inside a code fence that
// never closes. Claude Code's Markdown lexer runs such a fence to EOF, so an
// import appended below it would never load.
var ErrUnclosedFence = errors.New("ends inside an unclosed code fence; close it first")

// Preflight returns an error wrapping ErrUnclosedFence when Register would
// refuse to append line to the CLAUDE.md at path, so enable can refuse before
// it changes anything else. Register repeats the check under the lock.
func Preflight(path, line string) error {
	real, _, err := resolveTarget(path)
	if err != nil {
		return err
	}
	data, err := readIfExists(real)
	if err != nil {
		return err
	}
	if len(matchIndices(data, line)) > 0 {
		return nil
	}
	return appendable(real, data)
}

// appendable returns an error wrapping ErrUnclosedFence when a line appended
// to data would sit inside a fence that never closes.
func appendable(real string, data []byte) error {
	if _, open := scanTopLevel(textscan.SplitKeepEnds(data)); open {
		return fmt.Errorf("%s %w; the import line was not added", real, ErrUnclosedFence)
	}
	return nil
}

// Register appends line to the CLAUDE.md at path when no top-level,
// terminator-insensitive match is already present, and returns whether it
// wrote. A missing file is created (mode 0644). Every byte outside the
// appended line is preserved; the appended line uses the host's EOL.
// A repository directory-sync failure returns true and an
// *layout.InstallDurabilityError: the edit applied, but durability is unconfirmed.
// A matching-file retry also confirms directory durability; it returns false
// (no new write), with the same error type if confirmation still fails.
// A host ending inside an unclosed fence, with no live import above it, is
// refused with an error wrapping ErrUnclosedFence.
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
		if len(matchIndices(data, line)) > 0 {
			return confirmUnchangedHost(real, policies...)
		}
		if err := appendable(real, data); err != nil {
			return err
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
		// An import after an unclosed fence is inert (Claude Code runs the fence
		// to EOF), so it is not matched and is left alone; one above the open
		// fence is live and is removed.
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
	top, _ := scanTopLevel(lines)
	var out []int
	for i, raw := range lines {
		// Tolerate trailing whitespace on the host line: a hand-edited import
		// with trailing spaces/tabs must still match, or enable appends a
		// duplicate beside it (audit AC4) and disable leaves an ill-formed
		// line behind (AC6). The written line stays exactly canonical.
		if top[i] && strings.TrimRight(textscan.StripTerminator(raw), " \t") == line {
			out = append(out, i)
		}
	}
	return out
}

// scanTopLevel flags each line true when it sits at the markdown top level,
// and reports whether the document ends inside a fence that never closes.
// Fences are balanced pairs (§2.4): a run of N backticks or tildes opens a
// block that only a later same-marker run of at least N, with nothing but
// whitespace after it, closes; blocks never nest. Delimiter lines are never
// top level. An unclosed opener runs to EOF, as in Claude Code's lexer, so
// every line after it is fenced.
func scanTopLevel(lines []string) (top []bool, open bool) {
	top = make([]bool, len(lines))
	var marker byte
	var run int
	for i, raw := range lines {
		content := textscan.StripTerminator(raw)
		m, n, info, ok := parseFence(content)
		switch {
		case open:
			if ok && m == marker && n >= run && strings.Trim(info, " ") == "" {
				open = false
			}
		case ok:
			open, marker, run = true, m, n
		default:
			top[i] = !isIndentedCode(content)
		}
	}
	return top, open
}

// parseFence reports whether content is a fence delimiter and returns its
// marker, run length, and the text after the run. After at most three leading
// spaces (and no tab) it begins with three or more backticks or tildes. Per
// tool-enable-disable §2.4, a line indented with a tab or four or more spaces
// is an INDENTED CODE line, not a fence. A backtick fence's info string cannot
// contain a backtick (CommonMark), so a line such as ```x``` is an inline code
// span, not a fence.
func parseFence(content string) (marker byte, run int, info string, ok bool) {
	spaces := 0
	for spaces < len(content) && content[spaces] == ' ' {
		spaces++
	}
	if spaces > 3 {
		return 0, 0, "", false // four or more leading spaces: indented code
	}
	t := content[spaces:]
	if t == "" || (t[0] != '`' && t[0] != '~') {
		return 0, 0, "", false // includes a tab in the indent: indented code
	}
	marker = t[0]
	for run < len(t) && t[run] == marker {
		run++
	}
	info = t[run:]
	if run < 3 || (marker == '`' && strings.ContainsRune(info, '`')) {
		return 0, 0, "", false
	}
	return marker, run, info, true
}

// endsWithTerminator reports whether data's last byte is a line terminator.
func endsWithTerminator(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	last := data[len(data)-1]
	return last == '\n' || last == '\r'
}

// isFence reports whether content is a fence delimiter (see parseFence).
func isFence(content string) bool {
	_, _, _, ok := parseFence(content)
	return ok
}

// isIndentedCode reports whether content begins an indented code block: it
// starts with a tab or four or more spaces.
func isIndentedCode(content string) bool {
	return strings.HasPrefix(content, "\t") || strings.HasPrefix(content, "    ")
}
