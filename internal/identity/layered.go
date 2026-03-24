package identity

import (
	"fmt"
	"os"

	"github.com/punt-labs/ethos/internal/attribute"
	"gopkg.in/yaml.v3"
)

// LayeredStore implements IdentityStore with two layers:
// repo-local (git-tracked) and user-global (~/.punt-labs/ethos/).
// Repo-local takes precedence for identity lookup. Extensions
// always resolve from the global layer.
type LayeredStore struct {
	repo   *Store // may be nil (not in a git repo)
	global *Store
}

// Compile-time check: *LayeredStore satisfies IdentityStore.
var _ IdentityStore = (*LayeredStore)(nil)

// NewLayeredStore creates a two-layer store. repo may be nil when
// the caller is not inside a git repository.
func NewLayeredStore(repo *Store, global *Store) *LayeredStore {
	return &LayeredStore{repo: repo, global: global}
}

// Load reads an identity by handle, checking repo first then global.
// Extensions always come from global. Attribute resolution falls back
// to global when repo attributes are missing.
func (ls *LayeredStore) Load(handle string, opts ...LoadOption) (*Identity, error) {
	var cfg loadConfig
	for _, o := range opts {
		o(&cfg)
	}

	id, source := ls.loadRaw(handle, opts...)
	if id == nil {
		return nil, fmt.Errorf("identity %q not found in repo or global store", handle)
	}

	// Extensions always come from global.
	extData, extWarnings := ls.global.loadExtensions(handle)
	id.Ext = extData

	// Attribute resolution: try repo first, fall back to global.
	if !cfg.reference {
		id.Warnings = ls.resolveAttributesLayered(id, source)
	}
	id.Warnings = append(id.Warnings, extWarnings...)

	return id, nil
}

// loadRaw loads the identity YAML without attribute resolution or ext.
// Returns the identity and which store it came from ("repo" or "global").
// Uses loadNoMigrate to avoid voice migration writing ext to the repo
// store — extensions always belong in global.
func (ls *LayeredStore) loadRaw(handle string, opts ...LoadOption) (*Identity, string) {
	if ls.repo != nil {
		id, err := ls.repo.loadNoMigrate(handle)
		if err == nil {
			ls.relocateRepoVoice(handle, id)
			return id, "repo"
		}
	}
	id, err := ls.global.Load(handle, Reference(true))
	if err == nil {
		return id, "global"
	}
	return nil, ""
}

// relocateRepoVoice migrates a legacy voice field from a repo identity
// into the global ext store and strips the field from the YAML. This
// ensures extensions always live in global, never in repo.
func (ls *LayeredStore) relocateRepoVoice(handle string, id *Identity) {
	path := ls.repo.Path(handle)
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return
	}
	v, ok := raw["voice"]
	if !ok {
		return
	}
	vm, ok := v.(map[string]interface{})
	if !ok || len(vm) == 0 {
		delete(raw, "voice")
		_ = ls.repo.rewriteRaw(path, raw)
		return
	}
	provider, _ := vm["provider"].(string)
	voiceID, _ := vm["voice_id"].(string)
	if provider != "" {
		_ = ls.global.ExtSet(handle, "vox", "provider", provider)
	}
	if voiceID != "" {
		_ = ls.global.ExtSet(handle, "vox", "voice_id", voiceID)
	}
	delete(raw, "voice")
	_ = ls.repo.rewriteRaw(path, raw)
}

// resolveAttributesLayered resolves attribute content, trying the source
// store first and falling back to global for any missing attributes.
func (ls *LayeredStore) resolveAttributesLayered(id *Identity, source string) []string {
	var primary, fallback *Store
	if source == "repo" && ls.repo != nil {
		primary = ls.repo
		fallback = ls.global
	} else {
		primary = ls.global
		fallback = nil
	}

	var warnings []string

	if id.Personality != "" {
		content, err := loadAttribute(primary, attribute.Personalities, id.Personality)
		if err != nil && fallback != nil {
			content, err = loadAttribute(fallback, attribute.Personalities, id.Personality)
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("personality %q: %v", id.Personality, err))
		} else {
			id.PersonalityContent = content
		}
	}

	if id.WritingStyle != "" {
		content, err := loadAttribute(primary, attribute.WritingStyles, id.WritingStyle)
		if err != nil && fallback != nil {
			content, err = loadAttribute(fallback, attribute.WritingStyles, id.WritingStyle)
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("writing_style %q: %v", id.WritingStyle, err))
		} else {
			id.WritingStyleContent = content
		}
	}

	if len(id.Talents) > 0 {
		id.TalentContents = make([]string, len(id.Talents))
		for i, slug := range id.Talents {
			content, err := loadAttribute(primary, attribute.Talents, slug)
			if err != nil && fallback != nil {
				content, err = loadAttribute(fallback, attribute.Talents, slug)
			}
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("talent %q: %v", slug, err))
			} else {
				id.TalentContents[i] = content
			}
		}
	}

	return warnings
}

// loadAttribute loads a single attribute's content from a store.
func loadAttribute(s *Store, kind attribute.Kind, slug string) (string, error) {
	store := attribute.NewStore(s.Root(), kind)
	a, err := store.Load(slug)
	if err != nil {
		return "", err
	}
	return a.Content, nil
}

// Save writes an identity to the repo store if available, otherwise global.
// ValidateRefs checks both layers before writing. We bypass the inner
// Store.Save to avoid its single-store ValidateRefs check.
func (ls *LayeredStore) Save(id *Identity) error {
	if err := ls.ValidateRefs(id); err != nil {
		return err
	}
	s := ls.writeStore()
	dir := s.IdentitiesDir()
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
	return os.MkdirAll(s.ExtDir(id.Handle), 0o700)
}

// List returns identities from both stores, deduplicated by handle.
// Repo identities win on collision. Returned identities are in
// reference mode (attribute slugs only, no resolved .md content),
// consistent with Store.List.
func (ls *LayeredStore) List() (*ListResult, error) {
	var repoResult *ListResult
	if ls.repo != nil {
		var err error
		repoResult, err = ls.repo.List()
		if err != nil {
			return nil, fmt.Errorf("listing repo identities: %w", err)
		}
	}

	globalResult, err := ls.global.List()
	if err != nil {
		return nil, fmt.Errorf("listing global identities: %w", err)
	}

	// Merge: repo wins on handle collision.
	seen := make(map[string]bool)
	result := &ListResult{}

	if repoResult != nil {
		for _, id := range repoResult.Identities {
			seen[id.Handle] = true
			result.Identities = append(result.Identities, id)
		}
		result.Warnings = append(result.Warnings, repoResult.Warnings...)
	}

	for _, id := range globalResult.Identities {
		if !seen[id.Handle] {
			result.Identities = append(result.Identities, id)
		}
	}
	result.Warnings = append(result.Warnings, globalResult.Warnings...)

	return result, nil
}

// FindBy searches repo first, then global. If repo returns an error
// but no identity (e.g., I/O failure reading the directory), falls
// through to global rather than aborting.
func (ls *LayeredStore) FindBy(field, value string) (*Identity, error) {
	if ls.repo != nil {
		id, err := ls.repo.FindBy(field, value)
		if id != nil {
			return id, err
		}
		// repo had nothing or errored — fall through to global.
	}
	return ls.global.FindBy(field, value)
}

// Exists returns true if the handle exists in either store.
func (ls *LayeredStore) Exists(handle string) bool {
	if ls.repo != nil && ls.repo.Exists(handle) {
		return true
	}
	return ls.global.Exists(handle)
}

// Update applies a mutation to the identity in the owning store.
// If the identity exists in repo, updates repo; otherwise updates global.
// Uses cross-layer ValidateRefs so attribute references in either store
// are accepted. Bypasses the inner Store.ValidateRefs which only checks
// a single layer.
func (ls *LayeredStore) Update(handle string, fn func(*Identity) error) error {
	owner := ls.global
	if ls.repo != nil && ls.repo.Exists(handle) {
		owner = ls.repo
	}
	// Wrap the mutation to include cross-layer validation.
	validated := func(id *Identity) error {
		if err := fn(id); err != nil {
			return err
		}
		return ls.ValidateRefs(id)
	}
	return owner.updateNoValidate(handle, validated)
}

// ValidateRefs checks that attribute references exist in either layer.
func (ls *LayeredStore) ValidateRefs(id *Identity) error {
	if id.Personality != "" {
		if err := attribute.ValidateSlug(id.Personality); err != nil {
			return &ValidationError{Field: "personality", Message: fmt.Sprintf("invalid slug %q: %v", id.Personality, err)}
		}
		if !ls.attrExists(attribute.Personalities, id.Personality) {
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
		if !ls.attrExists(attribute.WritingStyles, id.WritingStyle) {
			return &ValidationError{
				Field:   "writing_style",
				Message: fmt.Sprintf("%q not found — create it with 'ethos writing-style create %s'", id.WritingStyle, id.WritingStyle),
			}
		}
	}
	for _, slug := range id.Talents {
		if err := attribute.ValidateSlug(slug); err != nil {
			return &ValidationError{Field: "talents", Message: fmt.Sprintf("invalid slug %q: %v", slug, err)}
		}
		if !ls.attrExists(attribute.Talents, slug) {
			return &ValidationError{
				Field:   "talents",
				Message: fmt.Sprintf("%q not found — create it with 'ethos talent create %s'", slug, slug),
			}
		}
	}
	return nil
}

// attrExists checks if an attribute slug exists in repo or global.
func (ls *LayeredStore) attrExists(kind attribute.Kind, slug string) bool {
	if ls.repo != nil {
		s := attribute.NewStore(ls.repo.Root(), kind)
		if s.Exists(slug) {
			return true
		}
	}
	s := attribute.NewStore(ls.global.Root(), kind)
	return s.Exists(slug)
}

// Root returns the repo root if available, otherwise global root.
func (ls *LayeredStore) Root() string {
	if ls.repo != nil {
		return ls.repo.Root()
	}
	return ls.global.Root()
}

// IdentitiesDir returns the identities directory of the primary store.
func (ls *LayeredStore) IdentitiesDir() string {
	if ls.repo != nil {
		return ls.repo.IdentitiesDir()
	}
	return ls.global.IdentitiesDir()
}

// Path returns the filesystem path for the given handle in the primary store.
func (ls *LayeredStore) Path(handle string) string {
	if ls.repo != nil {
		return ls.repo.Path(handle)
	}
	return ls.global.Path(handle)
}

// ExtDir returns the extension directory from the global store.
func (ls *LayeredStore) ExtDir(persona string) string {
	return ls.global.ExtDir(persona)
}

// ExtGet delegates to the global store.
func (ls *LayeredStore) ExtGet(persona, namespace, key string) (map[string]string, error) {
	return ls.global.ExtGet(persona, namespace, key)
}

// ExtSet delegates to the global store.
func (ls *LayeredStore) ExtSet(persona, namespace, key, value string) error {
	return ls.global.ExtSet(persona, namespace, key, value)
}

// ExtDel delegates to the global store.
func (ls *LayeredStore) ExtDel(persona, namespace, key string) error {
	return ls.global.ExtDel(persona, namespace, key)
}

// ExtList delegates to the global store.
func (ls *LayeredStore) ExtList(persona string) ([]string, error) {
	return ls.global.ExtList(persona)
}

// writeStore returns the store to write to: repo if available, else global.
func (ls *LayeredStore) writeStore() *Store {
	if ls.repo != nil {
		return ls.repo
	}
	return ls.global
}
