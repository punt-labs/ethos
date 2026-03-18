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
	Name         string   `yaml:"name" json:"name"`
	Handle       string   `yaml:"handle" json:"handle"`
	Kind         string   `yaml:"kind" json:"kind"`
	Email        string   `yaml:"email,omitempty" json:"email,omitempty"`
	GitHub       string   `yaml:"github,omitempty" json:"github,omitempty"`
	Voice        *Voice   `yaml:"voice,omitempty" json:"voice,omitempty"`
	Agent        string   `yaml:"agent,omitempty" json:"agent,omitempty"`
	WritingStyle string   `yaml:"writing_style,omitempty" json:"writing_style,omitempty"`
	Personality  string   `yaml:"personality,omitempty" json:"personality,omitempty"`
	Skills       []string `yaml:"skills,omitempty" json:"skills,omitempty"`
}

// Voice binds an identity to a Vox voice configuration.
type Voice struct {
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	VoiceID  string `yaml:"voice_id,omitempty" json:"voice_id,omitempty"`
}

// identityDir returns the global identity storage directory.
func identityDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".punt-labs", "ethos", "identities"), nil
}

// configDir returns the global ethos config directory.
func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".punt-labs", "ethos"), nil
}

// identityPath returns the filesystem path for the given handle.
// Uses filepath.Base to prevent path traversal.
func identityPath(handle string) (string, error) {
	dir, err := identityDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.Base(handle)+".yaml"), nil
}

// loadIdentity reads an identity YAML file by handle.
func loadIdentity(handle string) (*Identity, error) {
	path, err := identityPath(handle)
	if err != nil {
		return nil, err
	}
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
	dir, err := identityDir()
	if err != nil {
		return nil, err
	}
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
			fmt.Fprintf(os.Stderr, "ethos: skipping %s: %v\n", entry.Name(), err)
			continue
		}
		identities = append(identities, id)
	}
	return identities, nil
}

// activeIdentity returns the currently active identity.
func activeIdentity() (*Identity, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
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

// identityExists checks whether an identity file exists for the given handle.
func identityExists(handle string) bool {
	path, err := identityPath(handle)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// saveIdentity writes an identity YAML file. Returns an error if an
// identity with the same handle already exists.
func saveIdentity(id *Identity) error {
	path, err := identityPath(id.Handle)
	if err != nil {
		return err
	}
	if identityExists(id.Handle) {
		return fmt.Errorf("identity %q already exists — delete %q to recreate", id.Handle, path)
	}
	dir, err := identityDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating identity directory: %w", err)
	}
	data, err := yaml.Marshal(id)
	if err != nil {
		return fmt.Errorf("marshaling identity: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}

// setActiveIdentity sets the active identity by handle.
func setActiveIdentity(handle string) error {
	// Verify the identity exists
	if _, err := loadIdentity(handle); err != nil {
		return err
	}
	dir, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	path := filepath.Join(dir, "active")
	return os.WriteFile(path, []byte(handle+"\n"), 0o600)
}
