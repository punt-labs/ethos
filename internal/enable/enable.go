// Package enable turns ethos on and off in a repo per the
// tool-enable-disable standard: it deposits the vendored guide and its §7
// manifest, writes the enabled marker, adds the canonical @-import line to
// the repo CLAUDE.md, and chains the two git hooks — and reverses all four
// non-destructively on disable. It composes internal/claudemd (the import
// line) and internal/githook (the hook chaining); it never reads, merges, or
// overwrites repo config or seal-managed data.
package enable

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/hooks"
	"github.com/punt-labs/ethos/internal/audit"
	"github.com/punt-labs/ethos/internal/claudemd"
	"github.com/punt-labs/ethos/internal/githook"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/punt-labs/ethos/internal/textscan"
)

// Guide is the vendored agent-facing user guide deposited at
// .punt-labs/ethos/CLAUDE.md. It is static content shipped with the binary,
// the same everywhere.
//
//go:embed guide/CLAUDE.md
var Guide []byte

// CanonicalImport is the exact import line enable writes to and disable
// removes from the repo CLAUDE.md. It must be byte-identical across every
// ethos install.
const CanonicalImport = "@.punt-labs/ethos/CLAUDE.md"

const markerRel = ".punt-labs/ethos/enabled"

// Hook tags and idents, shared by chain and unchain.
const (
	sealTag      = "ETHOS DES-058 SEAL"
	sealIdent    = "hooks/pre-commit.sh — Seal pending live audit lines"
	trailerTag   = "ETHOS DES-054 TRAILER"
	trailerIdent = "hooks/commit-msg.sh — Append Mission:/Delegation:"
)

// StepResult is one line of the per-step report.
type StepResult struct {
	Step   string `json:"step"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Report is the outcome of an enable or disable run.
type Report struct {
	RepoRoot string       `json:"repo_root"`
	Steps    []StepResult `json:"steps"`
	Warnings []string     `json:"warnings,omitempty"`
	Hint     string       `json:"hint,omitempty"`
}

func (r *Report) step(name, status, detail string) {
	r.Steps = append(r.Steps, StepResult{Step: name, Status: status, Detail: detail})
}

// Enable turns ethos on in the repo at repoRoot. It is idempotent;
// re-running is the upgrade path. Steps run in order: gitlink guard, vendored
// deposit, marker (written only after a complete deposit), import line, hook
// chaining. It ends with a next-step hint when the repo has no ethos config.
func Enable(repoRoot string) (*Report, error) {
	rep := &Report{RepoRoot: repoRoot}

	// Guard the gitlink case: a submodule-mounted .punt-labs/ethos is a
	// foreign git repo the vendored zone cannot be written into.
	if audit.IsGitlinkMount(repoRoot) {
		return rep, fmt.Errorf(
			".punt-labs/ethos is a git submodule (gitlink); the vendored guide cannot be written into a foreign git repo — convert it to an inline directory first (ethos-e29s)")
	}

	depositWarns, err := deposit(repoRoot, Guide)
	if err != nil {
		return rep, err
	}
	rep.Warnings = append(rep.Warnings, depositWarns...)
	rep.step("vendored", "done", "deposited "+guideRel+" and "+manifestRel)

	// Marker-last: written only after the deposit completes, so a marker
	// present always implies a complete vendored zone.
	if err := writeMarker(repoRoot); err != nil {
		return rep, err
	}
	rep.step("marker", "done", markerRel)

	added, err := claudemd.Register(filepath.Join(repoRoot, "CLAUDE.md"), CanonicalImport)
	if err != nil {
		return rep, fmt.Errorf("adding import line: %w", err)
	}
	if added {
		rep.step("import", "done", "added "+CanonicalImport)
	} else {
		rep.step("import", "already", CanonicalImport+" already present")
	}

	if err := chainHooks(repoRoot, rep); err != nil {
		return rep, err
	}

	hint, warning := configStatus(repoRoot)
	if warning != "" {
		rep.Warnings = append(rep.Warnings, warning)
	}
	rep.Hint = hint
	return rep, nil
}

// Disable turns ethos off in the repo at repoRoot, non-destructively. It
// refuses when a sibling worktree is still enabled (the hooks dir is shared)
// unless force is set. It removes the import line, deletes the marker, and
// unchains both hooks; it leaves the vendored guide and all config/seal data
// dormant on disk and does not run a final seal.
func Disable(repoRoot string, force bool) (*Report, error) {
	rep := &Report{RepoRoot: repoRoot}

	if !force {
		// Fail closed: a probe that cannot confirm the siblings are disabled
		// must refuse, not silently degrade the operator-ruled refuse-by-
		// default to --force behavior.
		siblings, err := enabledSiblingWorktrees(repoRoot)
		if err != nil {
			return rep, fmt.Errorf(
				"cannot verify sibling worktrees are disabled: %w — refusing; re-run with --force to unchain anyway", err)
		}
		if len(siblings) > 0 {
			return rep, fmt.Errorf(
				"disable would unchain the shared git hooks, but these worktrees are still enabled: %s — re-run with --force to unchain anyway",
				strings.Join(siblings, ", "))
		}
	}

	removed, err := claudemd.Deregister(filepath.Join(repoRoot, "CLAUDE.md"), CanonicalImport)
	if err != nil {
		return rep, fmt.Errorf("removing import line: %w", err)
	}
	if removed {
		rep.step("import", "done", "removed "+CanonicalImport)
	} else {
		rep.step("import", "already", "no import line present")
	}

	markerRemoved, err := removeMarker(repoRoot)
	if err != nil {
		return rep, err
	}
	if markerRemoved {
		rep.step("marker", "done", "deleted "+markerRel)
	} else {
		rep.step("marker", "already", "no marker present")
	}

	if err := unchainHooks(repoRoot, rep); err != nil {
		return rep, err
	}

	if n := unsealedAuditLines(repoRoot); n > 0 {
		rep.step("audit", "info", fmt.Sprintf("%d unsealed audit lines remain in the local zone; re-enable to seal them", n))
	}

	rep.step("vendored", "kept", "guide left dormant at "+guideRel)
	return rep, nil
}

func writeMarker(repoRoot string) error {
	path := filepath.Join(repoRoot, markerRel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating marker dir: %w", err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		return fmt.Errorf("writing marker %s: %w", markerRel, err)
	}
	return nil
}

// removeMarker deletes the enabled marker and reports whether it removed one.
// A missing marker is not an error (idempotent) but returns removed=false, so
// disable can report "already" rather than telling a --json consumer a
// deletion happened.
func removeMarker(repoRoot string) (bool, error) {
	if err := os.Remove(filepath.Join(repoRoot, markerRel)); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("deleting marker %s: %w", markerRel, err)
	}
	return true, nil
}

// chainHooks resolves the shared hooks directory and chains the seal and
// trailer sections into pre-commit and commit-msg.
func chainHooks(repoRoot string, rep *Report) error {
	dir, warns := githook.HooksDir(repoRoot)
	rep.Warnings = append(rep.Warnings, warns...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating hooks dir %s: %w", dir, err)
	}
	specs := []struct {
		name  string
		src   []byte
		tag   string
		ident string
	}{
		{"pre-commit", hooks.PreCommit, sealTag, sealIdent},
		{"commit-msg", hooks.CommitMsg, trailerTag, trailerIdent},
	}
	for _, s := range specs {
		res, err := githook.Chain(filepath.Join(dir, s.name), s.src, s.tag, s.ident)
		if err != nil {
			return fmt.Errorf("chaining %s: %w", s.name, err)
		}
		rep.Warnings = append(rep.Warnings, res.Warnings...)
		rep.step("hook:"+s.name, res.Action, res.Path)
	}
	return nil
}

// unchainHooks strips the seal and trailer sections from the shared hooks.
func unchainHooks(repoRoot string, rep *Report) error {
	dir, warns := githook.HooksDir(repoRoot)
	rep.Warnings = append(rep.Warnings, warns...)
	specs := []struct {
		name  string
		tag   string
		ident string
	}{
		{"pre-commit", sealTag, sealIdent},
		{"commit-msg", trailerTag, trailerIdent},
	}
	for _, s := range specs {
		res, err := githook.Unchain(filepath.Join(dir, s.name), s.tag, s.ident)
		if err != nil {
			return fmt.Errorf("unchaining %s: %w", s.name, err)
		}
		rep.Warnings = append(rep.Warnings, res.Warnings...)
		rep.step("hook:"+s.name, res.Action, res.Path)
	}
	return nil
}

// configStatus decides enable's closing message. It returns a "run ethos
// setup" hint when the repo has no ethos config (no .punt-labs/ethos.yaml and
// no identities), or a warning when the config file exists but cannot be read
// or parsed — a malformed config must not masquerade as absent and draw the
// setup hint. At most one of the two is non-empty.
func configStatus(repoRoot string) (hint, warning string) {
	cfg, err := resolve.LoadRepoConfig(repoRoot)
	if err != nil {
		return "", fmt.Sprintf(".punt-labs/ethos.yaml is unreadable: %v", err)
	}
	if cfg != nil {
		return "", ""
	}
	if entries, err := os.ReadDir(filepath.Join(repoRoot, ".punt-labs", "ethos", "identities")); err == nil && len(entries) > 0 {
		return "", ""
	}
	return "run `ethos setup` to configure identities", ""
}

// enabledSiblingWorktrees returns the other worktrees of this repo that still
// carry the enabled marker. The git hooks dir is shared across all worktrees,
// so unchaining here disables the seal for every one of them. It fails closed:
// a worktree-list probe failure returns an error (disable refuses), and a
// sibling marker whose stat fails for a non-IsNotExist reason is counted as
// enabled with the errno named, never silently treated as disabled.
func enabledSiblingWorktrees(repoRoot string) ([]string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain").Output()
	if err != nil {
		// Surface git's own diagnostic (dubious ownership, not a repo, …)
		// rather than a bare "exit status 128".
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("git worktree list: %w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	var enabled []string
	for _, line := range strings.Split(string(out), "\n") {
		path, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		path = strings.TrimSpace(path)
		if path == "" || textscan.SamePath(path, repoRoot) {
			continue
		}
		_, statErr := os.Stat(filepath.Join(path, markerRel))
		switch {
		case statErr == nil:
			enabled = append(enabled, path)
		case !os.IsNotExist(statErr):
			enabled = append(enabled, fmt.Sprintf("%s (marker stat error: %v)", path, statErr))
		}
	}
	return enabled, nil
}

// unsealedAuditLines counts live session audit lines past the sealed
// watermark. A session with no sealed directory yet has every live line
// unsealed. The count is informational only.
func unsealedAuditLines(repoRoot string) int {
	dir := audit.LiveSessionsDir(repoRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	total := 0
	const suffix = ".audit.jsonl"
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		sid := strings.TrimSuffix(e.Name(), suffix)
		if n, err := audit.SessionUnsealedCount(repoRoot, sid); err == nil {
			total += n
			continue
		}
		total += countNonEmptyLines(filepath.Join(dir, e.Name()))
	}
	return total
}

func countNonEmptyLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}
