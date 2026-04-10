package role

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateModel(t *testing.T) {
	tests := []struct {
		name  string
		model string
		ok    bool
	}{
		{"empty is valid", "", true},
		{"opus", "opus", true},
		{"sonnet", "sonnet", true},
		{"haiku", "haiku", true},
		{"inherit", "inherit", true},
		{"claude prefix opus", "claude-opus-4-6", true},
		{"claude prefix sonnet", "claude-sonnet-4-6", true},
		{"claude prefix haiku", "claude-haiku-4-5-20251001", true},
		{"future model ID", "claude-opus-4-7", true},
		{"claude prefix only", "claude-", false},
		{"unknown model", "gpt-4", false},
		{"random string", "banana", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateModel(tt.model)
			if tt.ok {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "unrecognized model")
			}
		})
	}
}

func TestLoadSafetyConstraints(t *testing.T) {
	dir := t.TempDir()
	rolesDir := filepath.Join(dir, "roles")
	require.NoError(t, os.MkdirAll(rolesDir, 0o700))

	data := []byte(`name: secure-role
tools:
  - Bash
safety_constraints:
  - tool: Bash
    message: Never run destructive rm commands
  - tool: "Write|Edit"
    message: Never modify dotenv files
`)
	require.NoError(t, os.WriteFile(filepath.Join(rolesDir, "secure-role.yaml"), data, 0o600))

	s := NewStore(dir)
	r, err := s.Load("secure-role")
	require.NoError(t, err)
	require.Len(t, r.SafetyConstraints, 2)

	assert.Equal(t, "Bash", r.SafetyConstraints[0].Tool)
	assert.Equal(t, "Never run destructive rm commands", r.SafetyConstraints[0].Message)

	assert.Equal(t, "Write|Edit", r.SafetyConstraints[1].Tool)
	assert.Equal(t, "Never modify dotenv files", r.SafetyConstraints[1].Message)
}

func TestLoadRoleWithoutSafetyConstraints(t *testing.T) {
	dir := t.TempDir()
	rolesDir := filepath.Join(dir, "roles")
	require.NoError(t, os.MkdirAll(rolesDir, 0o700))

	data := []byte("name: plain-role\ntools:\n  - Read\n")
	require.NoError(t, os.WriteFile(filepath.Join(rolesDir, "plain-role.yaml"), data, 0o600))

	s := NewStore(dir)
	r, err := s.Load("plain-role")
	require.NoError(t, err)
	assert.Empty(t, r.SafetyConstraints)
}

func TestLoadInvalidModel(t *testing.T) {
	dir := t.TempDir()
	rolesDir := filepath.Join(dir, "roles")
	require.NoError(t, os.MkdirAll(rolesDir, 0o700))

	data := []byte("name: bad-role\nmodel: banana\n")
	require.NoError(t, os.WriteFile(filepath.Join(rolesDir, "bad-role.yaml"), data, 0o600))

	s := NewStore(dir)
	_, err := s.Load("bad-role")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unrecognized model")
}
