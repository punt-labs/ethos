package role

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Store provides CRUD for role YAML files under a root directory.
type Store struct {
	root string // ethos root, e.g. ~/.punt-labs/ethos
}

// NewStore creates a Store rooted at the given directory.
// Roles are stored in root/roles/.
func NewStore(root string) *Store {
	return &Store{root: root}
}

// Dir returns the roles directory.
func (s *Store) Dir() string {
	return filepath.Join(s.root, "roles")
}

// path returns the filesystem path for the given role name.
func (s *Store) path(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	safe := filepath.Base(name)
	return filepath.Join(s.Dir(), safe+".yaml"), nil
}

// Save writes a role to disk. Returns an error if it already exists.
func (s *Store) Save(r *Role) error {
	if err := ValidateName(r.Name); err != nil {
		return err
	}
	if err := ValidateModel(r.Model); err != nil {
		return err
	}

	p, err := s.path(r.Name)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		return fmt.Errorf("creating roles directory: %w", err)
	}

	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("role %q already exists", r.Name)
		}
		return fmt.Errorf("creating role file: %w", err)
	}

	data, err := yaml.Marshal(r)
	if err != nil {
		f.Close()
		os.Remove(p)
		return fmt.Errorf("marshaling role: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(p)
		return fmt.Errorf("writing role file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(p)
		return fmt.Errorf("writing role file: %w", err)
	}
	return nil
}

// Load reads a role by name.
func (s *Store) Load(name string) (*Role, error) {
	p, err := s.path(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("role %q not found: %w", name, err)
		}
		return nil, fmt.Errorf("reading role: %w", err)
	}
	var r Role
	if err := yaml.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parsing role %q: %w", name, err)
	}
	if err := ValidateModel(r.Model); err != nil {
		return nil, fmt.Errorf("role %q: %w", name, err)
	}
	return &r, nil
}

// List returns the names of all roles in the store.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.Dir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading roles directory: %w", err)
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

// Delete removes a role file.
func (s *Store) Delete(name string) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("role %q not found", name)
		}
		return fmt.Errorf("deleting role: %w", err)
	}
	return nil
}

// Exists checks whether a role file exists.
func (s *Store) Exists(name string) bool {
	p, err := s.path(name)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}
