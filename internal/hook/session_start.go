package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/bundle"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/process"
	"github.com/punt-labs/ethos/v4/internal/repomiss"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/role"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/punt-labs/ethos/v4/internal/team"
)

// SessionStartResult is the JSON output of the session-start hook.
type SessionStartResult struct {
	// SystemMessage is shown to the user; set only to report a problem.
	SystemMessage      string `json:"systemMessage,omitempty"`
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext,omitempty"`
	} `json:"hookSpecificOutput"`
}

// SessionStartDeps holds the stores needed by the SessionStart hook.
type SessionStartDeps struct {
	// Located is the command's entry snapshot. Nil preserves direct callers:
	// the handler captures one before reading potentially blocked stdin.
	Located  *resolve.Located
	Store    identity.IdentityStore
	Sessions *session.Store
	Teams    *team.LayeredStore
	Roles    *role.LayeredStore
	// Issue is why the repository's bundle or policy could not be honoured, if
	// so (bundle.ConfigIssue): the session is told in its systemMessage.
	Issue error
	// RefreshHooks re-chains git hook sections that differ from this build and
	// reports what it did (checkout, store: the roots). Nil disables the
	// refresh. It runs only in a located repository; an error never fails the
	// session, but is told to it, because a stale seal section blocks commits.
	RefreshHooks func(checkout, store string) (HookRefresh, error)
	// Notice is the one line autorepair.Ensure earned on this start, "" when
	// nothing was repaired. It goes into systemMessage and additionalContext
	// because this hook's stderr goes to an unread log (DES-084 Output).
	Notice string
}

// HookRefresh is what a git hook refresh did, by hook name. A failed refresh
// may still return a partial result alongside its error.
type HookRefresh struct {
	Refreshed []string // stale sections rewritten in an existing hook
	Installed []string // hooks that did not exist and were created
	Chained   []string // foreign shell hooks that had no ethos section and got one
	// NonShell names hooks whose host is not a shell script: the section was
	// not chained in, so the seal never runs from them.
	NonShell []string
	// Tracked names stale hooks inside the work tree, left unrewritten.
	Tracked []string
	// Shared names hooks in a core.hooksPath outside the repo, left alone.
	Shared []string
	// TrackedMissing names hooks inside the work tree that do not exist and
	// were not created.
	TrackedMissing []string
	Warnings       []string // advisory lines for stderr
}

// HandleSessionStart reads the SessionStart hook payload from stdin,
// resolves identity, creates a session roster, and emits context.
func HandleSessionStart(r io.Reader, deps SessionStartDeps) error {
	if deps.Store == nil || deps.Sessions == nil {
		return fmt.Errorf("session-start: Store and Sessions stores are required")
	}

	store := deps.Store
	ss := deps.Sessions
	loc := resolve.Located{}
	if deps.Located == nil {
		loc = resolve.Roots()
	} else {
		loc = *deps.Located
	}
	deps.Located = &loc
	input, err := ReadInput(r, hookInputTimeout)
	if err != nil {
		return fmt.Errorf("session-start: %w", err)
	}

	sessionID, _ := input["session_id"].(string)
	// The repository is located once (resolve.Roots) and everything below reads
	// that answer. A store that cannot be read — a dangling symlink in
	// .punt-labs/ethos, an unreadable config, an unreadable .git — means this
	// repo's agent persona and team are not loaded; the session is told, no
	// persona is taken from the global store in its place, and the roster
	// records repo_unresolved.
	storeFailure := loc.StoreFailure()
	// A bundle or policy that cannot be honoured refuses the persona as a broken
	// store does, as the CLI and the MCP do: answering from the layers left
	// would consult the global one the repository did not choose.
	var policy *resolve.ConfigError
	if storeFailure == nil && errors.As(deps.Issue, &policy) && policy.Policy {
		storeFailure = deps.Issue
	}
	var resolvedID *identity.Identity
	var humanErr error
	if storeFailure != nil {
		humanErr = storeFailure
	} else {
		resolvedID, humanErr = resolveHumanIdentity(store, loc.Checkout)
	}
	purgeStale(ss)

	// Resolve agent persona from repo config. A non-nil error means
	// the config file exists but cannot be read or parsed — a loud
	// failure mode the user needs to see. Propagate fail-closed, same
	// pattern as the GenerateAgentFiles wrap below (ethos-9ai.6 C1).
	// The shell wrapper's `|| true` keeps Claude Code session startup
	// fail-open (cli.md §Hook Architecture); the non-zero exit code is
	// the signal for direct CLI invocation and `ethos doctor`.
	repoRoot := loc.Store
	note := storeUnlocatedNote(storeFailure)
	if issueNote := storeUnlocatedNote(deps.Issue); issueNote != "" && !errors.Is(storeFailure, deps.Issue) {
		if note != "" {
			note += " "
		}
		note += issueNote
	}
	if note != "" {
		fmt.Fprintf(os.Stderr, "ethos: session-start: %s\n", note)
	}
	if deps.Notice != "" {
		if note != "" {
			note += " "
		}
		note += deps.Notice
	}
	if refreshed := refreshStaleHooks(deps.RefreshHooks, loc); refreshed != "" {
		if note != "" {
			note += " "
		}
		note += refreshed
	}
	agentPersona := ""
	if storeFailure == nil && loc.Config != nil {
		agentPersona = loc.Config.Agent
	}

	if rosterNotice := createSessionRoster(ss, sessionID, resolvedID, humanErr, agentPersona, loc); rosterNotice != "" {
		fmt.Fprintf(os.Stderr, "ethos: session-start: %s\n", rosterNotice)
		if note != "" {
			note += " "
		}
		note += rosterNotice
	}

	// Emit the agent's persona block (not the human's).
	// The agent persona comes from repo config (.punt-labs/ethos.yaml).
	if agentPersona == "" {
		if err := emitHumanFallback(resolvedID, note); err != nil {
			return err
		}
		// An unreadable config is also the hook's exit status: the session was
		// told above, and a direct invocation or doctor sees the failure.
		if loc.ConfigErr != nil {
			return fmt.Errorf("resolving agent: %w", fmt.Errorf("resolve agent: %w", loc.ConfigErr))
		}
		return nil
	}

	agentID, agentLoadErr := store.Load(agentPersona)
	if agentLoadErr != nil {
		fmt.Fprintf(os.Stderr, "ethos: session-start: failed to load agent identity %q: %v\n", agentPersona, agentLoadErr)
		loadNote := fmt.Sprintf("Ethos: the agent identity %q could not be loaded (%v), so its persona was not applied.", agentPersona, agentLoadErr)
		if note != "" {
			note += " "
		}
		return emitHumanFallback(resolvedID, note+loadNote)
	}
	for _, w := range agentID.Warnings {
		fmt.Fprintf(os.Stderr, "ethos: session-start: agent identity %q: %s\n", agentPersona, w)
	}
	// DES-057's degrade: an extension the repo vendored but no longer
	// holds is reported and the session continues. Failing here would
	// brick a live session over agent memory wiring; saying nothing would
	// hand the operator an agent that looks right and has quietly lost
	// it. Doctor and agent generation are the surfaces that refuse.
	if len(agentID.MissingExt) > 0 {
		fmt.Fprintf(os.Stderr, "ethos: session-start: %v\n",
			repomiss.New(agentPersona, agentID.MissingExt))
	}

	// Generate .claude/agents/<handle>.md from ethos identity data.
	// Propagates: the returned error is the single authoritative
	// signal the CLI can gate on. The shell wrapper's `|| true` keeps
	// Claude Code session startup fail-open (cli.md §Hook Architecture)
	// so a broken config does not brick sessions, but `ethos hook
	// session-start` invoked directly exits non-zero — useful for
	// `ethos doctor` and manual debugging.
	//
	// Two roots: the config/team selection resolves from the shared store
	// (repoRoot = StoreRootIfAny), while the agent files are written to the
	// CURRENT checkout (CheckoutRootIfAny) — the tree the worktree's Claude reads
	// and where InstallAgentDefinitions writes. A single root would generate
	// a worktree's agents into the main tree, invisible to that worktree
	// (Bugbot HIGH on PR #370).
	if loc.CheckoutErr != nil {
		return fmt.Errorf("locating the checkout for agent generation: %w", loc.CheckoutErr)
	}
	checkoutRoot := loc.Checkout
	plan := AgentPlan{}
	var planErr error
	if deps.Teams != nil && deps.Roles != nil {
		plan, planErr = planSessionAgents(loc, store, deps.Teams, deps.Roles)
		if planErr != nil {
			fmt.Fprintf(os.Stderr, "ethos: session-start: resolving generated agents: %v; installing no stubs\n", planErr)
		}
	}
	var installErr error
	if deps.Teams != nil && deps.Roles == nil {
		// A partial dependency set cannot render a plan and establish its
		// ownership. Never pass nil as if the generator owned nothing.
		fmt.Fprintln(os.Stderr, "ethos: session-start: Roles store missing; installing no stubs because generator ownership is unavailable")
	} else if planErr == nil {
		installErr = installAgentDefsFor(loc, plan.Owned)
	}
	if repoRoot != "" && checkoutRoot != "" && deps.Teams != nil && deps.Roles != nil {
		policy, err := loc.Layout()
		if err != nil {
			return fmt.Errorf("constructing agent storage: %w", err)
		}
		if genErr := errors.Join(planErr, ApplyAgentPlan(policy, plan)); genErr != nil {
			return fmt.Errorf("generating agents: %w", errors.Join(installErr, genErr))
		}
	}
	if installErr != nil {
		return fmt.Errorf("installing agent definitions: %w", installErr)
	}

	return emitAgentContext(agentID, agentPersona, store, deps, loc.Checkout, note)
}

// hookRefreshSkipped is the sentence for a refresh that did not run because of
// cause (a located failure).
func hookRefreshSkipped(cause string, err error) string {
	return fmt.Sprintf("Git hook refresh skipped: %s%v. A stale seal section may block commits; run `ethos doctor`.", cause, err)
}

// skippedHookNote tells an enabled repo's session that the refresh was skipped
// for storeFailure. A dormant repo has no hooks worth refreshing and hears
// nothing.
func skippedHookNote(checkout string, storeFailure error) string {
	if on, _ := resolve.EnabledMarker(checkout); !on {
		return ""
	}
	return hookRefreshSkipped("", storeFailure)
}

// refreshStaleHooks runs refresh for the located repository and returns the
// sentence telling the session what happened to its git hook sections; "" when
// nothing was stale, when refresh is nil, or when there is no repository. A
// failure to locate the checkout or the store skips the refresh and says so. A
// stale seal section can block every commit, so a failure or a section that
// could not be refreshed is told to the session, not only logged: the shell
// wrapper sends stderr to a log file the session never reads. A store failure
// skips the refresh, and the session is told that too.
func refreshStaleHooks(refresh func(checkout, store string) (HookRefresh, error), loc resolve.Located) string {
	if refresh == nil {
		return ""
	}
	// Without a checkout there is no way to tell whether ethos is enabled, so
	// the session is told: a stale seal section would block its commits.
	if loc.CheckoutErr != nil {
		return hookRefreshSkipped("checkout not located: ", loc.CheckoutErr)
	}
	if loc.Checkout == "" {
		return ""
	}
	if storeFailure := loc.StoreFailure(); storeFailure != nil {
		return skippedHookNote(loc.Checkout, storeFailure)
	}
	if loc.Store == "" {
		return ""
	}
	r, err := refresh(loc.Checkout, loc.Store)
	for _, w := range r.Warnings {
		fmt.Fprintf(os.Stderr, "ethos: session-start: git hooks: %s\n", w)
	}
	var parts []string
	if len(r.Refreshed) > 0 {
		parts = append(parts, "refreshed stale git hook section(s) at session start: "+strings.Join(r.Refreshed, ", "))
	}
	if len(r.Installed) > 0 {
		parts = append(parts, "installed missing git hook(s) at session start: "+strings.Join(r.Installed, ", "))
	}
	if len(r.Chained) > 0 {
		parts = append(parts, "chained the ethos section into git hook(s): "+strings.Join(r.Chained, ", ")+" at session start")
	}
	var problems []string
	for _, h := range r.NonShell {
		problems = append(problems, fmt.Sprintf(
			"Ethos did not chain the seal section into %s: non-shell hook; run `ethos doctor`.", h))
	}
	for _, h := range r.Tracked {
		problems = append(problems, fmt.Sprintf(
			"Ethos did not rewrite the stale %s hook: core.hooksPath places it in a tracked file; run `ethos enable` to update it.", h))
	}
	for _, h := range r.Shared {
		problems = append(problems, fmt.Sprintf(
			"Ethos did not rewrite the stale %s hook: core.hooksPath is shared outside this repo; run `ethos enable`.", h))
	}
	for _, h := range r.TrackedMissing {
		problems = append(problems, fmt.Sprintf(
			"Ethos did not install the %s hook: core.hooksPath places it in the work tree; run `ethos enable` to install it.", h))
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: session-start: refreshing git hook sections: %v\n", err)
		fail := "could not refresh git hook section(s)"
		if len(parts) > 0 {
			parts = append(parts, fail+": "+err.Error())
		} else {
			problems = append(problems, fmt.Sprintf(
				"Ethos could not refresh stale git hook section(s): %v. Commits may be blocked; run `ethos enable` or `ethos doctor`.", err))
		}
	}
	var out []string
	if len(parts) > 0 {
		joined := strings.Join(parts, "; ")
		fmt.Fprintf(os.Stderr, "ethos: session-start: %s\n", joined)
		sentence := "Ethos " + joined + "."
		if err != nil {
			sentence += " Commits may be blocked; run `ethos enable` or `ethos doctor`."
		}
		out = append(out, sentence)
	}
	return strings.Join(append(out, problems...), " ")
}

// planSessionAgents resolves bundle skills using the same config that selected
// the persona, team and stores. Neither planning nor applying rereads config.
func planSessionAgents(loc resolve.Located, identities identity.IdentityStore, teams *team.LayeredStore, roles *role.LayeredStore) (AgentPlan, error) {
	if err := loc.Err(); err != nil {
		return AgentPlan{}, fmt.Errorf("locating agent generation inputs: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return AgentPlan{}, fmt.Errorf("locating global bundle root: %w", err)
	}
	_, active, err := bundle.ResolveRootFromConfig(loc.Store, filepath.Join(home, ".punt-labs", "ethos"), loc.Config)
	if err != nil {
		return AgentPlan{}, err
	}
	var skills []string
	if active != nil {
		skills = active.Manifest.DefaultSkills
	}
	return PlanGeneratedAgentFilesFromConfig(loc.Config, AgentProjectPatterns(loc.Checkout), skills, AgentIdentities(identities), teams, roles)
}

// resolveHumanIdentity loads the human caller's identity from git config
// or the OS user. It returns (nil, nil) when no identity matches — an absence,
// for which the roster falls back to the OS user — and (nil, err) when
// resolution or the load FAILED (git did not answer, a store would not read):
// a failure is never recorded as no identity, and never as the OS user's.
// Warnings are logged to stderr.
//
// Deliberately does not consult the session store (resolve.Resolve's step
// 1, an iam declaration): at SessionStart, this invocation is what WOULD
// create the session's pointer file and roster, so neither can exist yet
// for this exact call — a session-based lookup here is a structural,
// guaranteed miss, not a signal about the human's identity. Before
// DES-074 that miss was silent and free, so this call site always fell
// through to git/OS regardless; DES-074's fail-loud contract for
// "session was expected" made that miss cost a bounded retry (since we
// genuinely are running under Claude Code) and then propagate an error
// that this function caught and logged, without ever reaching git/OS —
// degrading every fresh session's greeting to the bare OS username
// instead of the git-configured identity (round 2, R2). Passing ss=nil
// to Resolve skips step 1 entirely and restores the pre-DES-074
// observable behavior (git/OS, immediately, no retry) without weakening
// DES-074 anywhere a session COULD exist.
func resolveHumanIdentity(store identity.IdentityStore, checkout string) (*identity.Identity, error) {
	store = identity.ForCaller(store)
	handle, err := resolve.Resolve(store, nil, checkout)
	if errors.Is(err, resolve.ErrNoIdentityMatch) {
		fmt.Fprintf(os.Stderr, "ethos: identity resolution: %v (using OS username)\n", err)
		return nil, nil
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: identity resolution failed: %v\n", err)
		return nil, err
	}
	id, err := store.Load(handle)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: failed to load identity %q: %v\n", handle, err)
		return nil, fmt.Errorf("loading identity %q: %w", handle, err)
	}
	for _, w := range id.Warnings {
		fmt.Fprintf(os.Stderr, "ethos: session-start: identity %q: %s\n", handle, w)
	}
	return id, nil
}

// purgeStale cleans stale PID files from previous sessions (fire-and-forget).
func purgeStale(ss *session.Store) {
	if purged, err := ss.PurgeCurrent(); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: session-start: failed to purge stale PID files: %v\n", err)
	} else if len(purged) > 0 {
		fmt.Fprintf(os.Stderr, "ethos: session-start: cleaned %d stale PID file(s)\n", len(purged))
	}
}

// createSessionRoster creates the session roster entry when a session ID
// is available. Logs errors to stderr without returning them — roster
// creation is not a blocking failure. It returns a notice naming every lookup
// that failed (the human identity, the repository, the checkout), for the
// session's additionalContext; "" when none did.
//
// A failure is never written as an answer: no persona is recorded for a human
// whose resolution failed (not the OS user's), and a repository lookup that
// failed records repo_unresolved with the reason in place of Repo. A checkout
// with no origin still records Repo "".
func createSessionRoster(ss *session.Store, sessionID string, resolvedID *identity.Identity, humanErr error, agentPersona string, loc resolve.Located) string {
	if sessionID == "" {
		return "the SessionStart payload carried no session_id, so no session roster was written"
	}
	var problems []string

	userID := os.Getenv("USER")
	if userID == "" {
		userID = "unknown"
	}
	userPersona := ""
	switch {
	case resolvedID != nil:
		userPersona = resolvedID.Handle
	case humanErr != nil:
		// The human could not be resolved: nothing about them is recorded, the
		// OS user's name included (it is how the roster would otherwise name
		// them, and a roster that failed to resolve must not claim an owner).
		userID = "unknown"
		problems = append(problems, fmt.Sprintf(
			"could not resolve the human identity (%v); no persona is recorded for the human", humanErr))
	default:
		userPersona = userID // no identity matched: the OS user is the fallback
	}
	claudePID := process.FindClaudePID()

	root := session.Participant{AgentID: userID, Persona: userPersona}
	primary := session.Participant{AgentID: claudePID, Persona: agentPersona, Parent: userID}

	checkout, coErr := loc.Checkout, loc.CheckoutErr
	storeErr := loc.StoreFailure()
	var repo string
	var repoErr error
	if coErr == nil && storeErr == nil {
		repo, repoErr = ResolveRepo(checkout)
	}
	host := ResolveHost()

	var create func() error
	if repoErr != nil || coErr != nil || storeErr != nil {
		var reasons []string
		for _, e := range []error{storeErr, repoErr, coErr} {
			// The same failure often arrives twice (the store and the checkout
			// share a .git): say it once.
			if e != nil && !slices.Contains(reasons, e.Error()) {
				reasons = append(reasons, e.Error())
			}
		}
		reason := strings.Join(reasons, "; ")
		problems = append(problems, "the session's repository could not be resolved ("+reason+
			"); its roster records repo_unresolved, and purge and the vacuum cross-check skip it")
		create = func() error {
			return ss.CreateUnresolved(sessionID, root, primary, reason, checkout, host)
		}
	} else {
		create = func() error { return ss.CreateInCheckout(sessionID, root, primary, repo, checkout, host) }
	}
	if err := create(); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: failed to create session roster: %v\n", err)
		problems = append(problems, fmt.Sprintf("the session roster could not be written (%v)", err))
	} else if err := ss.WriteCurrentSession(claudePID, sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: failed to write current session: %v\n", err)
		problems = append(problems, fmt.Sprintf("the session pointer could not be written (%v)", err))
	}
	return strings.Join(problems, "; ")
}

// emitHumanFallback emits a one-liner for the human identity when no
// agent persona is configured or agent loading fails, followed by note (why the
// repository store was not located) when there is one. Emits nothing when there
// is neither a human identity nor a note.
func emitHumanFallback(resolvedID *identity.Identity, note string) error {
	var msg string
	if resolvedID != nil {
		msg = fmt.Sprintf("Ethos session started. Active identity: %s (%s).", resolvedID.Name, resolvedID.Handle)
	}
	if note != "" {
		if msg != "" {
			msg += " "
		}
		msg += note
	}
	if msg == "" {
		return nil
	}
	result := SessionStartResult{SystemMessage: note}
	result.HookSpecificOutput.HookEventName = "SessionStart"
	result.HookSpecificOutput.AdditionalContext = msg
	return json.NewEncoder(os.Stdout).Encode(result)
}

// installStubAgentDefs installs the legacy stub agent definitions,
// subordinated to the DES-026 generator: a stub is copied only for a
// handle the generator does not own.
//
// Ownership must be KNOWN before anything is copied. A nil owned-set
// tells InstallAgentDefinitions to copy every stub, so there is no safe
// set to pass when the lookup itself failed — copying everything would
// put stubs over generator-owned files, the sticky-stub overwrite this
// subordination exists to prevent. A lookup error therefore installs
// nothing and says so on stderr. The generator writes the authoritative
// files right after; if it also fails, no stub is a better state than a
// stub that shadows a file the generator owns.
//
// A nil set from a SUCCESSFUL lookup is a different thing and still
// copies everything: it means the generator owns nothing — no team store
// wired, or no team configured — so the stubs are the only source for
// those agents.
func installStubAgentDefs(configRoot, ethosRoot string, identities identity.IdentityStore, teams *team.LayeredStore) {
	generated, err := GeneratedAgentHandles(configRoot, identities, teams)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: session-start: resolving generated agents: %v; installing no stubs\n", err)
		return
	}
	installAgentDefs(ethosRoot, generated)
}

// installAgentDefs copies agent definitions from the ethos agents dir
// into .claude/agents/. Logs results to stderr.
//
// Call it through installStubAgentDefs, which establishes that generated
// is a known ownership set and not the nil a failed lookup returns.
func installAgentDefs(ethosRoot string, generated map[string]bool) {
	if ethosRoot == "" {
		return
	}
	deployed, err := InstallAgentDefinitions(ethosRoot, generated)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: session-start: agent install failed: %v\n", err)
	}
	for _, name := range deployed {
		fmt.Fprintf(os.Stderr, "ethos: session-start: deployed agent definition %s\n", name)
	}
}

// installAgentDefsFor retains the source and destination selected at entry.
// Unlike the direct-call compatibility wrapper, it never rediscovers a root.
func installAgentDefsFor(loc resolve.Located, generated map[string]bool) error {
	if err := loc.Err(); err != nil {
		return fmt.Errorf("locating agent installation paths: %w", err)
	}
	if loc.Ethos == "" {
		return nil
	}
	files, err := PlanInstalledAgentDefinitions(loc.Ethos, generated)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	policy, err := loc.Layout()
	if err != nil {
		return err
	}
	return ApplyAgentPlan(policy, AgentPlan{Files: files})
}

// emitAgentContext builds and emits the full agent persona block with
// extensions, team context, and working context.
func emitAgentContext(agentID *identity.Identity, agentPersona string, store identity.IdentityStore, deps SessionStartDeps, checkout, notice string) error {
	var sections []string
	if persona := BuildPersonaBlock(agentID); persona != "" {
		sections = append(sections, persona)
	} else {
		sections = append(sections, fmt.Sprintf("Ethos session started. Active identity: %s (%s).", agentID.Name, agentID.Handle))
	}
	if extCtx := BuildExtensionContext(agentID.Ext); extCtx != "" {
		sections = append(sections, extCtx)
	}
	if teamCtx := BuildTeamSectionFor(*deps.Located, deps.Teams, deps.Roles, store, agentPersona); teamCtx != "" {
		sections = append(sections, teamCtx)
	}
	if workCtx := BuildWorkingContext(checkout); workCtx != "" {
		sections = append(sections, workCtx)
	}

	if notice != "" {
		sections = append(sections, notice)
	}

	result := SessionStartResult{SystemMessage: notice}
	result.HookSpecificOutput.HookEventName = "SessionStart"
	result.HookSpecificOutput.AdditionalContext = strings.Join(sections, "\n\n")
	return json.NewEncoder(os.Stdout).Encode(result)
}

// ResolveRepo is the org/name identity of the checkout's origin remote — the
// same value the SessionStart hook records for a roster, read with
// `git -C <checkout>` as audit.RepoIdentity does, so a session started from a
// directory outside the repository still records its repository's origin.
// Exported so `ethos session start` resolves repo identically. It returns
// ("", nil) for a checkout with no origin, or no checkout; any other failure —
// a timeout, dubious ownership, git not installed — is the error, and the
// caller records repo_unresolved, never "".
func ResolveRepo(checkout string) (string, error) {
	return audit.RepoIdentity(checkout)
}

// ResolveHost returns the short hostname (no domain) — the same value the
// SessionStart hook records for a roster. Exported so `ethos session start`
// resolves host identically.
func ResolveHost() string {
	name, err := os.Hostname()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: could not resolve hostname: %v\n", err)
		return ""
	}
	if i := strings.IndexByte(name, '.'); i >= 0 {
		name = name[:i]
	}
	return name
}
