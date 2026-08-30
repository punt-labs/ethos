package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	resolvedID := resolveHumanIdentity(store, ss)
	purgeStale(ss)

	// Resolve agent persona from repo config. A non-nil error means
	// the config file exists but cannot be read or parsed — a loud
	// failure mode the user needs to see. Propagate fail-closed, same
	// pattern as the GenerateAgentFiles wrap below (ethos-9ai.6 C1).
	// The shell wrapper's `|| true` keeps Claude Code session startup
	// fail-open (cli.md §Hook Architecture); the non-zero exit code is
	// the signal for direct CLI invocation and `ethos doctor`.
	repoRoot := resolve.StoreRepoRoot()
	agentPersona, err := resolve.ResolveAgent(repoRoot)
	if err != nil {
		return fmt.Errorf("resolving agent: %w", err)
	}

	createSessionRoster(ss, sessionID, resolvedID, agentPersona)

	// Emit the agent's persona block (not the human's).
	// The agent persona comes from repo config (.punt-labs/ethos.yaml).
	if agentPersona == "" {
		return emitHumanFallback(resolvedID)
	}

	agentID, agentLoadErr := store.Load(agentPersona)
	if agentLoadErr != nil {
		fmt.Fprintf(os.Stderr, "ethos: session-start: failed to load agent identity %q: %v\n", agentPersona, agentLoadErr)
		return emitHumanFallback(resolvedID)
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

	installStubAgentDefs(repoRoot, store, deps.Teams)

	// Generate .claude/agents/<handle>.md from ethos identity data.
	// Propagates: the returned error is the single authoritative
	// signal the CLI can gate on. The shell wrapper's `|| true` keeps
	// Claude Code session startup fail-open (cli.md §Hook Architecture)
	// so a broken config does not brick sessions, but `ethos hook
	// session-start` invoked directly exits non-zero — useful for
	// `ethos doctor` and manual debugging.
	//
	// Two roots: the config/team selection resolves from the shared store
	// (repoRoot = StoreRepoRoot), while the agent files are written to the
	// CURRENT checkout (EnvRepoRoot) — the tree the worktree's Claude reads
	// and where InstallAgentDefinitions writes. A single root would generate
	// a worktree's agents into the main tree, invisible to that worktree
	// (Bugbot HIGH on PR #370).
	checkoutRoot := resolve.EnvRepoRoot()
	if repoRoot != "" && checkoutRoot != "" && deps.Teams != nil && deps.Roles != nil {
		if genErr := GenerateAgentFilesTo(repoRoot, checkoutRoot, store, deps.Teams, deps.Roles); genErr != nil {
			return fmt.Errorf("generating agents: %w", genErr)
		}
	}

	return emitAgentContext(agentID, agentPersona, store, deps)
}

// resolveHumanIdentity loads the human caller's identity. Returns nil
// on any resolution or load failure (warnings are logged to stderr).
func resolveHumanIdentity(store identity.IdentityStore, ss *session.Store) *identity.Identity {
	handle, err := resolve.Resolve(store, ss)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: identity resolution failed: %v (using OS username)\n", err)
		return nil
	}
	id, err := store.Load(handle)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: failed to load identity %q: %v\n", handle, err)
		return nil
	}
	for _, w := range id.Warnings {
		fmt.Fprintf(os.Stderr, "ethos: session-start: identity %q: %s\n", handle, w)
	}
	return id
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
// creation is not a blocking failure.
func createSessionRoster(ss *session.Store, sessionID string, resolvedID *identity.Identity, agentPersona string) {
	if sessionID == "" {
		return
	}

	userID := os.Getenv("USER")
	if userID == "" {
		userID = "unknown"
	}
	userPersona := ""
	if resolvedID != nil {
		userPersona = resolvedID.Handle
	}
	if userPersona == "" {
		userPersona = userID
	}
	claudePID := process.FindClaudePID()

	root := session.Participant{AgentID: userID, Persona: userPersona}
	primary := session.Participant{AgentID: claudePID, Persona: agentPersona, Parent: userID}

	repo := ResolveRepo()
	checkout := ResolveCheckout()
	host := ResolveHost()

	if err := ss.CreateInCheckout(sessionID, root, primary, repo, checkout, host); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: failed to create session roster: %v\n", err)
	} else if err := ss.WriteCurrentSession(claudePID, sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: failed to write current session: %v\n", err)
	}
}

// emitHumanFallback emits a one-liner for the human identity when no
// agent persona is configured or agent loading fails. Returns nil when
// no human identity is available either.
func emitHumanFallback(resolvedID *identity.Identity) error {
	if resolvedID == nil {
		return nil
	}
	msg := fmt.Sprintf("Ethos session started. Active identity: %s (%s).", resolvedID.Name, resolvedID.Handle)
	result := SessionStartResult{}
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
func installStubAgentDefs(configRoot string, identities identity.IdentityStore, teams *team.LayeredStore) {
	generated, err := GeneratedAgentHandles(configRoot, identities, teams)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: session-start: resolving generated agents: %v; installing no stubs\n", err)
		return
	}
	installAgentDefs(generated)
}

// installAgentDefs copies agent definitions from the ethos agents dir
// into .claude/agents/. Logs results to stderr.
//
// Call it through installStubAgentDefs, which establishes that generated
// is a known ownership set and not the nil a failed lookup returns.
func installAgentDefs(generated map[string]bool) {
	ethosRoot := resolve.FindRepoEthosRoot()
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
func emitAgentContext(agentID *identity.Identity, agentPersona string, store identity.IdentityStore, deps SessionStartDeps) error {
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
	if workCtx := BuildWorkingContext(); workCtx != "" {
		sections = append(sections, workCtx)
	}

	result := SessionStartResult{}
	result.HookSpecificOutput.HookEventName = "SessionStart"
	result.HookSpecificOutput.AdditionalContext = strings.Join(sections, "\n\n")
	return json.NewEncoder(os.Stdout).Encode(result)
}

// ResolveRepo extracts org/name from the git remote of the working
// directory — the same value the SessionStart hook records for a roster.
// Exported so `ethos session start` resolves repo identically. Empty when
// unresolvable.
func ResolveRepo() string {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: could not resolve repo from git remote: %v\n", err)
		return ""
	}
	return audit.ParseGitRemote(string(out))
}

// ResolveCheckout returns the work-tree root whose machine-local live audit
// zone this session will write to — the same value the SessionStart hook
// records for a roster. Empty outside a repo. Exported so `ethos session start`
// resolves it identically.
//
// It calls the resolver the live-write path itself uses (cmd/ethos/hook.go's
// audit-log entry point resolves repoRoot the same way), so the recorded path
// and the path the live file lands under agree by construction rather than by
// two walks that could disagree. In a linked worktree that is the WORKTREE
// root, which is right: the live zone belongs to the checkout, not the shared
// store.
func ResolveCheckout() string {
	return resolve.EnvRepoRoot()
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
