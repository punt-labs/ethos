package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/punt-labs/ethos/v4/internal/bundle"
	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/team"
)

// --- flags ---

var (
	addBundleName   string
	addBundleGlobal bool
	addBundleApply  bool
)

// --- commands ---

var teamAvailableCmd = &cobra.Command{
	Use:   "available",
	Short: "List all discoverable team bundles",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTeamAvailable(cmd)
	},
}

var teamActivateCmd = &cobra.Command{
	Use:   "activate <name>",
	Short: "Set the active team bundle in repo config",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTeamActivate(cmd, args[0])
	},
}

var teamActiveCmd = &cobra.Command{
	Use:   "active",
	Short: "Show the currently active team bundle",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTeamActive(cmd)
	},
}

var teamDeactivateCmd = &cobra.Command{
	Use:   "deactivate",
	Short: "Clear the active team bundle in repo config",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTeamDeactivate(cmd)
	},
}

var teamAddBundleCmd = &cobra.Command{
	Use:   "add-bundle <git-url>",
	Short: "Scaffold a new team bundle from a git URL",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTeamAddBundle(cmd, args[0])
	},
}

func init() {
	teamAddBundleCmd.Flags().StringVar(&addBundleName, "name", "", "Bundle name (defaults to last URL path segment)")
	teamAddBundleCmd.Flags().BoolVar(&addBundleGlobal, "global", false, "Install into ~/.punt-labs/ethos/bundles/ via git clone")
	teamAddBundleCmd.Flags().BoolVar(&addBundleApply, "apply", false, "Execute the git commands (default: dry-run)")

	teamCmd.AddCommand(
		teamAvailableCmd,
		teamActivateCmd,
		teamActiveCmd,
		teamDeactivateCmd,
		teamAddBundleCmd,
	)
}

// --- available ---

// availableRow is a single row of the `team available` table / JSON.
type availableRow struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Path   string `json:"path"`
	Active bool   `json:"active"`
}

func runTeamAvailable(cmd *cobra.Command) error {
	loc := currentRoots()
	if rootErr := loc.StoreFailure(); rootErr != nil {
		return fmt.Errorf("locating the repository store: %w", rootErr)
	}
	repoRoot := loc.Store
	globalRoot := defaultGlobalRoot()

	bundles, err := bundle.List(repoRoot, globalRoot)
	if err != nil {
		return fmt.Errorf("listing bundles: %w", err)
	}

	// Resolve the actually-active bundle (honors repo-over-global
	// precedence) so we mark only the bundle ResolveActive picks —
	// not every bundle whose name matches.
	var activePath string
	if repoRoot != "" {
		if active, err := bundle.ResolveActive(repoRoot, globalRoot); err == nil &&
			active != nil && active.Source != bundle.SourceLegacy {
			activePath = active.Path
		}
	}

	rows := make([]availableRow, 0, len(bundles))
	for _, b := range bundles {
		rows = append(rows, availableRow{
			Name:   b.Name,
			Source: string(b.Source),
			Path:   b.Path,
			Active: activePath != "" && b.Path == activePath,
		})
	}

	out := cmd.OutOrStdout()
	if jsonOutput {
		return writeJSON(out, rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "No bundles discovered.")
		return nil
	}
	printAvailableTable(out, rows)
	return nil
}

// printAvailableTable formats rows as a text table via hook.FormatTable.
func printAvailableTable(w io.Writer, rows []availableRow) {
	headers := []string{"NAME", "SOURCE", "PATH", "ACTIVE"}
	data := make([][]string, len(rows))
	for i, r := range rows {
		active := ""
		if r.Active {
			active = "*"
		}
		data[i] = []string{r.Name, r.Source, r.Path, active}
	}
	fmt.Fprintln(w, hook.FormatTable(headers, data))
}

// --- activate ---

func runTeamActivate(cmd *cobra.Command, name string) error {
	if !bundle.ValidName.MatchString(name) {
		return fmt.Errorf("invalid bundle name %q (must match %s)", name, bundle.ValidName.String())
	}
	loc := currentRoots()
	if rootErr := loc.StoreFailure(); rootErr != nil {
		return fmt.Errorf("locating the repository store: %w", rootErr)
	}
	repoRoot := loc.Store
	if repoRoot == "" {
		return fmt.Errorf("not in a git repository")
	}
	globalRoot := defaultGlobalRoot()

	bundles, err := bundle.List(repoRoot, globalRoot)
	if err != nil {
		return fmt.Errorf("listing bundles: %w", err)
	}
	var match *bundle.Bundle
	for i := range bundles {
		if bundles[i].Source == bundle.SourceLegacy {
			continue
		}
		if bundles[i].Name == name {
			match = &bundles[i]
			break
		}
	}
	if match == nil {
		return fmt.Errorf("bundle %q not found; available bundles:\n%s", name, listBundleNames(bundles))
	}

	out := cmd.OutOrStdout()

	// The chosen bundle is checked against the resolution policy before anything
	// is written: under `resolution: repo-only` a global bundle does not travel
	// with the checkout and is refused, with the reason. (An unreadable or
	// unknown policy cannot be checked; that is the repair path below.)
	mode, modeErr := resolve.ResolveResolution(repoRoot)
	if modeErr == nil && mode == resolve.ResolutionRepoOnly {
		if err := bundle.VerifyRepoOnly(loc.Ethos, match.Path, match); err != nil {
			return fmt.Errorf("team activate: %w; nothing was written", err)
		}
	}

	// A bundle or policy that cannot be honoured is exactly what activate may be
	// run to repair. Write the two config keys and nothing else: no identity
	// lookup, no CEO/COO resolution (both would consult stores the broken
	// configuration governs). Say so, and say how to finish.
	if issue := loc.configIssue(); issue != nil {
		if err := setConfigKey(repoRoot, "team", name); err != nil {
			return fmt.Errorf("writing team: %w", err)
		}
		if err := setConfigKey(repoRoot, "active_bundle", name); err != nil {
			return fmt.Errorf("writing config: %w", err)
		}
		if modeErr != nil {
			// The resolution: value itself is the failure; activate cannot repair it.
			fmt.Fprintf(cmd.ErrOrStderr(), "ethos: team activate: the resolution: value in .punt-labs/ethos.yaml is not valid (%v) "+
				"and must be fixed there: activate does not repair it. %q is written as the active bundle, but the policy "+
				"it is checked against is unknown and the CEO/COO leadership roles were not assigned; "+
				"after fixing the value, run `ethos team activate %s` again.\n", modeErr, name, name)
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "ethos: team activate: the repository's configuration was unusable (%v); "+
				"%q is written as the active bundle and the CEO/COO leadership roles were not assigned. "+
				"Once the configuration is valid, run `ethos team activate %s` again to assign them.\n", issue, name, name)
		}
		if jsonOutput {
			return writeJSON(out, map[string]string{"name": name, "status": "activated-leadership-skipped"})
		}
		fmt.Fprintf(out, "activated: %s (source: %s, path: %s); leadership not assigned\n", match.Name, match.Source, match.Path)
		return nil
	}

	current, err := resolve.ResolveActiveBundle(repoRoot)
	if err != nil {
		return fmt.Errorf("reading active bundle: %w", err)
	}

	// Resolve the human CEO to bind BEFORE any config write, so a caller who
	// cannot supply one (an agent persona such as claude, or no resolvable
	// identity) skips the rebind cleanly rather than failing after the switch
	// has already persisted. The ceo seat is the human; an agent caller can't
	// determine the human, so leadership from the human's earlier `ethos
	// setup` is left intact.
	ceoHandle, bindCEO := resolveActivateCEO(cmd.ErrOrStderr())

	if current == name {
		// Explicitly running activate commands convergence. If the team key
		// diverged (or is absent), repair it here — otherwise the remedy
		// setup suggests ("run ethos team activate <bundle>") is a no-op in
		// exactly the state where it is suggested.
		team, err := resolve.ResolveTeam(repoRoot)
		if err != nil {
			return fmt.Errorf("reading team: %w", err)
		}
		if team != name {
			if err := applyLeadershipOnActivate(cmd, repoRoot, match.Path, name, ceoHandle, bindCEO); err != nil {
				return err
			}
			if err := setConfigKey(repoRoot, "team", name); err != nil {
				return fmt.Errorf("writing team: %w", err)
			}
			if jsonOutput {
				return writeJSON(out, map[string]string{"name": name, "status": "team-repaired"})
			}
			fmt.Fprintf(out, "team repaired to %q\n", name)
			return nil
		}
		if err := applyLeadershipOnActivate(cmd, repoRoot, match.Path, name, ceoHandle, bindCEO); err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(out, map[string]string{"name": name, "status": "already-active"})
		}
		fmt.Fprintf(out, "bundle %q is already active\n", name)
		return nil
	}

	// Validate the leadership rebind against the target bundle's team BEFORE
	// touching config, so a malformed team or illegal rebind fails with no
	// bundle switch persisted. The actual write happens after activation
	// (below): the repo-team Save validates member existence against the
	// ACTIVE bundle, so the new bundle's specialists only resolve once
	// active_bundle points at it. This split leaves only a genuine I/O Save
	// error as a post-switch failure.
	if err := precheckActivateLeadership(match.Path, name, ceoHandle, bindCEO); err != nil {
		return err
	}

	// Write team before active_bundle (the sentinel) so an interrupted run
	// self-heals, and keep the two keys consistent: a bundle switch that
	// left team pointing at the old bundle would inject the wrong team at
	// SessionStart with no signal.
	if err := setConfigKey(repoRoot, "team", name); err != nil {
		return fmt.Errorf("writing team: %w", err)
	}
	if err := setConfigKey(repoRoot, "active_bundle", name); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	if err := applyLeadershipOnActivate(cmd, repoRoot, match.Path, name, ceoHandle, bindCEO); err != nil {
		return err
	}

	if jsonOutput {
		return writeJSON(out, availableRow{
			Name:   match.Name,
			Source: string(match.Source),
			Path:   match.Path,
			Active: true,
		})
	}
	fmt.Fprintf(out, "activated: %s (source: %s, path: %s)\n", match.Name, match.Source, match.Path)
	return nil
}

// resolveActivateCEO resolves the human identity to bind to the ceo seat on
// activation, and reports whether one was found. The ceo is the human, so an
// agent caller (claude via `ethos iam`) or an unresolvable/absent caller
// yields ok=false with a loud skip warning — activation still succeeds and
// the leadership from the human's earlier `ethos setup` stays intact. This
// runs before any config write so the skip decision never leaves a persisted
// switch reported as a failure.
func resolveActivateCEO(errw io.Writer) (handle string, ok bool) {
	is := identityStore()
	checkout, err := currentRoots().checkoutForGit()
	var h string
	if err == nil {
		h, err = resolve.Resolve(is, sessionStore(), checkout)
	}
	if err != nil {
		fmt.Fprintf(errw, "ethos: team activate: skipping CEO/COO assignment — no caller identity resolved (%v); run \"ethos setup\" first\n", err)
		return "", false
	}
	id, err := is.Load(h)
	if err != nil {
		fmt.Fprintf(errw, "ethos: team activate: skipping CEO/COO assignment — cannot load caller %q (%v)\n", h, err)
		return "", false
	}
	if id.Kind != "human" {
		fmt.Fprintf(errw, "ethos: team activate: skipping CEO/COO assignment — caller %q is an agent, not the human CEO; leadership from \"ethos setup\" is left intact\n", h)
		return "", false
	}
	return h, true
}

// applyLeadershipOnActivate rebinds the target bundle's ceo/coo seats to the
// resolved human and claude, so `ethos team activate` yields the same default
// org shape as `ethos setup` (shared assignLeadershipTeam logic). bundleRoot
// is the target bundle's path (match.Path), so this resolves the correct team
// even before active_bundle is written — callers invoke it before the config
// write to keep activation atomic. bindCEO gates the rebind: when false (agent
// or no human caller) it is a no-op — resolveActivateCEO explained the skip.
func applyLeadershipOnActivate(cmd *cobra.Command, repoRoot, bundleRoot, bundleName, ceoHandle string, bindCEO bool) error {
	if !bindCEO {
		return nil
	}
	return assignLeadershipTeam(cmd.ErrOrStderr(), repoRoot, bundleRoot, bundleName, ceoHandle)
}

// precheckActivateLeadership validates the target bundle's leadership rebind
// (loaded read-only from bundleRoot) before the caller writes any config, so
// an incomplete pair or a handle collision fails the activation with nothing
// persisted. It is a no-op when the rebind is skipped (bindCEO false) or the
// bundle ships no team; ErrNotFound is not an error here.
func precheckActivateLeadership(bundleRoot, bundleName, ceoHandle string, bindCEO bool) error {
	if !bindCEO || bundleRoot == "" {
		return nil
	}
	src, err := team.NewStore(bundleRoot).Load(bundleName)
	if err != nil {
		if errors.Is(err, team.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("loading bundle team %q: %w", bundleName, err)
	}
	_, err = validateLeadership(src, ceoHandle, cooIdentity)
	return err
}

// listBundleNames formats bundle names with their source for error output.
func listBundleNames(bundles []bundle.Bundle) string {
	if len(bundles) == 0 {
		return "  (none)"
	}
	var b strings.Builder
	for _, bn := range bundles {
		if bn.Source == bundle.SourceLegacy {
			continue
		}
		fmt.Fprintf(&b, "  - %s (%s)\n", bn.Name, bn.Source)
	}
	if b.Len() == 0 {
		return "  (none)"
	}
	return strings.TrimRight(b.String(), "\n")
}

// --- active ---

func runTeamActive(cmd *cobra.Command) error {
	loc := currentRoots()
	if rootErr := loc.StoreFailure(); rootErr != nil {
		return fmt.Errorf("locating the repository store: %w", rootErr)
	}
	repoRoot := loc.Store
	globalRoot := defaultGlobalRoot()

	b, err := bundle.ResolveActive(repoRoot, globalRoot)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if jsonOutput {
		if b == nil {
			_, err := fmt.Fprintln(out, "null")
			return err
		}
		return writeJSON(out, availableRow{
			Name:   b.Name,
			Source: string(b.Source),
			Path:   b.Path,
			Active: b.Source != bundle.SourceLegacy,
		})
	}

	if b == nil {
		fmt.Fprintln(out, "(none)")
		return nil
	}
	if b.Source == bundle.SourceLegacy {
		fmt.Fprintln(out, "(legacy)")
		return nil
	}
	fmt.Fprintf(out, "%s (source: %s, path: %s)\n", b.Name, b.Source, b.Path)
	return nil
}

// --- deactivate ---

func runTeamDeactivate(cmd *cobra.Command) error {
	loc := currentRoots()
	if rootErr := loc.StoreFailure(); rootErr != nil {
		return fmt.Errorf("locating the repository store: %w", rootErr)
	}
	repoRoot := loc.Store
	if repoRoot == "" {
		return fmt.Errorf("not in a git repository")
	}
	current, err := resolve.ResolveActiveBundle(repoRoot)
	if err != nil {
		return fmt.Errorf("reading active bundle: %w", err)
	}
	out := cmd.OutOrStdout()
	if current == "" {
		if jsonOutput {
			return writeJSON(out, map[string]string{"status": "no-active-bundle"})
		}
		fmt.Fprintln(out, "no active bundle to deactivate")
		return nil
	}
	if err := setConfigKey(repoRoot, "active_bundle", ""); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	if jsonOutput {
		return writeJSON(out, map[string]string{"deactivated": current})
	}
	fmt.Fprintf(out, "deactivated: was %s\n", current)
	return nil
}

// --- add-bundle ---

// bundleNameFromURL extracts the last path segment from a git URL and
// slug-sanitizes it so it matches bundle.ValidName.
func bundleNameFromURL(url string) string {
	s := strings.TrimSuffix(url, ".git")
	// Split on / and : to handle both HTTPS and SSH URLs.
	for _, sep := range []string{"/", ":"} {
		if i := strings.LastIndex(s, sep); i >= 0 {
			s = s[i+1:]
		}
	}
	s = strings.ToLower(s)
	// Replace disallowed characters with hyphens.
	re := regexp.MustCompile(`[^a-z0-9-]+`)
	s = re.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// bundleCloneTimeout bounds `git clone` and `git submodule add` for a team
// bundle: a network operation, so it gets its own limit, not the 10s every
// local git read gets.
const bundleCloneTimeout = 5 * time.Minute

// cloneLimit is the bound runTeamAddBundle applies; a test may shorten it.
var cloneLimit = bundleCloneTimeout

func runTeamAddBundle(cmd *cobra.Command, url string) error {
	name := addBundleName
	if name == "" {
		name = bundleNameFromURL(url)
	}
	if !bundle.ValidName.MatchString(name) {
		return fmt.Errorf("invalid bundle name %q (must match %s); use --name to override", name, bundle.ValidName.String())
	}

	var gitArgs []string
	var targetDir string
	var gitDir string
	var relTarget string // the submodule path, relative to the repo root
	if addBundleGlobal {
		globalRoot := defaultGlobalRoot()
		if globalRoot == "" {
			return fmt.Errorf("cannot determine home directory for global install")
		}
		targetDir = filepath.Join(globalRoot, "bundles", name)
		gitArgs = []string{"git", "clone", url, targetDir}
	} else {
		loc := currentRoots()
		if rootErr := loc.StoreFailure(); rootErr != nil {
			return fmt.Errorf("locating the repository store: %w", rootErr)
		}
		repoRoot := loc.Store
		if repoRoot == "" {
			return fmt.Errorf("not in a git repository (use --global to install without a repo)")
		}
		targetDir = filepath.Join(repoRoot, ".punt-labs", "ethos-bundles", name)
		// git submodule add expects a path relative to the repo root.
		// Absolute paths produce "outside repository" errors and
		// incorrect .gitmodules entries.
		relTarget = filepath.Join(".punt-labs", "ethos-bundles", name)
		gitArgs = []string{"git", "submodule", "add", url, relTarget}
		gitDir = repoRoot
	}

	out := cmd.OutOrStdout()
	if !addBundleApply {
		if jsonOutput {
			return writeJSON(out, map[string]any{
				"name":    name,
				"target":  targetDir,
				"command": gitArgs,
				"applied": false,
			})
		}
		fmt.Fprintf(out, "dry-run: would run: %s\n", strings.Join(gitArgs, " "))
		fmt.Fprintf(out, "target: %s\n", targetDir)
		fmt.Fprintln(out, "re-run with --apply to execute")
		return nil
	}

	// What a clone or submodule add would refuse for local reasons is checked
	// before git runs, so the failure is the real one and no network is touched.
	if err := checkBundleDestination(targetDir, gitDir, relTarget); err != nil {
		return err
	}
	if err := checkGitConfigReadable(cmd.ErrOrStderr()); err != nil {
		return err
	}

	// git's stderr is ethos's own, untouched: clone progress shows when it is a
	// terminal, and no pipe of ours can hold Wait open after git exits. The ssh
	// advice is decided only after a failure, from a short second look at the
	// remote (sshHint), never from what git printed here.
	_, err := gitenv.Run(gitenv.Spec{
		Git: gitArgs[0], Args: gitArgs[1:], Dir: gitDir, Env: gitenv.UserConfigEnv(),
		Stdout: out, Stderr: cmd.ErrOrStderr(), Limit: cloneLimit, Network: true,
	})
	if err != nil {
		wrapped := fmt.Errorf("running %s: %w", strings.Join(gitArgs, " "), err)
		var ee *exec.ExitError
		if errors.As(err, &ee) && sshHint(url) {
			wrapped = fmt.Errorf("%w; add-bundle runs ssh without a terminal; load the key with ssh-add, or connect once with ssh to accept the host key", wrapped)
		}
		return wrapped
	}
	if jsonOutput {
		return writeJSON(out, map[string]any{
			"name":    name,
			"target":  targetDir,
			"applied": true,
		})
	}
	fmt.Fprintf(out, "added bundle %q at %s\n", name, targetDir)
	return nil
}

// checkBundleDestination refuses a destination git itself would refuse. A clone
// (gitDir "") goes into a directory that is empty or absent; a symlink to an
// empty directory is accepted, a dangling one is not. A submodule add (gitDir
// is the repo root) is refused when the path is already in the index, or when
// it exists and is not a git repository of its own; an existing clone is left
// for git submodule add, which adopts it.
func checkBundleDestination(targetDir, gitDir, relTarget string) error {
	_, lerr := os.Lstat(targetDir)
	if errors.Is(lerr, fs.ErrNotExist) {
		if gitDir == "" {
			return nil
		}
	} else if lerr != nil {
		return fmt.Errorf("examining the bundle destination %s: %w", targetDir, lerr)
	}
	info, err := os.Stat(targetDir)
	exists := lerr == nil
	if exists && err != nil {
		return fmt.Errorf("the bundle destination %s is a symlink to nothing: remove it, or choose another --name", targetDir)
	}
	if gitDir == "" {
		if !exists {
			return nil
		}
		if info.IsDir() {
			ents, err := os.ReadDir(targetDir)
			if err != nil {
				return fmt.Errorf("reading the bundle destination %s: %w", targetDir, err)
			}
			if len(ents) == 0 {
				return nil
			}
		}
		return fmt.Errorf("the bundle destination %s already exists and is not an empty directory: remove it, or choose another --name", targetDir)
	}
	out, err := gitenv.Output(gitDir, "git", "ls-files", "--", relTarget)
	if err != nil {
		return fmt.Errorf("checking whether %s is in the index: %w", relTarget, err)
	}
	if len(bytes.TrimSpace(out)) > 0 {
		return fmt.Errorf("the bundle path %s is already in the index: choose another --name, or remove it with git rm", relTarget)
	}
	if !exists {
		return nil
	}
	if !info.IsDir() || !isOwnGitRepo(targetDir) {
		return fmt.Errorf("the bundle path %s already exists and is not a git repository: remove it, or choose another --name", targetDir)
	}
	return nil
}

// isOwnGitRepo reports whether dir is the top level of a git work tree.
func isOwnGitRepo(dir string) bool {
	out, err := gitenv.Output(dir, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return false
	}
	top, err := os.Stat(strings.TrimSpace(string(out)))
	if err != nil {
		return false
	}
	d, err := os.Stat(dir)
	return err == nil && os.SameFile(top, d)
}

// checkGitConfigReadable names every git config file that cannot be read. An
// unreadable system file is fatal for git, so it refuses before the clone; an
// unreadable global or XDG file is skipped by git, so its core.sshCommand,
// insteadOf and credential settings do not apply, and it is a notice on w.
func checkGitConfigReadable(w io.Writer) error {
	var fatal []string
	for _, u := range gitenv.UnreadableConfigs() {
		if u.System {
			fatal = append(fatal, fmt.Sprintf("%s (%v)", u.Path, u.Err))
			continue
		}
		fmt.Fprintf(w, "ethos: team add-bundle: git will ignore %s: its core.sshCommand, insteadOf and credential settings do not apply (%v)\n", u.Path, u.Err)
	}
	if len(fatal) > 0 {
		return fmt.Errorf("team add-bundle: git will refuse the system config %s; make it readable", strings.Join(fatal, ", "))
	}
	return nil
}

// sshHint reports whether the remote refuses for a reason a terminal would have
// answered. It looks again, only after a failure, with `git ls-remote` bounded
// at hintLimit and run as the clone ran (no terminal, no prompts), and reads
// what git says. A relative URL (submodule add) cannot be looked at this way.
func sshHint(url string) bool {
	if strings.HasPrefix(url, "./") || strings.HasPrefix(url, "../") {
		return false
	}
	// In a neutral directory: the user's own config applies, and no repository's
	// (a submodule clone does not apply its superproject's insteadOf either).
	ndir, nenv, cleanup, nerr := gitenv.Neutral()
	if nerr != nil {
		return false
	}
	defer cleanup()
	_, err := gitenv.Run(gitenv.Spec{
		Git: "git", Dir: ndir, Env: nenv, Network: true, Limit: hintLimit,
		Args: []string{"ls-remote", "--exit-code", url, "HEAD"},
	})
	var ee *exec.ExitError
	return errors.As(err, &ee) && sshFailed(string(ee.Stderr))
}

// hintLimit bounds sshHint's second look.
var hintLimit = 20 * time.Second

// sshFailed reports whether git's stderr shows ssh failing in a way a terminal
// prompt would have fixed: a key refused or no way to ask for one, or a host key
// not accepted. Generic transport text ("Could not read from remote repository",
// a repository not found, a refused connection, a DNS failure) is not evidence
// of that and gets no advice.
func sshFailed(stderr string) bool {
	for _, marker := range []string{
		"Permission denied (publickey", "Permission denied (password", "Permission denied (keyboard-interactive",
		"Host key verification failed",
		"Enter passphrase", "read_passphrase", "Could not open a connection to your authentication agent",
	} {
		if strings.Contains(stderr, marker) {
			return true
		}
	}
	return false
}

// --- legacy submodule detection ---

// legacySubmoduleURL scans .gitmodules under repoRoot for an entry
// whose path is .punt-labs/ethos and returns its url. Returns "" if no
// such entry exists. Key order within a section does not matter — url
// and path can appear in any order.
func legacySubmoduleURL(repoRoot string) (string, error) {
	p := filepath.Join(repoRoot, ".gitmodules")
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading .gitmodules: %w", err)
	}
	defer f.Close()

	type section struct {
		path, url string
	}
	var sections []section
	var cur *section

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[submodule") {
			sections = append(sections, section{})
			cur = &sections[len(sections)-1]
			continue
		}
		if cur == nil {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		switch key {
		case "path":
			cur.path = val
		case "url":
			cur.url = val
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("scanning .gitmodules: %w", err)
	}

	const target = ".punt-labs/ethos"
	for _, s := range sections {
		if s.path == target {
			return s.url, nil
		}
	}
	return "", nil
}
