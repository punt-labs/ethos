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
	CreatedAt string `yaml:"created_at" json:"created_at"`
	UpdatedAt string `yaml:"updated_at" json:"updated_at"`
	ClosedAt  string `yaml:"closed_at,omitempty" json:"closed_at,omitempty"`
	Session   string `yaml:"session,omitempty" json:"session,omitempty"`
	Repo      string `yaml:"repo,omitempty" json:"repo,omitempty"`

	// Bead ID lives at inputs.bead — the single source of truth.
	// An earlier draft carried both a top-level Bead and Inputs.Bead,
	// but the duplication made divergence trivial and silent. 3.1
	// removes the top-level field; callers should populate Inputs.Bead
	// and consumers should read it from there.

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

// Inputs are what the worker reads from. Files are paths the worker
// MUST read; references are supporting docs the worker MAY consult.
type Inputs struct {
	Bead       string   `yaml:"bead,omitempty" json:"bead,omitempty"`
	Files      []string `yaml:"files,omitempty" json:"files,omitempty"`
	References []string `yaml:"references,omitempty" json:"references,omitempty"`
}

// Budget is the round limit and reflection requirement for the mission.
//
// 3.1 stores the field. 3.4 will enforce it via hook.
type Budget struct {
	Rounds              int  `yaml:"rounds" json:"rounds"`
	ReflectionAfterEach bool `yaml:"reflection_after_each" json:"reflection_after_each"`
}
