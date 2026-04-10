package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// importTestDir creates a temp directory under /tmp (not TMPDIR) so the
// subprocess does not inherit the git repo tree. FindRepoRoot walks
// up from cwd; if TMPDIR is inside the repo, cmd.Dir = t.TempDir()
// still resolves the repo's .punt-labs/ethos. Using /tmp avoids this.
func importTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ethos-import-test-*")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestImportSoulSpecFull(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	tmp := importTestDir(t)
	ethosRoot := filepath.Join(tmp, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(ethosRoot, 0o700))

	// Seed SoulSpec files.
	srcDir := filepath.Join(tmp, "soulspec")
	require.NoError(t, os.MkdirAll(srcDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(srcDir, "SOUL.md"),
		[]byte("# Kaylee\n\nAlways sees the bright side. Fixes engines.\n"),
		0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(srcDir, "IDENTITY.md"),
		[]byte("# Kaylee Frye\n\nHandle: kaylee\nRole: mechanic\n"),
		0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(srcDir, "STYLE.md"),
		[]byte("# Kaylee Writing Style\n\nCheerful and warm. Uses metaphors from machinery.\n"),
		0o600,
	))

	cmd := exec.Command(ethosBinary, "import", "--from", "soulspec", "--dir", srcDir)
	cmd.Dir = tmp
	cmd.Env = append(os.Environ(), "HOME="+tmp)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	require.NoError(t, err, "import failed: stdout=%s stderr=%s", stdout.String(), stderr.String())

	out := stdout.String()
	assert.Contains(t, out, "created personality")
	assert.Contains(t, out, "created writing style")
	assert.Contains(t, out, "imported identity")
	assert.Contains(t, out, "kaylee-frye")

	// Verify identity YAML was created.
	idPath := filepath.Join(ethosRoot, "identities", "kaylee-frye.yaml")
	data, err := os.ReadFile(idPath)
	require.NoError(t, err, "identity file should exist")

	var id struct {
		Name         string `yaml:"name"`
		Handle       string `yaml:"handle"`
		Kind         string `yaml:"kind"`
		Personality  string `yaml:"personality"`
		WritingStyle string `yaml:"writing_style"`
	}
	require.NoError(t, yaml.Unmarshal(data, &id))
	assert.Equal(t, "Kaylee Frye", id.Name)
	assert.Equal(t, "kaylee-frye", id.Handle)
	assert.Equal(t, "agent", id.Kind)
	assert.Equal(t, "kaylee-frye", id.Personality)
	assert.Equal(t, "kaylee-frye", id.WritingStyle)

	// Verify personality content was saved.
	persPath := filepath.Join(ethosRoot, "personalities", "kaylee-frye.md")
	persData, err := os.ReadFile(persPath)
	require.NoError(t, err, "personality file should exist")
	assert.Contains(t, string(persData), "Fixes engines")
	assert.NotContains(t, string(persData), "# Kaylee", "heading should be stripped")

	// Verify writing style content was saved.
	wsPath := filepath.Join(ethosRoot, "writing-styles", "kaylee-frye.md")
	wsData, err := os.ReadFile(wsPath)
	require.NoError(t, err, "writing style file should exist")
	assert.Contains(t, string(wsData), "metaphors from machinery")
	assert.NotContains(t, string(wsData), "# Kaylee", "heading should be stripped")
}

func TestImportSoulSpecMinimal(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	tmp := importTestDir(t)
	ethosRoot := filepath.Join(tmp, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(ethosRoot, 0o700))

	srcDir := filepath.Join(tmp, "soulspec")
	require.NoError(t, os.MkdirAll(srcDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(srcDir, "SOUL.md"),
		[]byte("Quiet and methodical. Gets the job done.\n"),
		0o600,
	))

	cmd := exec.Command(ethosBinary, "import", "--from", "soulspec", "--dir", srcDir, "--handle", "test-agent")
	cmd.Dir = tmp
	cmd.Env = append(os.Environ(), "HOME="+tmp)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	require.NoError(t, err, "import failed: stdout=%s stderr=%s", stdout.String(), stderr.String())

	// Warnings about missing files.
	assert.Contains(t, stderr.String(), "IDENTITY.md not found")
	assert.Contains(t, stderr.String(), "STYLE.md not found")

	out := stdout.String()
	assert.Contains(t, out, "imported identity")
	assert.Contains(t, out, "test-agent")

	// Identity created with handle as name.
	idPath := filepath.Join(ethosRoot, "identities", "test-agent.yaml")
	data, err := os.ReadFile(idPath)
	require.NoError(t, err)

	var id struct {
		Name         string `yaml:"name"`
		Handle       string `yaml:"handle"`
		Kind         string `yaml:"kind"`
		WritingStyle string `yaml:"writing_style"`
	}
	require.NoError(t, yaml.Unmarshal(data, &id))
	assert.Equal(t, "test-agent", id.Name)
	assert.Equal(t, "test-agent", id.Handle)
	assert.Equal(t, "agent", id.Kind)
	assert.Empty(t, id.WritingStyle, "writing style should be empty when STYLE.md is absent")
}

func TestImportSoulSpecMissingSoul(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	tmp := importTestDir(t)
	cmd := exec.Command(ethosBinary, "import", "--from", "soulspec", "--dir", tmp)
	cmd.Dir = tmp
	cmd.Env = append(os.Environ(), "HOME="+tmp)
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "should fail when SOUL.md is missing")

	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok)
	assert.Equal(t, 1, exitErr.ExitCode())
	assert.Contains(t, string(out), "SOUL.md not found")
}

func TestImportInvalidFormat(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	tmp := importTestDir(t)
	cmd := exec.Command(ethosBinary, "import", "--from", "invalid")
	cmd.Dir = tmp
	cmd.Env = append(os.Environ(), "HOME="+tmp)
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "should fail for invalid format")

	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok)
	assert.Equal(t, 1, exitErr.ExitCode())
	assert.Contains(t, string(out), "unsupported format")
}

func TestExtractHeading(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple heading", "# Kaylee Frye\n\nContent here.", "Kaylee Frye"},
		{"heading with spaces", "# Mal Reynolds \n\nMore.", "Mal Reynolds"},
		{"no heading", "Just content.\n", ""},
		{"empty", "", ""},
		{"h2 not matched", "## Subsection\n", ""},
		{"leading blank lines", "\n\n# Name\n", "Name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractHeading(tt.input))
		})
	}
}

func TestStripLeadingHeading(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			"heading with content",
			"# Title\n\nBody text here.",
			"Body text here.",
		},
		{
			"no heading",
			"Just content.",
			"Just content.",
		},
		{
			"heading only",
			"# Title",
			"",
		},
		{
			"heading with blank lines then content",
			"# Title\n\n\nContent after blanks.",
			"Content after blanks.",
		},
		{
			"leading blank lines before heading",
			"\n\n# Title\n\nContent.",
			"Content.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stripLeadingHeading(tt.input))
		})
	}
}

func TestImportSoulSpecHandleOverride(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not available; TestMain build failed")
	}

	tmp := importTestDir(t)
	ethosRoot := filepath.Join(tmp, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(ethosRoot, 0o700))

	srcDir := filepath.Join(tmp, "soulspec")
	require.NoError(t, os.MkdirAll(srcDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(srcDir, "SOUL.md"),
		[]byte("# Original Name\n\nSome personality content.\n"),
		0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(srcDir, "IDENTITY.md"),
		[]byte("# Original Name\n\nHandle: original\n"),
		0o600,
	))

	cmd := exec.Command(ethosBinary, "import", "--from", "soulspec", "--dir", srcDir, "--handle", "custom")
	cmd.Dir = tmp
	cmd.Env = append(os.Environ(), "HOME="+tmp)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	require.NoError(t, err, "import failed: stdout=%s stderr=%s", stdout.String(), stderr.String())

	// Handle override should win.
	idPath := filepath.Join(ethosRoot, "identities", "custom.yaml")
	data, err := os.ReadFile(idPath)
	require.NoError(t, err, "identity file should exist at overridden handle")

	var id struct {
		Handle string `yaml:"handle"`
		Name   string `yaml:"name"`
	}
	require.NoError(t, yaml.Unmarshal(data, &id))
	assert.Equal(t, "custom", id.Handle)
	// Name still comes from IDENTITY.md heading.
	assert.Equal(t, "Original Name", id.Name)
}
