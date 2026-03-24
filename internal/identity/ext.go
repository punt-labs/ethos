package identity

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

var (
	validNamespace = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	validExtKey    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// ExtDir returns the extension directory path for the given persona.
func (s *Store) ExtDir(persona string) string {
	return filepath.Join(s.identitiesDir(), filepath.Base(persona)+".ext")
}

// extPath returns the path to a specific namespace file.
func (s *Store) extPath(persona, namespace string) string {
	return filepath.Join(s.ExtDir(persona), filepath.Base(namespace)+".yaml")
}

// ExtGet reads a single key from a namespace, or all keys if key is empty.
func (s *Store) ExtGet(persona, namespace, key string) (map[string]string, error) {
	if err := validateNamespace(namespace); err != nil {
		return nil, err
	}
	if key != "" {
		if err := validateExtKey(key); err != nil {
			return nil, err
		}
	}
	data, err := os.ReadFile(s.extPath(persona, namespace))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("namespace %q not found for %q", namespace, persona)
		}
		return nil, fmt.Errorf("reading extension: %w", err)
	}
	var m map[string]string
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("invalid extension file: %w", err)
	}
	if m == nil {
		m = make(map[string]string)
	}
	if key != "" {
		v, ok := m[key]
		if !ok {
			return nil, fmt.Errorf("key %q not found in %q/%q", key, persona, namespace)
		}
		return map[string]string{key: v}, nil
	}
	return m, nil
}

// ExtSet writes a key-value pair to a namespace.
func (s *Store) ExtSet(persona, namespace, key, value string) error {
	// Ensure the persona exists in this store.
	if !s.Exists(persona) {
		return fmt.Errorf("persona %q does not exist", persona)
	}
	return s.extSetDirect(persona, namespace, key, value)
}

// extSetDirect writes a key-value pair to a namespace without checking
// persona existence. Used by LayeredStore which performs its own
// cross-layer existence check before delegating.
func (s *Store) extSetDirect(persona, namespace, key, value string) error {
	if err := validateNamespace(namespace); err != nil {
		return err
	}
	if err := validateExtKey(key); err != nil {
		return err
	}
	if len(value) > MaxValueLen {
		return fmt.Errorf("value exceeds maximum length of %d bytes", MaxValueLen)
	}

	// Check namespace count limit.
	dir := s.ExtDir(persona)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating extension directory: %w", err)
	}

	// Load existing namespace data.
	path := s.extPath(persona, namespace)
	var m map[string]string
	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, &m); err != nil {
			return fmt.Errorf("corrupt extension file %s: %w", path, err)
		}
		if m == nil {
			m = make(map[string]string)
		}
	} else if os.IsNotExist(err) {
		// New namespace — check namespace count limit (best-effort;
		// concurrent writers may briefly exceed the limit).
		if err := s.checkNamespaceLimit(persona); err != nil {
			return err
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
	return os.WriteFile(path, data, 0o600)
}

// ExtDel deletes a key from a namespace, or the entire namespace if key is empty.
func (s *Store) ExtDel(persona, namespace, key string) error {
	if err := validateNamespace(namespace); err != nil {
		return err
	}
	if key != "" {
		if err := validateExtKey(key); err != nil {
			return err
		}
	}
	if key == "" {
		// Delete entire namespace file.
		path := s.extPath(persona, namespace)
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("namespace %q not found for %q", namespace, persona)
			}
			return fmt.Errorf("deleting namespace: %w", err)
		}
		return nil
	}

	// Delete single key.
	path := s.extPath(persona, namespace)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("namespace %q not found for %q", namespace, persona)
		}
		return fmt.Errorf("reading extension: %w", err)
	}
	var m map[string]string
	if err := yaml.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("invalid extension file: %w", err)
	}
	if _, ok := m[key]; !ok {
		return fmt.Errorf("key %q not found in %q/%q", key, persona, namespace)
	}
	delete(m, key)
	if len(m) == 0 {
		return os.Remove(path)
	}
	out, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshaling extension: %w", err)
	}
	return os.WriteFile(path, out, 0o600)
}

// ExtList returns all namespace names for a persona.
func (s *Store) ExtList(persona string) ([]string, error) {
	dir := s.ExtDir(persona)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading extension directory: %w", err)
	}
	var namespaces []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		namespaces = append(namespaces, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	return namespaces, nil
}

// loadExtensions reads all extension namespaces for a persona and returns
// the merged map and any warnings for unreadable/corrupt files.
// Called by Store.Load to assemble the full identity view.
func (s *Store) loadExtensions(persona string) (map[string]map[string]string, []string) {
	namespaces, err := s.ExtList(persona)
	if err != nil {
		// ExtList handles os.IsNotExist internally (returns nil, nil),
		// so any error here is a real failure worth surfacing.
		return map[string]map[string]string{}, []string{
			fmt.Sprintf("extensions %s: %v", persona, err),
		}
	}
	if len(namespaces) == 0 {
		return map[string]map[string]string{}, nil
	}
	ext := make(map[string]map[string]string, len(namespaces))
	var warnings []string
	for _, ns := range namespaces {
		data, err := os.ReadFile(s.extPath(persona, ns))
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("extension %s/%s: %v", persona, ns, err))
			continue
		}
		var m map[string]string
		if err := yaml.Unmarshal(data, &m); err != nil {
			warnings = append(warnings, fmt.Sprintf("extension %s/%s: invalid YAML: %v", persona, ns, err))
			continue
		}
		ext[ns] = m
	}
	return ext, warnings
}

func (s *Store) checkNamespaceLimit(persona string) error {
	namespaces, err := s.ExtList(persona)
	if err != nil {
		return err
	}
	if len(namespaces) >= MaxNamespacesPerID {
		return fmt.Errorf("persona %q already has %d namespaces (max %d)", persona, len(namespaces), MaxNamespacesPerID)
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
