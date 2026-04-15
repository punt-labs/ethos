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

	Inputs          Inputs   `yaml:"inputs" json:"inputs"`
	WriteSet        []string `yaml:"write_set" json:"write_set"`
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
