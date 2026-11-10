// Package enable turns ethos on and off in a repo per the
// tool-enable-disable standard: it deposits the vendored guide and its §7
// manifest, writes the enabled marker, adds the canonical @-import line to
// the repo CLAUDE.md, chains the two git hooks, and ensures the repo
// .gitignore covers ethos's runtime zones — and reverses the reversible parts
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

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/claudemd"
	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/githook"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/textscan"
	"github.com/punt-labs/ethos/v4/plugin/hooks"
)

// Guide is the vendored agent-facing user guide deposited at
// .punt-labs/ethos/CLAUDE.md. It is static content shipped with the binary,
// the same everywhere.
//
//go:embed guide/CLAUDE.md
var Guide []byte

// Setup is the vendored one-time setup playbook deposited at
// .punt-labs/ethos/ETHOS-SETUP.md (DES-071 tier C). It is not @-imported by
// the guide; an agent opens it on demand. Kept in sync with
// docs/ETHOS-SETUP.md via `make sync-embed` and its validate-content check.
//
//go:embed setup/ETHOS-SETUP.md
var Setup []byte

// CanonicalImport is the exact import line enable writes to and disable
// removes from the repo CLAUDE.md. It must be byte-identical across every
// ethos install.
const CanonicalImport = "@.punt-labs/ethos/CLAUDE.md"

const markerRel = ".punt-labs/ethos/enabled"

// StepResult is one line of the per-step report.
type StepResult struct {
	Step   string `json:"step"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	// Cause states why a skipped step stays skipped, when the reason is a
	// permanent condition of the file rather than of the run.
	Cause string `json:"cause,omitempty"`
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

func (r *Report) stepCause(name, status, detail, cause string) {
	r.Steps = append(r.Steps, StepResult{Step: name, Status: status, Detail: detail, Cause: cause})
}

// Enable turns ethos on in the repo at repoRoot. It is idempotent;
// re-running is the upgrade path. Steps run in order: gitlink guard,
// legacy-ignore preflight (a refusal still chains the hooks), vendored deposit,
// hook chaining, gitignore protection, marker (written only after a complete
// deposit and verified protection), import line. It ends with a next-step hint
// when the repo has no ethos config.
func Enable(repoRoot string) (*Report, error) {
	return EnableTo(repoRoot, repoRoot)
}

// EnableTo is Enable with the checkout root (repoRoot: the per-checkout
// deposit — vendored guide, marker, @-import, hooks) split from the store
// root (storeRoot: shared-local locks/staging and the config-existence read). They
// differ only in a linked worktree: setup writes .punt-labs/ethos.yaml to the
// store, so configStatus must read it there or it spuriously hints "run
// setup" (#370 reader-side). Enable(repoRoot) passes repoRoot for both, so
// existing callers and the single-tree case are unchanged.
func EnableTo(repoRoot, storeRoot string) (*Report, error) {
	return enableTo(repoRoot, storeRoot, defaultGitignoreIO())
}

// enableTo carries per-request ignore I/O so persistence failures can be tested
// through the orchestration boundary without replacing process-wide hooks.
func enableTo(repoRoot, storeRoot string, io gitignoreIO) (*Report, error) {
	rep := &Report{RepoRoot: repoRoot}
	// Host staging does not depend on the recording mode. These roots are
	// supplied by the caller's resolver, never independently rediscovered here.
	p, err := layout.NewRepository(storeRoot, repoRoot, layout.RecordBlame)
	if err != nil {
		return rep, fmt.Errorf("enable layout: %w", err)
	}

	// Refuse before writing anything: a root with no .git of its own would
	// get hook files git never runs.
	lk, hooksDir, err := hooksTarget(repoRoot)
	if err != nil {
		return rep, err
	}

	// Guard the gitlink case: a submodule-mounted .punt-labs/ethos is a
	// foreign git repo the vendored zone cannot be written into. After the
	// hooks check, so a missing git is reported as "install git".
	gitlink, err := audit.IsGitlinkMount(repoRoot)
	if err != nil {
		return rep, fmt.Errorf("checking for a gitlink mount: %w", err)
	}
	if gitlink {
		return rep, fmt.Errorf(
			".punt-labs/ethos is a git submodule (gitlink); the vendored guide cannot be written into a foreign git repo — convert it to an inline directory first (ethos-e29s)")
	}

	// A marker that exists but is not a regular file (a directory, a FIFO, a
	// link to nothing) is refused before anything is written: writing through
	// the link or opening the FIFO would be the wrong answer to it.
	if _, err := resolve.EnabledMarker(repoRoot); err != nil {
		return rep, fmt.Errorf("enable: %w; marker left unchanged", err)
	}

	// Preflight both hosts before any ignore mutation (or deposit). Old rules
	// may be the only protection for private overlays or another tool's data;
	// enable is not permission to move or expose those files.
	//
	// A refusal here still refreshes the hook sections of an already-enabled
	// repo before returning. A stale seal section can block every commit in the
	// repo, so the refresh must never wait on an unrelated migration (legacy
	// ignore retirement) that needs operator cleanup. A repo with no marker is
	// left untouched.
	if err := preflightLegacyIgnores(repoRoot); err != nil {
		return rep, refreshHooksThen(err, repoRoot, storeRoot, rep)
	}
	if !textscan.SamePath(repoRoot, storeRoot) {
		if err := preflightLegacyIgnores(storeRoot); err != nil {
			return rep, refreshHooksThen(err, repoRoot, storeRoot, rep)
		}
	}

	depositWarns, err := deposit(repoRoot, Guide, Setup, p)
	if err != nil {
		return rep, err
	}
	rep.Warnings = append(rep.Warnings, depositWarns...)
	rep.step("vendored", "done", "deposited "+guideRel+", "+setupRel+", and "+manifestRel)

	// Hooks go before gitignore protection, which can still refuse (an
	// unwritable or unprotectable ignore file). The sections are stateless
	// templates, so chaining them first does not create an enabled-but-
	// unprotected state: the marker below is still written last.
	if err := chainHooks(lk, hooksDir, rep, p); err != nil {
		return rep, err
	}

	// Protect before marking enabled, so "enabled implies protected" holds: a
	// rare .gitignore write error must not leave an enabled-but-unprotected
	// checkout.
	action, detail, err := ensureGitignoreWithIO(repoRoot, p, io)
	if action != "" {
		rep.step("gitignore", action, detail)
	}
	if err != nil {
		return rep, err
	}
	if !textscan.SamePath(repoRoot, storeRoot) {
		// The checkout's ignore file travels with its next commit, but it does
		// not exclude runtime we just created in the canonical store checkout.
		// Maintain that owner's shared-local exclusion as well.
		action, detail, err := ensureGitignoreWithIO(storeRoot, p, io)
		if action != "" {
			rep.step("gitignore:store", action, detail)
		}
		if err != nil {
			return rep, fmt.Errorf("protecting shared store local zone: %w", err)
		}
	}

	// The .gitignore only stops future tracking; warn (loudly) when runtime
	// files are already committed — the exact state this guard targets.
	if tracked, err := trackedRuntimeFiles(repoRoot); err != nil {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("checking for already-tracked runtime files: %v", err))
	} else if len(tracked) > 0 {
		rep.step("gitignore", "tracked", fmt.Sprintf("%d runtime file(s) already git-tracked — see warning", len(tracked)))
		rep.Warnings = append(rep.Warnings, trackedRuntimeWarning(tracked))
	}

	// Marker-last: install a new marker only after deposit and protection
	// complete. A preexisting marker is not proof of protection and is not
	// removed on failure; re-enable must still fail visibly if rules changed.
	// Recheck C after maintaining S too; success at one host is not proof about
	// the other. Like other host edits this does not fence concurrent user edits.
	protectionRoots := []string{repoRoot}
	if !textscan.SamePath(repoRoot, storeRoot) {
		protectionRoots = append(protectionRoots, storeRoot)
	}
	for _, root := range protectionRoots {
		if err := verifyLocalProtection(root, p, io); err != nil {
			return rep, err
		}
	}
	if err := writeMarker(repoRoot); err != nil {
		return rep, err
	}
	rep.step("marker", "done", markerRel)

	added, err := claudemd.Register(filepath.Join(repoRoot, "CLAUDE.md"), CanonicalImport, claudemd.WithRepository(p))
	if err != nil {
		return rep, fmt.Errorf("adding import line: %w", err)
	}
	if added {
		rep.step("import", "done", "added "+CanonicalImport)
	} else {
		rep.step("import", "already", CanonicalImport+" already present")
	}

	hint, warning := configStatus(storeRoot)
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
	return DisableTo(repoRoot, repoRoot, force)
}

// DisableTo is Disable with explicit checkout and canonical shared-store roots.
// Linked-worktree callers must use this form so host locks match EnableTo.
func DisableTo(repoRoot, storeRoot string, force bool) (*Report, error) {
	rep := &Report{RepoRoot: repoRoot}
	p, err := layout.NewRepository(storeRoot, repoRoot, layout.RecordBlame)
	if err != nil {
		return rep, fmt.Errorf("disable layout: %w", err)
	}

	// Resolve the hooks directory before the sibling probe and before changing
	// anything, so a refusal leaves the repo enabled rather than half disabled,
	// and a degraded lookup is reported as such, not as a failed git probe.
	lk, hooksDir, err := unchainTarget(repoRoot, force)
	if err != nil {
		return rep, err
	}

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

	removed, err := claudemd.Deregister(filepath.Join(repoRoot, "CLAUDE.md"), CanonicalImport, claudemd.WithRepository(p))
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

	if err := unchainHooks(lk, hooksDir, rep, p); err != nil {
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
	present, err := resolve.EnabledMarker(repoRoot)
	if err != nil {
		return fmt.Errorf("writing marker %s: %w", markerRel, err)
	}
	if present {
		return nil // a regular file already: nothing to write
	}
	// O_EXCL: never through a link or onto anything that appeared since.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("writing marker %s: %w", markerRel, err)
	}
	if err := f.Close(); err != nil {
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

// hooksTarget resolves the hooks directory for enable. Only a directory git
// itself named is acted on: a degraded lookup ignores core.hooksPath, so the
// hooks written there might never run, and enable would report success for a
// repo whose seal does not fire.
func hooksTarget(repoRoot string) (githook.Lookup, string, error) {
	l := githook.HooksDir(repoRoot)
	dir, err := l.Verified()
	if err != nil {
		return l, "", fmt.Errorf("cannot install hooks: %w", l.Refusal())
	}
	return l, dir, nil
}

// refreshHooksThen returns cause. When cause is a legacy-retirement refusal in
// a repo that is already enabled, it first refreshes the hook sections, joining
// any refresh failure so neither is lost. A first-time enable that refuses
// leaves the repo untouched: chaining hooks with no marker is the "hook chained
// but ethos not enabled here" state doctor tells the operator to clean up.
// Every other refusal mutates nothing.
func refreshHooksThen(cause error, repoRoot, storeRoot string, rep *Report) error {
	if !errors.Is(cause, errRetirementRefused) {
		return cause // other refusals (hazardous files, git failures) mutate nothing
	}
	present, err := resolve.EnabledMarker(repoRoot)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("refreshing hooks: %w", err))
	}
	if !present {
		return cause
	}
	if err := refreshHooks(repoRoot, storeRoot, rep, true); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// chainHooks chains the seal and trailer sections into pre-commit and
// commit-msg in dir, the hooks directory hooksTarget verified.
func chainHooks(l githook.Lookup, dir string, rep *Report, p layout.Policy) error {
	rep.Warnings = append(rep.Warnings, l.Warnings...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating hooks dir %s: %w", dir, err)
	}
	for _, name := range layout.HookNames() {
		s, ok := hookSpecs[name]
		if !ok {
			return fmt.Errorf("no hook template for %s", name)
		}
		res, err := githook.Chain(filepath.Join(dir, name), s.src, s.tag, s.ident, githook.WithRepository(p))
		if err != nil {
			return fmt.Errorf("chaining %s: %w", name, err)
		}
		rep.Warnings = append(rep.Warnings, res.Warnings...)
		rep.step("hook:"+name, res.Action, res.Path)
	}
	return nil
}

// hookSpecs is what `ethos enable` chains into each hook named by
// layout.HookNames.
var hookSpecs = map[string]struct {
	src   []byte
	tag   string
	ident string
}{
	layout.HookPreCommit: {hooks.PreCommit, hooks.SealTag, hooks.SealIdent},
	layout.HookCommitMsg: {hooks.CommitMsg, hooks.TrailerTag, hooks.TrailerIdent},
}

// unchainTarget resolves the hooks directory disable strips. A degraded lookup
// cannot see core.hooksPath, so the directory it guesses may not be the one git
// runs, whether or not the guess exists. Without force disable refuses; with
// force it returns an empty dir and unchainHooks records that the hooks were
// skipped.
func unchainTarget(repoRoot string, force bool) (githook.Lookup, string, error) {
	l := githook.HooksDir(repoRoot)
	dir, err := l.Verified()
	switch {
	case err == nil:
		return l, dir, nil
	case force:
		return l, "", nil
	}
	msg := err.Error()
	if r := l.Remedy(); r != "" {
		msg += "; " + r
	}
	return l, "", fmt.Errorf("%s; or re-run with --force to remove the import and marker and leave hook sections in place", msg)
}

// unchainHooks strips the seal and trailer sections from the hooks in dir. An
// empty dir means unchainTarget found none it could verify.
func unchainHooks(l githook.Lookup, dir string, rep *Report, p layout.Policy) error {
	rep.Warnings = append(rep.Warnings, l.Warnings...)
	if dir == "" {
		rep.step("hook:*", "skipped", fmt.Sprintf(
			"hooks directory not verified (%s); ethos sections may remain under core.hooksPath", l.Reason))
		return nil
	}
	for _, name := range layout.HookNames() {
		s, ok := hookSpecs[name]
		if !ok {
			return fmt.Errorf("no hook template for %s", name)
		}
		res, err := githook.Unchain(filepath.Join(dir, name), s.tag, s.ident, githook.WithRepository(p))
		if err != nil {
			return fmt.Errorf("unchaining %s: %w", name, err)
		}
		rep.Warnings = append(rep.Warnings, res.Warnings...)
		rep.step("hook:"+name, res.Action, res.Path)
	}
	return nil
}

// configStatus decides enable's closing message. It returns a "run ethos
// setup" hint when the repo has no ethos config (no .punt-labs/ethos.yaml and
// no identities), or a warning when the config file exists but cannot be read
// or parsed — a malformed config must not masquerade as absent and draw the
// setup hint. At most one of the two is non-empty.
//
// storeRoot is the shared store (StoreRootIfAny), where setup writes
// .punt-labs/ethos.yaml and identities live — NOT the checkout, or from a
// worktree the check reads an absent config and spuriously hints "run setup"
// (#370). An empty storeRoot (no repo / a refused override) yields no hint:
// the config existence cannot be determined, and a relative read would probe
// the cwd.
func configStatus(storeRoot string) (hint, warning string) {
	if storeRoot == "" {
		return "", ""
	}
	cfg, err := resolve.ConfigOf(storeRoot)
	if err != nil {
		return "", fmt.Sprintf(".punt-labs/ethos.yaml is unreadable: %v", err)
	}
	if cfg != nil {
		return "", ""
	}
	if entries, err := os.ReadDir(filepath.Join(storeRoot, ".punt-labs", "ethos", "identities")); err == nil && len(entries) > 0 {
		return "", ""
	}
	return "run `ethos setup` to configure identities", ""
}

// enabledSiblingWorktrees returns the other worktrees of this repo that still
// carry the enabled marker (resolve.EnabledMarker). git runs in the parsed
// environment, so an inherited GIT_DIR (a hook's) cannot redirect it. The git
// hooks dir is shared across all worktrees,
// so unchaining here disables the seal for every one of them. It fails closed:
// a worktree-list probe failure returns an error (disable refuses), and a
// sibling whose marker cannot be examined is counted as enabled with the cause
// named, never silently treated as disabled.
func enabledSiblingWorktrees(repoRoot string) ([]string, error) {
	out, err := gitenv.OutputEnv(repoRoot, "git", gitenv.Parsed(), "worktree", "list", "--porcelain")
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
		// The same rule as doctor and the hooks: a store path that cannot be read,
		// or a marker that is not a regular file, is counted as enabled with the
		// cause named, never silently as disabled.
		present, markErr := resolve.EnabledMarker(path)
		switch {
		case markErr != nil:
			enabled = append(enabled, fmt.Sprintf("%s (cannot tell: %v)", path, markErr))
		case present:
			enabled = append(enabled, path)
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
