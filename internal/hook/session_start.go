package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
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
	Store    identity.IdentityStore
	Sessions *session.Store
	Teams    *team.LayeredStore
	Roles    *role.LayeredStore
	// Issue is why the repository's bundle or policy could not be honoured, if
	// so (bundle.ConfigIssue): the session is told in its systemMessage.
	Issue error
}

// HandleSessionStart reads the SessionStart hook payload from stdin,
// resolves identity, creates a session roster, and emits context.
func HandleSessionStart(r io.Reader, deps SessionStartDeps) error {
	if deps.Store == nil || deps.Sessions == nil {
		return fmt.Errorf("session-start: Store and Sessions stores are required")
	}

	store := deps.Store
	ss := deps.Sessions
	input, err := ReadInput(r, time.Second)
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
	loc := resolve.Roots()
	storeFailure := loc.StoreFailure()
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
	if issueNote := storeUnlocatedNote(deps.Issue); issueNote != "" {
		if note != "" {
			note += " "
		}
		note += issueNote
	}
	if note != "" {
		fmt.Fprintf(os.Stderr, "ethos: session-start: %s\n", note)
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

	installStubAgentDefs(repoRoot, loc.Ethos, store, deps.Teams)

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
	if repoRoot != "" && checkoutRoot != "" && deps.Teams != nil && deps.Roles != nil {
		if genErr := GenerateAgentFilesTo(repoRoot, checkoutRoot, store, deps.Teams, deps.Roles); genErr != nil {
			return fmt.Errorf("generating agents: %w", genErr)
		}
	}

	return emitAgentContext(agentID, agentPersona, store, deps, loc.Checkout, note)
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
	if teamCtx := BuildTeamSection(deps.Teams, deps.Roles, store, agentPersona); teamCtx != "" {
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
