// Package doctor provides shared health-check logic for the ethos CLI
// and MCP server.
package doctor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/githook"
	"github.com/punt-labs/ethos/v4/internal/identity"
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

// RunAll executes every standard health check and returns the results.
//
// repoRoot is the current work tree (FindRepoRoot) — the checkout whose
// .claude/agents and seal hook the checks inspect. storeRoot is the shared
// mission/config store (StoreRepoRoot); it differs from repoRoot only inside
// a linked worktree, and only the team-config read in CheckOrphanedAgentFiles
// needs it (Bugbot #370 class: resolving the active team from the worktree
// while activation wrote it to the store produced false orphan reports).
func RunAll(s identity.IdentityStore, ss *session.Store, repoRoot, storeRoot string, teams *team.LayeredStore) []Result {
	results := make([]Result, 0, 6)

	dir, ok := CheckIdentityDir(s, ss, hasRepoLocalTeam(storeRoot))
	results = append(results, passFail("Identity directory", dir, ok))

	// Human identity carries its own status: a fresh install (no identity
	// yet) is a WARN pointing at `ethos setup`, not a FAIL.
	results = append(results, CheckHumanIdentity(s, ss))

	// Default agent reads the agent: key from the shared store (storeRoot),
	// matching session_start/resolveLeader/resolve-agent; the "in a repo"
	// guard stays on the checkout (repoRoot) (#370).
	agent, ok := CheckDefaultAgent(repoRoot, storeRoot)
	results = append(results, passFail("Default agent", agent, ok))

	dup, ok := CheckDuplicateFields(s, ss)
	results = append(results, passFail("Duplicate fields", dup, ok))

	results = append(results, CheckOrphanedAgentFiles(repoRoot, storeRoot, teams, s))
	results = append(results, CheckSealHook(repoRoot))

	// ethos-bfml/ethos-hy40: the trailer hook gets the same presence check
	// the seal hook already had — gated on the enabled marker, active
	// determined by execution (checkHookPresence), not by whether a section
	// merely exists. See the DES-hook-presence ADR in DESIGN.md.
	results = append(results, CheckTrailerHook(repoRoot))

	// DES-hook-drift-detection: content-currency checks, independent of the
	// enabled marker and of the presence checks' active states — see
	// docs/design-hook-drift-detection.md.
	results = append(results, CheckHookCurrency(repoRoot, sealHookSpec))
	results = append(results, CheckHookCurrency(repoRoot, trailerHookSpec))

	// DES-057. The completeness gate reads the shared store (storeRoot),
	// matching every other resolution consumer; the git-boundary check
	// reads the checkout (repoRoot), because the index and .gitignore
	// belong to the tree being committed.
	results = append(results, CheckRepoSetComplete(s, storeRoot))
	results = append(results, CheckLocalExtNotTracked(repoRoot))
	results = append(results, CheckExtCredentialNames(s))

	// DES-072: hand-edited mission files read as machine-written and
	// are invisible to every mission-reading surface — the exact
	// failure mode `ethos mission correct` exists to replace. Reads
	// storeRoot (the shared record), matching CheckRepoSetComplete.
	results = append(results, CheckNoHandEditedMissionFiles(storeRoot))

	// ethos-e05k: the delegated-worker invariant on the "implement" and
	// "test" archetypes is data-driven from the deployed YAML, so a binary
	// upgrade with no `ethos seed` re-run silently loses enforcement.
	results = append(results, CheckDelegatedWorkerArchetypes(storeRoot))
	return results
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
// StoreRepoRoot by every other reader and the writer (team activate), so the
// team read must use it too — resolving the team from the worktree while
// activation wrote it to the store would compute "expected" agents from the
// wrong team and flag valid agents as orphaned (Bugbot #370 class). They
// coincide outside a worktree.
//
// s classifies each flagged handle for the FAIL detail (ethos-jw1z): a
// handle that resolves to a real identity somewhere in s is a STALE
// generated file — the common case, left behind by a team-scope change,
// safe to delete or regenerate — while a handle matching no identity at
// all is a genuine orphan the operator should look at before deleting. s
// may be nil (some callers have no identity store in scope); the
// classification is then skipped and every handle reads as an
// undifferentiated orphan, same as before this distinction existed.
func CheckOrphanedAgentFiles(repoRoot, storeRoot string, teams *team.LayeredStore, s identity.IdentityStore) Result {
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

	teamName, err := resolve.ResolveTeam(storeRoot)
	if err != nil {
		// ResolveTeam only errors when .punt-labs/ethos.yaml exists but is
		// unreadable or malformed — a real misconfiguration the operator
		// needs to see, not a "no team configured" no-op.
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("could not load repo config: %s", err)}
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

	var orphaned []string
	for _, path := range matches {
		handle := strings.TrimSuffix(filepath.Base(path), ".md")
		if members[handle] || checklist[handle] {
			continue
		}
		orphaned = append(orphaned, handle)
	}

	if len(orphaned) == 0 {
		return Result{Name: name, Status: "PASS", Detail: "no orphaned agent files"}
	}
	sort.Strings(orphaned)
	return Result{Name: name, Status: "FAIL", Detail: classifyOrphans(orphaned, s)}
}

// classifyOrphans builds the FAIL detail for CheckOrphanedAgentFiles,
// splitting handle by whether it resolves to a known identity anywhere in s
// (ethos-jw1z). A resolvable handle is not on the active team right now but
// was clearly generated for some team at some point — "stale", safe to
// delete or fixed by re-running `ethos session start` once the identity is
// back on a team. A handle matching no identity at all did not come from a
// team-scope change this store can see; it is a genuine orphan, worth
// looking at before deleting. s may be nil, in which case the distinction
// cannot be made honestly and every handle reads as a plain, undifferentiated
// orphan rather than guessing.
func classifyOrphans(orphaned []string, s identity.IdentityStore) string {
	if s == nil {
		return "orphaned agent files (not on any team): " + strings.Join(orphaned, ", ")
	}
	var stale, unresolved []string
	for _, handle := range orphaned {
		if _, err := s.Load(handle, identity.Reference(true)); err == nil {
			stale = append(stale, handle)
		} else {
			unresolved = append(unresolved, handle)
		}
	}
	var parts []string
	if len(stale) > 0 {
		parts = append(parts, fmt.Sprintf(
			"stale (known identity, not on the active team — generated for a previous team scope, safe to delete): %s",
			strings.Join(stale, ", ")))
	}
	if len(unresolved) > 0 {
		parts = append(parts, fmt.Sprintf(
			"unresolved (no matching identity anywhere — investigate before deleting): %s",
			strings.Join(unresolved, ", ")))
	}
	return "orphaned agent files — " + strings.Join(parts, "; ")
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

// CheckSealHook reports on the DES-058 audit-seal pre-commit hook.
func CheckSealHook(repoRoot string) Result { return checkHookPresence(repoRoot, sealHookSpec) }

// CheckTrailerHook reports on the DES-054 Mission/Delegation commit-msg
// hook (ethos-bfml/ethos-hy40): the same presence check CheckSealHook has
// always run, extended to the trailer side, so a hand-removed or
// host-clobbered commit-msg chain on an enabled repo no longer reads green
// on every line.
func CheckTrailerHook(repoRoot string) Result { return checkHookPresence(repoRoot, trailerHookSpec) }

// checkHookPresence reports on spec's hook, keyed on the enabled marker
// (§2.11). Four states:
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
func checkHookPresence(repoRoot string, spec HookSpec) Result {
	name := "Audit " + spec.ShortName + " hook"
	const remedy = " — run `ethos enable`"

	if repoRoot == "" {
		return Result{Name: name, Status: "PASS", Detail: "not in a repo"}
	}

	// A marker stat error that is not "does not exist" must not collapse to
	// "not enabled" — a doubly-broken repo (unreadable .punt-labs/ethos plus a
	// lost hook) would then read PASS while commits flow unsealed. Surface it
	// as a FAIL instead of guessing dormancy (S4).
	markerPresent := false
	if _, err := os.Stat(filepath.Join(repoRoot, ".punt-labs", "ethos", "enabled")); err == nil {
		markerPresent = true
	} else if !os.IsNotExist(err) {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("cannot determine enablement here: %v", err)}
	}
	dir, _ := githook.HooksDir(repoRoot)
	hook := filepath.Join(dir, spec.File)

	info, statErr := os.Stat(hook)
	var body []byte
	if statErr == nil {
		data, err := os.ReadFile(hook)
		if err != nil {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("cannot read %s: %v%s", hook, err, remedy)}
		}
		body = data
	}

	// Only a shell hook is ever attempted by execution — a non-shell body
	// can never run the way git would run it (matches the shebang check
	// below), and there is no interpreter-neutral way to "run" it safely.
	//
	// Execution is further gated on markerPresent (M1): a dormant repo's
	// hook is read-only diagnostic input here, not something `ethos doctor`
	// should ever run — a foreign pre-commit chained in a repo where ethos
	// is switched off would otherwise execute third-party shell to answer a
	// question ("is this repo enabled") that execution never needed to
	// settle. hasMarkerSection's lexical fallback below is sufficient for
	// the dormant-repo WARN case; it does not need proof by execution.
	shellHook := statErr == nil && markerPresent && textscan.IsShellHook(body)
	var active bool
	if shellHook {
		observed, err := hookInvocationObserved(body, spec.InvokeArgs, spec.NeedsMsgArg)
		if err != nil {
			if errors.Is(err, errSandboxGitUnavailable) {
				return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
					"cannot verify the %s hook by execution: %v — install git to run this check", spec.ShortName, err)}
			}
			if errors.Is(err, errSandboxUnsupportedPlatform) {
				// M4: this build cannot exercise the sandbox on this GOOS.
				// FAILing here would be a false-positive on a perfectly
				// healthy install — the pre-execution lexical scan was
				// platform-neutral, so silently defaulting to FAIL on
				// today's execution-based check is a regression in the
				// dangerous direction. WARN, honestly, instead.
				return Result{Name: name, Status: "WARN", Detail: fmt.Sprintf(
					"cannot verify the %s hook by execution on this platform — inspect it manually", spec.ShortName)}
			}
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
				"cannot verify the %s hook by execution: %v", spec.ShortName, err)}
		}
		active = observed
	}
	// "Chained" for the gate check is the section marker OR an active call —
	// a stale section still counts as present.
	chained := statErr == nil && (active || hasMarkerSection(body, spec.Tag))

	if !markerPresent {
		if chained {
			return Result{Name: name, Status: "WARN", Detail: spec.ShortName + " hook chained but ethos not enabled here" + remedy + " to converge, or remove the stale hook"}
		}
		return Result{Name: name, Status: "PASS", Detail: "not enabled here"}
	}

	// Enabled: the hook must be present and active.
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("enabled here but no %s hook", spec.File) + remedy}
		}
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("cannot stat %s: %v%s", hook, statErr, remedy)}
	}
	if !active {
		// shellHook is false here only when the body could not be executed
		// at all; the best-effort text scan then picks a more specific
		// message (present-but-wrong-interpreter vs section-but-no-call)
		// purely for wording — it never grants a PASS, which comes only
		// from hookInvocationObserved above.
		if !shellHook && looksLikeInvocation(body, spec.InvokeArgs) {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
				"%s call present but the hook's shebang is not a shell — git runs it under another interpreter", spec.ShortName) + remedy}
		}
		if bytes.Contains(body, []byte(spec.Tag)) {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
				"%s section present but no active %q call (stale)", spec.Name, strings.Join(spec.InvokeArgs, " ")) + remedy}
		}
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("enabled here but the %s hook is not chained", spec.ShortName) + remedy}
	}
	if info.Mode().Perm()&0o111 == 0 {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("%s hook present but not executable — run: chmod +x %s", spec.ShortName, hook)}
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
func looksLikeInvocation(body []byte, argv []string) bool {
	pat := invocationPattern(argv)
	for _, line := range strings.Split(string(body), "\n") {
		if pat.MatchString(line) {
			return true
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
	// InvokeArgs is the ethos subcommand checkHookPresence looks for by
	// executing the hook, e.g. []string{"audit", "seal"}.
	InvokeArgs []string
	// NeedsMsgArg requests a scratch commit-message file be created and
	// passed as $1 in the sandbox — the commit-msg hook refuses to do
	// anything without one.
	NeedsMsgArg bool
}

var sealHookSpec = HookSpec{
	Name:       "Seal hook",
	ShortName:  "seal",
	File:       "pre-commit",
	Tag:        hooks.SealTag,
	Ident:      hooks.SealIdent,
	Canonical:  hooks.PreCommit,
	InvokeArgs: []string{"audit", "seal"},
}

var trailerHookSpec = HookSpec{
	Name:        "Trailer hook",
	ShortName:   "trailer",
	File:        "commit-msg",
	Tag:         hooks.TrailerTag,
	Ident:       hooks.TrailerIdent,
	Canonical:   hooks.CommitMsg,
	InvokeArgs:  []string{"hook", "commit-trailers"},
	NeedsMsgArg: true,
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
// ethos build would install today (docs/design-hook-drift-detection.md). It
// never reads the enabled marker: a section that doesn't exist is not this
// check's concern (PASS, nothing installed); a section that exists is
// checked for currency regardless of whether ethos is enabled in this repo
// right now, because `ethos enable` is the remedy for both problems.
func CheckHookCurrency(repoRoot string, spec HookSpec) Result {
	name := spec.Name + " currency"

	if repoRoot == "" {
		return Result{Name: name, Status: "PASS", Detail: "not in a repo"}
	}

	dir, _ := githook.HooksDir(repoRoot)
	hookPath := filepath.Join(dir, spec.File)
	data, err := os.ReadFile(hookPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{Name: name, Status: "PASS", Detail: fmt.Sprintf("no %s section installed", spec.Name)}
		}
		remedy := " — inspect the file manually"
		if os.IsPermission(err) {
			remedy = " — check file permissions"
		}
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("cannot read %s: %v%s", hookPath, err, remedy)}
	}

	// A host ending inside an unterminated heredoc masks its trailing lines
	// opaque — a real section below the open heredoc is invisible to the
	// scanner and would otherwise read PASS "nothing installed". Chain and
	// Unchain already refuse to touch such a file; this check must refuse
	// for the same reason before trusting InstalledSection's answer.
	if textscan.HeredocOpenAtEOF(data) {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
			"%s ends inside an unterminated here-document — close it and re-run; `ethos enable` cannot chain into it either", hookPath)}
	}

	installed, ok, err := githook.InstalledSection(data, spec.Tag, spec.Ident)
	if err != nil {
		if errors.Is(err, githook.ErrSectionTruncated) {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
				"%s section has a BEGIN with no matching END — hand-truncated; fix it by hand", spec.Name)}
		} else if errors.Is(err, githook.ErrSectionNotEthosOwned) {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
				"%s section present but does not carry ethos's fingerprint — not a recognized ethos section; remove it and run `ethos enable`", spec.Name)}
		} else if errors.Is(err, githook.ErrSectionDuplicated) {
			return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
				"multiple %q sections found — hand-duplicated; run `ethos enable` to collapse them into one", spec.Tag)}
		}
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf(
			"%s section could not be read: %v — re-run `ethos doctor`; if this persists, file a bug", spec.Name, err)}
	}
	if !ok {
		if githook.IsLegacyStandalone(data, spec.Ident) {
			return Result{Name: name, Status: "WARN", Detail: fmt.Sprintf(
				"%s is active but still in the legacy pre-marker standalone format — run `ethos enable` to refresh it into the current format", spec.Name)}
		}
		return Result{Name: name, Status: "PASS", Detail: fmt.Sprintf("no %s section installed", spec.Name)}
	}

	installedDigest := digestSection(installed)
	expectedDigest := digestSection(githook.ExpectedSection(spec.Tag, spec.Canonical))
	if installedDigest == expectedDigest {
		return Result{Name: name, Status: "PASS", Detail: fmt.Sprintf(
			"%s section matches this ethos build (sha256:%s)", spec.Name, shortHex(installedDigest))}
	}
	return Result{Name: name, Status: "WARN", Detail: fmt.Sprintf(
		"%s section content differs from what this ethos build would install (installed sha256:%s, current sha256:%s) — run `ethos enable` to refresh",
		spec.Name, shortHex(installedDigest), shortHex(expectedDigest))}
}

// CheckIdentityDir verifies the identity directory exists.
//
// A layered store reports its repo-local identities dir as primary. That
// dir is legitimately absent when a repo carries a repo-local team under
// .punt-labs/ethos/ and its identities live in the active bundle or the
// global store — the default shape after `ethos setup`. Only in that case
// does the check fall back to the global identities dir. The fallback is
// gated on hasRepoTeam so a repo missing its identities with NO repo-local
// team — for example an uninitialized submodule checkout with an empty
// .punt-labs/ethos/identities/ — still FAILs loudly with the correct
// "not found" signal instead of being masked by a populated global store.
func CheckIdentityDir(s identity.IdentityStore, _ *session.Store, hasRepoTeam bool) (string, bool) {
	dir := s.IdentitiesDir()
	if _, err := os.Stat(dir); err == nil {
		return dir, true
	} else if !os.IsNotExist(err) {
		return fmt.Sprintf("error: %v", err), false
	}
	if hasRepoTeam {
		if ls, ok := s.(*identity.LayeredStore); ok {
			gdir := identity.NewStore(ls.GlobalRoot()).IdentitiesDir()
			if _, err := os.Stat(gdir); err == nil {
				return gdir, true
			}
		}
	}
	return fmt.Sprintf("not found: %s", dir), false
}

// hasRepoLocalTeam reports whether the repo carries at least one team file
// under <storeRoot>/.punt-labs/ethos/teams/. This is the signal that a repo
// is legitimately teams-only: the team is repo-tracked while identities
// resolve from the active bundle or the global store.
func hasRepoLocalTeam(storeRoot string) bool {
	if storeRoot == "" {
		return false
	}
	ethosRoot := filepath.Join(storeRoot, ".punt-labs", "ethos")
	names, err := team.NewStore(ethosRoot).List()
	return err == nil && len(names) > 0
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
// The failure detail is the resolver's own error, with no prefix. Every error
// it returns already says what went wrong — "no identity matches git user ..."
// or "ambiguous identity: 2 matches for email ...". The old "no match — "
// prefix contradicted the second one: an ambiguous store has too many matches,
// not none.
func CheckHumanIdentity(s identity.IdentityStore, ss *session.Store) Result {
	name := "Human identity"
	handle, err := resolve.Resolve(s, ss)
	if err != nil {
		if list, listErr := s.List(); listErr == nil &&
			len(list.Identities) == 0 && len(list.Warnings) == 0 {
			return Result{Name: name, Status: "WARN", Detail: "no identity yet — run `ethos setup` to create yours"}
		}
		return Result{Name: name, Status: "FAIL", Detail: err.Error()}
	}
	id, err := s.Load(handle, identity.Reference(true))
	if err != nil {
		return Result{Name: name, Status: "FAIL", Detail: fmt.Sprintf("handle %q not loadable: %v", handle, err)}
	}
	return Result{Name: name, Status: "PASS", Detail: fmt.Sprintf("%s (%s)", id.Name, id.Handle)}
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
// storeRoot is the shared store: the agent: key resolves via StoreRepoRoot
// in every other consumer (session_start's persona, resolveLeader, ethos
// resolve-agent), so this health check must read it from the same tree — or
// from a worktree it would report a default agent that disagrees with the
// one the session and dispatch actually use (Bugbot #370 class).
func CheckDefaultAgent(repoRoot, storeRoot string) (string, bool) {
	if repoRoot == "" {
		return "not in a git repo", true
	}
	handle, err := resolve.ResolveAgent(storeRoot)
	if err != nil {
		return err.Error(), false
	}
	if handle == "" {
		return "not configured", true
	}
	return handle, true
}

// CheckDuplicateFields scans all identities for duplicate github or email
// bindings.
func CheckDuplicateFields(s identity.IdentityStore, _ *session.Store) (string, bool) {
	result, err := s.List()
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
