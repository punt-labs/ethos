package identity

import (
	"errors"
	"fmt"
	"os"

	"github.com/punt-labs/ethos/internal/attribute"
	"gopkg.in/yaml.v3"
)

// LayeredStore implements IdentityStore with up to three layers:
// repo-local (git-tracked), bundle (read-only shared content), and
// user-global (~/.punt-labs/ethos/). Repo-local takes precedence for
// identity lookup, then bundle, then global. Extensions always resolve
// from the global layer.
type LayeredStore struct {
	repo   *Store // may be nil (not in a git repo)
	bundle *Store // may be nil (no active bundle)
	global *Store
}

// Compile-time check: *LayeredStore satisfies IdentityStore.
var _ IdentityStore = (*LayeredStore)(nil)

// NewLayeredStore creates a two-layer store. repo may be nil when
// the caller is not inside a git repository. Kept as a thin wrapper
// over NewLayeredStoreWithBundle for callers that do not participate
// in bundle resolution.
func NewLayeredStore(repo *Store, global *Store) *LayeredStore {
	return &LayeredStore{repo: repo, global: global}
}

// NewLayeredStoreWithBundle creates a three-layer store. Any of repo
// or bundle may be nil.
func NewLayeredStoreWithBundle(repo, bundle, global *Store) *LayeredStore {
	return &LayeredStore{repo: repo, bundle: bundle, global: global}
}

// Load reads an identity by handle, checking repo first then global.
// Extensions always come from global. Attribute resolution falls back
// to global when repo attributes are missing.
func (ls *LayeredStore) Load(handle string, opts ...LoadOption) (*Identity, error) {
	var cfg loadConfig
	for _, o := range opts {
		o(&cfg)
	}

	id, source, err := ls.loadRaw(handle)
	if err != nil {
		return nil, fmt.Errorf("identity %q: %w", handle, err)
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
// Returns the identity, which store it came from ("repo", "bundle", or
// "global"), and any error. Parse errors from any layer are surfaced
// (not silently fallen through). File-not-found falls through.
func (ls *LayeredStore) loadRaw(handle string) (*Identity, string, error) {
	if ls.repo != nil {
		id, err := ls.repo.loadNoMigrate(handle)
		if err == nil {
			if err := ls.relocateRepoVoice(handle); err != nil {
				return nil, "", fmt.Errorf("relocating voice for %q: %w", handle, err)
			}
			return id, "repo", nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, "", err
		}
	}
	if ls.bundle != nil {
		id, err := ls.bundle.loadNoMigrate(handle)
		if err == nil {
			return id, "bundle", nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, "", err
		}
	}
	id, err := ls.global.Load(handle, Reference(true))
	if err == nil {
		return id, "global", nil
	}
	return nil, "", err
}

// relocateRepoVoice migrates a legacy voice field from a repo identity
// into the global ext store and strips the field from the YAML. This
// ensures extensions always live in global, never in repo.
// Returns an error if ext writes or YAML rewrite fails.
func (ls *LayeredStore) relocateRepoVoice(handle string) error {
	path := ls.repo.Path(handle)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading repo identity %q: %w", handle, err)
	}
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing repo identity %q: %w", handle, err)
	}
	v, ok := raw["voice"]
	if !ok {
		return nil
	}
	vm, ok := v.(map[string]interface{})
	if !ok {
		// Non-map voice value (e.g. "voice: elevenlabs") — cannot migrate.
		// Leave the YAML untouched and surface the error.
		return fmt.Errorf("voice field has unexpected type %T; manual migration required", v)
	}
	if len(vm) == 0 {
		delete(raw, "voice")
		return ls.repo.rewriteRaw(path, raw)
	}
	provider, _ := vm["provider"].(string)
	voiceID, _ := vm["voice_id"].(string)
	// Write ext data before stripping the voice key. If ext writes fail,
	// the voice key remains in the YAML so no data is lost.
	if provider != "" {
		if err := ls.global.ExtSet(handle, "vox", "provider", provider); err != nil {
			return fmt.Errorf("setting ext/vox/provider: %w", err)
		}
	}
	if voiceID != "" {
		if err := ls.global.ExtSet(handle, "vox", "voice_id", voiceID); err != nil {
			return fmt.Errorf("setting ext/vox/voice_id: %w", err)
		}
	}
	delete(raw, "voice")
	return ls.repo.rewriteRaw(path, raw)
}

// resolveAttributesLayered resolves attribute content, trying the source
// layer first and falling through to any remaining layers for missing
// attributes. The chain is: start from source; then the layers of
// lower precedence (repo → bundle → global).
func (ls *LayeredStore) resolveAttributesLayered(id *Identity, source string) []string {
	chain := ls.attrChain(source)

	var warnings []string
	resolve := func(kind attribute.Kind, slug string) (string, error) {
		var lastErr error
		for _, s := range chain {
			content, err := loadAttribute(s, kind, slug)
			if err == nil {
				return content, nil
			}
			lastErr = err
		}
		return "", lastErr
	}

	if id.Personality != "" {
		content, err := resolve(attribute.Personalities, id.Personality)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("personality %q: %v", id.Personality, err))
		} else {
			id.PersonalityContent = content
		}
	}

	if id.WritingStyle != "" {
		content, err := resolve(attribute.WritingStyles, id.WritingStyle)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("writing_style %q: %v", id.WritingStyle, err))
		} else {
			id.WritingStyleContent = content
		}
	}

	if len(id.Talents) > 0 {
		id.TalentContents = make([]string, len(id.Talents))
		for i, slug := range id.Talents {
			content, err := resolve(attribute.Talents, slug)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("talent %q: %v", slug, err))
			} else {
				id.TalentContents[i] = content
			}
		}
	}

	return warnings
}

// attrChain returns the ordered list of stores to consult when
// resolving attribute content, starting from the identity's source
// layer and falling through to lower-precedence layers.
func (ls *LayeredStore) attrChain(source string) []*Store {
	var chain []*Store
	switch source {
	case "repo":
		if ls.repo != nil {
			chain = append(chain, ls.repo)
		}
		if ls.bundle != nil {
			chain = append(chain, ls.bundle)
		}
		chain = append(chain, ls.global)
	case "bundle":
		if ls.bundle != nil {
			chain = append(chain, ls.bundle)
		}
		chain = append(chain, ls.global)
	default:
		chain = append(chain, ls.global)
	}
	return chain
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
	// Extensions always live in global, even when the identity is in repo.
	return os.MkdirAll(ls.global.ExtDir(id.Handle), 0o700)
}

// List returns identities from all layers, deduplicated by handle.
// Precedence on collision: repo > bundle > global. Returned identities
// are in reference mode (attribute slugs only, no resolved .md content),
// consistent with Store.List.
func (ls *LayeredStore) List() (*ListResult, error) {
	seen := make(map[string]bool)
	result := &ListResult{}

	if ls.repo != nil {
		repoResult, err := ls.repo.listNoMigrate()
		if err != nil {
			return nil, fmt.Errorf("listing repo identities: %w", err)
		}
		for _, id := range repoResult.Identities {
			if seen[id.Handle] {
				continue
			}
			seen[id.Handle] = true
			result.Identities = append(result.Identities, id)
		}
		result.Warnings = append(result.Warnings, repoResult.Warnings...)
	}

	if ls.bundle != nil {
		bundleResult, err := ls.bundle.listNoMigrate()
		if err != nil {
			return nil, fmt.Errorf("listing bundle identities: %w", err)
		}
		for _, id := range bundleResult.Identities {
			if seen[id.Handle] {
				continue
			}
			seen[id.Handle] = true
			result.Identities = append(result.Identities, id)
		}
		result.Warnings = append(result.Warnings, bundleResult.Warnings...)
	}

	globalResult, err := ls.global.List()
	if err != nil {
		return nil, fmt.Errorf("listing global identities: %w", err)
	}
	for _, id := range globalResult.Identities {
		if seen[id.Handle] {
			continue
		}
		seen[id.Handle] = true
		result.Identities = append(result.Identities, id)
	}
	result.Warnings = append(result.Warnings, globalResult.Warnings...)

	return result, nil
}

// FindBy searches repo, then bundle, then global. Propagates I/O errors
// from any layer. Falls through only when a layer returns no match.
func (ls *LayeredStore) FindBy(field, value string) (*Identity, error) {
	if ls.repo != nil {
		id, err := ls.repo.FindBy(field, value)
		if err != nil {
			return nil, fmt.Errorf("repo FindBy: %w", err)
		}
		if id != nil {
			return id, nil
		}
	}
	if ls.bundle != nil {
		id, err := ls.bundle.FindBy(field, value)
		if err != nil {
			return nil, fmt.Errorf("bundle FindBy: %w", err)
		}
		if id != nil {
			return id, nil
		}
	}
	return ls.global.FindBy(field, value)
}

// Exists returns true if the handle exists in any layer.
func (ls *LayeredStore) Exists(handle string) bool {
	if ls.repo != nil && ls.repo.Exists(handle) {
		return true
	}
	if ls.bundle != nil && ls.bundle.Exists(handle) {
		return true
	}
	return ls.global.Exists(handle)
}

// Update applies a mutation to the identity in the owning writable
// store. If the identity exists in repo, updates repo; otherwise
// updates global. Bundle-layer identities are read-only — attempting
// to update one returns an error. Uses cross-layer ValidateRefs so
// attribute references in any store are accepted.
func (ls *LayeredStore) Update(handle string, fn func(*Identity) error) error {
	owner := ls.global
	switch {
	case ls.repo != nil && ls.repo.Exists(handle):
		owner = ls.repo
	case ls.bundle != nil && ls.bundle.Exists(handle) && !ls.global.Exists(handle):
		return fmt.Errorf("identity %q is bundle-only and cannot be modified via CLI; edit the bundle directly", handle)
	}
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

// attrExists checks if an attribute slug exists in any layer.
func (ls *LayeredStore) attrExists(kind attribute.Kind, slug string) bool {
	if ls.repo != nil {
		if attribute.NewStore(ls.repo.Root(), kind).Exists(slug) {
			return true
		}
	}
	if ls.bundle != nil {
		if attribute.NewStore(ls.bundle.Root(), kind).Exists(slug) {
			return true
		}
	}
	return attribute.NewStore(ls.global.Root(), kind).Exists(slug)
}

// Root returns the repo root if available, otherwise global root.
func (ls *LayeredStore) Root() string {
	if ls.repo != nil {
		return ls.repo.Root()
	}
	return ls.global.Root()
}

// GlobalRoot returns the global store's root directory.
func (ls *LayeredStore) GlobalRoot() string {
	return ls.global.Root()
}

// RepoRoot returns the repo store's root directory, or empty string if
// there is no repo layer.
func (ls *LayeredStore) RepoRoot() string {
	if ls.repo != nil {
		return ls.repo.Root()
	}
	return ""
}

// BundleRoot returns the active bundle's root directory, or empty string
// if no bundle is active.
func (ls *LayeredStore) BundleRoot() string {
	if ls.bundle != nil {
		return ls.bundle.Root()
	}
	return ""
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
func (ls *LayeredStore) ExtDir(handle string) string {
	return ls.global.ExtDir(handle)
}

// ExtGet delegates to the global store.
func (ls *LayeredStore) ExtGet(handle, namespace, key string) (map[string]string, error) {
	return ls.global.ExtGet(handle, namespace, key)
}

// ExtSet writes to the global store after checking handle existence
// across both layers. Extensions always live in global, but the handle
// may exist only in repo.
func (ls *LayeredStore) ExtSet(handle, namespace, key, value string) error {
	if !ls.Exists(handle) {
		return fmt.Errorf("handle %q does not exist", handle)
	}
	return ls.global.extSetDirect(handle, namespace, key, value)
}

// ExtDel delegates to the global store.
func (ls *LayeredStore) ExtDel(handle, namespace, key string) error {
	return ls.global.ExtDel(handle, namespace, key)
}

// ExtList delegates to the global store.
func (ls *LayeredStore) ExtList(handle string) ([]string, error) {
	return ls.global.ExtList(handle)
}

// writeStore returns the store to write to: repo if available, else global.
func (ls *LayeredStore) writeStore() *Store {
	if ls.repo != nil {
		return ls.repo
	}
	return ls.global
}
