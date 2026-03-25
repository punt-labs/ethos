package identity

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/internal/attribute"
	"gopkg.in/yaml.v3"
)

// Store provides CRUD operations for identities on the filesystem.
// Identities are stored as YAML files in the identities subdirectory.
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
// attribute references (personality, writing_style, talents) to their
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
	// Migrate legacy voice field to ext/vox.
	if err := s.migrateVoice(handle, path, data); err != nil {
		return nil, fmt.Errorf("migrating voice for %q: %w", handle, err)
	}
	// Assemble extension data from <handle>.ext/ directory.
	extData, extWarnings := s.loadExtensions(handle)
	id.Ext = extData

	// Resolve attribute content unless reference-only mode.
	if !cfg.reference {
		id.Warnings = s.resolveAttributes(&id)
	}
	id.Warnings = append(id.Warnings, extWarnings...)

	return &id, nil
}

// migrateVoice checks raw YAML for a legacy voice field and moves it to
// ext/vox. Re-saves the identity without the voice key.
func (s *Store) migrateVoice(handle, path string, data []byte) error {
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil // already validated above; skip migration
	}
	v, ok := raw["voice"]
	if !ok {
		return nil
	}
	vm, ok := v.(map[string]interface{})
	if !ok {
		// Non-map voice value (e.g. "voice: elevenlabs") — cannot migrate.
		// Leave the YAML untouched; manual fix required.
		return fmt.Errorf("voice field has unexpected type %T; manual migration required", v)
	}
	if len(vm) == 0 {
		// Empty voice map — just strip the key and rewrite.
		delete(raw, "voice")
		return s.rewriteRaw(path, raw)
	}
	provider, _ := vm["provider"].(string)
	voiceID, _ := vm["voice_id"].(string)
	if provider == "" && voiceID == "" {
		delete(raw, "voice")
		return s.rewriteRaw(path, raw)
	}

	// Write to ext/vox.
	if provider != "" {
		if err := s.ExtSet(handle, "vox", "provider", provider); err != nil {
			return fmt.Errorf("setting ext/vox/provider: %w", err)
		}
	}
	if voiceID != "" {
		if err := s.ExtSet(handle, "vox", "voice_id", voiceID); err != nil {
			return fmt.Errorf("setting ext/vox/voice_id: %w", err)
		}
	}

	// Strip voice from the identity YAML.
	delete(raw, "voice")
	return s.rewriteRaw(path, raw)
}

// rewriteRaw marshals a raw map back to the identity YAML file.
func (s *Store) rewriteRaw(path string, raw map[string]interface{}) error {
	out, err := yaml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshaling identity: %w", err)
	}
	return os.WriteFile(path, out, 0o600)
}

// resolveAttributes reads .md files for personality, writing_style, and
// talent slugs. Returns warnings for any files that could not be read.
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

	if len(id.Talents) > 0 {
		store := attribute.NewStore(s.root, attribute.Talents)
		id.TalentContents = make([]string, len(id.Talents))
		for i, slug := range id.Talents {
			a, err := store.Load(slug)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("talent %q: %v", slug, err))
			} else {
				id.TalentContents[i] = a.Content
			}
		}
	}

	return warnings
}

// ValidateRefs checks that all attribute references in an identity point
// to existing .md files. Returns an error on the first missing reference.
func (s *Store) ValidateRefs(id *Identity) error {
	if id.Personality != "" {
		if err := attribute.ValidateSlug(id.Personality); err != nil {
			return &ValidationError{Field: "personality", Message: fmt.Sprintf("invalid slug %q: %v", id.Personality, err)}
		}
		store := attribute.NewStore(s.root, attribute.Personalities)
		if !store.Exists(id.Personality) {
			return &ValidationError{
				Field:   "personality",
				Message: fmt.Sprintf("%q not found — create it with 'ethos personality create %s'", id.Personality, id.Personality),
			}
		}
	}
	if id.WritingStyle != "" {
		if err := attribute.ValidateSlug(id.WritingStyle); err != nil {
			return &ValidationError{Field: "writing_style", Message: fmt.Sprintf("invalid slug %q: %v", id.WritingStyle, err)}
		}
		store := attribute.NewStore(s.root, attribute.WritingStyles)
		if !store.Exists(id.WritingStyle) {
			return &ValidationError{
				Field:   "writing_style",
				Message: fmt.Sprintf("%q not found — create it with 'ethos writing-style create %s'", id.WritingStyle, id.WritingStyle),
			}
		}
	}
	talentStore := attribute.NewStore(s.root, attribute.Talents)
	for _, slug := range id.Talents {
		if err := attribute.ValidateSlug(slug); err != nil {
			return &ValidationError{Field: "talents", Message: fmt.Sprintf("invalid slug %q: %v", slug, err)}
		}
		if !talentStore.Exists(slug) {
			return &ValidationError{
				Field:   "talents",
				Message: fmt.Sprintf("%q not found — create it with 'ethos talent create %s'", slug, slug),
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
	return s.list(false)
}

// listNoMigrate lists identities without running voice migration.
// Used by LayeredStore.List to avoid writing ext to the wrong store.
func (s *Store) listNoMigrate() (*ListResult, error) {
	return s.list(true)
}

// list is the shared implementation for List and listNoMigrate.
func (s *Store) list(skipMigrate bool) (*ListResult, error) {
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
		var id *Identity
		var loadErr error
		if skipMigrate {
			id, loadErr = s.loadNoMigrate(handle)
		} else {
			id, loadErr = s.Load(handle, Reference(true))
		}
		if loadErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("skipping %s: %v", entry.Name(), loadErr))
			continue
		}
		result.Identities = append(result.Identities, id)
	}
	return result, nil
}

// FindBy searches for an identity where the named field matches value.
// Supported fields: "handle", "email", "github".
// Returns nil, nil when no identity matches (not an error).
// Returns an error for unsupported fields or store read failures.
func (s *Store) FindBy(field, value string) (*Identity, error) {
	switch field {
	case "handle", "email", "github":
	default:
		return nil, fmt.Errorf("unsupported FindBy field: %q", field)
	}
	if value == "" {
		return nil, nil
	}
	result, err := s.List()
	if err != nil {
		return nil, err
	}
	for _, id := range result.Identities {
		var fieldValue string
		switch field {
		case "handle":
			fieldValue = id.Handle
		case "email":
			fieldValue = id.Email
		case "github":
			fieldValue = id.GitHub
		}
		if fieldValue == value {
			return id, nil
		}
	}
	return nil, nil
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
// Used by attribute binding operations (set_personality, add_talent, etc.).
func (s *Store) Update(handle string, mutate func(*Identity) error) error {
	path := s.Path(handle)
	lockPath := path + ".lock"

	// Acquire exclusive lock.
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("creating lock file: %w", err)
	}
	defer lockFile.Close()
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
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("writing identity: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming identity: %w", err)
	}
	return nil
}

// loadNoMigrate reads an identity YAML without running voice migration.
// Used by LayeredStore.loadRaw to avoid writing ext to the wrong store.
func (s *Store) loadNoMigrate(handle string) (*Identity, error) {
	path := s.Path(handle)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("identity %q not found: %w", handle, err)
	}
	var id Identity
	if err := yaml.Unmarshal(data, &id); err != nil {
		return nil, fmt.Errorf("invalid identity file %s: %w", path, err)
	}
	return &id, nil
}

// updateNoValidate is like Update but skips ValidateRefs. Used by
// LayeredStore which runs its own cross-layer validation.
func (s *Store) updateNoValidate(handle string, mutate func(*Identity) error) error {
	path := s.Path(handle)
	lockPath := path + ".lock"

	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("creating lock file: %w", err)
	}
	defer lockFile.Close()
	if err := flock(lockFile); err != nil {
		return fmt.Errorf("acquiring lock: %w", err)
	}
	defer funlock(lockFile)

	// Use loadNoMigrate to avoid triggering voice migration as a side
	// effect. LayeredStore.loadRaw handles migration through its own
	// relocateRepoVoice path which writes ext to the correct (global)
	// store.
	id, err := s.loadNoMigrate(handle)
	if err != nil {
		return err
	}
	if err := mutate(id); err != nil {
		return err
	}
	data, err := yaml.Marshal(id)
	if err != nil {
		return fmt.Errorf("marshaling identity: %w", err)
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("writing identity: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming identity: %w", err)
	}
	return nil
}

// IdentitiesDir returns the path to the identities subdirectory.
func (s *Store) IdentitiesDir() string {
	return s.identitiesDir()
}
