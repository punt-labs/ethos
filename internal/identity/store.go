package identity

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/punt-labs/ethos/internal/attribute"
	"gopkg.in/yaml.v3"
)

func flock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

func funlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// ErrNoActive is returned when no active identity is configured.
var ErrNoActive = errors.New("no active identity")

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

// Root returns the store's root directory.
func (s *Store) Root() string {
	return s.root
}

func (s *Store) identitiesDir() string {
	return filepath.Join(s.root, "identities")
}

// Path returns the filesystem path for the given handle.
// Uses filepath.Base to prevent path traversal.
func (s *Store) Path(handle string) string {
	return filepath.Join(s.identitiesDir(), filepath.Base(handle)+".yaml")
}

// LoadOption configures Load behavior.
type LoadOption func(*loadConfig)

type loadConfig struct {
	reference bool
}

// Reference returns a LoadOption that skips attribute content resolution.
// When true, Load returns attribute slugs in the path fields without
// reading the .md files.
func Reference(v bool) LoadOption {
	return func(c *loadConfig) { c.reference = v }
}

// Load reads an identity YAML file by handle. By default, it resolves
// attribute references (personality, writing_style, skills) to their
// markdown content. Pass Reference(true) to return slugs only.
func (s *Store) Load(handle string, opts ...LoadOption) (*Identity, error) {
	var cfg loadConfig
	for _, o := range opts {
		o(&cfg)
	}

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

	// Resolve attribute content unless reference-only mode.
	if !cfg.reference {
		id.Warnings = s.resolveAttributes(&id)
	}

	return &id, nil
}

// resolveAttributes reads .md files for personality, writing_style, and
// skills slugs. Returns warnings for any files that could not be read.
func (s *Store) resolveAttributes(id *Identity) []string {
	var warnings []string

	if id.Personality != "" {
		store := attribute.NewStore(s.root, attribute.Personalities)
		a, err := store.Load(id.Personality)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("personality %q: %v", id.Personality, err))
		} else {
			id.PersonalityContent = a.Content
		}
	}

	if id.WritingStyle != "" {
		store := attribute.NewStore(s.root, attribute.WritingStyles)
		a, err := store.Load(id.WritingStyle)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("writing_style %q: %v", id.WritingStyle, err))
		} else {
			id.WritingStyleContent = a.Content
		}
	}

	if len(id.Skills) > 0 {
		store := attribute.NewStore(s.root, attribute.Skills)
		id.SkillContents = make([]string, len(id.Skills))
		for i, slug := range id.Skills {
			a, err := store.Load(slug)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("skill %q: %v", slug, err))
			} else {
				id.SkillContents[i] = a.Content
			}
		}
	}

	return warnings
}

// ValidateRefs checks that all attribute references in an identity point
// to existing .md files. Returns an error on the first missing reference.
func (s *Store) ValidateRefs(id *Identity) error {
	if id.Personality != "" {
		store := attribute.NewStore(s.root, attribute.Personalities)
		if !store.Exists(id.Personality) {
			return &ValidationError{
				Field:   "personality",
				Message: fmt.Sprintf("%q not found — create it with 'ethos personality create %s'", id.Personality, id.Personality),
			}
		}
	}
	if id.WritingStyle != "" {
		store := attribute.NewStore(s.root, attribute.WritingStyles)
		if !store.Exists(id.WritingStyle) {
			return &ValidationError{
				Field:   "writing_style",
				Message: fmt.Sprintf("%q not found — create it with 'ethos writing-style create %s'", id.WritingStyle, id.WritingStyle),
			}
		}
	}
	skillStore := attribute.NewStore(s.root, attribute.Skills)
	for _, slug := range id.Skills {
		if !skillStore.Exists(slug) {
			return &ValidationError{
				Field:   "skills",
				Message: fmt.Sprintf("%q not found — create it with 'ethos skill create %s'", slug, slug),
			}
		}
	}
	return nil
}

// ListResult holds the results of listing identities, including any
// warnings from files that could not be loaded.
type ListResult struct {
	Identities []*Identity
	Warnings   []string
}

// List returns all identities in the store. Uses reference mode (no
// attribute content resolution). Files that cannot be loaded are
// reported as warnings.
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
		id, err := s.Load(handle, Reference(true))
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("skipping %s: %v", entry.Name(), err))
			continue
		}
		result.Identities = append(result.Identities, id)
	}
	return result, nil
}

// Active returns the currently active identity (with resolved content).
func (s *Store) Active(opts ...LoadOption) (*Identity, error) {
	path := filepath.Join(s.root, "active")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoActive
		}
		return nil, fmt.Errorf("reading active identity: %w", err)
	}
	handle := strings.TrimSpace(string(data))
	if handle == "" {
		return nil, ErrNoActive
	}
	return s.Load(handle, opts...)
}

// SetActive sets the active identity by handle. Returns an error if
// the identity does not exist.
func (s *Store) SetActive(handle string) error {
	if _, err := s.Load(handle, Reference(true)); err != nil {
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
// with the same handle already exists or if attribute references are
// invalid. Uses O_EXCL for atomic create.
func (s *Store) Save(id *Identity) error {
	if err := s.ValidateRefs(id); err != nil {
		return err
	}
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

// Update reads an existing identity, applies a mutation function, validates
// the result, and writes it back. Uses flock for concurrency safety.
// Used by attribute binding operations (set_personality, add_skill, etc.).
func (s *Store) Update(handle string, mutate func(*Identity) error) error {
	path := s.Path(handle)
	lockPath := path + ".lock"

	// Acquire exclusive lock.
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("creating lock file: %w", err)
	}
	defer lockFile.Close()
	defer os.Remove(lockPath)
	if err := flock(lockFile); err != nil {
		return fmt.Errorf("acquiring lock: %w", err)
	}
	defer funlock(lockFile)

	id, err := s.Load(handle, Reference(true))
	if err != nil {
		return err
	}
	if err := mutate(id); err != nil {
		return err
	}
	if err := s.ValidateRefs(id); err != nil {
		return err
	}
	data, err := yaml.Marshal(id)
	if err != nil {
		return fmt.Errorf("marshaling identity: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

// IdentitiesDir returns the path to the identities subdirectory.
func (s *Store) IdentitiesDir() string {
	return s.identitiesDir()
}
