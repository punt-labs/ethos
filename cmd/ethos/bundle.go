package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/bundle"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"gopkg.in/yaml.v3"
)

// resolveBundle resolves the repo's active bundle and the root a
// layered store should read as its bundle layer, through
// bundle.ResolveRoot — the one place that logic lives, so
// cmd/validate-content resolves the bundle layer the same way rather
// than through a second, drifting copy (ethos-ccjz).
//
// Configuration errors (malformed ethos.yaml, missing named bundle) are
// fatal: the user asked for a specific bundle and silent fall-through
// would hide the misconfiguration. The process exits with a diagnostic
// on stderr. This matches how other fatal startup errors are handled.
func resolveBundle() (root string, active *bundle.Bundle) {
	root, active, err := bundle.ResolveRoot(resolve.StoreRepoRoot(), defaultGlobalRoot())
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: bundle resolution failed: %v\n", err)
		os.Exit(1)
	}
	return root, active
}

// resolveBundleRoot returns the active bundle's root path for layered
// store construction, or "" if no bundle is active or if the active
// "bundle" is the legacy .punt-labs/ethos/ directory (which already
// serves as the repo-local layer).
func resolveBundleRoot() string {
	root, _ := resolveBundle()
	return root
}

// activeBundle resolves the repo's active bundle, or nil when none is
// active. Callers that need the bundle's Source (the DES-057 repo-only
// startup check) use this rather than the path.
func activeBundle() *bundle.Bundle {
	_, active := resolveBundle()
	return active
}

// defaultGlobalRoot returns ~/.punt-labs/ethos, matching identity.DefaultStore.
// Returns "" if the home directory cannot be determined; callers treat
// that the same as "no global bundle scope."
func defaultGlobalRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".punt-labs", "ethos")
}

// setConfigKey reads .punt-labs/ethos.yaml under repoRoot, sets key to
// value in place (preserving other keys and comments via yaml.Node),
// and writes the file back atomically. If value is "", the key is
// removed. If the file does not exist, it is created with just this
// key (unless value is "", which is a no-op).
func setConfigKey(repoRoot, key, value string) error {
	if repoRoot == "" {
		return fmt.Errorf("not in a git repository")
	}
	path := filepath.Join(repoRoot, ".punt-labs", "ethos.yaml")

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	// Fresh-file case: nothing to parse.
	if len(data) == 0 {
		if value == "" {
			return nil
		}
		return writeConfigFile(path, fmt.Sprintf("%s: %s\n", key, value))
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	// root.Kind == DocumentNode with one mapping child.
	var mapping *yaml.Node
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		mapping = root.Content[0]
	}
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		// Empty or non-mapping document — rewrite cleanly.
		if value == "" {
			return nil
		}
		return writeConfigFile(path, fmt.Sprintf("%s: %s\n", key, value))
	}

	if err := applyKey(mapping, key, value); err != nil {
		return err
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return fmt.Errorf("encoding yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encoding yaml: %w", err)
	}
	return writeConfigFile(path, buf.String())
}

// applyKey sets or removes key in mapping. Mapping content alternates
// key, value, key, value, ...
func applyKey(mapping *yaml.Node, key, value string) error {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		k := mapping.Content[i]
		if k.Value == key {
			if value == "" {
				mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
				return nil
			}
			mapping.Content[i+1].Value = value
			mapping.Content[i+1].Tag = "!!str"
			mapping.Content[i+1].Style = 0
			return nil
		}
	}
	if value == "" {
		return nil
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value, Tag: "!!str"},
	)
	return nil
}

// writeConfigFile writes content to path atomically, creating parent
// directories as needed.
func writeConfigFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming config: %w", err)
	}
	return nil
}
