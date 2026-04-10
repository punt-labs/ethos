package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedExportIdentity creates a test identity with personality, writing
// style, and talents in the given ethos root. Returns the root.
func seedExportIdentity(t *testing.T, root string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(root, 0o700))

	require.NoError(t, attribute.NewStore(root, attribute.Personalities).Save(&attribute.Attribute{
		Slug:    "captain",
		Content: "Steady under fire. Leads by example.\n",
	}))
	require.NoError(t, attribute.NewStore(root, attribute.WritingStyles).Save(&attribute.Attribute{
		Slug:    "terse",
		Content: "Short sentences. No filler.\n",
	}))
	require.NoError(t, attribute.NewStore(root, attribute.Talents).Save(&attribute.Attribute{
		Slug:    "piloting",
		Content: "# Piloting\n",
	}))
	require.NoError(t, attribute.NewStore(root, attribute.Talents).Save(&attribute.Attribute{
		Slug:    "tactics",
		Content: "# Tactics\n",
	}))

	require.NoError(t, identity.NewStore(root).Save(&identity.Identity{
		Name:         "Mal Reynolds",
		Handle:       "mal",
		Kind:         "human",
		Email:        "mal@serenity.ship",
		GitHub:       "mal",
		Personality:  "captain",
		WritingStyle: "terse",
		Talents:      []string{"piloting", "tactics"},
	}))
}

func TestExportSoulSpec(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	tmp := t.TempDir()
	ethosRoot := filepath.Join(tmp, ".punt-labs", "ethos")
	seedExportIdentity(t, ethosRoot)

	outDir := filepath.Join(tmp, "export")
	cmd := exec.Command(ethosBinary, "export", "--to", "soulspec", "mal", "--dir", outDir)
	cmd.Env = append(os.Environ(), "HOME="+tmp)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "export soulspec failed: %s", out)

	// SOUL.md
	soul, err := os.ReadFile(filepath.Join(outDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(soul), "# Mal Reynolds")
	assert.Contains(t, string(soul), "Steady under fire")

	// IDENTITY.md
	ident, err := os.ReadFile(filepath.Join(outDir, "IDENTITY.md"))
	require.NoError(t, err)
	assert.Contains(t, string(ident), "# Mal Reynolds")
	assert.Contains(t, string(ident), "Handle: mal")
	assert.Contains(t, string(ident), "Kind: human")
	assert.Contains(t, string(ident), "Email: mal@serenity.ship")
	assert.Contains(t, string(ident), "GitHub: mal")

	// STYLE.md
	style, err := os.ReadFile(filepath.Join(outDir, "STYLE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(style), "Writing Style")
	assert.Contains(t, string(style), "Short sentences")

	// Stdout lists written files.
	assert.Contains(t, string(out), "SOUL.md")
	assert.Contains(t, string(out), "IDENTITY.md")
	assert.Contains(t, string(out), "STYLE.md")
}

func TestExportClaudeMD(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	tmp := t.TempDir()
	ethosRoot := filepath.Join(tmp, ".punt-labs", "ethos")
	seedExportIdentity(t, ethosRoot)

	cmd := exec.Command(ethosBinary, "export", "--to", "claude-md", "mal")
	cmd.Env = append(os.Environ(), "HOME="+tmp)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	require.NoError(t, err, "export claude-md failed: %s", stderr.String())

	out := stdout.String()
	assert.Contains(t, out, "# Identity: Mal Reynolds")
	assert.Contains(t, out, "Handle: mal")
	assert.Contains(t, out, "Kind: human")
	assert.Contains(t, out, "## Personality")
	assert.Contains(t, out, "Steady under fire")
	assert.Contains(t, out, "## Writing Style")
	assert.Contains(t, out, "Short sentences")
	assert.Contains(t, out, "## Talents")
	assert.Contains(t, out, "piloting, tactics")
}

func TestExportMissingPersonality(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	tmp := t.TempDir()
	ethosRoot := filepath.Join(tmp, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(ethosRoot, 0o700))

	// Identity with no personality or writing style.
	require.NoError(t, identity.NewStore(ethosRoot).Save(&identity.Identity{
		Name:   "Bare Agent",
		Handle: "bare",
		Kind:   "agent",
	}))

	outDir := filepath.Join(tmp, "export")
	cmd := exec.Command(ethosBinary, "export", "--to", "soulspec", "bare", "--dir", outDir)
	cmd.Env = append(os.Environ(), "HOME="+tmp)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	require.NoError(t, err, "expected success with warnings, got: %s", stderr.String())

	// Warnings on stderr for missing content.
	assert.Contains(t, stderr.String(), "no personality content")
	assert.Contains(t, stderr.String(), "no writing style content")

	// IDENTITY.md still written.
	_, err = os.Stat(filepath.Join(outDir, "IDENTITY.md"))
	assert.NoError(t, err, "IDENTITY.md should exist")

	// SOUL.md and STYLE.md not written.
	_, err = os.Stat(filepath.Join(outDir, "SOUL.md"))
	assert.True(t, os.IsNotExist(err), "SOUL.md should not exist")
	_, err = os.Stat(filepath.Join(outDir, "STYLE.md"))
	assert.True(t, os.IsNotExist(err), "STYLE.md should not exist")
}

func TestExportInvalidFormat(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	cmd := exec.Command(ethosBinary, "export", "--to", "invalid", "anyone")
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "expected error for invalid format")

	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok)
	assert.Equal(t, 1, exitErr.ExitCode())
	assert.Contains(t, string(out), "unsupported format")
}

func TestExportMissingHandle(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	cmd := exec.Command(ethosBinary, "export", "--to", "soulspec", "nonexistent")
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "expected error for missing identity")

	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok)
	assert.Equal(t, 1, exitErr.ExitCode())
	assert.Contains(t, string(out), "not found")
}
