package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/mission"
	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/session"
)

// verifierMission pairs a parsed contract with the raw on-disk bytes
// that produced it. checkVerifierHash reads the contract once;
// renderVerifierBlock uses the same bytes for the isolation block,
// eliminating the TOCTOU window a second os.ReadFile would open.
type verifierMission struct {
	Contract *mission.Contract
	RawYAML  []byte
}

// SubagentStartResult is the JSON output of the subagent-start hook.
type SubagentStartResult struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext,omitempty"`
	} `json:"hookSpecificOutput"`
	// Env is an optional map of environment variables that Claude Code
	// sets in the spawned subagent's process. Used by verifier isolation
	// to pass ETHOS_VERIFIER_ALLOWLIST to the subagent's PreToolUse hooks.
	Env map[string]string `json:"env,omitempty"`
}

// SubagentStartDeps groups the dependencies HandleSubagentStartWithDeps
// needs. The legacy HandleSubagentStart entry point builds an empty
// deps struct and delegates here so existing callers and tests keep
// compiling without an extra plumbing pass.
//
// Identities and Sessions are required. Missions and HashSources are
// optional: if either is nil the verifier hash check is skipped (the
// installation has no mission store, the hook still emits the persona
// block as before).
type SubagentStartDeps struct {
	Identities identity.IdentityStore
	Sessions   *session.Store
	Missions   *mission.Store
	// Hash is the source bundle ComputeEvaluatorHash uses to recompute
	// the live evaluator content. Required when Missions is non-nil
	// and Phase 3.3 verifier discipline is in effect.
	Hash mission.HashSources
	// RepoRoot is the repository root directory, used by the verifier
	// isolation block to resolve write_set entries to concrete files
	// on disk via WalkWriteSet. Empty means the walk is skipped.
	RepoRoot string
}

// HandleSubagentStart reads the SubagentStart hook payload from stdin,
// joins the subagent to the session roster, and emits persona context
// if the subagent matches an ethos identity. This is the legacy entry
// point — it skips the Phase 3.3 verifier hash check.
//
// New code should call HandleSubagentStartWithDeps so the verifier
// hash gate runs. The legacy signature is preserved so existing
// callers and tests in the hook package continue to compile.
func HandleSubagentStart(r io.Reader, store identity.IdentityStore, ss *session.Store) error {
	return HandleSubagentStartWithDeps(r, SubagentStartDeps{
		Identities: store,
		Sessions:   ss,
	})
}

// HandleSubagentStartWithDeps is the full subagent-start handler. In
// addition to the persona block emission HandleSubagentStart provides,
// it enforces DES-033's frozen-evaluator gate: when the spawning
// subagent matches the evaluator handle of any open mission, the
// hook recomputes the evaluator's current hash and refuses the spawn
// if any open mission's pinned hash disagrees.
//
// The mismatch error names the offending mission, the pinned and
// current hash prefixes, and the relaunch instruction the operator
// needs to recover. Hash success is silent — operators only see
// the hash when something is wrong.
func HandleSubagentStartWithDeps(r io.Reader, deps SubagentStartDeps) error {
	input, err := ReadInput(r, time.Second)
	if err != nil {
		return fmt.Errorf("subagent-start: %w", err)
	}

	agentID, _ := input["agent_id"].(string)
	agentType, _ := input["agent_type"].(string)
	sessionID, _ := input["session_id"].(string)

	if agentID == "" || sessionID == "" {
		return nil
	}

	// Phase 3.3: verifier hash gate. Run BEFORE joining the session
	// roster — a refused spawn must leave no trace in the roster, and
	// the operator's diagnostic must be the hash mismatch, not a
	// confusing post-join failure. The check is a no-op when the
	// installation has no mission store wired in (legacy hook flow).
	//
	// Phase 3.5: the hash gate also returns the set of open missions
	// that name this agentType as evaluator. A non-empty list is the
	// single source of truth for "is this a verifier spawn?" — the
	// context-isolation path below and the hash gate both consume it
	// without re-scanning the mission store.
	verifierMissions, err := checkVerifierHash(agentType, deps)
	if err != nil {
		// Return a non-nil error so cmd/ethos/hook.go's runner exits
		// non-zero, which Claude Code surfaces to the operator as a
		// fatal subagent launch failure. The error string carries
		// the diagnostic; the runner prints it verbatim.
		return err
	}

	// Resolve persona: if an identity exists with the same handle as
	// agent_type, use it as the persona.
	persona := ""
	if agentType != "" && deps.Identities.Exists(agentType) {
		persona = agentType
	}

	p := session.Participant{
		AgentID:   agentID,
		Persona:   persona,
		Parent:    process.FindClaudePID(),
		AgentType: agentType,
	}

	if joinErr := deps.Sessions.Join(sessionID, p); joinErr != nil {
		fmt.Fprintf(os.Stderr, "ethos: failed to join session %s: %v\n", sessionID, joinErr)
	}

	// Phase 3.5: verifier context isolation. When at least one open
	// mission names this agentType as its evaluator, REPLACE the
	// normal persona/extension injection with an isolation block
	// containing only the mission contract (byte-for-byte from the
	// single read checkVerifierHash already performed), the file
	// allowlist derived from the write_set, the explicit verification
	// criteria, and the file-level delta. Parent transcript, worker
	// scratch, and prior reasoning are excluded by virtue of never
	// being added.
	//
	// The verifier spawn's agent definition is loaded by Claude Code
	// from the agent's `.md` file on disk — the hook does not touch
	// that. The isolation block is additionalContext on top of that
	// agent definition.
	if len(verifierMissions) > 0 {
		block, blockErr := buildVerifierIsolationBlock(verifierMissions, deps.Missions, deps.RepoRoot)
		if blockErr != nil {
			// Refuse the spawn rather than silently fall through to
			// the normal persona path: a verifier with the wrong
			// context is exactly the bug Phase 3.5 exists to prevent.
			return fmt.Errorf("verifier context isolation: %w", blockErr)
		}
		result := SubagentStartResult{}
		result.HookSpecificOutput.HookEventName = "SubagentStart"
		result.HookSpecificOutput.AdditionalContext = block
		// Set ETHOS_VERIFIER_ALLOWLIST so PreToolUse hooks in the
		// subagent can enforce the file allowlist mechanically.
		result.Env = buildVerifierAllowlistEnv(verifierMissions, deps.Missions)
		return json.NewEncoder(os.Stdout).Encode(result)
	}

	// If no persona matched, nothing more to do.
	if persona == "" {
		return nil
	}

	// Load identity with full attribute content for persona injection.
	id, err := deps.Identities.Load(persona)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: subagent-start: identity %q exists but attribute resolution failed: %v\n", persona, err)
		return nil
	}
	for _, w := range id.Warnings {
		fmt.Fprintf(os.Stderr, "ethos: subagent-start: identity %q: %s\n", persona, w)
	}

	var sections []string

	block := BuildPersonaBlock(id)
	if block != "" {
		// Prepend parent context if we can resolve the parent from the roster.
		parentLine := resolveParentLine(deps.Sessions, sessionID, p.Parent, deps.Identities)
		if parentLine != "" {
			block = insertAfterFirstLine(block, parentLine)
		}
		sections = append(sections, block)
	}

	if extCtx := BuildExtensionContext(id.Ext); extCtx != "" {
		sections = append(sections, extCtx)
	}

	if len(sections) == 0 {
		return nil
	}

	result := SubagentStartResult{}
	result.HookSpecificOutput.HookEventName = "SubagentStart"
	result.HookSpecificOutput.AdditionalContext = strings.Join(sections, "\n\n")
	return json.NewEncoder(os.Stdout).Encode(result)
}

// buildVerifierIsolationBlock renders the additionalContext injected
// into a verifier subagent spawn. One mission produces one block;
// multiple missions (the rare case where one agent is evaluator for
// several concurrent missions) concatenate with a clear separator.
//
// The block shape is deliberately narrow — exactly the four
// invariants Phase 3.5 promises the verifier subagent will see:
//
//  1. "You are the frozen verifier …" opener anchored to the
//     evaluator handle and mission ID — the verifier knows its role
//     and its scope on the first line.
//  2. The mission contract YAML, byte-for-byte from the single read
//     performed by checkVerifierHash. The hook does not re-marshal
//     the Contract struct or re-read the file; a re-marshal could
//     produce different bytes than the originally pinned contract
//     (key reordering, comment loss), and a second read opens a
//     TOCTOU window. The raw bytes from checkVerifierHash are the
//     one true source.
//  3. Success criteria repeated explicitly, so the verifier's
//     verdict cannot "drift" to a different rubric than the one
//     pinned at launch.
//  4. The file allowlist — the write_set plus the contract file
//     itself. The verifier may read any repo file for context, but
//     Write/Edit outside this list is blocked by the PreToolUse hook.
//
// The block explicitly says what the verifier must NOT do:
// write outside the allowlist, read the parent transcript or worker
// scratch state, or trust prior reasoning from the worker. Prose
// reinforces the mechanical enforcement (PreToolUse hook blocks
// Write/Edit outside the allowlist) and carries the contract into
// the verifier's first prompt so the intent is unambiguous.
//
// Returns an error if the contract file is missing or unreadable;
// the caller refuses the spawn. A successful build always returns
// non-empty bytes.
func buildVerifierIsolationBlock(missions []verifierMission, store *mission.Store, repoRoot string) (string, error) {
	if len(missions) == 0 {
		return "", fmt.Errorf("no verifier missions")
	}
	if store == nil {
		return "", fmt.Errorf("mission store is nil")
	}
	var blocks []string
	for _, vm := range missions {
		body, err := renderVerifierBlock(vm, store, repoRoot)
		if err != nil {
			return "", err
		}
		blocks = append(blocks, body)
	}
	return strings.Join(blocks, "\n\n---\n\n"), nil
}

// renderVerifierBlock renders one mission's verifier isolation block.
// Factored out of buildVerifierIsolationBlock so the per-mission
// render logic is unit-testable without a multi-mission harness.
//
// The contract bytes come from the verifierMission struct, which was
// populated by checkVerifierHash's single read. This eliminates the
// TOCTOU window a second os.ReadFile would open: the bytes used for
// hash verification are the same bytes rendered into the isolation
// block.
func renderVerifierBlock(vm verifierMission, store *mission.Store, repoRoot string) (string, error) {
	m := vm.Contract
	if m == nil {
		return "", fmt.Errorf("mission contract is nil")
	}
	contractBytes := vm.RawYAML
	if len(contractBytes) == 0 {
		return "", fmt.Errorf("contract %q has empty raw YAML", m.MissionID)
	}

	repoAllowlist, absAllowlist := verifierAllowlistSplit(m, store)

	var b strings.Builder
	// H2 header for the block root and H3 for its sub-sections. The
	// host prompt already uses H1 and H2 for its own structure (the
	// persona block uses ## Personality / ## Writing Style / ## Talents);
	// an H1 here would collide with the host and produce a broken
	// outline. The block's per-mission separator is an HR written by
	// the caller, not another header level.
	fmt.Fprintf(&b, "## Verifier context (mission %s)\n\n", m.MissionID)
	fmt.Fprintf(&b, "You are the frozen verifier %q for mission %s.\n",
		m.Evaluator.Handle, m.MissionID)
	b.WriteString("\n")
	b.WriteString("You operate under Phase 3.5 context isolation:\n")
	b.WriteString("  - You may read any file in the repo to understand context.\n")
	b.WriteString("  - You MUST NOT write or edit any file outside the allowlist below.\n")
	b.WriteString("  - You MUST NOT read the worker's scratch state or the parent transcript.\n")
	b.WriteString("  - Your verdict is scored against the success criteria pinned in the contract, not against any rubric you invent.\n")
	b.WriteString("\n")

	b.WriteString("### Mission contract (byte-for-byte from disk)\n\n")
	b.WriteString("```yaml\n")
	b.Write(contractBytes)
	if len(contractBytes) == 0 || contractBytes[len(contractBytes)-1] != '\n' {
		b.WriteString("\n")
	}
	b.WriteString("```\n\n")

	b.WriteString("### Verification criteria\n\n")
	// Contract.Validate() refuses an empty SuccessCriteria, so this
	// loop is always non-empty — the store never persists a contract
	// that would render a blank criteria section. The defensive
	// "no criteria" branch that used to live here would be silently
	// incorrect if Validate ever regressed; leave the loud nothing
	// instead of prose that says "refuse the spawn" without refusing.
	for _, sc := range m.SuccessCriteria {
		fmt.Fprintf(&b, "  - %s\n", sc)
	}
	b.WriteString("\n")

	b.WriteString("### File allowlist\n\n")
	b.WriteString("These are the only paths the verifier may read:\n\n")
	// Split by path kind so the operator can see at a glance which
	// entries resolve from the repo root and which are absolute. The
	// write_set is repo-relative per the per-entry validator in
	// validate.go; the contract file lives at an absolute path under
	// the mission store.
	if len(repoAllowlist) > 0 {
		b.WriteString("Repo-relative paths (resolve from repo root):\n")
		for _, entry := range repoAllowlist {
			fmt.Fprintf(&b, "  - %s\n", entry)
		}
		b.WriteString("\n")
	}
	if len(absAllowlist) > 0 {
		b.WriteString("Absolute paths:\n")
		for _, entry := range absAllowlist {
			fmt.Fprintf(&b, "  - %s\n", entry)
		}
		b.WriteString("\n")
	}
	b.WriteString("Any Read, Grep, or Glob against a path outside this list must be\n")
	b.WriteString("refused as out-of-scope for this verification pass.\n")

	// Walk the write_set to concrete files on disk so the verifier
	// sees exactly which files exist, not just the static entries.
	if repoRoot != "" {
		walked, walkErr := mission.WalkWriteSet(repoRoot, m.WriteSet)
		if walkErr != nil {
			// Log the walk error but do not fail the spawn; the
			// static allowlist above is sufficient for verification.
			fmt.Fprintf(os.Stderr, "ethos: subagent-start: walk write_set for %s: %v\n", m.MissionID, walkErr)
		} else if len(walked) > 0 {
			b.WriteString("\n### Concrete files on disk\n\n")
			b.WriteString("The write_set entries resolve to these files:\n\n")
			for _, f := range walked {
				fmt.Fprintf(&b, "  - %s\n", f)
			}
		}
	}

	return b.String(), nil
}

// verifierAllowlist returns the ordered file-access allowlist for a
// verifier subagent. Derived from the mission contract's write_set
// plus the contract file itself. The allowlist is what the verifier
// sees in its injection and (in Phase 3.5+) what a PreToolUse hook
// enforces against tool calls.
//
// Order: write_set entries first (in declaration order — the
// operator wrote them in that order for a reason), followed by the
// contract file path. Duplicates are dropped so a contract that
// accidentally lists the contract file in its write_set does not
// produce a double entry.
//
// The contract path is an absolute filesystem path (as returned by
// Store.ContractPath) so a verifier that resolves paths against
// the repo root still finds it. Write_set entries are relative
// per the per-entry validator in validate.go — the verifier's
// working directory is the repo root.
func verifierAllowlist(m *mission.Contract, store *mission.Store) []string {
	repo, abs := verifierAllowlistSplit(m, store)
	out := make([]string, 0, len(repo)+len(abs))
	out = append(out, repo...)
	out = append(out, abs...)
	return out
}

// verifierAllowlistSplit returns the same allowlist as verifierAllowlist
// but split into two slices: repo-relative entries (the write_set) and
// absolute entries (the contract file). The renderer uses the split
// form so the operator can see which paths resolve from the repo root
// and which are anchored on the filesystem; the flat form is kept for
// the deduplication and ordering unit tests, which only care about the
// final list shape.
//
// Deduplication matches the original helper: an entry that appears in
// both the write_set and the contract file path is emitted once,
// under the section its first-seen occurrence lives in.
func verifierAllowlistSplit(m *mission.Contract, store *mission.Store) (repo, abs []string) {
	seen := make(map[string]struct{})
	for _, entry := range m.WriteSet {
		if entry == "" {
			continue
		}
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		repo = append(repo, entry)
	}
	if store != nil {
		contractPath := store.ContractPath(m.MissionID)
		if _, ok := seen[contractPath]; !ok {
			seen[contractPath] = struct{}{}
			abs = append(abs, contractPath)
		}
	}
	return repo, abs
}

// buildVerifierAllowlistEnv returns the env map for a verifier
// subagent. The ETHOS_VERIFIER_ALLOWLIST value is a colon-separated
// list of all allowed paths across all verifier missions. PreToolUse
// reads this env var and blocks tool calls targeting paths outside it.
func buildVerifierAllowlistEnv(missions []verifierMission, store *mission.Store) map[string]string {
	seen := make(map[string]struct{})
	var entries []string
	for _, vm := range missions {
		for _, p := range verifierAllowlist(vm.Contract, store) {
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			entries = append(entries, p)
		}
	}
	if len(entries) == 0 {
		return nil
	}
	return map[string]string{
		"ETHOS_VERIFIER_ALLOWLIST": strings.Join(entries, ":"),
	}
}

// checkVerifierHash recomputes the evaluator hash for every open
// mission whose Evaluator.Handle matches agentType and returns a
// single fatal error aggregating every mismatch.
//
// The check is intentionally conservative: a single drifted mission
// blocks the spawn even if other missions still match. This protects
// the per-mission verdict integrity DES-033 was written to enforce —
// silently allowing the spawn against a stale pinned hash would
// invalidate the original mission's launch contract.
//
// Returns (nil, nil) and is a no-op when:
//   - Missions is nil (legacy install, no mission store)
//   - agentType is empty
//   - No open mission names agentType as evaluator
//
// Returns (matching open missions, nil) when every matching open
// mission is either legacy (empty pinned hash) or has current content
// matching its pinned hash. The returned slice is what Phase 3.5's
// context-isolation path uses to build the verifier's injection
// block — a non-empty slice is the single source of truth for "this
// IS a verifier spawn".
//
// Returns (nil, fatal error) when:
//   - deps.Hash is misconfigured (Missions is non-nil but HashSources
//     is incomplete). Silent skip would let stale evaluator content
//     through under a configuration error.
//   - A matching mission fails to load (corrupt or unparseable file).
//   - The current hash recomputation itself fails.
//   - Any matching open mission's pinned hash does not equal the
//     recomputed current hash.
//
// On one or more real mismatches the error is a multi-line block
// naming every drifted mission, the pinned and current rollup hash
// prefixes, the per-section hashes of the CURRENT content so the
// operator can cross-reference which file they edited, and two
// recovery options (revert the edit to preserve the missions, or
// close and relaunch to accept the new content).
func checkVerifierHash(agentType string, deps SubagentStartDeps) ([]verifierMission, error) {
	if deps.Missions == nil {
		return nil, nil // legacy install: no mission store
	}
	if err := deps.Hash.Validate(); err != nil {
		// Misconfiguration: a mission store is present but the hash
		// sources are not. Refusing spawns on misconfiguration is
		// the safe default — silently skipping the gate would let
		// stale evaluator content through.
		return nil, fmt.Errorf("verifier hash gate misconfigured: %w", err)
	}
	if strings.TrimSpace(agentType) == "" {
		return nil, nil
	}

	ids, err := deps.Missions.List()
	if err != nil {
		return nil, fmt.Errorf("verifier hash gate: listing missions: %w", err)
	}

	// Breakdown is computed at most once per checkVerifierHash call,
	// lazily, on the first NON-LEGACY matching open mission. Legacy
	// missions (empty pinned hash) must never trigger the compute:
	// they cannot match any recomputed hash and the compute itself
	// might fail (e.g. the evaluator's identity content was removed
	// after the legacy mission was launched), which would wrongly
	// block the spawn. The legacy check is therefore the first
	// filter after status and handle match.
	var (
		breakdown        mission.EvaluatorHashBreakdown
		breakdownLoaded  bool
		mismatches       []driftedMission
		verifierMissions []verifierMission
	)

	for _, id := range ids {
		// Single read: rejectSymlink + ReadFile + decode from the
		// same bytes. Eliminates the TOCTOU window that two separate
		// reads (Store.Load + os.ReadFile) would open.
		// Inline symlink rejection — mirrors mission.rejectSymlink
		// (unexported, different package). Same Lstat-before-Read
		// TOCTOU gap as rejectSymlink; see ethos-jjm for context.
		path := deps.Missions.ContractPath(id)
		if info, lErr := os.Lstat(path); lErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf(
					"verifier hash gate: refusing to follow symlink: %s", path,
				)
			}
		} else if !errors.Is(lErr, fs.ErrNotExist) {
			return nil, fmt.Errorf(
				"verifier hash gate: lstat %s: %w", path, lErr,
			)
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				return nil, fmt.Errorf(
					"verifier hash gate: mission %q not found", id,
				)
			}
			return nil, fmt.Errorf(
				"verifier hash gate: reading mission %q: %w", id, readErr,
			)
		}
		c, decErr := mission.DecodeContractStrict(raw, id)
		if decErr != nil {
			return nil, fmt.Errorf(
				"verifier hash gate: failed to load mission %q: %w",
				id, decErr,
			)
		}
		// Match Store.Load's default-fill for pre-3.4 contracts.
		if c.CurrentRound == 0 {
			c.CurrentRound = 1
		}
		if vErr := c.Validate(); vErr != nil {
			return nil, fmt.Errorf(
				"verifier hash gate: contract %q failed validation: %w",
				id, vErr,
			)
		}
		if c.MissionID != id {
			return nil, fmt.Errorf(
				"verifier hash gate: contract filename %q does not match mission_id %q",
				id, c.MissionID,
			)
		}
		if c.Status != mission.StatusOpen {
			continue
		}
		if c.Evaluator.Handle != agentType {
			continue
		}
		verifierMissions = append(verifierMissions, verifierMission{
			Contract: c,
			RawYAML:  raw,
		})
		if c.Evaluator.Hash == "" {
			// Pre-3.3 mission with an empty pinned hash. Warn and
			// continue; do not attempt to recompute the current
			// hash. A legacy mission can never match a recomputed
			// hash, and the recompute itself may fail against
			// content that was valid at launch time but no longer
			// resolves — which would wrongly refuse every spawn.
			// The mission's launch predates the gate.
			fmt.Fprintf(os.Stderr,
				"ethos: subagent-start: warning: mission %s has empty Evaluator.Hash (pre-3.3); skipping gate\n",
				c.MissionID,
			)
			continue
		}
		if !breakdownLoaded {
			breakdown, err = mission.ComputeEvaluatorHashBreakdown(c.Evaluator.Handle, deps.Hash)
			if err != nil {
				return nil, fmt.Errorf(
					"verifier hash gate: recomputing hash for evaluator %q: %w",
					c.Evaluator.Handle, err,
				)
			}
			breakdownLoaded = true
		}
		if c.Evaluator.Hash != breakdown.Rollup {
			mismatches = append(mismatches, driftedMission{
				ID:     c.MissionID,
				Pinned: c.Evaluator.Hash,
			})
		}
	}
	if len(mismatches) == 0 {
		return verifierMissions, nil
	}
	return nil, errors.New(formatDriftError(agentType, breakdown, mismatches))
}

// driftedMission is an internal record collected during checkVerifierHash
// and consumed by formatDriftError. Pinned is the full hex the
// formatter truncates; the mission ID is rendered verbatim.
type driftedMission struct {
	ID     string
	Pinned string
}

// formatDriftError renders the operator-facing multi-line block the
// verifier gate emits when one or more open missions disagree with
// the current evaluator content.
//
// The block has four parts:
//  1. A summary line naming the evaluator and the mission count.
//  2. One line per drifted mission showing pinned → current rollup.
//  3. The per-section breakdown of the CURRENT content so the
//     operator can cross-reference which source file they edited.
//  4. Two recovery options — revert the edit, or close and relaunch.
//
// Mission lines are sorted by mission ID so two runs of the gate
// against the same drifted set produce identical output.
func formatDriftError(
	evaluator string,
	breakdown mission.EvaluatorHashBreakdown,
	mismatches []driftedMission,
) string {
	sort.Slice(mismatches, func(i, j int) bool {
		return mismatches[i].ID < mismatches[j].ID
	})

	var b strings.Builder
	if len(mismatches) == 1 {
		fmt.Fprintf(&b,
			"refusing verifier spawn: evaluator %q content has drifted since mission %s was launched\n",
			evaluator, mismatches[0].ID,
		)
	} else {
		fmt.Fprintf(&b,
			"refusing verifier spawn: evaluator %q content has drifted since %d open missions were launched\n",
			evaluator, len(mismatches),
		)
	}
	for _, m := range mismatches {
		fmt.Fprintf(&b, "  %s: pinned %s -> current %s\n",
			m.ID, hashPrefix(m.Pinned), hashPrefix(breakdown.Rollup),
		)
	}
	b.WriteString("  current content sections (check which you edited):\n")
	fmt.Fprintf(&b, "    personality:       %s\n", hashPrefix(breakdown.Personality))
	fmt.Fprintf(&b, "    writing_style:     %s\n", hashPrefix(breakdown.WritingStyle))
	// Render talents and roles in sorted order for determinism.
	talentSlugs := make([]string, 0, len(breakdown.Talents))
	for slug := range breakdown.Talents {
		talentSlugs = append(talentSlugs, slug)
	}
	sort.Strings(talentSlugs)
	for _, slug := range talentSlugs {
		fmt.Fprintf(&b, "    talent %-12s %s\n",
			fmt.Sprintf("%q:", slug), hashPrefix(breakdown.Talents[slug]),
		)
	}
	roleNames := make([]string, 0, len(breakdown.Roles))
	for name := range breakdown.Roles {
		roleNames = append(roleNames, name)
	}
	sort.Strings(roleNames)
	for _, name := range roleNames {
		fmt.Fprintf(&b, "    role %-14s %s\n",
			fmt.Sprintf("%q:", name), hashPrefix(breakdown.Roles[name]),
		)
	}
	b.WriteString("  to preserve these missions: revert the edit to the evaluator's identity content\n")
	if len(mismatches) == 1 {
		fmt.Fprintf(&b,
			"  to accept the new content: close mission %s and relaunch it",
			mismatches[0].ID,
		)
	} else {
		b.WriteString("  to accept the new content: close the listed missions and relaunch them")
	}
	return b.String()
}

// hashPrefix returns the first 12 hex characters of a hash string,
// or the full string if shorter. Used in operator-facing error
// messages so the line stays readable while still distinguishing
// hashes for follow-up debugging.
func hashPrefix(h string) string {
	const n = 12
	if len(h) <= n {
		return h
	}
	return h[:n]
}

// resolveParentLine finds the primary Claude agent in the session roster
// and returns a "You report to Name (handle)." line. The primary agent
// is identified as the participant whose AgentID matches the subagent's
// Parent field (the Claude PID). Returns "" if the parent cannot be resolved.
func resolveParentLine(ss *session.Store, sessionID, parentID string, store identity.IdentityStore) string {
	if parentID == "" {
		return ""
	}
	roster, err := ss.Load(sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: subagent-start: resolveParentLine: session load failed: %v\n", err)
		return ""
	}
	// Find the participant whose AgentID matches the subagent's parent.
	var parentHandle string
	for _, p := range roster.Participants {
		if p.AgentID == parentID && p.Persona != "" {
			parentHandle = p.Persona
			break
		}
	}
	if parentHandle == "" {
		return ""
	}
	parentIdentity, err := store.Load(parentHandle, identity.Reference(true))
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: subagent-start: resolveParentLine: load identity %q: %v\n", parentHandle, err)
		return ""
	}
	return fmt.Sprintf("You report to %s (%s).", parentIdentity.Name, parentIdentity.Handle)
}

// insertAfterFirstLine inserts extra after the first line of text.
func insertAfterFirstLine(text, extra string) string {
	for i, c := range text {
		if c == '\n' {
			return text[:i] + "\n" + extra + text[i:]
		}
	}
	return text + "\n" + extra
}
