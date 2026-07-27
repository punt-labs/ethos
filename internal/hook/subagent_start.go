package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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
	// RepoRoot is the current work tree root, used by the verifier
	// isolation block to resolve write_set entries to concrete files on
	// disk via WalkWriteSet. The files under review live in this checkout
	// (a linked worktree holds the branch), so this is the work-tree root
	// (FindRepoRoot). Empty means the walk is skipped.
	RepoRoot string
	// StoreRoot is the repo whose .punt-labs/ethos mission store the
	// hash-refusal path closes the delegation skeleton in. The skeleton
	// was written by the Tier B dispatch against the store root, so the
	// close must target the same tree — the main work tree in a linked
	// worktree (StoreRepoRoot), not this checkout (CR#3). Empty skips the
	// close.
	StoreRoot string
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
// it enforces DES-033's frozen-evaluator gate. The gate is bound by
// (mission_id, role): the spawn's declared MISSION_ID names the one
// mission it serves, and the gate applies only when that mission is
// open and names this subagent as its evaluator. A handle that is
// merely the evaluator of some OTHER open mission is not treated as
// that mission's verifier (ethos-z69l).
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
	// The gate is keyed by the spawn's declared MISSION_ID (emitted by
	// the Tier B dispatch into this subagent's env). That mission, and
	// only that mission, decides whether this is a verifier spawn: the
	// gate fires when the declared mission is open and names agentType
	// as its evaluator. A verifier spawn yields a one-element slice
	// that Phase 3.5's context-isolation path below consumes.
	declaredMissionID := os.Getenv("MISSION_ID")
	verifierMissions, err := checkVerifierHash(agentType, declaredMissionID, deps)
	if err != nil {
		// DES-054 phase 2d: when the refusal fires after PreToolUse-on-
		// Agent wrote a delegation skeleton (MISSION_ID + DELEGATION_ID
		// both set in env), finalize the skeleton with verdict=aborted
		// so an audit query distinguishes a hash-gate refusal from a
		// spawn that ran and failed downstream. The close error is
		// logged but never masks the original refusal — the operator's
		// primary diagnostic stays the hash drift, not a follow-on
		// audit-store failure.
		closeSkeletonOnHashRefusal(deps.StoreRoot)
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
	b.WriteString("These are the paths the verifier may write or edit:\n\n")
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
	b.WriteString("Any Write or Edit against a path outside this list is blocked by the\n")
	b.WriteString("PreToolUse hook. You may read any file in the repo for context.\n")

	// DES-052 extract_into: list directories under which new files
	// may be created. The audit summary line goes first so a reviewer
	// scanning the block sees the union directory list at a glance
	// and cannot misread it as "modify everywhere in these
	// directories" — only "create new files there".
	if len(m.ExtractInto) > 0 {
		b.WriteString("\n### Extract into (new files only)\n\n")
		fmt.Fprintf(&b,
			"extract_into authorizes new files under: %s\n\n",
			strings.Join(m.ExtractInto, ", "))
		b.WriteString("These directories permit the creation of new files at paths that do\n")
		b.WriteString("not yet exist on disk. Modification of an existing file under one\n")
		b.WriteString("of these directories is NOT authorized by this section — that\n")
		b.WriteString("requires a matching write_set entry above. The PreToolUse hook\n")
		b.WriteString("enforces both rules: existing-file Write/Edit checks the write_set\n")
		b.WriteString("allowlist, and new-file Write/Edit (target does not yet exist)\n")
		b.WriteString("checks the extract_into list.\n\n")
		for _, dir := range m.ExtractInto {
			fmt.Fprintf(&b, "  - %s\n", dir)
		}
	}

	// Walk the write_set to concrete files on disk so the verifier
	// sees exactly which files exist, not just the static entries.
	if repoRoot != "" {
		walked, walkErr := mission.WalkWriteSet(repoRoot, m.WriteSet)
		if walkErr != nil {
			// Log the walk error but do not fail the spawn; the
			// static allowlist above is sufficient for verification.
			// Surface the degradation in the injection block too so
			// the verifier knows the concrete-files section is
			// missing (silent-failure-hunter NOTED on DES-052; bead
			// ethos-jiqn item 4).
			fmt.Fprintf(os.Stderr, "ethos: subagent-start: walk write_set for %s: %v\n", m.MissionID, walkErr)
			b.WriteString("\n### Concrete files on disk\n\n")
			b.WriteString("(walk error: see stderr — listing static entries only)\n")
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
		contractPath, err := store.ContractPath(m.MissionID)
		switch {
		case err != nil:
			// Surface the failure so an operator running the
			// SubagentStart hook with -v sees that the contract path
			// could not be resolved — better to omit the allowlist
			// entry than to anchor it on a stale path the verifier
			// then cannot read.
			fmt.Fprintf(os.Stderr,
				"ethos: verifierAllowlist: resolving contract path for %q: %v\n",
				m.MissionID, err)
		default:
			if _, ok := seen[contractPath]; !ok {
				seen[contractPath] = struct{}{}
				abs = append(abs, contractPath)
			}
		}
	}
	return repo, abs
}

// buildVerifierAllowlistEnv returns the env map for a verifier
// subagent. The ETHOS_VERIFIER_ALLOWLIST value is a colon-separated
// list of all allowed paths across all verifier missions. PreToolUse
// reads this env var and blocks tool calls targeting paths outside it.
//
// DES-052: when at least one verifier mission has a non-empty
// extract_into list, ETHOS_VERIFIER_EXTRACT_INTO is also set to the
// deduplicated colon-separated union of those entries. PreToolUse
// reads that var and authorizes Write/Edit to non-existing paths
// under any listed directory; existing-file Write/Edit still
// requires the allowlist match.
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
	env := map[string]string{
		"ETHOS_VERIFIER_ALLOWLIST": strings.Join(entries, ":"),
	}

	// Contract.Validate rejects empty extract_into entries at the
	// admission trust boundary, so this loop trusts every entry it
	// sees here. No defensive empty-string skip — if such an entry
	// ever appears it means validation regressed, and we want the
	// resulting malformed env value to surface that regression
	// rather than silently swallow it (silent-failure-hunter NOTED
	// on DES-052; bead ethos-jiqn item 3).
	eiSeen := make(map[string]struct{})
	var eiEntries []string
	for _, vm := range missions {
		if vm.Contract == nil {
			continue
		}
		for _, dir := range vm.Contract.ExtractInto {
			if _, ok := eiSeen[dir]; ok {
				continue
			}
			eiSeen[dir] = struct{}{}
			eiEntries = append(eiEntries, dir)
		}
	}
	if len(eiEntries) > 0 {
		env["ETHOS_VERIFIER_EXTRACT_INTO"] = strings.Join(eiEntries, ":")
	}
	return env
}

// checkVerifierHash enforces DES-033's frozen-evaluator gate for a
// single spawn, bound by (mission_id, role). declaredMissionID is the
// MISSION_ID the Tier B dispatch emitted into this spawn's env; it
// names the one mission the spawn serves. The gate applies ONLY when
// that mission is open and names agentType as its evaluator — i.e. the
// spawn actually serves that mission as its verifier. A handle that is
// merely the evaluator of some OTHER open mission is not that mission's
// verifier when the spawn carries a different (or no) MISSION_ID; that
// misbinding was ethos-z69l.
//
// Returns (nil, nil) — no gate, normal persona path — when:
//   - Missions is nil (legacy install, no mission store)
//   - agentType is empty
//   - declaredMissionID is empty: without a declared mission we cannot
//     know which mission the spawn serves, so we never apply a verifier
//     gate by handle alone
//   - the declared mission is not open
//   - the declared mission's evaluator handle is not agentType (the
//     spawn serves the mission in another role, e.g. worker)
//
// Returns (a one-element slice, nil) when the spawn IS the declared
// mission's verifier and the mission is either legacy (empty pinned
// hash) or its current content matches the pinned hash. That slice is
// Phase 3.5's single source of truth for "this IS a verifier spawn".
//
// Returns (nil, fatal error) when:
//   - deps.Hash is misconfigured (Missions is non-nil but HashSources
//     is incomplete). Silent skip would let stale evaluator content
//     through under a configuration error.
//   - the declared mission fails to load (missing, corrupt, or
//     unparseable file).
//   - the current hash recomputation fails.
//   - the declared mission's pinned hash does not equal the recomputed
//     current hash — the error is a multi-line block naming the mission,
//     the pinned and current rollup hash prefixes, the per-section
//     hashes of the CURRENT content so the operator can cross-reference
//     which file they edited, and two recovery options.
func checkVerifierHash(agentType, declaredMissionID string, deps SubagentStartDeps) ([]verifierMission, error) {
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
	declaredMissionID = strings.TrimSpace(declaredMissionID)
	if declaredMissionID == "" {
		// No declared mission: the spawn cannot be bound to a
		// (mission_id, role). Applying the verifier gate by handle
		// alone is exactly the misbinding ethos-z69l fixes.
		return nil, nil
	}

	c, raw, err := readGateContract(deps.Missions, declaredMissionID)
	if err != nil {
		return nil, fmt.Errorf("verifier hash gate: %w", err)
	}
	if c.Status != mission.StatusOpen {
		// A closed, failed, or escalated mission is out of the gate's
		// purview; the spawn falls through to the normal persona path.
		return nil, nil
	}
	if c.Evaluator.Handle != agentType {
		// The spawn serves the declared mission in another role (the
		// worker, most commonly). It is not the mission's verifier, so
		// the frozen-evaluator gate does not apply — THE ethos-z69l FIX.
		return nil, nil
	}

	vm := verifierMission{Contract: c, RawYAML: raw}
	if c.Evaluator.Hash == "" {
		// Pre-3.3 mission with an empty pinned hash. Warn and allow;
		// do not recompute the current hash. A legacy mission can never
		// match a recomputed hash, and the recompute itself may fail
		// against content valid at launch but no longer resolvable —
		// which would wrongly refuse the spawn. The launch predates
		// the gate.
		fmt.Fprintf(os.Stderr,
			"ethos: subagent-start: warning: mission %s has empty Evaluator.Hash (pre-3.3); skipping gate\n",
			c.MissionID,
		)
		return []verifierMission{vm}, nil
	}

	breakdown, err := mission.ComputeEvaluatorHashBreakdown(c.Evaluator.Handle, deps.Hash)
	if err != nil {
		return nil, fmt.Errorf(
			"verifier hash gate: recomputing hash for evaluator %q: %w",
			c.Evaluator.Handle, err,
		)
	}
	if c.Evaluator.Hash != breakdown.Rollup {
		return nil, errors.New(formatDriftError(agentType, breakdown, driftedMission{
			ID:     c.MissionID,
			Pinned: c.Evaluator.Hash,
		}))
	}
	return []verifierMission{vm}, nil
}

// readGateContract reads and validates one mission contract for the
// verifier gate with a single TOCTOU-safe read: Lstat symlink check,
// ReadFile, and strict decode from the same bytes. It returns the
// parsed contract and the raw bytes so Phase 3.5 renders the exact
// pinned YAML without a second read.
//
// The symlink rejection mirrors mission.rejectSymlink (unexported,
// different package); the same Lstat-before-Read gap applies (ethos-jjm).
func readGateContract(missions *mission.Store, id string) (*mission.Contract, []byte, error) {
	path, err := missions.ContractPath(id)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving contract path for %q: %w", id, err)
	}
	if info, lErr := os.Lstat(path); lErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, fmt.Errorf("refusing to follow symlink: %s", path)
		}
	} else if !errors.Is(lErr, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("lstat %s: %w", path, lErr)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("mission %q not found", id)
		}
		return nil, nil, fmt.Errorf("reading mission %q: %w", id, err)
	}
	c, err := mission.DecodeContractStrict(raw, id)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load mission %q: %w", id, err)
	}
	// Match Store.Load's default-fill for pre-3.4 contracts.
	if c.CurrentRound == 0 {
		c.CurrentRound = 1
	}
	if err := c.Validate(); err != nil {
		return nil, nil, fmt.Errorf("contract %q failed validation: %w", id, err)
	}
	if c.MissionID != id {
		return nil, nil, fmt.Errorf(
			"contract filename %q does not match mission_id %q", id, c.MissionID)
	}
	return c, raw, nil
}

// driftedMission is an internal record built by checkVerifierHash and
// consumed by formatDriftError. Pinned is the full hex the formatter
// truncates; the mission ID is rendered verbatim.
type driftedMission struct {
	ID     string
	Pinned string
}

// formatDriftError renders the operator-facing multi-line block the
// verifier gate emits when the declared mission's pinned evaluator
// hash disagrees with the current evaluator content. Per-mission
// binding (ethos-z69l) means a spawn serves exactly one mission, so
// the block reports one mission — not an aggregate.
//
// The block has four parts:
//  1. A summary line naming the evaluator and the mission.
//  2. One line showing pinned → current rollup.
//  3. The per-section breakdown of the CURRENT content so the operator
//     can cross-reference which source file they edited.
//  4. Two recovery options — revert the edit, or close and relaunch.
func formatDriftError(
	evaluator string,
	breakdown mission.EvaluatorHashBreakdown,
	m driftedMission,
) string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"refusing verifier spawn: evaluator %q content has drifted since mission %s was launched\n",
		evaluator, m.ID,
	)
	fmt.Fprintf(&b, "  %s: pinned %s -> current %s\n",
		m.ID, hashPrefix(m.Pinned), hashPrefix(breakdown.Rollup),
	)
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
	b.WriteString("  to preserve this mission: revert the edit to the evaluator's identity content\n")
	fmt.Fprintf(&b,
		"  to accept the new content: close mission %s and relaunch it",
		m.ID,
	)
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

// closeSkeletonOnHashRefusal finalizes a Tier B delegation skeleton with
// verdict=aborted when the verifier hash gate refuses the spawn AFTER
// PreToolUse-on-Agent has written the skeleton record. The trigger is
// the joint presence of MISSION_ID and DELEGATION_ID in the env — only
// the Tier B dispatch sets both, so the close call cannot misfire on a
// Tier A spawn or a legacy hook flow.
//
// repoRoot may be empty; the close call surfaces the resulting error to
// stderr and returns. Errors do NOT propagate up to the caller — the
// hash-gate refusal already carries the operator-facing diagnostic, and
// a follow-on audit-store failure must not mask it.
func closeSkeletonOnHashRefusal(repoRoot string) {
	missionID := os.Getenv("MISSION_ID")
	delegationID := os.Getenv("DELEGATION_ID")
	if missionID == "" || delegationID == "" {
		return
	}
	if repoRoot == "" {
		fmt.Fprintln(os.Stderr,
			"ethos: subagent-start: hash refusal: repoRoot empty; "+
				"skipping skeleton close for "+delegationID)
		return
	}
	// Acquire the per-delegation exclusive flock around the close
	// so a concurrent SubagentStop or CLI close cannot interleave
	// and produce a last-writer-wins verdict (Copilot MED on PR
	// #327: closeSkeletonOnHashRefusal wrote record.yaml without
	// the lock the rest of the dispatch path holds). Lock path is
	// the same global tree the PreToolUse-on-Agent dispatch uses;
	// derive it from os.UserHomeDir here to keep the close path
	// independent of the dispatch's tierBGlobalRoot helper.
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: subagent-start: hash refusal: user home dir: %v; "+
				"skipping skeleton close for %q\n", err, delegationID)
		return
	}
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	release, err := mission.AcquireDelegationLock(globalRoot, delegationID)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: subagent-start: hash refusal: acquiring delegation lock for %q: %v\n",
			delegationID, err)
		return
	}
	defer release()
	closedAt := time.Now().UTC().Format(time.RFC3339)
	if err := mission.CloseDelegationSkeleton(
		repoRoot, missionID, delegationID,
		mission.DelegationVerdictAborted, closedAt,
	); err != nil {
		fmt.Fprintf(os.Stderr,
			"ethos: subagent-start: closing aborted skeleton %q: %v\n",
			delegationID, err,
		)
	}
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
