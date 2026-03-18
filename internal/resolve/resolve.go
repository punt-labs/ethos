// Package resolve implements the identity resolution chain:
// repo-local config → global active identity → error.
package resolve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// RepoConfig holds the repo-local ethos configuration.
type RepoConfig struct {
	Active string   `yaml:"active,omitempty"` // active identity handle
	Agents []string `yaml:"agents,omitempty"` // agent handles available in this repo
}

// Resolve returns the active identity handle for the current context.
// Priority: repo-local .punt-labs/ethos/config.yaml → global ~/.punt-labs/ethos/active.
func Resolve(repoRoot string) (string, error) {
	// Check repo-local config first
	repoConfig := filepath.Join(repoRoot, ".punt-labs", "ethos", "config.yaml")
	if data, err := os.ReadFile(repoConfig); err == nil {
		var cfg RepoConfig
		if err := yaml.Unmarshal(data, &cfg); err == nil && cfg.Active != "" {
			return cfg.Active, nil
		}
	}

	// Fall back to global active identity
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	activePath := filepath.Join(home, ".punt-labs", "ethos", "active")
	data, err := os.ReadFile(activePath)
	if err != nil {
		return "", fmt.Errorf("no active identity: run 'ethos whoami <handle>' or configure .punt-labs/ethos/config.yaml")
	}
	handle := strings.TrimSpace(string(data))
	if handle == "" {
		return "", fmt.Errorf("active identity file is empty")
	}
	return handle, nil
}
