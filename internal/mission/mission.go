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
	Bead      string `yaml:"bead,omitempty" json:"bead,omitempty"`

	Leader    string    `yaml:"leader" json:"leader"`
	Worker    string    `yaml:"worker" json:"worker"`
	Evaluator Evaluator `yaml:"evaluator" json:"evaluator"`

	Inputs          Inputs   `yaml:"inputs" json:"inputs"`
	WriteSet        []string `yaml:"write_set" json:"write_set"`
	Tools           []string `yaml:"tools,omitempty" json:"tools,omitempty"`
	SuccessCriteria []string `yaml:"success_criteria" json:"success_criteria"`

	Budget  Budget `yaml:"budget" json:"budget"`
	Context string `yaml:"context,omitempty" json:"context,omitempty"`
}

// Evaluator is the frozen reviewer pinned at mission launch.
//
// Hash is OPTIONAL in 3.1; 3.3 will populate it from the resolved
// evaluator's personality+role+writing-style content (sha256). Until
// then it is an empty string.
type Evaluator struct {
	Handle   string `yaml:"handle" json:"handle"`
	PinnedAt string `yaml:"pinned_at" json:"pinned_at"`
	Hash     string `yaml:"hash,omitempty" json:"hash,omitempty"`
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
