package identity

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Extension validation constraints.
const (
	MaxNamespaceLen    = 32
	MaxKeyLen          = 64
	MaxValueLen        = 4096
	MaxKeysPerNS       = 64
	MaxNamespacesPerID = 32
)

// ExtLocalSuffix marks secret or machine-specific namespace overlays. An
// explicit repository policy places them under LocalRoot/identities; global
// and bundle reads retain the sibling layout from DES-057 Part C. Source-side
// repository siblings are compatibility reads only. Vendor always skips them.
const ExtLocalSuffix = ".local.yaml"

var (
	validNamespace = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	validExtKey    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// ExtDir returns the extension directory path for the given handle.
func (s *Store) ExtDir(handle string) string {
	if handle == "" {
		return filepath.Join(s.identitiesDir(), ".ext")
	}
	return filepath.Join(s.identitiesDir(), filepath.Base(handle)+".ext")
}

// extPath returns the path to a namespace's base file — the git-tracked
// half, the one vendor copies.
func (s *Store) extPath(handle, namespace string) string {
	return filepath.Join(s.ExtDir(handle), filepath.Base(namespace)+".yaml")
}

// extLocalPath returns the write destination for a namespace's local overlay,
// not its legacy repository compatibility location.
func (s *Store) extLocalPath(handle, namespace string) string {
	if s.config.repository {
		return filepath.Join(s.config.policy.LocalRoot(), "identities", filepath.Base(handle)+".ext", filepath.Base(namespace)+ExtLocalSuffix)
	}
	return s.legacyExtLocalPath(handle, namespace)
}

func (s *Store) legacyExtLocalPath(handle, namespace string) string {
	return filepath.Join(s.ExtDir(handle), filepath.Base(namespace)+ExtLocalSuffix)
}

// OverlayConflictError means two supported locations contain different local
// data. Its diagnostic names paths only, never keys or secret values.
type OverlayConflictError struct {
	Legacy string
	Local  string
}

func (e *OverlayConflictError) Error() string {
	return fmt.Sprintf("divergent local extension overlays at %s and %s; the storage upgrade keeps the one in the local zone and deletes the legacy copy", e.Legacy, e.Local)
}

// readLocalNamespace permits compatibility reads only. Writers never copy or
// delete the legacy source-side overlay; the storage upgrade (RelocateLegacyOverlays) moves it.
func (s *Store) readLocalNamespace(handle, namespace string) (map[string]string, error) {
	if err := s.validatePolicy(); err != nil {
		return nil, err
	}
	local, err := s.readExtFile(s.extLocalPath(handle, namespace))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if !s.config.repository {
		return local, err
	}
	legacy, legacyErr := s.readExtFile(s.legacyExtLocalPath(handle, namespace))
	if legacyErr != nil && !errors.Is(legacyErr, os.ErrNotExist) {
		return nil, legacyErr
	}
	if local != nil && legacy != nil && !reflect.DeepEqual(local, legacy) {
		return nil, &OverlayConflictError{Legacy: s.legacyExtLocalPath(handle, namespace), Local: s.extLocalPath(handle, namespace)}
	}
	if local != nil {
		return local, nil
	}
	return legacy, legacyErr
}

func (s *Store) prepareExtWrite(handle, namespace string, local bool) error {
	if err := s.validatePolicy(); err != nil {
		return err
	}
	if !s.config.repository {
		return os.MkdirAll(s.ExtDir(handle), 0o700)
	}
	if err := s.checkPath(s.extPath(handle, namespace), false); err != nil {
		return err
	}
	if _, err := s.readLocalNamespace(handle, namespace); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if local {
		// Editing only the new file while a compatibility copy remains would
		// create divergence or resurrect deleted keys on the next read.
		legacy := s.legacyExtLocalPath(handle, namespace)
		if _, err := os.Lstat(legacy); err == nil {
			return fmt.Errorf("legacy local overlay at %s is not in the local zone yet; the storage upgrade moves it to %s, and writes resume once it has", legacy, s.extLocalPath(handle, namespace))
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("examining legacy local overlay: %w", err)
		}
		path, err := s.config.policy.LocalExtensionPath(filepath.Base(handle), namespace)
		if err != nil {
			return err
		}
		return s.ensureDir(filepath.Dir(path))
	}
	return s.ensureDir(s.ExtDir(handle))
}

// extWritePath returns the file an ext write targets: the .local companion
// when local is set, else the base file.
func (s *Store) extWritePath(handle, namespace string, local bool) string {
	if local {
		return s.extLocalPath(handle, namespace)
	}
	return s.extPath(handle, namespace)
}

// ExtOption configures which file an extension write targets.
type ExtOption func(*extConfig)

type extConfig struct {
	local bool
}

// Local returns an ExtOption selecting the <ns>.local.yaml overlay instead of
// the base <ns>.yaml file. Repository overlays live in the policy's local tree.
func Local(v bool) ExtOption {
	return func(c *extConfig) { c.local = v }
}

// extOpts folds a variadic option list into its config.
func extOpts(opts []ExtOption) extConfig {
	var c extConfig
	for _, o := range opts {
		o(&c)
	}
	return c
}

// readNamespace returns one namespace's merged view: the base
// <ns>.yaml overlaid with <ns>.local.yaml, where .local wins per key
// (DES-057 Part C). It is the single read path for every ext consumer,
// so base-only and base+local layouts cannot diverge.
//
// The result is a READ-ONLY view and is never marshalled back to a file:
// writing it to the base file would fold .local secrets into the
// committable half. Writers target one file (see extSetDirect).
//
// Returns an error satisfying errors.Is(err, os.ErrNotExist) when neither
// file exists. A namespace with only a .local file exists.
func (s *Store) readNamespace(handle, namespace string) (map[string]string, error) {
	if err := s.validatePolicy(); err != nil {
		return nil, err
	}
	base, baseErr := s.readExtFile(s.extPath(handle, namespace))
	if baseErr != nil && !errors.Is(baseErr, os.ErrNotExist) {
		return nil, baseErr
	}
	local, localErr := s.readLocalNamespace(handle, namespace)
	if localErr != nil && !errors.Is(localErr, os.ErrNotExist) {
		return nil, localErr
	}
	if base == nil && local == nil {
		return nil, fmt.Errorf("namespace %q not found for %q: %w", namespace, handle, os.ErrNotExist)
	}
	m := make(map[string]string, len(base)+len(local))
	for k, v := range base {
		m[k] = v
	}
	for k, v := range local {
		m[k] = v
	}
	return m, nil
}

// readExtFile reads one namespace file. A present-but-empty file yields
// an empty (non-nil) map, so callers distinguish "exists, no keys" from
// "absent" by the nil map alone.
func (s *Store) readExtFile(path string) (map[string]string, error) {
	data, err := s.readFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("invalid extension file %s: %w", path, err)
	}
	if m == nil {
		m = make(map[string]string)
	}
	return m, nil
}

// ExtGet reads a single key from a namespace, or all keys if key is empty.
// Values come from the base file overlaid with .local.
func (s *Store) ExtGet(handle, namespace, key string) (map[string]string, error) {
	if handle == "" {
		return nil, fmt.Errorf("handle is required")
	}
	if err := validateNamespace(namespace); err != nil {
		return nil, err
	}
	if key != "" {
		if err := validateExtKey(key); err != nil {
			return nil, err
		}
	}
	m, err := s.readNamespace(handle, namespace)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("namespace %q not found for %q", namespace, handle)
		}
		return nil, err
	}
	if key != "" {
		v, ok := m[key]
		if !ok {
			return nil, fmt.Errorf("key %q not found in %q/%q", key, handle, namespace)
		}
		return map[string]string{key: v}, nil
	}
	return m, nil
}

// ExtSet writes a key-value pair to a namespace. Local(true) targets the
// <ns>.local.yaml companion; the default targets the base file.
// A *PublicationError means the write applied but crash durability is unconfirmed.
func (s *Store) ExtSet(handle, namespace, key, value string, opts ...ExtOption) error {
	if handle == "" {
		return fmt.Errorf("handle is required")
	}
	if err := s.checkPath(s.Path(handle), false); err != nil {
		return err
	}
	// Ensure the handle exists in this store.
	if !s.Exists(handle) {
		return fmt.Errorf("handle %q does not exist", handle)
	}
	return s.extSetDirect(handle, namespace, key, value, opts...)
}

// extSetDirect writes a key-value pair to a namespace without checking
// handle existence. Used by LayeredStore which performs its own
// cross-layer existence check before delegating.
//
// It reads, mutates, and writes ONE file — never the merged view — so a
// base write cannot fold a .local value into the committable file.
func (s *Store) extSetDirect(handle, namespace, key, value string, opts ...ExtOption) error {
	cfg := extOpts(opts)
	if err := validateExtWrite(namespace, key, value); err != nil {
		return err
	}
	if s.config.repository {
		return s.withLock(handle, func() error {
			return s.extSetUnlocked(handle, namespace, key, value, cfg)
		})
	}
	return s.extSetUnlocked(handle, namespace, key, value, cfg)
}

func validateExtWrite(namespace, key, value string) error {
	if err := validateNamespace(namespace); err != nil {
		return err
	}
	if err := validateExtKey(key); err != nil {
		return err
	}
	if len(value) > MaxValueLen {
		return fmt.Errorf("value exceeds maximum length of %d bytes", MaxValueLen)
	}
	return nil
}

func (s *Store) extSetUnlocked(handle, namespace, key, value string, cfg extConfig) error {
	// Legacy voice migration shares this path while already holding the lock.
	if err := validateExtWrite(namespace, key, value); err != nil {
		return err
	}
	// Check namespace count limit.
	if err := s.prepareExtWrite(handle, namespace, cfg.local); err != nil {
		return fmt.Errorf("creating extension directory: %w", err)
	}

	// Load existing namespace data.
	path := s.extWritePath(handle, namespace, cfg.local)
	var m map[string]string
	if data, err := s.readFile(path); err == nil {
		if err := yaml.Unmarshal(data, &m); err != nil {
			return fmt.Errorf("corrupt extension file %s: %w", path, err)
		}
		if m == nil {
			m = make(map[string]string)
		}
	} else if os.IsNotExist(err) {
		// The TARGET FILE is new — but a namespace is two files, so that
		// is not the same as a new namespace. Adding the .local companion
		// to an existing base (or a base to a .local-only namespace)
		// creates no namespace, and charging it against the limit would
		// refuse the Part C secret split exactly when an identity is at
		// capacity (Bugbot, PR #410). Repository writers hold the handle lock.
		exists, err := s.namespaceExists(handle, namespace)
		if err != nil {
			return err
		}
		if !exists {
			if err := s.checkNamespaceLimit(handle); err != nil {
				return err
			}
		}
		m = make(map[string]string)
	} else {
		return fmt.Errorf("reading extension file: %w", err)
	}

	// Check key count limit.
	if _, exists := m[key]; !exists && len(m) >= MaxKeysPerNS {
		return fmt.Errorf("namespace %q already has %d keys (max %d)", namespace, len(m), MaxKeysPerNS)
	}

	m[key] = value
	data, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshaling extension: %w", err)
	}
	return s.writeFile(path, data, false)
}

// ExtDel deletes a key from a namespace, or the entire namespace if key
// is empty. It targets ONE file — the base by default, the .local
// companion under Local(true) — not the merged view, so deleting a base
// key never silently removes the .local value shadowing it.
// A *PublicationError means deletion applied but crash durability is unconfirmed.
func (s *Store) ExtDel(handle, namespace, key string, opts ...ExtOption) error {
	cfg := extOpts(opts)
	if handle == "" {
		return fmt.Errorf("handle is required")
	}
	if err := validateNamespace(namespace); err != nil {
		return err
	}
	if key != "" {
		if err := validateExtKey(key); err != nil {
			return err
		}
	}
	if s.config.repository {
		return s.withLock(handle, func() error {
			return s.extDelUnlocked(handle, namespace, key, cfg)
		})
	}
	return s.extDelUnlocked(handle, namespace, key, cfg)
}

func (s *Store) extDelUnlocked(handle, namespace, key string, cfg extConfig) error {
	if s.config.repository {
		if err := s.prepareExtWrite(handle, namespace, cfg.local); err != nil {
			return err
		}
	}
	path := s.extWritePath(handle, namespace, cfg.local)
	if key == "" {
		// Delete entire namespace file.
		if err := s.removeFile(path); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("namespace %q not found for %q", namespace, handle)
			}
			return fmt.Errorf("deleting namespace: %w", err)
		}
		return nil
	}

	// Delete single key.
	data, err := s.readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("namespace %q not found for %q", namespace, handle)
		}
		return fmt.Errorf("reading extension: %w", err)
	}
	var m map[string]string
	if err := yaml.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("invalid extension file: %w", err)
	}
	if _, ok := m[key]; !ok {
		return fmt.Errorf("key %q not found in %q/%q", key, handle, namespace)
	}
	delete(m, key)
	if len(m) == 0 {
		return s.removeFile(path)
	}
	out, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshaling extension: %w", err)
	}
	return s.writeFile(path, out, false)
}

// ExtList returns all namespace names for a handle: the union of those
// with a base file and those with only a .local companion, deduplicated.
//
// The .local.yaml suffix is stripped AS A UNIT and tested BEFORE the
// plain .yaml case. Testing .yaml first would leave "quarry.local" — a
// phantom namespace no read or write path can address.
func (s *Store) ExtList(handle string) ([]string, error) {
	if handle == "" {
		return nil, fmt.Errorf("handle is required")
	}
	if err := s.validatePolicy(); err != nil {
		return nil, err
	}
	var namespaces []string
	seen := make(map[string]bool)
	dirs := []string{s.ExtDir(handle)}
	if s.config.repository {
		dirs = append(dirs, filepath.Dir(s.extLocalPath(handle, "unused")))
	}
	for i, dir := range dirs {
		entries, err := s.readDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading extension directory: %w", err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			var ns string
			switch {
			case strings.HasSuffix(e.Name(), ExtLocalSuffix):
				ns = strings.TrimSuffix(e.Name(), ExtLocalSuffix)
			case strings.HasSuffix(e.Name(), ".yaml"):
				if i > 0 {
					continue // the local tree supplies overlays only, never bases
				}
				ns = strings.TrimSuffix(e.Name(), ".yaml")
			default:
				continue
			}
			if err := s.checkPath(filepath.Join(dir, e.Name()), false); err != nil {
				return nil, err
			}
			if ns == "" || seen[ns] {
				continue
			}
			seen[ns] = true
			namespaces = append(namespaces, ns)
		}
	}
	sort.Strings(namespaces)
	if s.config.repository {
		for _, ns := range namespaces {
			if _, err := s.readLocalNamespace(handle, ns); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
	}
	return namespaces, nil
}

// loadExtensions reads all extension namespaces for a handle and returns
// the merged map and any warnings for unreadable/corrupt files.
// Called by Store.Load to assemble the full identity view.
func (s *Store) loadExtensions(handle string) (map[string]map[string]string, []string, error) {
	namespaces, err := s.ExtList(handle)
	if err != nil {
		// ExtList handles os.IsNotExist internally (returns nil, nil),
		// so any error here is a real failure worth surfacing.
		var conflict *OverlayConflictError
		var boundary *repositoryPathError
		if errors.As(err, &conflict) || errors.As(err, &boundary) {
			return nil, nil, err
		}
		return map[string]map[string]string{}, []string{
			fmt.Sprintf("extensions %s: %v", handle, err),
		}, nil
	}
	if len(namespaces) == 0 {
		return map[string]map[string]string{}, nil, nil
	}
	ext := make(map[string]map[string]string, len(namespaces))
	var warnings []string
	for _, ns := range namespaces {
		m, err := s.readNamespace(handle, ns)
		if err != nil {
			var conflict *OverlayConflictError
			var boundary *repositoryPathError
			if errors.As(err, &conflict) || errors.As(err, &boundary) {
				return nil, nil, err
			}
			warnings = append(warnings, fmt.Sprintf("extension %s/%s: %v", handle, ns, err))
			continue
		}
		ext[ns] = m
	}
	return ext, warnings, nil
}

// namespaceExists reports whether either half of a namespace is already
// on disk. It is the counterpart of ExtList's union: both must agree on
// what "a namespace" is, or the limit and the listing disagree about how
// many there are.
func (s *Store) namespaceExists(handle, namespace string) (bool, error) {
	for _, p := range []string{
		s.extPath(handle, namespace),
		s.extLocalPath(handle, namespace),
		s.legacyExtLocalPath(handle, namespace),
	} {
		if err := s.checkPath(p, false); err != nil {
			return false, err
		}
		if _, err := os.Stat(p); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

func (s *Store) checkNamespaceLimit(handle string) error {
	namespaces, err := s.ExtList(handle)
	if err != nil {
		return err
	}
	if len(namespaces) >= MaxNamespacesPerID {
		return fmt.Errorf("handle %q already has %d namespaces (max %d)", handle, len(namespaces), MaxNamespacesPerID)
	}
	return nil
}

func validateNamespace(ns string) error {
	if len(ns) > MaxNamespaceLen {
		return fmt.Errorf("namespace exceeds maximum length of %d characters", MaxNamespaceLen)
	}
	if !validNamespace.MatchString(ns) {
		return fmt.Errorf("namespace must match %s", validNamespace.String())
	}
	return nil
}

func validateExtKey(key string) error {
	if len(key) > MaxKeyLen {
		return fmt.Errorf("key exceeds maximum length of %d characters", MaxKeyLen)
	}
	if !validExtKey.MatchString(key) {
		return fmt.Errorf("key must match %s", validExtKey.String())
	}
	return nil
}
