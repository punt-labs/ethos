// Package session manages session rosters — the set of participants
// (human + agents) active in a Claude Code session.
package session

// Participant represents a single entity in a session roster.
type Participant struct {
	AgentID   string         `yaml:"agent_id" json:"agent_id"`
	Persona   string         `yaml:"persona,omitempty" json:"persona,omitempty"`
	AgentType string         `yaml:"agent_type,omitempty" json:"agent_type,omitempty"`
	Parent    string         `yaml:"parent,omitempty" json:"parent,omitempty"`
	Joined    string         `yaml:"joined,omitempty" json:"joined,omitempty"`
	Ext       map[string]any `yaml:"ext,omitempty" json:"ext,omitempty"`
}

// Roster is the session-scoped list of all participants.
//
// Repo and Checkout are two different bindings and neither substitutes for the
// other. Repo is a git-remote identity (org/name) shared by every checkout of
// that repo. Checkout is the one filesystem path whose machine-local live audit
// zone this session writes to (DES-058) — the path DESIGN.md §Seal failure
// requires the vacuum cross-check to retain, so a probe reads the live files
// where they were written rather than wherever a commit happens to run. It is
// empty on rosters written before the field existed, which reads as "writer
// unknown".
type Roster struct {
	Session      string        `yaml:"session" json:"session"`
	Started      string        `yaml:"started" json:"started"`
	Repo         string        `yaml:"repo,omitempty" json:"repo,omitempty"`
	Checkout     string        `yaml:"checkout,omitempty" json:"checkout,omitempty"`
	Host         string        `yaml:"host,omitempty" json:"host,omitempty"`
	Participants []Participant `yaml:"participants" json:"participants"`
}

// FindParticipant returns the participant with the given agent_id, or nil.
func (r *Roster) FindParticipant(agentID string) *Participant {
	for i := range r.Participants {
		if r.Participants[i].AgentID == agentID {
			return &r.Participants[i]
		}
	}
	return nil
}

// RemoveParticipant removes the participant with the given agent_id.
// Returns true if a participant was removed.
func (r *Roster) RemoveParticipant(agentID string) bool {
	for i, p := range r.Participants {
		if p.AgentID == agentID {
			r.Participants = append(r.Participants[:i], r.Participants[i+1:]...)
			return true
		}
	}
	return false
}
