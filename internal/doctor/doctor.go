// Package doctor provides shared health-check logic for the ethos CLI
// and MCP server.
package doctor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/agentmark"
	"github.com/punt-labs/ethos/v4/internal/bundle"
	"github.com/punt-labs/ethos/v4/internal/githook"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/seed"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/punt-labs/ethos/v4/internal/team"
	"github.com/punt-labs/ethos/v4/internal/textscan"
	"github.com/punt-labs/ethos/v4/plugin/hooks"
)

// Result holds the outcome of a single health check.
type Result struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// Passed reports whether the check did not fail. It is explicit about the
// three valid statuses: PASS and WARN return true (WARN is advisory — an
// expected state that needs attention but is not a fault, such as a
// gated-but-unenabled repo or a fresh install with no identity yet), FAIL
// returns false. Any other value (empty or a typo like "PAS") returns false,
// so a malformed status surfaces as a failure in summaries rather than being
// silently counted as passed. Only AnyFailed gates a non-zero exit; callers
// that want to surface WARN distinctly read Status, which renders verbatim in
// the CLI table and the MCP summary.
func (r Result) Passed() bool {
	return r.Status == "PASS" || r.Status == "WARN"
}

// HumanIdentityRow is the name of doctor's human-identity row. Everything that
// reports or replaces that row uses it, so the row cannot be told apart by a
// string that has drifted.
const HumanIdentityRow = "Human identity"

// RunAllFor is RunAll for a located repository: a store ethos cannot use
// (StoreFailure) is passed as no store root, so no check reads a broken store
// as if it were usable, and the "Repository store" row fails. The CLI and the
// MCP doctor tool both go through it.
func RunAllFor(loc resolve.Located, s identity.IdentityStore, ss *session.Store, teams *team.LayeredStore) []Result {
	return runAllFor(loc, s, ss, teams, nil)
}

// RunAllForCaller is RunAllFor with the "Human identity" row supplied by the
// caller instead of resolved here. A caller that already knows who is calling
// (the MCP server, answering from a declared participant) passes its answer, so
// the row can never be one derived from the environment, the process tree, git
// or the OS user. The row's Name must be HumanIdentityRow.
func RunAllForCaller(loc resolve.Located, s identity.IdentityStore, ss *session.Store, teams *team.LayeredStore, human Result) []Result {
	return runAllFor(loc, s, ss, teams, &human)
}

func runAllFor(loc resolve.Located, s identity.IdentityStore, ss *session.Store, teams *team.LayeredStore, human *Result) []Result {
	storeRoot, storeErr := loc.Store, loc.StoreFailure()
	if storeErr != nil {
		storeRoot = ""
	}
	// A bundle or policy that cannot be honoured is its own failing row, and
	// every row that read a store read layers the configuration governs.
	var issue error
	if storeErr == nil {
		home, herr := os.UserHomeDir()
		global := ""
		if herr == nil {
			global = filepath.Join(home, ".punt-labs", "ethos")
		}
		issue = bundle.ConfigIssueFromConfig(loc.Store, loc.Ethos, global, loc.Config)
	}
	results := runAll(s, ss, loc.Checkout, loc.CheckoutErr, storeRoot, storeErr, issue, loc.Config, loc.ConfigErr, teams, human)
	if issue != nil {
		what, _, _ := resolve.Describe(issue)
		results = append([]Result{{Name: "Repository configuration", Status: "FAIL", Detail: what + ": " + issue.Error()}}, results...)
	}
	return results
}

// RunAll executes every standard health check and returns the results.
//
// repoRoot is the current work tree (LocateRepoRoot) — the checkout whose
// .claude/agents and seal hook the checks inspect. storeRoot is the shared
// mission/config store (StoreRootIfAny); it differs from repoRoot only inside
// a linked worktree. Configuration is read once from storeRoot and shared by
// every configuration-dependent row (Bugbot #370 class: resolving the active
// team from the worktree while activation wrote it to the store produced false
// orphan reports).
//
// rootErr is LocateRepoRoot's error. When repoRoot is empty because an
// ETHOS_REPO_ROOT was refused, or the checkout could not be examined, the
// checks that read the checkout are gated (gateCheckout); being outside any
// repository is not a problem.
//
// storeErr is LocateStoreRoot's error. When the store could not be located
// for any other reason, doctor emits a FAIL "Repository store" row — every
// non-exempt command refuses in that state — and the checks that read the
// store are gated (gateStore).
func RunAll(s identity.IdentityStore, ss *session.Store, repoRoot string, rootErr error, storeRoot string, storeErr error, teams *team.LayeredStore) []Result {
	var cfg *resolve.RepoConfig
	var configErr error
	if repoRoot != "" && storeRoot != "" {
		cfg, configErr = resolve.ConfigOf(storeRoot)
	}
	return runAll(s, ss, repoRoot, rootErr, storeRoot, storeErr, nil, cfg, configErr, teams, nil)
}

// runAll is RunAll with the configuration issue that governs the store layers
// (nil for none): every store check gates on it as it does on a store that was
// not located. human, when not nil, is the "Human identity" row to report in
// place of resolving one. cfg/configErr are the request's single configuration
// snapshot, including absence or failure; subordinate checks must not reread it.
func runAll(s identity.IdentityStore, ss *session.Store, repoRoot string, rootErr error, storeRoot string, storeErr, policy error, cfg *resolve.RepoConfig, configErr error, teams *team.LayeredStore, human *Result) []Result {
	results := make([]Result, 0, 19) // shared-store placement adds a row in linked worktrees

	checkout := func(r Result) Result { return gateCheckout(r, repoRoot, rootErr) }
	store := func(r Result) Result { return gateStore(r, storeRoot, storeErr) }
	if policy != nil {
		store = func(r Result) Result { return gateStore(r, "", policy) }
	}

	if unlocated(storeRoot, storeErr) {
		detail := "not located: " + storeErr.Error()
		if what, _, _ := resolve.Describe(storeErr); what != "the repository store could not be located" {
			detail = what + ": " + storeErr.Error()
		}
		results = append(results, Result{Name: "Repository store", Status: "FAIL", Detail: detail})
	}

	// The identity store, like the team store, is layered over the repository
	// store, so these checks read it and are gated with it.
	results = append(results, store(identityDirResult(s, ss)))

	// Human identity carries its own status: a fresh install (no identity
	// yet) is a WARN pointing at `ethos setup`, not a FAIL.
	if human != nil {
		results = append(results, checkout(store(*human)))
	} else {
		results = append(results, checkout(store(CheckHumanIdentity(s, ss, repoRoot))))
	}

	// Default agent reads the agent: key from the shared store (storeRoot),
	// matching session_start/resolveLeader/resolve-agent; the "in a repo"
	// guard stays on the checkout (repoRoot) (#370).
	results = append(results, checkout(store(defaultAgentResult(repoRoot, storeRoot, cfg, configErr))))

	results = append(results, store(duplicateFieldsResult(s, ss)))

	results = append(results, checkout(store(checkOrphanedAgentFilesFromConfig(repoRoot, storeRoot, cfg, configErr, teams, s))))
	if configErr != nil {
		results = append(results, checkout(store(Result{Name: "Agent file currency", Status: "FAIL", Detail: configErr.Error()})))
	} else {
		results = append(results, checkout(store(CheckAgentCurrencyFromConfig(repoRoot, storeRoot, cfg, teams, s))))
	}

	results = append(results, store(checkTeamMembersFromConfig(storeRoot, cfg, configErr, teams, s)))

	// ethos-bfml/ethos-hy40: every hook gets the presence check the seal hook
	// had first — gated on the enabled marker, active determined by execution
	// (checkHookPresence), not by whether a section merely exists. See the
	// DES-hook-presence ADR in DESIGN.md.
	for _, spec := range hookSpecs {
		results = append(results, checkout(checkHookPresence(repoRoot, spec)))
	}

	// DES-hook-drift-detection: content-currency checks, independent of the
	// enabled marker and of the presence checks' active states — see
	// docs/design-hook-drift-detection.md.
	for _, spec := range hookSpecs {
		results = append(results, checkout(CheckHookCurrency(repoRoot, spec)))
	}

	// DES-083: the shim runs whichever binary it resolves, not the one running
	// this check, so the row asks that one whether it knows the subcommand.
	results = append(results, checkout(CheckHookBinary(repoRoot)))

	// DES-057. The completeness gate reads the shared store (storeRoot),
	// matching every other resolution consumer; the git-boundary check
	// reads the checkout (repoRoot), because the index and .gitignore
	// belong to the tree being committed.
	results = append(results, store(checkRepoSetCompleteFromConfig(s, storeRoot, cfg, configErr)))
	results = append(results, checkout(CheckLocalExtNotTracked(repoRoot)))
	results = append(results, store(CheckExtCredentialNames(s)))

	// DES-072: hand-edited mission files read as machine-written and
	// are invisible to every mission-reading surface — the exact
	// failure mode `ethos mission correct` exists to replace. Reads
	// storeRoot (the shared record), matching CheckRepoSetComplete.
	results = append(results, store(CheckNoHandEditedMissionFiles(storeRoot)))

	// ethos-e05k: the delegated-worker invariant on the "implement" and
	// "test" archetypes is data-driven from the deployed YAML, so a binary
	// upgrade with no `ethos seed` re-run silently loses enforcement.
	results = append(results, store(CheckDelegatedWorkerArchetypes(storeRoot)))

	// DES-079: every file ethos owns has a class in the path-class table.
	results = append(results, checkout(CheckUnclassifiedFiles(repoRoot)))
	results = append(results, checkout(CheckPhysicalPlacement(repoRoot)))
	if storeRoot != "" && storeRoot != repoRoot {
		results = append(results, store(CheckPhysicalPlacement(storeRoot)))
	}
	return results
}

// unlocated reports whether a tree was not located for a reason other than
// being outside a repository.
func unlocated(root string, err error) bool {
	return root == "" && err != nil && !errors.Is(err, resolve.ErrNotInRepo)
}

// gateStore marks a check that read the identity, team or repository store
// when the repository store was not located: a PASS becomes WARN "not checked"
// (it saw the global layer only), a FAIL keeps its status with a note.
func gateStore(r Result, root string, err error) Result {
	if !unlocated(root, err) {
		return r
	}
	// A check that PASSed on the global layer alone, or examined nothing because
	// the store was not located, did not check the repository: it is a WARN
	// that says so. A FAIL stays a FAIL, with a note.
	if r.Status == "PASS" {
		what, _, _ := resolve.Describe(err)
		r.Status, r.Detail = "WARN", "not checked: "+what+": "+err.Error()
		return r
	}
	// Worded from the failure: a policy that cannot be honoured is not a store
	// that was not located.
	var ce *resolve.ConfigError
	if errors.As(err, &ce) {
		r.Detail += " (" + ce.What + ": " + err.Error() + ")"
		return r
	}
	r.Detail += " (global layer only; repo store not located: " + err.Error() + ")"
	return r
}

// gateCheckout marks a check that reads the checkout when the checkout was not
// located: a FAIL keeps its status with a note; anything else, which examined
// no tree, becomes WARN "not checked".
func gateCheckout(r Result, root string, err error) Result {
	if !unlocated(root, err) {
		return r
	}
	if r.Status == "FAIL" {
		r.Detail += " (checkout not located: " + err.Error() + ")"
		return r
	}
	r.Status, r.Detail = "WARN", "not checked: "+err.Error()
	return r
}

// passFail builds a Result from a boolean check outcome: true is PASS, false
// is FAIL. Checks that need the advisory WARN state return a Result directly.
func passFail(name, detail string, ok bool) Result {
	status := "PASS"
	if !ok {
		status = "FAIL"
	}
	return Result{Name: name, Status: status, Detail: detail}
}

// identityDirResult is identityDirStatus as a Result.
func identityDirResult(s identity.IdentityStore, _ *session.Store) Result {
	detail, status := identityDirStatus(s)
	return Result{Name: "Identity directory", Status: status, Detail: detail}
}

// duplicateFieldsResult is CheckDuplicateFields as a Result.
func duplicateFieldsResult(s identity.IdentityStore, ss *session.Store) Result {
	detail, ok := CheckDuplicateFields(s, ss)
	return passFail("Duplicate fields", detail, ok)
}

// defaultAgentResult is CheckDefaultAgent as a Result.
func defaultAgentResult(repoRoot, storeRoot string, cfg *resolve.RepoConfig, configErr error) Result {
	detail, ok := checkDefaultAgentFromConfig(repoRoot, storeRoot, cfg, configErr)
	return passFail("Default agent", detail, ok)
}

// AllPassed returns true when every result passed.
func AllPassed(results []Result) bool {
	for _, r := range results {
		if !r.Passed() {
			return false
		}
	}
	return true
}

// AnyFailed returns true when any result is an outright FAIL. A WARN is
// advisory and does not count — doctor's exit status gates on this, not on
// AllPassed, so a gated-but-unenabled repo (WARN) does not fail the command.
func AnyFailed(results []Result) bool {
	for _, r := range results {
		if r.Status == "FAIL" {
			return true
		}
	}
	return false
}

// WarnCount returns the number of advisory WARN results, so a summary can
// surface them distinctly rather than folding them into "passed".
func WarnCount(results []Result) int {
	n := 0
	for _, r := range results {
		if r.Status == "WARN" {
			n++
		}
	}
	return n
}

// PassedCount returns the number of strictly-PASS results. WARN is excluded
// so a summary that also reports WarnCount does not count a warned check
// twice (as both passed and a warning); the total is PassedCount + WarnCount +
// failures. Advisory gating (Passed/AllPassed treating WARN as not-failed) is
// unchanged — this is a counting distinction, not a status one.
func PassedCount(results []Result) int {
	n := 0
	for _, r := range results {
		if r.Status == "PASS" {
			n++
		}
	}
	return n
}

// CheckOrphanedAgentFiles flags agent files in .claude/agents/ whose
// handle is not a member of any configured team.
//
// The two roots differ inside a linked worktree. repoRoot is the checkout:
// .claude/agents/ is per-checkout state (session_start and agent_installer
// write it there), so the glob is checkout-rooted. storeRoot is the shared
// store: the active team lives in .punt-labs/ethos.yaml resolved via
// StoreRootIfAny by every other reader and the writer (team activate), so the
// team read must use it too — resolving the team from the worktree while
// activation wrote it to the store would compute "expected" agents from the
// wrong team and flag valid agents as orphaned (Bugbot #370 class). They
// coincide outside a worktree.
//
// s classifies each flagged handle for the FAIL detail (ethos-jw1z): a
// handle that resolves to a real identity somewhere in s is a STALE
// generated file — the common case, left behind by a team-scope change,
// safe to delete or regenerate. A handle matching no identity fails only if
// the file carries the ethos provenance marker (generated for an identity
// since removed); without it the file is hand-authored, reported as ignored,
// and does not fail the check. s may be nil (some
// callers have no identity store in scope); the classification is then
// skipped and every handle reads as an undifferentiated FAIL.
func CheckOrphanedAgentFiles(repoRoot, storeRoot string, teams *team.LayeredStore, s identity.IdentityStore) Result {
	var cfg *resolve.RepoConfig
	var err error
	if repoRoot != "" && storeRoot != "" {
		cfg, err = resolve.ConfigOf(storeRoot)
		if err != nil {
			err = fmt.Errorf("resolve team: %w", err)
		}
	}
	return checkOrphanedAgentFilesFromConfig(repoRoot, storeRoot, cfg, err, teams, s)
}

// checkOrphanedAgentFilesFromConfig shares the standalone check's policy, but
// uses only the supplied configuration (including a captured read failure).
func checkOrphanedAgentFilesFromConfig(repoRoot, storeRoot string, cfg *resolve.RepoConfig, configErr error, teams *team.LayeredStore, s identity.IdentityStore) Result {
	name := "Orphaned agent files"

	if repoRoot == "" {
		return Result{Name: name, Status: "PASS", Detail: "not in a repo"}
	}

	pattern := filepath.Join(repoRoot, ".claude", "agents", "*.md")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		// filepath.Glob only errors on a malformed pattern (ErrBadPattern) —
		// a defect in this check's own code, not a runtime condition to
		// swallow as "nothing to check". Reading it as PASS would hide an
		// unchecked invariant behind a green column (M3, matching the
		// checklistAgentNames precedent below).
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("could not glob agents: %s", err)}
	}
	if len(matches) == 0 {
		return Result{Name: name, Status: "PASS", Detail: "no agent files"}
	}

	if configErr != nil {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("could not load repo config: %s", configErr)}
	}
	teamName := ""
	if storeRoot != "" && cfg != nil {
		teamName = cfg.Team
	}
	if teamName == "" {
		return Result{Name: name, Status: "PASS", Detail: "no team configured"}
	}
	if teams == nil {
		// Every production caller (RunAll) supplies a team store; reaching
		// here with a configured team name but no store to check it against
		// is an integration bug, not a legitimate "nothing to check" state.
		return Result{Name: name, Status: "FAIL", Detail: "no team store available to verify agent files against"}
	}

	t, err := teams.Load(teamName)
	if err != nil {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("could not load team %q: %s", teamName, err)}
	}

	members := make(map[string]bool, len(t.Members))
	for _, m := range t.Members {
		members[m.Identity] = true
	}

	checklist, err := checklistAgentNames(seed.Agents, "sidecar/agents")
	if err != nil {
		// A broken embed is a build-time defect, not a runtime condition to
		// swallow. Reporting it as an ordinary orphan FAIL would misdirect the
		// operator toward deleting valid agent files; name the real cause.
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("could not read seeded agent list: %s", err)}
	}

	var orphaned []orphanFile
	for _, path := range matches {
		handle := strings.TrimSuffix(filepath.Base(path), ".md")
		if members[handle] || checklist[handle] {
			continue
		}
		// The installer copies <store>/.punt-labs/ethos/agents/<handle>.md
		// into .claude/agents/ every SessionStart, so deleting such a file
		// is undone. It is not an orphan whatever its identity state.
		srcPath := filepath.Join(storeRoot, ".punt-labs", "ethos", "agents", handle+".md")
		if _, err := os.Stat(srcPath); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			// Classifying on a guess would call an installer-owned file
			// "safe to delete", and the next SessionStart would undo it.
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("could not stat agent source %s: %s", srcPath, err)}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("could not read agent file %s: %s", path, err)}
		}
		orphaned = append(orphaned, orphanFile{handle: handle, marked: agentmark.Has(string(data))})
	}

	if len(orphaned) == 0 {
		return Result{Name: name, Status: "PASS", Detail: "no orphaned agent files"}
	}
	sort.Slice(orphaned, func(i, j int) bool { return orphaned[i].handle < orphaned[j].handle })
	status, detail := classifyOrphans(orphaned, s)
	return Result{Name: name, Status: status, Detail: detail}
}

// orphanFile is an agent file whose handle is on no team. marked reports
// whether it carries the ethos provenance marker (internal/agentmark).
type orphanFile struct {
	handle string
	marked bool
}

// classifyOrphans returns the status and detail for CheckOrphanedAgentFiles.
// Each orphan falls in one of three classes:
//
//   - stale: an identity file exists anywhere in s. The agent was generated
//     for some team and is not on the active one; safe to delete or fixed by
//     re-running `ethos session start` once the identity is back on a team.
//     FAIL.
//   - removed: no identity, but the file carries the provenance marker, so
//     ethos wrote it for an identity that has since been removed (the
//     generator never deletes its output). FAIL.
//   - hand-authored: no identity and no marker. Ethos writes agent files only
//     from identities and always marks them, so this is not ethos's file.
//     Reported as ignored; it does not fail the check. Files generated before
//     the marker existed read this way.
//
// s may be nil, in which case the classes cannot be told apart honestly and
// every handle reads as a plain, undifferentiated FAIL rather than a guess.
//
// Staleness checks existence (s.Exists, a bare os.Stat), not Load (N1): Load
// resolves attribute content and, for a legacy identity carrying a
// pre-ext-migration `voice:` key, RE-SAVES the file to relocate it — a
// mutation an operator running a read-only health check does not expect
// and would not consent to. A malformed or unreadable file at the handle's
// path still counts as "a file is there" (stale), which is the more useful
// signal and avoids misattributing an I/O or parse error to absence.
func classifyOrphans(orphaned []orphanFile, s identity.IdentityStore) (status, detail string) {
	if s == nil {
		handles := make([]string, len(orphaned))
		for i, o := range orphaned {
			handles[i] = o.handle
		}
		return "FAIL", "orphaned agent files (not on any team): " + strings.Join(handles, ", ")
	}
	var stale, removed, unresolved, unvendored, unknown []string
	for _, o := range orphaned {
		p, err := identityPresence(s, o.handle)
		switch {
		case err != nil:
			unknown = append(unknown, fmt.Sprintf("%s (%v)", o.handle, err))
		case p == idPresent:
			stale = append(stale, o.handle)
		case p == idGlobalOnly && o.marked:
			unvendored = append(unvendored, o.handle)
		case o.marked:
			removed = append(removed, o.handle)
		default:
			unresolved = append(unresolved, o.handle)
		}
	}
	var parts []string
	if len(unknown) > 0 {
		parts = append(parts, "cannot tell whether an identity exists: "+strings.Join(unknown, ", "))
	}
	if len(unvendored) > 0 {
		parts = append(parts, fmt.Sprintf(
			"not vendored (exists in the global store) — run ethos vendor: %s",
			strings.Join(unvendored, ", ")))
	}
	if len(stale) > 0 {
		parts = append(parts, fmt.Sprintf(
			"stale (known identity, not on the active team — generated for a previous team scope, safe to delete): %s",
			strings.Join(stale, ", ")))
	}
	if len(removed) > 0 {
		parts = append(parts, fmt.Sprintf(
			"generated by ethos for an identity that no longer exists — safe to delete: %s",
			strings.Join(removed, ", ")))
	}
	note := ""
	if len(unresolved) > 0 {
		note = "not ethos-generated, ignored: " + strings.Join(unresolved, ", ")
	}
	if len(parts) == 0 {
		return "PASS", "no orphaned agent files (" + note + ")"
	}
	detail = "orphaned agent files — " + strings.Join(parts, "; ")
	if note != "" {
		detail += " (" + note + ")"
	}
	return "FAIL", detail
}

// CheckTeamMembers fails when a member of the active team has no identity
// in any layer s reads (repo, bundle, global). Under `resolution: repo-only`
// it defers to CheckRepoSetComplete, which already reports an incomplete
// repo set; reporting it here would double-count.
func CheckTeamMembers(storeRoot string, teams *team.LayeredStore, s identity.IdentityStore) Result {
	var cfg *resolve.RepoConfig
	var err error
	if storeRoot != "" {
		cfg, err = resolve.ConfigOf(storeRoot)
		if err != nil {
			err = fmt.Errorf("resolve team: %w", err)
		}
	}
	return checkTeamMembersFromConfig(storeRoot, cfg, err, teams, s)
}

// checkTeamMembersFromConfig never resolves configuration or store layers again.
func checkTeamMembersFromConfig(storeRoot string, cfg *resolve.RepoConfig, configErr error, teams *team.LayeredStore, s identity.IdentityStore) Result {
	name := "Team members"

	if storeRoot == "" {
		return Result{Name: name, Status: "PASS", Detail: "not in a repo"}
	}
	if configErr != nil {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("could not load repo config: %s", configErr)}
	}
	teamName := ""
	if cfg != nil {
		teamName = cfg.Team
	}
	if teamName == "" {
		return Result{Name: name, Status: "PASS", Detail: "no team configured"}
	}
	mode, err := resolve.ResolutionFromConfig(cfg)
	if err != nil {
		return Result{Name: name, Status: "FAIL", Detail: err.Error()}
	}
	if mode == resolve.ResolutionRepoOnly {
		return Result{Name: name, Status: "PASS", Detail: "resolution: repo-only — see Repo-only completeness"}
	}
	if teams == nil || s == nil {
		return Result{Name: name, Status: "FAIL", Detail: "no team or identity store available to verify members against"}
	}
	t, err := teams.Load(teamName)
	if err != nil {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("could not load team %q: %s", teamName, err)}
	}
	var missing []string
	for _, m := range t.Members {
		p, err := identityPresence(s, m.Identity)
		switch {
		case err != nil:
			missing = append(missing, fmt.Sprintf("%s (%v)", m.Identity, err))
		case p == idAbsent:
			missing = append(missing, m.Identity)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
			"team %q members with no identity in the repo, bundle, or global store: %s",
			teamName, strings.Join(missing, ", "))}
	}
	return Result{Name: name, Status: "PASS", Detail: fmt.Sprintf("%d members resolve", len(t.Members))}
}

type presence int

const (
	idAbsent     presence = iota
	idPresent             // resolvable through s
	idGlobalOnly          // only in the global store, which repo-only never reads
)

// identityPresence reports where handle's identity file is, stat-ing each
// layer s resolves from (repo, bundle, and global unless repo-only). Unlike
// IdentityStore.Exists it distinguishes absence from a stat error such as
// EACCES, and it finds a global-only identity under repo-only.
func identityPresence(s identity.IdentityStore, handle string) (presence, error) {
	roots := []string{s.Root()}
	global := ""
	if ls, ok := s.(*identity.LayeredStore); ok {
		roots = nil
		for _, r := range []string{ls.RepoRoot(), ls.BundleRoot()} {
			if r != "" {
				roots = append(roots, r)
			}
		}
		if ls.RepoAuthoritative() {
			global = ls.GlobalRoot()
		} else {
			roots = append(roots, ls.GlobalRoot())
		}
	}
	for _, r := range roots {
		found, err := identityFileExists(r, handle)
		if err != nil {
			return idAbsent, err
		}
		if found {
			return idPresent, nil
		}
	}
	if global != "" {
		found, err := identityFileExists(global, handle)
		if err != nil {
			return idAbsent, err
		}
		if found {
			return idGlobalOnly, nil
		}
	}
	return idAbsent, nil
}

func identityFileExists(root, handle string) (bool, error) {
	_, err := os.Stat(identity.NewStore(root).Path(handle))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// checklistAgentNames returns the handles this check exempts as seeded
// review-checklist agents (DES-070): code-reviewer, silent-failure-hunter,
// invariant-completeness-reviewer. The exemption is name-based, not
// provenance-based: any repo-local .claude/agents/<name>.md matching one of
// these handles is exempt, whether or not `ethos seed` actually put it
// there. The name set itself is read directly from internal/seed's embedded
// sidecar/agents/, so it can never drift from what `ethos seed` deploys —
// but that guarantees the set of exempted *names*, not that every file
// bearing one of those names on disk was seeded. They carry no team
// membership by design (no persona, no identity), so the orphan check must
// not flag them.
//
// fsys and root are parameterized (rather than reading seed.Agents
// directly) so a test can inject a broken fs.FS and exercise the error path
// without depending on an unreadable compile-time embed.
func checklistAgentNames(fsys fs.FS, root string) (map[string]bool, error) {
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		return nil, fmt.Errorf("reading embedded %s: %w", root, err)
	}
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "README.md" {
			continue
		}
		names[strings.TrimSuffix(e.Name(), ".md")] = true
	}
	return names, nil
}

// hookBinaryTimeout bounds the --help probe of the binary the shims run.
const hookBinaryTimeout = 10 * time.Second

// sandboxUnavailableDetail words the FAIL for a hook that cannot be run because
// the sandbox found no git. err already names the cause.
func sandboxUnavailableDetail(short string, err error) string {
	return fmt.Sprintf("cannot verify the %s hook by execution: %v; check skipped", short, err)
}

// CheckHookBinary reports whether the binary the installed git hook shims
// resolve implements the subcommands they call (DES-083). The shims look for
// ethos on PATH, then in $HOME/.local/bin, exactly as resolveHookBinary does.
// An enabled repo whose resolved binary predates `ethos hook pre-commit` FAILs,
// because every commit there is blocked until ethos is upgraded. A repo where
// ethos is not enabled is never blocked by a missing or old binary, so it PASSes.
func CheckHookBinary(repoRoot string) Result {
	const name = "Hook binary"
	if repoRoot == "" {
		return Result{Name: name, Status: "PASS", Detail: "not in a repo"}
	}
	enabled, err := resolve.EnabledMarker(repoRoot)
	if err != nil {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("cannot determine enablement here: %v", err)}
	}
	if !enabled {
		return Result{Name: name, Status: "PASS", Detail: "not enabled here"}
	}
	onPath, local := hookBinaryCandidates()
	bin := onPath
	if bin == "" {
		bin = local
	}
	if bin == "" {
		return Result{Name: name, Status: "FAIL", Detail: "ethos is enabled here but no ethos binary is on PATH or in ~/.local/bin; every commit is blocked"}
	}
	// Git runs the hooks with its own PATH, which may differ from this
	// process's, so what follows is about the binary doctor resolves.
	resolved := fmt.Sprintf("resolved via doctor's PATH to %s; git may resolve differently", bin)

	// Both shims must find their subcommand: a binary with one and not the other
	// passes the pre-commit shim and blocks every commit at commit-msg.
	for _, sub := range []string{"pre-commit", "commit-msg"} {
		ctx, cancel := context.WithTimeout(context.Background(), hookBinaryTimeout)
		out, err := exec.CommandContext(ctx, bin, "hook", sub, "--help").CombinedOutput()
		timedOut := ctx.Err() != nil
		cancel()
		var exitErr *exec.ExitError
		switch {
		case timedOut:
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("%s did not answer hook %s --help within %s (%s)", bin, sub, hookBinaryTimeout, resolved)}
		case err != nil && !errors.As(err, &exitErr):
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("cannot run %s: %v (%s)", bin, err, resolved)}
		case err != nil || !strings.Contains(string(out), "ethos hook "+sub):
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("%s predates `ethos hook %s`, so the installed hooks block every commit (%s)", bin, sub, resolved)}
		}
	}
	if onPath != "" && local != "" && !sameFile(onPath, local) {
		return Result{Name: name, Status: "WARN", Detail: fmt.Sprintf("two ethos binaries: %s on PATH and %s in ~/.local/bin; the hooks run whichever git's PATH finds first, and only %s was checked (%s)", onPath, local, bin, resolved)}
	}
	return Result{Name: name, Status: "PASS", Detail: fmt.Sprintf("%s (%s)", bin, resolved)}
}

// hookBinaryCandidates returns the ethos binary on PATH and the one in the
// default install directory, each "" when absent. The shims try them in that
// order.
func hookBinaryCandidates() (onPath, local string) {
	if p, err := exec.LookPath("ethos"); err == nil {
		onPath = p
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".local", "bin", "ethos")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode().Perm()&0o111 != 0 {
			local = p
		}
	}
	return onPath, local
}

// sameFile reports whether a and b are one file, so a symlink from one install
// location to the other is not two binaries.
func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}

// CheckSealHook reports on the DES-058 audit-seal pre-commit hook.
func CheckSealHook(repoRoot string) Result { return checkHookPresence(repoRoot, sealHookSpec) }

// CheckTrailerHook reports on the DES-054 Mission/Delegation commit-msg
// hook (ethos-bfml/ethos-hy40): the same presence check CheckSealHook has
// always run, extended to the trailer side, so a hand-removed or
// host-clobbered commit-msg chain on an enabled repo no longer reads green
// on every line.
func CheckTrailerHook(repoRoot string) Result { return checkHookPresence(repoRoot, trailerHookSpec) }

// checkHookPresence reports on spec's hook, keyed on the enabled marker
// (§2.11). Four states, each resolved against the hooks directory git itself
// names; when git cannot name it (a degraded lookup) an enabled repo is a FAIL
// and a dormant one a WARN, both "cannot verify hooks" with the reason:
//
//   - Enabled (marker present): FAIL when the hook is missing or inactive;
//     PASS when it carries an active call, proven by executing it
//     (hookInvocationObserved, ethos-kcbv) rather than by pattern-matching
//     its text.
//   - Dormant / Absent (marker absent, no ethos hook): PASS "not enabled
//     here" — a never-enabled or disabled repo must not fail.
//   - Gated-but-unenabled (marker absent, hook chained): WARN — the chained
//     hook is inert behind its own marker gate, so a PASS would hide it and a
//     FAIL would over-report a repo awaiting convergence.
//
// This is the composed presence check ethos-bfml/ethos-hy40 asked for: it is
// what makes an enabled repo with no seal (or no trailer) hook installed
// FAIL, where CheckHookCurrency alone — by its own deliberate, documented
// design — PASSes "no section installed" regardless of enablement.
func checkHookPresence(repoRoot string, spec HookSpec) (result Result) {
	name := "Audit " + spec.ShortName + " hook"
	if repoRoot == "" {
		return Result{Name: name, Status: "PASS", Detail: "not in a repo"}
	}

	// A marker stat error that is not "does not exist" must not collapse to
	// "not enabled" — a doubly-broken repo (unreadable .punt-labs/ethos plus a
	// lost hook) would then read PASS while commits flow unsealed. Surface it
	// as a FAIL instead of guessing dormancy (S4).
	// resolve.EnabledMarker validates the store path and the marker: a dangling
	// link, an unreadable directory, or a marker that is not a regular file is
	// an error here, never "not enabled".
	markerPresent, err := resolve.EnabledMarker(repoRoot)
	if err != nil {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("cannot determine enablement here: %v", err)}
	}
	lk := githook.HooksDir(repoRoot)
	// A degraded lookup did not read core.hooksPath: the directory it guessed
	// may not be the one git runs, so a verdict on its files would be a guess.
	// An enabled repo must not read green on a guess (its commits may be
	// unsealed); a dormant one is a WARN, because a stale chained hook in the
	// directory git really runs would otherwise stay hidden.
	dir, lkErr := lk.Verified()
	if lkErr != nil {
		status := "WARN"
		if markerPresent {
			status = "FAIL"
		}
		return Result{Name: name, Status: status, Detail: "cannot verify hooks: " + lk.Explain()}
	}
	hooksDirWarnings := lk.Warnings
	// A diverted core.hooksPath (inside the work tree, or outside the repo
	// entirely) is exactly the kind of surprise an operator running `ethos
	// doctor` needs named, not silently discarded — doctor is the one place
	// that already inspects hook state closely enough to say it (LOW).
	if len(hooksDirWarnings) > 0 {
		defer func() {
			result.Detail += " (" + strings.Join(hooksDirWarnings, "; ") + ")"
		}()
	}
	hook := filepath.Join(dir, spec.File)

	info, statErr := os.Stat(hook)
	var body []byte
	if statErr == nil {
		data, err := os.ReadFile(hook)
		if err != nil {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("cannot read %s: %v", hook, err)}
		}
		body = data
	}

	// Dormant: resolved BEFORE any execution is attempted, and structurally
	// so — this branch returns, so nothing below it can run for a repo
	// where ethos is not enabled here (S2/P1). A dormant repo's hook is
	// read-only diagnostic input, not something `ethos doctor` should ever
	// run: a foreign pre-commit chained in a not-enabled repo would
	// otherwise have its third-party shell executed to answer a question
	// ("is this repo enabled") that execution never needed to settle, and —
	// worse — any sandbox-infrastructure failure unrelated to the hook at
	// all (no git on PATH, an unwritable TMPDIR) would surface as a FAIL on
	// a repo that was never enabled, contradicting the dormant state this
	// function documents just below. Two lexical, non-executing scans are
	// sufficient here — hasMarkerSection for a marked chained section, and
	// looksLikeInvocation for an unmarked but textually-literal standalone
	// call (qodo, PR #515: previously only the marked shape reached WARN,
	// so a standalone `ethos audit seal` with no BEGIN/END markers
	// silently collapsed into "not enabled here" alongside a genuinely
	// absent hook) — the dormant case only needs PASS-vs-WARN, and WARN is
	// advisory regardless. This narrows, but does not close, M1's
	// documented residual below: a call assembled dynamically (`eval`, a
	// variable holding the command) is invisible to both scans the same
	// way it is invisible to every lexical scan, and still reads PASS.
	if !markerPresent {
		chained := statErr == nil && (hasMarkerSection(body, spec.Tag) || looksLikeInvocation(body, spec.accepted()...))
		if chained {
			return Result{Name: name, Status: "WARN", Detail: spec.ShortName + " hook chained but ethos not enabled here"}
		}
		return Result{Name: name, Status: "PASS", Detail: "not enabled here"}
	}

	// A hook present but not executable is never run by git either — check
	// this BEFORE any sandbox execution is attempted (qodo, PR #515). The
	// sandbox writes its own copy of body at mode 0o755 regardless of the
	// installed file's own permission bits, so without this early return
	// doctor would execute untrusted shell content from an operator's
	// checkout solely to diagnose a case (missing +x) that never needed
	// running to answer.
	//
	// Windows is excluded because the execute bit carries no execute
	// semantics there. Go's os.Stat on Windows derives a regular file's
	// permission bits from the read-only attribute alone — 0o444 when
	// FILE_ATTRIBUTE_READONLY is set, 0o666 otherwise, with 0o111 added only
	// for a directory (go1.26 os/types_windows.go, fileStat.mode; the
	// pre-Go1.23 path is identical on this point, and neither special-cases
	// .exe/.bat/.cmd). So Perm()&0o111 == 0 holds for EVERY hook file on
	// Windows: the test cannot tell a runnable hook from a non-runnable one,
	// and ungated it FAILs every enabled Windows install with a `chmod +x`
	// remedy that does not exist there — and returns before the
	// unsupported-platform branch below can run. Stated plainly: doctor
	// cannot detect a genuinely non-runnable hook on Windows at all.
	// sandboxGOOS, not runtime.GOOS, so a test can drive both arms.
	if statErr == nil && sandboxGOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("%s hook present but not executable — run: chmod +x %s", spec.ShortName, hook)}
	}

	// Enabled: only a shell hook is ever attempted by execution — a
	// non-shell body can never run the way git would run it (matches the
	// shebang check below), and there is no interpreter-neutral way to
	// "run" it safely.
	shellHook := statErr == nil && textscan.IsShellHook(body)
	var active bool
	if shellHook {
		observed, err := hookInvocationObservedAny(body, spec.accepted(), spec.NeedsMsgArg)
		if err != nil {
			if errors.Is(err, errSandboxGitUnavailable) {
				return Result{Name: name, Status: "FAIL", Detail: sandboxUnavailableDetail(spec.ShortName, err)}
			}
			if errors.Is(err, errSandboxUnsupportedPlatform) {
				// M4: this build cannot exercise the sandbox on this GOOS.
				// FAILing unconditionally here would be a false-positive on
				// a perfectly healthy install — the pre-execution lexical
				// scan was platform-neutral, so silently defaulting to FAIL
				// on today's execution-based check is a regression in the
				// dangerous direction.
				//
				// Narrowed by qodo #12 (PR #515): before execution-based
				// verification existed, this platform got a lexical check
				// (looksLikeInvocation's predecessor) that could at least
				// catch a hook with NO textual trace of the call at all —
				// broken on every platform, not merely unverifiable on this
				// one. Blanket-WARNing here regressed that case to "looks
				// fine," a real loss of detection on the one platform that
				// cannot fall back to execution. Consult the same lexical
				// scan used elsewhere in this function: no textual evidence
				// FAILs (this hook would not pass even the old check);
				// textual evidence present keeps the honest WARN, since
				// execution — the only thing that can PROVE the call is
				// live, not merely present — still cannot run here. This
				// does not reopen M4: every hook with textual evidence
				// still WARNs, never FAILs, so a healthy hook the OLD
				// lexical scanner would have accepted still does not FAIL
				// here either. Residual, stated plainly: an eval-obscured
				// or otherwise dynamically-assembled call is exactly as
				// invisible to looksLikeInvocation here as it is to the
				// dormant branch above — that hook still WARNs,
				// indistinguishable from one that never calls ethos at
				// all, since only execution (unavailable on this
				// platform) could tell them apart.
				if !looksLikeInvocation(body, spec.accepted()...) {
					return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
						"the %s hook shows no textual evidence of the required call, and this platform cannot verify by execution", spec.ShortName)}
				}
				return Result{Name: name, Status: "WARN", Detail: fmt.Sprintf(
					"cannot verify the %s hook by execution on this platform", spec.ShortName)}
			}
			var earlyExit *hookExitedEarlyError
			if errors.As(err, &earlyExit) && sectionIsCurrent(body, spec) {
				// P4: the sandbox's empty, freshly git-init'd repo can make a
				// perfectly healthy host section short-circuit before the
				// chained ethos section ever runs (H3's residual). When the
				// installed marker section is BYTE-IDENTICAL to what this
				// build would install today, the ethos section itself is
				// demonstrably not the problem — `ethos enable` would
				// re-chain the same content and reproduce this exact
				// failure. FAILing "stale" here is both wrong (the section
				// is current) and useless (the prescribed remedy fixes
				// nothing); WARN names what actually happened instead.
				return Result{Name: name, Status: "WARN", Detail: fmt.Sprintf(
					"could not verify — a host section exited (status %d) before reaching the ethos call; the %s section itself matches this ethos build, not stale — this looks like a host-section problem, not an ethos one",
					earlyExit.code, spec.ShortName)}
			}
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
				"cannot verify the %s hook by execution: %v", spec.ShortName, err)}
		}
		active = observed
	}

	// The hook must be present and active.
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("enabled here but no %s hook", spec.File)}
		}
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("cannot stat %s: %v", hook, statErr)}
	}
	if !active {
		// shellHook is false here only when the body could not be executed
		// at all; the best-effort text scan then picks a more specific
		// message (present-but-wrong-interpreter vs section-but-no-call)
		// purely for wording — it never grants a PASS, which comes only
		// from hookInvocationObserved above.
		if !shellHook && looksLikeInvocation(body, spec.accepted()...) {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
				"%s call present but the hook's shebang is not a shell — git runs it under another interpreter", spec.ShortName)}
		}
		if bytes.Contains(body, []byte(spec.Tag)) {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
				"%s section present but no active %q call (stale)", spec.Name, strings.Join(spec.InvokeArgs, " "))}
		}
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("enabled here but the %s hook is not chained", spec.ShortName)}
	}
	if hasMarkerSection(body, spec.Tag) {
		return Result{Name: name, Status: "PASS", Detail: fmt.Sprintf("chained %s section active", spec.ShortName)}
	}
	return Result{Name: name, Status: "PASS", Detail: fmt.Sprintf("standalone %s hook active", spec.ShortName)}
}

// hasMarkerSection reports whether body carries tag's BEGIN marker on a real
// (non-heredoc) line. It consults the same textscan heredoc mask as githook,
// so a foreign hook that merely documents the marker text inside a heredoc
// is not misread as a chained section.
func hasMarkerSection(body []byte, tag string) bool {
	lines := textscan.SplitKeepEnds(body)
	mask := textscan.HeredocMask(body)
	for i, raw := range lines {
		if !mask[i] && strings.HasPrefix(textscan.StripTerminator(raw), "# --- BEGIN "+tag) {
			return true
		}
	}
	return false
}

// sectionIsCurrent reports whether body's installed spec.Tag section is
// byte-identical (after line-terminator normalization) to what this ethos
// build would install today — the same digest comparison CheckHookCurrency
// runs, reused here so checkHookPresence's P4 WARN downgrade can ask "is
// the ethos section itself the problem" without duplicating that logic. A
// section that cannot be parsed at all (truncated, hand-duplicated, not
// ethos-owned) is treated as not current — only a clean, matching section
// justifies telling the operator the problem is elsewhere.
func sectionIsCurrent(body []byte, spec HookSpec) bool {
	installed, ok, err := githook.InstalledSection(body, spec.Tag, spec.Ident)
	if err != nil || !ok {
		return false
	}
	return digestSection(installed) == digestSection(githook.ExpectedSection(spec.Tag, spec.Canonical))
}

// invocationPattern matches an ethos subcommand call in command position:
// the ethos binary (bare `ethos` or the hook's "$ethos_bin" variable)
// followed by argv's words in order. Command position means the token
// begins the line (after only indentation) or follows a statement
// separator (`;`, `&`, `|`, `(`, `!`) and optional whitespace — not merely
// any whitespace, so the phrase passed as ARGUMENTS to another command
// (`echo ethos audit seal`) does not match, and neither does a
// string-literal mention (`echo "audit seal"`).
//
// This is diagnostic only (see looksLikeInvocation) — it does not decide
// PASS/FAIL, only which FAIL message to show for a hook that could not be
// executed. The authoritative active/inactive determination is
// hookInvocationObserved, by execution (ethos-kcbv).
func invocationPattern(argv []string) *regexp.Regexp {
	words := make([]string, len(argv))
	for i, w := range argv {
		words[i] = regexp.QuoteMeta(w)
	}
	return regexp.MustCompile(`(^[\t ]*|[;&|(!][\t ]*)("?\$\{?ethos_bin\}?"?|ethos)[\t ]+` + strings.Join(words, `[\t ]+`) + `([\s;&|)]|$)`)
}

// looksLikeInvocation is a best-effort, non-authoritative text scan used
// only to choose a more specific FAIL message when a hook's shebang means
// it was never executed (checkHookPresence attempts execution only for a
// shell hook). A false positive or negative here changes only which FAIL
// detail string is shown, never the PASS/FAIL verdict.
func looksLikeInvocation(body []byte, argvs ...[]string) bool {
	for _, argv := range argvs {
		pat := invocationPattern(argv)
		for _, line := range strings.Split(string(body), "\n") {
			if pat.MatchString(line) {
				return true
			}
		}
	}
	return false
}

// HookSpec names one ethos-managed git hook, shared by checkHookPresence
// (presence, execution-verified) and CheckHookCurrency (content drift).
type HookSpec struct {
	Name      string // report label, e.g. "Seal hook"
	ShortName string // lowercase, no "hook" suffix: "seal" / "trailer"
	File      string // hook filename inside the hooks dir: "pre-commit"
	Tag       string // hooks.SealTag / hooks.TrailerTag
	Ident     string // hooks.SealIdent / hooks.TrailerIdent
	Canonical []byte // hooks.PreCommit / hooks.CommitMsg
	// InvokeArgs is the ethos subcommand the DES-083 shim calls, which
	// checkHookPresence looks for by executing the hook.
	InvokeArgs []string
	// LegacyArgs are the public commands a hand-written or framework-managed
	// hook calls to do the same job, and a section copied before the shim
	// called. Such a hook is active, not unchained; whether a marker section is
	// current is CheckHookCurrency's byte comparison, not this list.
	LegacyArgs [][]string
	// NeedsMsgArg requests a scratch commit-message file be created and
	// passed as $1 in the sandbox — the commit-msg hook refuses to do
	// anything without one.
	NeedsMsgArg bool
	// StaleBlocksCommit marks a hook whose stale section can stop commits (the
	// seal hook: an old section made commits fail on legacy audit data). A stale
	// section of such a hook is FAIL; for the others it stays WARN.
	StaleBlocksCommit bool
}

// hookSpecs is every hook doctor verifies, one per layout.HookNames entry.
var hookSpecs = []HookSpec{sealHookSpec, trailerHookSpec}

var sealHookSpec = HookSpec{
	Name:       "Seal hook",
	ShortName:  "seal",
	File:       layout.HookPreCommit,
	Tag:        hooks.SealTag,
	Ident:      hooks.SealIdent,
	Canonical:  hooks.PreCommit,
	InvokeArgs: []string{"hook", "pre-commit"},
	LegacyArgs: [][]string{{"audit", "seal"}},

	StaleBlocksCommit: true,
}

var trailerHookSpec = HookSpec{
	Name:        "Trailer hook",
	ShortName:   "trailer",
	File:        layout.HookCommitMsg,
	Tag:         hooks.TrailerTag,
	Ident:       hooks.TrailerIdent,
	Canonical:   hooks.CommitMsg,
	InvokeArgs:  []string{"hook", "commit-msg"},
	LegacyArgs:  [][]string{{"hook", "commit-trailers"}, {"hook", "commit-message"}},
	NeedsMsgArg: true,
}

// accepted returns every argv that counts as the hook calling ethos: the shim's
// first, then the legacy ones.
func (s HookSpec) accepted() [][]string {
	return append([][]string{s.InvokeArgs}, s.LegacyArgs...)
}

// hashPrefixLen truncates a sha256 hex digest for Detail so a currency
// result stays a short, comparable fingerprint rather than the hook's full
// body — display only. Comparisons always use the full digest from
// digestSection; truncating before comparing would collapse SHA-256's
// collision resistance to a 32-bit birthday bound.
const hashPrefixLen = 8

// digestSection normalizes section's line terminators to LF (Chain rewrites
// them to match a foreign CRLF host, so a byte compare across EOL styles
// would false-positive on content that is otherwise unchanged) and returns
// the full SHA-256 digest of the result. Callers wanting a short fingerprint
// for display, not comparison, pass the result to shortHex.
func digestSection(section []byte) [sha256.Size]byte {
	var norm bytes.Buffer
	for _, line := range textscan.SplitKeepEnds(section) {
		norm.WriteString(textscan.StripTerminator(line))
		norm.WriteByte('\n')
	}
	return sha256.Sum256(norm.Bytes())
}

// shortHex renders sum as a hashPrefixLen-character hex fingerprint for
// Detail text. Never use this for equality checks — compare the [sha256.Size]byte
// values digestSection returns instead.
func shortHex(sum [sha256.Size]byte) string {
	return hex.EncodeToString(sum[:])[:hashPrefixLen]
}

// CheckHookCurrency compares spec's installed section against what this
// ethos build would install today (docs/design-hook-drift-detection.md). A
// section that doesn't exist is not this check's concern (PASS, nothing
// installed); a section that exists is checked for currency regardless of
// whether ethos is enabled in this repo right now, because `ethos enable` is
// the remedy for both problems. The marker is read for one thing only: how bad
// a stale section is. A stale seal section blocks commits only where ethos is
// enabled; in a dormant repo the hook passes through, so it is a WARN.
func CheckHookCurrency(repoRoot string, spec HookSpec) (result Result) {
	name := spec.Name + " currency"

	if repoRoot == "" {
		return Result{Name: name, Status: "PASS", Detail: "not in a repo"}
	}

	lk := githook.HooksDir(repoRoot)
	dir, lkErr := lk.Verified()
	if lkErr != nil {
		return Result{Name: name, Status: "WARN", Detail: "hooks directory not verified: " + lk.Explain()}
	}
	hooksDirWarnings := lk.Warnings
	if len(hooksDirWarnings) > 0 {
		defer func() {
			result.Detail += " (" + strings.Join(hooksDirWarnings, "; ") + ")"
		}()
	}
	hookPath := filepath.Join(dir, spec.File)
	data, err := os.ReadFile(hookPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{Name: name, Status: "PASS", Detail: fmt.Sprintf("no %s section installed", spec.Name)}
		}
		cause := ""
		if os.IsPermission(err) {
			cause = " (permissions)"
		}
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("cannot read %s: %v%s", hookPath, err, cause)}
	}

	// A host ending inside an unterminated heredoc masks its trailing lines
	// opaque — a real section below the open heredoc is invisible to the
	// scanner and would otherwise read PASS "nothing installed". Chain and
	// Unchain already refuse to touch such a file; this check must refuse
	// for the same reason before trusting InstalledSection's answer.
	if textscan.HeredocOpenAtEOF(data) {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
			"%s ends inside an unterminated here-document; ethos cannot chain its section into it", hookPath)}
	}

	installed, ok, err := githook.InstalledSection(data, spec.Tag, spec.Ident)
	if err != nil {
		if errors.Is(err, githook.ErrSectionTruncated) {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
				"%s section has a BEGIN with no matching END — truncated section", spec.Name)}
		} else if errors.Is(err, githook.ErrSectionNotEthosOwned) {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
				"%s section present but does not carry ethos's fingerprint — not a recognized ethos section; ethos leaves it in place", spec.Name)}
		} else if errors.Is(err, githook.ErrSectionDuplicated) {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
				"multiple %q sections found — duplicated section", spec.Tag)}
		}
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
			"%s section could not be read: %v", spec.Name, err)}
	}
	if !ok {
		if githook.IsLegacyStandalone(data, spec.Ident) {
			return Result{Name: name, Status: "WARN", Detail: fmt.Sprintf(
				"%s is active but still in the legacy pre-marker standalone format", spec.Name)}
		}
		return Result{Name: name, Status: "PASS", Detail: fmt.Sprintf("no %s section installed", spec.Name)}
	}

	installedDigest := digestSection(installed)
	expectedDigest := digestSection(githook.ExpectedSection(spec.Tag, spec.Canonical))
	if installedDigest == expectedDigest {
		return Result{Name: name, Status: "PASS", Detail: fmt.Sprintf(
			"%s section matches this ethos build (sha256:%s)", spec.Name, shortHex(installedDigest))}
	}
	if spec.StaleBlocksCommit {
		on, markErr := resolve.EnabledMarker(repoRoot)
		if markErr == nil && !on {
			return Result{Name: name, Status: "WARN", Detail: fmt.Sprintf(
				"%s section content differs from what this ethos build would install (installed sha256:%s, current sha256:%s) (dormant repo: hook passes through)",
				spec.Name, shortHex(installedDigest), shortHex(expectedDigest))}
		}
		// An unreadable marker is not proof of a dormant repo: keep the FAIL.
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
			"%s section content differs from what this ethos build would install (installed sha256:%s, current sha256:%s) — stale section may block commits",
			spec.Name, shortHex(installedDigest), shortHex(expectedDigest))}
	}
	return Result{Name: name, Status: "WARN", Detail: fmt.Sprintf(
		"%s section content differs from what this ethos build would install (installed sha256:%s, current sha256:%s)",
		spec.Name, shortHex(installedDigest), shortHex(expectedDigest))}
}

// identityDirStatus verifies the identity directory exists, is a readable
// directory, and holds at least one identity file (*.yaml).
//
// A layered store reports its repo-local identities dir as primary. That
// dir is legitimately absent or empty when a repo keeps its identities in the
// active bundle or the global store — a repo with only an ethos.yaml, or only
// missions/ under .punt-labs/ethos/. Layered resolution reads the global
// store, so then the check passes when the global identities dir is usable
// and names it; no usable identities in the repo or the global store is a
// fresh install, a WARN pointing at `ethos setup` that matches
// CheckHumanIdentity's first-run WARN, not a FAIL. Under `resolution:
// repo-only` nothing else can resolve an identity, so a missing or empty repo
// dir stays a FAIL. A dir that is not a directory or cannot be read fails in
// every mode, naming the cause.
func identityDirStatus(s identity.IdentityStore) (detail, status string) {
	dir := s.IdentitiesDir()
	state, why := inspectIdentitiesDir(dir)
	switch state {
	case dirUsable:
		return dir, "PASS"
	case dirBroken:
		return "error: " + why, "FAIL"
	}
	ls, layered := s.(*identity.LayeredStore)
	if layered && ls.BundleRoot() != "" {
		// The active bundle supplies identities in both modes, so a repo dir
		// that is missing or empty is fine when the bundle's is usable.
		bdir := identity.NewStore(ls.BundleRoot()).IdentitiesDir()
		bstate, bwhy := inspectIdentitiesDir(bdir)
		switch bstate {
		case dirUsable:
			return bdir + " (repo has none; resolved from the active bundle)", "PASS"
		case dirBroken:
			return fmt.Sprintf("error: bundle identities dir %s: %s", bdir, bwhy), "FAIL"
		}
	}
	if layered && !ls.RepoAuthoritative() {
		gdir := identity.NewStore(ls.GlobalRoot()).IdentitiesDir()
		gstate, gwhy := inspectIdentitiesDir(gdir)
		switch gstate {
		case dirUsable:
			return gdir + " (repo has none; layered resolution reads the global store)", "PASS"
		case dirBroken:
			return fmt.Sprintf("error: global identities dir %s: %s", gdir, gwhy), "FAIL"
		}
		return "no identities yet — run `ethos setup` to create yours", "WARN"
	}
	return why, "FAIL"
}

type dirState int

const (
	dirUsable  dirState = iota // a readable directory with at least one *.yaml
	dirMissing                 // absent or empty: layered resolution may fall back
	dirBroken                  // exists but cannot serve as an identities dir
)

// inspectIdentitiesDir classifies dir and returns the reason it is not usable.
func inspectIdentitiesDir(dir string) (dirState, string) {
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return dirMissing, "not found: " + dir
	}
	if err != nil {
		return dirBroken, err.Error()
	}
	if !info.IsDir() {
		return dirBroken, "not a directory: " + dir
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return dirBroken, err.Error()
	}
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".yaml" {
			return dirUsable, ""
		}
	}
	return dirMissing, "no identity files (*.yaml) in " + dir
}

// CheckHumanIdentity resolves and loads the current human identity. When
// resolution fails it separates two cases:
//
//   - Fresh install: the store holds no identities at all. This is the
//     expected first-run state, not a fault — WARN and point at `ethos setup`
//     rather than FAIL with circular "fix it, then re-run doctor" guidance.
//   - Misconfiguration: identities exist but the caller does not resolve to
//     exactly one of them — no match, or several sharing an email or GitHub
//     handle (ethos-u4kq). This is a real fault — FAIL loudly.
//
// An unreadable identity file (a Warning from List) counts as "not fresh": a
// broken file is a misconfiguration, so it FAILs rather than masquerading as a
// clean first run.
//
// Under repo-only resolution the caller is looked up in the repo's store and
// then the global store (identity.ForCaller): the repo commits its team and
// agents, not the person running ethos. A caller found only in the global store
// is a PASS that says so. A caller in neither is a FAIL.
//
// The failure detail is the resolver's own error, with no prefix. Every error
// it returns already says what went wrong — "no identity matches git user ..."
// or "ambiguous identity: 2 matches for email ...". The old "no match — "
// prefix contradicted the second one: an ambiguous store has too many matches,
// not none.
func CheckHumanIdentity(s identity.IdentityStore, ss *session.Store, checkout string) Result {
	name := HumanIdentityRow
	caller := identity.ForCaller(s)
	s = references(caller)
	handle, err := resolve.Resolve(s, ss, checkout)
	if err != nil {
		if list, listErr := s.List(); listErr == nil &&
			len(list.Identities) == 0 && len(list.Warnings) == 0 {
			return Result{Name: name, Status: "WARN", Detail: "no identity yet — run `ethos setup` to create yours"}
		}
		return Result{Name: name, Status: "FAIL", Detail: err.Error()}
	}
	var id *identity.Identity
	fromGlobal := false
	id, err = s.Load(handle, identity.Reference(true))
	if cs, ok := caller.(*identity.CallerStore); ok && err == nil {
		fromGlobal = !cs.IdentityStore.Exists(handle)
	}
	if err != nil {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("handle %q not loadable: %v", handle, err)}
	}
	detail := fmt.Sprintf("%s (%s)", id.Name, id.Handle)
	if fromGlobal {
		detail += " — resolved from the global store"
	}
	return Result{Name: name, Status: "PASS", Detail: detail}
}

// CheckDefaultAgent checks whether a default agent is configured for the
// current repository. Three states: not-in-a-repo and not-configured
// are both "OK" (empty repos and repos without an agent field are
// legitimate). A ResolveAgent error — unreadable or malformed
// `.punt-labs/ethos.yaml` — is a diagnostic failure the user needs to
// see. The detail string is the raw error text with no "error: " prefix
// — doctor's output already prints a FAIL status column derived from
// the returned bool, so prepending "error: " would double-label.
//
// repoRoot is the checkout, used only for the "in a git repo" guard.
// storeRoot is the shared store: the agent: key resolves via StoreRootIfAny
// in every other consumer (session_start's persona, resolveLeader, ethos
// resolve-agent), so this health check must read it from the same tree — or
// from a worktree it would report a default agent that disagrees with the
// one the session and dispatch actually use (Bugbot #370 class).
func CheckDefaultAgent(repoRoot, storeRoot string) (string, bool) {
	var cfg *resolve.RepoConfig
	var err error
	if repoRoot != "" && storeRoot != "" {
		cfg, err = resolve.ConfigOf(storeRoot)
		if err != nil {
			err = fmt.Errorf("resolve agent: %w", err)
		}
	}
	return checkDefaultAgentFromConfig(repoRoot, storeRoot, cfg, err)
}

// checkDefaultAgentFromConfig keeps the checkout guard separate from the shared
// store: an unavailable store must not cause a read relative to the process cwd.
func checkDefaultAgentFromConfig(repoRoot, storeRoot string, cfg *resolve.RepoConfig, configErr error) (string, bool) {
	if repoRoot == "" {
		return "not in a git repo", true
	}
	if storeRoot == "" {
		// RunAllFor withheld an unusable store. Its gate reports the captured
		// failure as "not checked", not a fresh answer from another config.
		return "not configured", true
	}
	if configErr != nil {
		return configErr.Error(), false
	}
	if cfg == nil || cfg.Agent == "" {
		return "not configured", true
	}
	return cfg.Agent, true
}

// CheckDuplicateFields scans all identities for duplicate github or email
// bindings.
func CheckDuplicateFields(s identity.IdentityStore, _ *session.Store) (string, bool) {
	result, err := references(s).List()
	if err != nil {
		return fmt.Sprintf("error: %v", err), false
	}
	var dupes []string
	seen := map[string]map[string]string{
		"github": {},
		"email":  {},
	}
	for _, id := range result.Identities {
		for field, values := range seen {
			var val string
			switch field {
			case "github":
				val = id.GitHub
			case "email":
				val = id.Email
			}
			if val == "" {
				continue
			}
			if prev, ok := values[val]; ok {
				dupes = append(dupes, fmt.Sprintf("%s %q: %s and %s", field, val, prev, id.Handle))
			} else {
				values[val] = id.Handle
			}
		}
	}
	if len(dupes) > 0 {
		return "duplicates found: " + strings.Join(dupes, "; "), false
	}
	return "no duplicates", true
}
