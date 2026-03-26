package team

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Store provides CRUD for team YAML files under a root directory.
type Store struct {
	root string // ethos root, e.g. ~/.punt-labs/ethos
}

// NewStore creates a Store rooted at the given directory.
// Teams are stored in root/teams/.
func NewStore(root string) *Store {
	return &Store{root: root}
}

// Dir returns the teams directory.
func (s *Store) Dir() string {
	return filepath.Join(s.root, "teams")
}

// path returns the filesystem path for the given team name.
func (s *Store) path(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	safe := filepath.Base(name)
	return filepath.Join(s.Dir(), safe+".yaml"), nil
}

// Save writes a team to disk. Validates invariants before writing.
// Returns an error if the team already exists.
func (s *Store) Save(t *Team, identityExists, roleExists func(string) bool) error {
	if err := Validate(t, identityExists, roleExists); err != nil {
		return err
	}

	p, err := s.path(t.Name)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		return fmt.Errorf("creating teams directory: %w", err)
	}

	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("team %q already exists", t.Name)
		}
		return fmt.Errorf("creating team file: %w", err)
	}

	data, err := yaml.Marshal(t)
	if err != nil {
		f.Close()
		os.Remove(p)
		return fmt.Errorf("marshaling team: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(p)
		return fmt.Errorf("writing team file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(p)
		return fmt.Errorf("writing team file: %w", err)
	}
	return nil
}

// save overwrites a team file (used by mutating operations after load).
func (s *Store) save(t *Team) error {
	p, err := s.path(t.Name)
	if err != nil {
		return err
	}

	data, err := yaml.Marshal(t)
	if err != nil {
		return fmt.Errorf("marshaling team: %w", err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return fmt.Errorf("writing team file: %w", err)
	}
	return nil
}

// Load reads a team by name.
func (s *Store) Load(name string) (*Team, error) {
	p, err := s.path(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("team %q not found", name)
		}
		return nil, fmt.Errorf("reading team: %w", err)
	}
	var t Team
	if err := yaml.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("parsing team %q: %w", name, err)
	}
	return &t, nil
}

// List returns the names of all teams in the store.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.Dir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading teams directory: %w", err)
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		names = append(names, strings.TrimSuffix(entry.Name(), ".yaml"))
	}
	return names, nil
}

// Delete removes a team file.
func (s *Store) Delete(name string) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("team %q not found", name)
		}
		return fmt.Errorf("deleting team: %w", err)
	}
	return nil
}

// Exists checks whether a team file exists.
func (s *Store) Exists(name string) bool {
	p, err := s.path(name)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// AddMember adds a member to an existing team. Validates that the identity
// and role exist and that the member is not a duplicate.
func (s *Store) AddMember(teamName string, m Member, identityExists, roleExists func(string) bool) error {
	t, err := s.Load(teamName)
	if err != nil {
		return err
	}

	if m.Identity == "" {
		return fmt.Errorf("identity is required")
	}
	if m.Role == "" {
		return fmt.Errorf("role is required")
	}
	if !identityExists(m.Identity) {
		return fmt.Errorf("identity %q not found", m.Identity)
	}
	if !roleExists(m.Role) {
		return fmt.Errorf("role %q not found", m.Role)
	}
	if hasMember(t, m.Identity, m.Role) {
		return fmt.Errorf("%s/%s already a member of team %q", m.Identity, m.Role, teamName)
	}

	t.Members = append(t.Members, m)
	return s.save(t)
}

// RemoveMember removes a member from a team. Cannot remove the last member.
// Also removes any collaborations that reference the removed member's role
// on this team, per the Z spec.
func (s *Store) RemoveMember(teamName, identity, role string) error {
	t, err := s.Load(teamName)
	if err != nil {
		return err
	}

	if !hasMember(t, identity, role) {
		return fmt.Errorf("%s/%s not a member of team %q", identity, role, teamName)
	}
	if len(t.Members) <= 1 {
		return fmt.Errorf("cannot remove last member of team %q", teamName)
	}

	// Remove the member.
	var kept []Member
	for _, m := range t.Members {
		if m.Identity == identity && m.Role == role {
			continue
		}
		kept = append(kept, m)
	}
	t.Members = kept

	// Check if the removed role is still filled by another member.
	roleStillFilled := false
	for _, m := range t.Members {
		if m.Role == role {
			roleStillFilled = true
			break
		}
	}

	// Remove dangling collaborations if the role is no longer filled.
	if !roleStillFilled {
		var keptCollabs []Collaboration
		for _, c := range t.Collaborations {
			if c.From == role || c.To == role {
				continue
			}
			keptCollabs = append(keptCollabs, c)
		}
		t.Collaborations = keptCollabs
	}

	return s.save(t)
}

// AddCollaboration adds a collaboration to an existing team.
// Validates that both roles are filled by members on the team,
// the type is valid, and no self-collaboration.
func (s *Store) AddCollaboration(teamName string, c Collaboration) error {
	t, err := s.Load(teamName)
	if err != nil {
		return err
	}

	if c.From == "" || c.To == "" {
		return fmt.Errorf("from and to are required")
	}
	if c.From == c.To {
		return fmt.Errorf("self-collaboration not allowed (%s)", c.From)
	}
	if !validCollabTypes[c.Type] {
		return fmt.Errorf("invalid type %q", c.Type)
	}

	// Check that both roles are filled by team members.
	filledRoles := make(map[string]bool)
	for _, m := range t.Members {
		filledRoles[m.Role] = true
	}
	if !filledRoles[c.From] {
		return fmt.Errorf("role %q not filled by any member", c.From)
	}
	if !filledRoles[c.To] {
		return fmt.Errorf("role %q not filled by any member", c.To)
	}

	t.Collaborations = append(t.Collaborations, c)
	return s.save(t)
}
