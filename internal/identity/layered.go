package identity

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/punt-labs/ethos/internal/attribute"
	"gopkg.in/yaml.v3"
)

// LayeredStore implements IdentityStore with up to three layers:
// repo-local (git-tracked), bundle (read-only shared content), and
// user-global (~/.punt-labs/ethos/). Repo-local takes precedence for
// identity lookup, then bundle, then global. Extensions always resolve
// from the global layer.
//
// Under repoAuthoritative (`resolution: repo-only`, DES-057) the global
// layer is dropped from every read and writes route to the repo layer,
// so a repo that vendors an incomplete set fails loud instead of being
// silently completed from the user's home directory.
type LayeredStore struct {
	repo   *Store // may be nil (not in a git repo)
	bundle *Store // may be nil (no active bundle)
	global *Store
	// repoAuthoritative is the one policy flag read from
	// `resolution: repo-only`. It is false for every layered-mode
	// caller, which keeps that path byte-identical.
	repoAuthoritative bool
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
// or bundle may be nil. repoAuthoritative selects DES-057's repo-only
// mode; false is the layered default.
func NewLayeredStoreWithBundle(repo, bundle, global *Store, repoAuthoritative bool) *LayeredStore {
	return &LayeredStore{repo: repo, bundle: bundle, global: global, repoAuthoritative: repoAuthoritative}
}

// RepoAuthoritative reports whether this store runs in repo-only mode.
// The role, team, and attribute stores are built from the identity
// store, so they read the policy here rather than re-reading the repo
// config — one read, one answer, no chance of the layers disagreeing.
func (ls *LayeredStore) RepoAuthoritative() bool {
	return ls.repoAuthoritative
}

// Load reads an identity by handle, checking repo, then bundle, then
// global. Extensions always come from global. Attribute content resolves
// through the full repo → bundle → global chain (DES-051), regardless of
// which layer the identity record itself came from.
func (ls *LayeredStore) Load(handle string, opts ...LoadOption) (*Identity, error) {
	var cfg loadConfig
	for _, o := range opts {
		o(&cfg)
	}

	id, source, err := ls.loadRaw(handle)
	if err != nil {
		return nil, fmt.Errorf("identity %q: %w", handle, err)
	}

	// Extensions come from global in layered mode; under repo-only they
	// come from the identity's own source layer (DES-057's DES-044
	// carve-out). Live Load stays additive either way — a missing ext file
	// never bricks a running session; the completeness verdict is doctor's
	// and vendor's to render.
	extData, extWarnings := ls.extLayer(source).loadExtensions(handle)
	id.Ext = extData

	// Attribute resolution walks the full layer chain regardless of
	// which layer the identity record came from.
	if !cfg.reference {
		warnings, missing := ls.resolveAttributesLayered(id)
		id.Warnings = warnings
		// Under repo-only there is no global tail to supply what the repo
		// lacks, so a referenced-but-missing attribute is a hard error
		// rather than a warning the caller may never print.
		if ls.repoAuthoritative {
			if err := newIncompleteRepoSet(handle, missing); err != nil {
				return nil, err
			}
		}
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
	if ls.repoAuthoritative {
		// Load prefixes the handle; naming it again here reads as a stutter.
		return nil, "", fmt.Errorf("not found in %s (resolution: repo-only): %w",
			ls.readRootsDesc(), os.ErrNotExist)
	}
	id, err := ls.global.Load(handle, Reference(true))
	if err == nil {
		return id, "global", nil
	}
	return nil, "", err
}

// readRootsDesc names the layers a read consults, for diagnostics that
// must tell the user WHERE ethos looked — a repo-only miss is otherwise
// indistinguishable from a typo.
func (ls *LayeredStore) readRootsDesc() string {
	var roots []string
	for _, l := range ls.attrChain() {
		roots = append(roots, l.store.Root())
	}
	return strings.Join(roots, ", ")
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
	// The migrated ext must land in the same layer resolution will read
	// it back from — global normally, the repo layer under repo-only.
	ext, err := ls.extWriteStore(handle)
	if err != nil {
		return fmt.Errorf("relocating voice for %q: %w", handle, err)
	}
	// Write ext data before stripping the voice key. If ext writes fail,
	// the voice key remains in the YAML so no data is lost.
	if provider != "" {
		if err := ext.ExtSet(handle, "vox", "provider", provider); err != nil {
			return fmt.Errorf("setting ext/vox/provider: %w", err)
		}
	}
	if voiceID != "" {
		if err := ext.ExtSet(handle, "vox", "voice_id", voiceID); err != nil {
			return fmt.Errorf("setting ext/vox/voice_id: %w", err)
		}
	}
	delete(raw, "voice")
	return ls.repo.rewriteRaw(path, raw)
}

// resolveAttributesLayered resolves attribute content, walking the layer
// chain repo → bundle → global (skipping absent layers) and taking the
// first match. The chain does not depend on which layer the identity
// record came from: a globally-stored identity still resolves attribute
// content from the active bundle, honoring DES-051.
//
// It returns the warnings layered mode surfaces AND the same misses as
// structured refs, which repo-only mode turns into a hard error. Both
// come from one walk, so the two modes cannot disagree about what is
// missing.
func (ls *LayeredStore) resolveAttributesLayered(id *Identity) (warnings []string, missing []MissingRef) {
	chain := ls.attrChain()

	// resolve walks the chain and returns the first layer's content. It
	// falls through to a lower layer only when the slug is absent there;
	// a real read error (permission, a directory in place of the file) is
	// surfaced as a warning naming the layer, even when a lower layer then
	// supplies content — a silent fall-through would mask a precedence
	// inversion (DES-051's higher layer skipped without a word).
	resolve := func(field string, kind attribute.Kind, slug string) (string, bool) {
		var lastErr error
		for _, l := range chain {
			content, err := loadAttribute(l.store, kind, slug)
			if err == nil {
				return content, true
			}
			if !errors.Is(err, os.ErrNotExist) {
				warnings = append(warnings,
					fmt.Sprintf("%s %q unreadable in %s layer: %v", field, slug, l.name, err))
			}
			lastErr = err
		}
		// A real read error on the last layer was already warned in the
		// loop; only the not-found tail needs the "unresolved" summary, so
		// an unreadable global layer produces one warning, not two.
		if errors.Is(lastErr, os.ErrNotExist) {
			warnings = append(warnings, fmt.Sprintf("%s %q: %v", field, slug, lastErr))
		}
		// An unreadable file counts as missing too: repo-only cannot fall
		// back, so a permission error leaves the set just as incomplete as
		// an absent one.
		missing = append(missing, MissingRef{
			Kind: kind.DirName,
			Slug: slug,
			Path: attributePath(chain, kind, slug),
		})
		return "", false
	}

	if id.Personality != "" {
		if content, ok := resolve("personality", attribute.Personalities, id.Personality); ok {
			id.PersonalityContent = content
		}
	}

	if id.WritingStyle != "" {
		if content, ok := resolve("writing_style", attribute.WritingStyles, id.WritingStyle); ok {
			id.WritingStyleContent = content
		}
	}

	if len(id.Talents) > 0 {
		id.TalentContents = make([]string, len(id.Talents))
		for i, slug := range id.Talents {
			if content, ok := resolve("talent", attribute.Talents, slug); ok {
				id.TalentContents[i] = content
			}
		}
	}

	return warnings, missing
}

// attributePath names the file a miss should have occupied: the first
// (highest-precedence) layer in the chain, which is where `ethos vendor`
// writes. Returns "" when the chain is empty, which cannot happen for a
// store that resolved an identity.
func attributePath(chain []attrLayer, kind attribute.Kind, slug string) string {
	if len(chain) == 0 {
		return ""
	}
	p, err := attribute.NewStore(chain[0].store.Root(), kind).Path(slug)
	if err != nil {
		return ""
	}
	return p
}

// attrLayer pairs a store with its layer name for diagnostics.
type attrLayer struct {
	store *Store
	name  string
}

// attrChain returns the ordered list of layers to consult when resolving
// attribute content: repo, then bundle, then global, skipping any layer
// that is absent. Global is always last. The chain is the same for every
// identity regardless of its source layer (DES-051).
//
// Under repo-only the global tail is dropped, so an attribute the repo
// did not vendor stays missing rather than resolving from the user's home
// (DES-057).
func (ls *LayeredStore) attrChain() []attrLayer {
	var chain []attrLayer
	if ls.repo != nil {
		chain = append(chain, attrLayer{ls.repo, "repo"})
	}
	if ls.bundle != nil {
		chain = append(chain, attrLayer{ls.bundle, "bundle"})
	}
	if ls.repoAuthoritative {
		return chain
	}
	chain = append(chain, attrLayer{ls.global, "global"})
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

// Save writes an identity to the repo store if available, otherwise
// global (repo-only refuses when there is no repo layer — see
// writeStore). ValidateRefs checks both layers before writing. We bypass
// the inner Store.Save to avoid its single-store ValidateRefs check.
func (ls *LayeredStore) Save(id *Identity) error {
	if err := ls.ValidateRefs(id); err != nil {
		return err
	}
	s, err := ls.writeStore()
	if err != nil {
		return err
	}
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
	// Extensions live in global, even when the identity is in repo — except
	// under repo-only, where they live beside the identity in the layer
	// that just received it (s).
	extRoot := ls.global
	if ls.repoAuthoritative {
		extRoot = s
	}
	return os.MkdirAll(extRoot.ExtDir(id.Handle), 0o700)
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

	if ls.repoAuthoritative {
		return result, nil
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
	if ls.repoAuthoritative {
		return nil, nil
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
	if ls.repoAuthoritative {
		return false
	}
	return ls.global.Exists(handle)
}

// Update applies a mutation to the identity in the owning writable
// store. If the identity exists in repo, updates repo; otherwise
// updates global. Bundle-layer identities are read-only — attempting
// to update one returns an error. Uses cross-layer ValidateRefs so
// attribute references in any store are accepted.
func (ls *LayeredStore) Update(handle string, fn func(*Identity) error) error {
	var owner *Store
	switch {
	case ls.repo != nil && ls.repo.Exists(handle):
		owner = ls.repo
	case ls.bundle != nil && ls.bundle.Exists(handle):
		// Reject even when a global copy exists: bundle shadows global on
		// read, so editing global would be silently invisible.
		return fmt.Errorf("identity %q is provided by the active bundle and cannot be modified via CLI; edit the bundle directly", handle)
	case ls.repoAuthoritative:
		// The handle is not in any layer this store reads. Falling back to
		// global would edit a record repo-only can never see.
		return fmt.Errorf("identity %q not found in %s (resolution: repo-only)", handle, ls.readRootsDesc())
	default:
		owner = ls.global
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

// attrExists checks if an attribute slug exists in any layer that reads
// consult — the same chain as attrChain, so validation cannot accept a
// reference that resolution will then fail to find.
func (ls *LayeredStore) attrExists(kind attribute.Kind, slug string) bool {
	for _, l := range ls.attrChain() {
		if attribute.NewStore(l.store.Root(), kind).Exists(slug) {
			return true
		}
	}
	return false
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

// extLayer returns the store an identity's extensions read from, given
// the layer its record came from.
//
// In layered mode extensions always live in global (DES-044) —
// unchanged. Under repo-only global is never read, so ext must resolve
// from the identity's own source layer: the one `ethos vendor` copied
// the .ext/ directory into. Without this, vendor writes ext into the
// repo and resolution never looks at it, so a global-less checkout
// silently drops agent memory wiring (DES-057, consumer-found on #345).
func (ls *LayeredStore) extLayer(source string) *Store {
	if !ls.repoAuthoritative {
		return ls.global
	}
	if source == "bundle" && ls.bundle != nil {
		return ls.bundle
	}
	if ls.repo != nil {
		return ls.repo
	}
	if ls.bundle != nil {
		return ls.bundle
	}
	return ls.global
}

// extStore returns the ext read layer for a handle whose source is not
// already known, resolving the source by existence.
func (ls *LayeredStore) extStore(handle string) *Store {
	if !ls.repoAuthoritative {
		return ls.global
	}
	if ls.repo != nil && ls.repo.Exists(handle) {
		return ls.repo
	}
	if ls.bundle != nil && ls.bundle.Exists(handle) {
		return ls.bundle
	}
	return ls.extLayer("")
}

// extWriteStore returns the layer an ext write targets, or an error when
// there is none. Under repo-only a bundle-sourced identity is refused,
// matching the read-only bundle rule elsewhere: writing ext to global
// would be invisible, and writing it into the bundle would edit shared
// content the repo does not own.
func (ls *LayeredStore) extWriteStore(handle string) (*Store, error) {
	if !ls.repoAuthoritative {
		return ls.global, nil
	}
	if ls.repo != nil && ls.repo.Exists(handle) {
		return ls.repo, nil
	}
	if ls.bundle != nil && ls.bundle.Exists(handle) {
		return nil, fmt.Errorf("identity %q is provided by the active bundle and its extensions cannot be modified via CLI; edit the bundle directly", handle)
	}
	return nil, fmt.Errorf("handle %q does not exist", handle)
}

// ExtDir returns the extension directory from the layer that owns the
// handle's extensions.
func (ls *LayeredStore) ExtDir(handle string) string {
	return ls.extStore(handle).ExtDir(handle)
}

// ExtGet reads from the layer that owns the handle's extensions.
func (ls *LayeredStore) ExtGet(handle, namespace, key string) (map[string]string, error) {
	return ls.extStore(handle).ExtGet(handle, namespace, key)
}

// ExtSet writes to the global store after checking handle existence
// across both layers. Extensions live in global in layered mode, but the
// handle may exist only in repo. Under repo-only the write follows the
// identity's source layer.
func (ls *LayeredStore) ExtSet(handle, namespace, key, value string, opts ...ExtOption) error {
	if !ls.Exists(handle) {
		return fmt.Errorf("handle %q does not exist", handle)
	}
	s, err := ls.extWriteStore(handle)
	if err != nil {
		return err
	}
	return s.extSetDirect(handle, namespace, key, value, opts...)
}

// ExtDel deletes from the layer that owns the handle's extensions.
func (ls *LayeredStore) ExtDel(handle, namespace, key string, opts ...ExtOption) error {
	s, err := ls.extWriteStore(handle)
	if err != nil {
		return err
	}
	return s.ExtDel(handle, namespace, key, opts...)
}

// ExtList lists namespaces from the layer that owns the handle's
// extensions.
func (ls *LayeredStore) ExtList(handle string) ([]string, error) {
	return ls.extStore(handle).ExtList(handle)
}

// writeStore returns the store to write to: repo if available, else
// global.
//
// Under repo-only, global is not a legal target. Writing there while
// reads never consult it produces the write-then-invisible footgun
// DES-057 names: `ethos identity create foo` would land in the user's
// home and be unreadable from the very repo that created it. With no
// repo layer (identities come from a read-only bundle) there is nowhere
// legal to write, so the write is refused.
func (ls *LayeredStore) writeStore() (*Store, error) {
	if ls.repo != nil {
		return ls.repo, nil
	}
	if ls.repoAuthoritative {
		return nil, fmt.Errorf(
			"resolution: repo-only has no repo-local identity store to write to — identities are provided by the read-only bundle at %s; edit the bundle directly",
			ls.bundleRootOrEmpty())
	}
	return ls.global, nil
}

// bundleRootOrEmpty names the active bundle for diagnostics, or "(none)"
// when there is not one.
func (ls *LayeredStore) bundleRootOrEmpty() string {
	if ls.bundle != nil {
		return ls.bundle.Root()
	}
	return "(none)"
}
