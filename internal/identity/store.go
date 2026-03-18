package identity

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Store provides CRUD operations for identities on the filesystem.
// Identities are stored as YAML files in the identities subdirectory.
// The active identity handle is stored in an "active" file.
type Store struct {
	root string // e.g. ~/.punt-labs/ethos
}

// NewStore creates a Store rooted at the given directory.
func NewStore(root string) *Store {
	return &Store{root: root}
}

// DefaultStore returns a Store using the default global directory (~/.punt-labs/ethos).
func DefaultStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	return &Store{root: filepath.Join(home, ".punt-labs", "ethos")}, nil
}

func (s *Store) identitiesDir() string {
	return filepath.Join(s.root, "identities")
}

// Path returns the filesystem path for the given handle.
// Uses filepath.Base to prevent path traversal.
func (s *Store) Path(handle string) string {
	return filepath.Join(s.identitiesDir(), filepath.Base(handle)+".yaml")
}

// Load reads an identity YAML file by handle.
func (s *Store) Load(handle string) (*Identity, error) {
	path := s.Path(handle)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("identity %q not found: %w", handle, err)
	}
	var id Identity
	if err := yaml.Unmarshal(data, &id); err != nil {
		return nil, fmt.Errorf("invalid identity file %s: %w", path, err)
	}
	// Normalize empty Voice to nil for consistent omitempty behavior.
	if id.Voice != nil && id.Voice.Provider == "" && id.Voice.VoiceID == "" {
		id.Voice = nil
	}
	// Assemble extension data from <persona>.ext/ directory.
	id.Ext = s.loadExtensions(handle)
	return &id, nil
}

// ListResult holds the results of listing identities, including any
// warnings from files that could not be loaded.
type ListResult struct {
	Identities []*Identity
	Warnings   []string
}

// List returns all identities in the store. Files that cannot be loaded
// are reported as warnings in the result rather than failing the entire list.
func (s *Store) List() (*ListResult, error) {
	dir := s.identitiesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return &ListResult{}, nil
		}
		return nil, fmt.Errorf("reading identity directory: %w", err)
	}

	result := &ListResult{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		handle := strings.TrimSuffix(entry.Name(), ".yaml")
		id, err := s.Load(handle)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("skipping %s: %v", entry.Name(), err))
			continue
		}
		result.Identities = append(result.Identities, id)
	}
	return result, nil
}

// Active returns the currently active identity.
func (s *Store) Active() (*Identity, error) {
	path := filepath.Join(s.root, "active")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("no active identity: %w", err)
	}
	handle := strings.TrimSpace(string(data))
	if handle == "" {
		return nil, fmt.Errorf("no active identity configured")
	}
	return s.Load(handle)
}

// SetActive sets the active identity by handle. Returns an error if
// the identity does not exist.
func (s *Store) SetActive(handle string) error {
	if _, err := s.Load(handle); err != nil {
		return err
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	path := filepath.Join(s.root, "active")
	return os.WriteFile(path, []byte(handle+"\n"), 0o600)
}

// Exists checks whether an identity file exists for the given handle.
func (s *Store) Exists(handle string) bool {
	_, err := os.Stat(s.Path(handle))
	return err == nil
}

// Save writes an identity YAML file. Returns an error if an identity
// with the same handle already exists. Uses O_EXCL for atomic create.
func (s *Store) Save(id *Identity) error {
	dir := s.identitiesDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating identity directory: %w", err)
	}
	data, err := yaml.Marshal(id)
	if err != nil {
		return fmt.Errorf("marshaling identity: %w", err)
	}
	path := s.Path(id.Handle)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("identity %q already exists — delete %q to recreate", id.Handle, path)
		}
		return fmt.Errorf("creating identity file: %w", err)
	}
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	// Create extension directory alongside the identity file.
	return os.MkdirAll(s.ExtDir(id.Handle), 0o700)
}

// IdentitiesDir returns the path to the identities subdirectory.
func (s *Store) IdentitiesDir() string {
	return s.identitiesDir()
}
