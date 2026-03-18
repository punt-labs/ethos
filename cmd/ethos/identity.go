package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Identity represents a human or agent identity with channel bindings.
type Identity struct {
	Name           string   `yaml:"name"`
	Handle         string   `yaml:"handle"`
	Kind           string   `yaml:"kind"` // "human" or "agent"
	Email          string   `yaml:"email,omitempty"`
	GitHub         string   `yaml:"github,omitempty"`
	Voice          Voice    `yaml:"voice,omitempty"`
	Agent          string   `yaml:"agent,omitempty"`
	WritingStyle   string   `yaml:"writing_style,omitempty"`
	Personality    string   `yaml:"personality,omitempty"`
	Skills         []string `yaml:"skills,omitempty"`
}

// Voice binds an identity to a Vox voice configuration.
type Voice struct {
	Provider string `yaml:"provider,omitempty"`
	VoiceID  string `yaml:"voice_id,omitempty"`
}

// identityDir returns the global identity storage directory.
func identityDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".punt-labs", "ethos", "identities")
}

// configDir returns the global ethos config directory.
func configDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".punt-labs", "ethos")
}

// loadIdentity reads an identity YAML file by handle.
func loadIdentity(handle string) (*Identity, error) {
	dir := identityDir()
	path := filepath.Join(dir, handle+".yaml")
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

// listIdentities returns all identities in the global directory.
func listIdentities() ([]*Identity, error) {
	dir := identityDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading identity directory: %w", err)
	}

	var identities []*Identity
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		handle := strings.TrimSuffix(entry.Name(), ".yaml")
		id, err := loadIdentity(handle)
		if err != nil {
			continue // skip malformed files
		}
		identities = append(identities, id)
	}
	return identities, nil
}

// activeIdentity returns the currently active identity.
func activeIdentity() (*Identity, error) {
	dir := configDir()
	path := filepath.Join(dir, "active")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("no active identity: %w", err)
	}
	handle := strings.TrimSpace(string(data))
	if handle == "" {
		return nil, fmt.Errorf("no active identity configured")
	}
	return loadIdentity(handle)
}

// saveIdentity writes an identity YAML file.
func saveIdentity(id *Identity) error {
	dir := identityDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating identity directory: %w", err)
	}
	data, err := yaml.Marshal(id)
	if err != nil {
		return fmt.Errorf("marshaling identity: %w", err)
	}
	path := filepath.Join(dir, id.Handle+".yaml")
	return os.WriteFile(path, data, 0o644)
}

// setActiveIdentity sets the active identity by handle.
func setActiveIdentity(handle string) error {
	// Verify the identity exists
	if _, err := loadIdentity(handle); err != nil {
		return err
	}
	dir := configDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	path := filepath.Join(dir, "active")
	return os.WriteFile(path, []byte(handle+"\n"), 0o644)
}
