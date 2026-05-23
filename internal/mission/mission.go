// Package mission defines the typed delegation artifact — the mission
// contract — that pins leader, worker, evaluator, write_set, tools,
// success criteria, and budget for a single delegated unit of work.
//
// The contract is the trust boundary. Validation rejects malformed input
// at parse time; storage is flock-protected; the event log is append-only.
package mission

// Status values for a Contract.
const (
	StatusOpen      = "open"
	StatusClosed    = "closed"
	StatusFailed    = "failed"
	StatusEscalated = "escalated"
)

// Contract is the typed delegation artifact pinned at mission launch.
// Stored as YAML at <root>/missions/<id>.yaml.
//
// All identity fields hold handles, not display names. Validate() must
// be called before persisting.
type Contract struct {
	MissionID string `yaml:"mission_id" json:"mission_id"`
	Status    string `yaml:"status" json:"status"`
	Type      string `yaml:"type,omitempty" json:"type,omitempty"`
	CreatedAt string `yaml:"created_at" json:"created_at"`
	UpdatedAt string `yaml:"updated_at" json:"updated_at"`
	ClosedAt  string `yaml:"closed_at,omitempty" json:"closed_at,omitempty"`
	Session   string `yaml:"session,omitempty" json:"session,omitempty"`
	Repo      string `yaml:"repo,omitempty" json:"repo,omitempty"`

	// Ticket ID lives at inputs.ticket — the single source of truth.
	// An earlier draft carried both a top-level Bead and Inputs.Ticket
	// (then called Bead), but the duplication made divergence trivial
	// and silent. 3.1 removed the top-level field. 3.4 renamed
	// Bead → Ticket for tracker-agnostic language; inputs.bead is
	// accepted as a deprecated alias during YAML/JSON decode.

	Leader    string    `yaml:"leader" json:"leader"`
	Worker    string    `yaml:"worker" json:"worker"`
	Evaluator Evaluator `yaml:"evaluator" json:"evaluator"`

	Inputs   Inputs   `yaml:"inputs" json:"inputs"`
	WriteSet []string `yaml:"write_set" json:"write_set"`
	// ExtractInto authorizes new-file creation under listed directories
	// without authorizing modification of existing files in those
	// directories. Entries are directories — the per-entry validator
	// (rule 17) rejects file-shaped entries with code-file extensions.
	// Empty is the backward-compatible default: contracts that omit the
	// field behave identically to pre-DES-052 behavior.
	//
	// PreToolUse honours ETHOS_VERIFIER_EXTRACT_INTO (set by
	// SubagentStart) to allow Write/Edit of non-existing paths under
	// any listed directory; existing path Write/Edit still requires
	// ETHOS_VERIFIER_ALLOWLIST match. See DES-052 in DESIGN.md for full
	// rationale and the cross-mission admission control table.
	ExtractInto     []string `yaml:"extract_into,omitempty" json:"extract_into,omitempty"`
	Tools           []string `yaml:"tools,omitempty" json:"tools,omitempty"`
	SuccessCriteria []string `yaml:"success_criteria" json:"success_criteria"`

	Budget Budget `yaml:"budget" json:"budget"`

	// CurrentRound is the round the worker is currently executing.
	// 3.4 makes Budget.Rounds enforceable: each call to
	// Store.AdvanceRound bumps this field by 1, and the gate refuses
	// the bump if (a) the previous round has no reflection, (b) the
	// previous round's reflection recommended stop or escalate, or
	// (c) the bump would exceed Budget.Rounds.
	//
	// A freshly created mission starts at round 1: round-tracking is
	// 1-indexed and the first round begins implicitly when Create
	// returns. Validate enforces 1 ≤ CurrentRound ≤ Budget.Rounds.
	//
	// Round tracking lives on the Contract (not derived from the
	// event log) so the gate can answer "what round are we in?" with
	// a single Load. Deriving from the log would force every gate
	// call to scan the JSONL file, complicating the per-mission
	// flock contract and exposing log reads outside the package.
	// The trade-off: a malformed CurrentRound on disk fails Validate
	// at load time, which is symmetric with every other Contract
	// field — the trust boundary stays in one place.
	CurrentRound int `yaml:"current_round,omitempty" json:"current_round"`

	Context string `yaml:"context,omitempty" json:"context,omitempty"`

	// Pipeline is an optional identifier grouping related missions into a
	// sequence. All missions in a pipeline share the same value. The leader
	// picks it at instantiation time, or ethos auto-generates one. Validate
	// requires a slug-like value: lowercase letters, digits, and hyphens
	// only, max 128 characters.
	Pipeline string `yaml:"pipeline,omitempty" json:"pipeline,omitempty"`

	// DependsOn is an optional list of mission IDs that must reach a
	// terminal status before this mission's worker should begin. Advisory —
	// the store does not block Create on dependency status. The leader or
	// daemon enforces ordering.
	DependsOn []string `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`

	// Preconditions are admission gates evaluated by the PreToolUse
	// hook before any non-Read tool call under a Tier B contract.
	// DES-054 v5 §"PreToolUse procedure preconditions" defines two
	// predicate forms:
	//
	//   - implicit: the tool input's target file_path(s) must each
	//     have been Read in this session.
	//   - explicit: each entry in RequireRead (with `${inputs.X}`
	//     substitution against the contract's Inputs) must have been
	//     Read in this session.
	//
	// A violated predicate blocks with the contract-supplied Message.
	// An unevaluable predicate (malformed substitution, missing input,
	// unreadable audit log) blocks under StrictPreconditions=true
	// (default) and warns + allows under StrictPreconditions=false.
	//
	// Empty list disables the gate — the contract has no read-set
	// admission requirements. See internal/hook/preconditions.go for
	// the evaluator.
	Preconditions []Precondition `yaml:"preconditions,omitempty" json:"preconditions,omitempty"`

	// StrictPreconditions controls the fail-open vs fail-closed
	// behavior of the precondition evaluator on unevaluable predicates
	// (malformed substitution, missing input, unreadable audit log). A
	// nil pointer is the default — see EffectiveStrictPreconditions —
	// and resolves to true (fail closed). An explicit `false` is the
	// documented escape hatch for migration from contracts written
	// before DES-054 v5.
	//
	// `*bool` keeps the YAML omitempty distinguishable from an
	// explicit `false`: the on-disk shape is "unset = default strict;
	// strict_preconditions: false = explicit opt-out".
	StrictPreconditions *bool `yaml:"strict_preconditions,omitempty" json:"strict_preconditions,omitempty"`

	// Delegations is the per-spawn template list for Tier B inheritance.
	// DES-054 v5 §"PreToolUse-on-Agent" dispatch rule (a): when a worker
	// running under this contract invokes the Agent tool, the hook walks
	// this list, matches `spawn_pattern` against the agent_type, and
	// — if a template's `inherits_contract` is true — applies this same
	// contract to the child spawn. Empty list disables inheritance; the
	// child falls through to MISSION_ID-explicit dispatch or Tier A.
	//
	// Patterns are regular expressions anchored at both ends. Admission-
	// time validation is a phase 3 deliverable (Contract.Validate does
	// not yet compile spawn_pattern); today MatchSpawnPattern compiles
	// at match time and emits a stderr warning on a malformed pattern,
	// allowing the spawn to fall through to MISSION_ID-explicit
	// dispatch or Tier A. See DelegationTemplate in delegation.go for
	// the per-entry shape.
	Delegations []DelegationTemplate `yaml:"delegations,omitempty" json:"delegations,omitempty"`
}

// Precondition is one admission gate evaluated by the PreToolUse hook
// before a non-Read tool call under a Tier B contract. Two Forms:
//
//   - "implicit": the tool's target file_path(s) must each have been
//     Read in this session. RequireRead is ignored.
//   - "explicit": each entry in RequireRead — after `${inputs.X}`
//     substitution against the contract's Inputs — must have been
//     Read in this session.
//
// Message is the block reason surfaced to Claude Code when the
// predicate fails. Empty Message is rejected at Validate time so a
// failed gate is never silently named.
type Precondition struct {
	Form        string   `yaml:"form" json:"form"`
	RequireRead []string `yaml:"require_read,omitempty" json:"require_read,omitempty"`
	Message     string   `yaml:"message,omitempty" json:"message,omitempty"`
}

// PreconditionForm values accepted by Precondition.Form.
const (
	PreconditionFormImplicit = "implicit"
	PreconditionFormExplicit = "explicit"
)

// EffectiveStrictPreconditions returns the effective fail-mode for
// unevaluable precondition predicates. A nil StrictPreconditions
// pointer (the field was omitted from the YAML) resolves to true —
// fail closed by default. An explicit `*c.StrictPreconditions =
// false` is the documented escape hatch.
func (c *Contract) EffectiveStrictPreconditions() bool {
	if c == nil || c.StrictPreconditions == nil {
		return true
	}
	return *c.StrictPreconditions
}

// Evaluator is the frozen reviewer pinned at mission launch.
//
// Hash is populated at create time in 3.3 via ApplyServerFields from
// the resolved evaluator's personality, writing_style, talents, and
// role content (sha256). The YAML tag keeps omitempty for on-disk
// backward compatibility with pre-3.3 mission files (no empty hash:
// "" line on save), but the JSON tag always emits the field so
// consumers that key on presence see a consistent shape.
type Evaluator struct {
	Handle   string `yaml:"handle" json:"handle"`
	PinnedAt string `yaml:"pinned_at" json:"pinned_at"`
	Hash     string `yaml:"hash,omitempty" json:"hash"`
}

// Trigger captures the provenance of an externally-triggered mission
// (e.g., an email that caused the mission to be created). Optional —
// most missions are leader-initiated and have no trigger.
type Trigger struct {
	Type      string `yaml:"type,omitempty" json:"type,omitempty"`
	MessageID string `yaml:"message_id,omitempty" json:"message_id,omitempty"`
	From      string `yaml:"from,omitempty" json:"from,omitempty"`
	Subject   string `yaml:"subject,omitempty" json:"subject,omitempty"`
}

// Inputs are what the worker reads from. Files are paths the worker
// MUST read; references are supporting docs the worker MAY consult.
//
// Custom UnmarshalYAML/UnmarshalJSON accept both "ticket" (canonical)
// and "bead" (deprecated alias). Setting both is an error. Marshal
// emits "ticket" only.
type Inputs struct {
	Ticket     string   `yaml:"ticket,omitempty" json:"ticket,omitempty"`
	Files      []string `yaml:"files,omitempty" json:"files,omitempty"`
	References []string `yaml:"references,omitempty" json:"references,omitempty"`
	Trigger    *Trigger `yaml:"trigger,omitempty" json:"trigger,omitempty"`
}

// Budget is the round limit and reflection requirement for the mission.
//
// 3.1 stores the field. 3.4 will enforce it via hook.
type Budget struct {
	Rounds              int  `yaml:"rounds" json:"rounds"`
	ReflectionAfterEach bool `yaml:"reflection_after_each" json:"reflection_after_each"`
}

// ShowPayload is the wire shape the CLI `mission show --json` and the
// MCP `mission show` handler return: the full contract, the
// round-by-round result log, and an optional warnings list.
//
// The contract is embedded by pointer so its json tags drive
// serialization. Any future Contract field is auto-propagated to
// both surfaces without touching the show path — round 2's
// hand-rolled map dropped `session` and `repo` precisely because
// the CLI and MCP sides each maintained their own field list.
//
// Results is always emitted: empty state serializes as [] (the
// caller pre-initializes to []Result{} so a typed-nil slice never
// leaks through the map[string]any boxing trap mdm caught in round
// 2). Warnings is omitempty so an open mission with a healthy
// results file produces no noise; when LoadResults fails, the
// caller populates Warnings with the load error so the failure
// surfaces in JSON mode without requiring a stderr channel the MCP
// surface does not have.
type ShowPayload struct {
	*Contract
	Results  []Result `json:"results"`
	Warnings []string `json:"warnings,omitempty"`
}

// LogPayload is the wire shape the CLI `mission log --json` and the
// MCP `mission log` handler return: the round-by-round event slice
// and an optional warnings list when one or more lines in the
// on-disk JSONL file failed to decode.
//
// Events is always emitted: empty state serializes as [] (the
// caller pre-initializes to []Event{} so a typed-nil slice never
// leaks through the map[string]any boxing trap Phase 3.6 round 2
// hit for the results show payload). Warnings is omitempty so a
// healthy log produces no noise; when any line failed to decode,
// the caller populates Warnings with the line-numbered failure so
// scripted consumers see the corruption in JSON mode without
// requiring a stderr channel the MCP surface does not have.
//
// Symmetric with ShowPayload's warnings field — the two surfaces
// share one degradation convention.
type LogPayload struct {
	Events   []Event  `json:"events"`
	Warnings []string `json:"warnings,omitempty"`
}
