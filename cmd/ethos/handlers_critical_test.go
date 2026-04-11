//go:build linux || darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/mission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Priority 1: Identity creation from file ---

func TestCreateFromFile_ValidIdentity(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	// Seed the attributes the identity references so ValidateRefs passes.
	ethosRoot := filepath.Join(se.home, ".punt-labs", "ethos")
	writeAttrFile(t, ethosRoot, "personalities", "intuitive.md", "# Intuitive\n")
	writeAttrFile(t, ethosRoot, "writing-styles", "cryptic.md", "# Cryptic\n")
	writeAttrFile(t, ethosRoot, "talents", "combat.md", "# Combat\n")
	writeAttrFile(t, ethosRoot, "talents", "psychic.md", "# Psychic\n")

	yamlBody := `name: River Tam
handle: river
kind: agent
email: river@serenity.ship
github: river-tam
personality: intuitive
writing_style: cryptic
talents:
  - combat
  - psychic
`
	path := filepath.Join(t.TempDir(), "river.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yamlBody), 0o644))

	stdout := captureStdoutE(t, func() error {
		return createFromFile(path)
	})
	assert.Contains(t, stdout, "Created identity")
	assert.Contains(t, stdout, "river")

	// Verify the identity was persisted with correct fields.
	// LayeredStore.Save writes to the repo-local store.
	is := identity.NewStore(filepath.Join(se.repo, ".punt-labs", "ethos"))
	id, err := is.Load("river")
	require.NoError(t, err)
	assert.Equal(t, "River Tam", id.Name)
	assert.Equal(t, "river", id.Handle)
	assert.Equal(t, "agent", id.Kind)
	assert.Equal(t, "river@serenity.ship", id.Email)
	assert.Equal(t, "river-tam", id.GitHub)
	assert.Equal(t, "intuitive", id.Personality)
	assert.Equal(t, "cryptic", id.WritingStyle)
	assert.Equal(t, []string{"combat", "psychic"}, id.Talents)
}

func TestCreateFromFile_WithVoiceExtraction(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	yamlBody := `name: Wash
handle: wash
kind: human
voice:
  provider: elevenlabs
  voice_id: abc123
`
	path := filepath.Join(t.TempDir(), "wash.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yamlBody), 0o644))

	captureStdoutE(t, func() error {
		return createFromFile(path)
	})

	// Verify voice extension data was extracted and stored.
	is := identity.NewStore(filepath.Join(se.home, ".punt-labs", "ethos"))
	providerMap, err := is.ExtGet("wash", "vox", "provider")
	require.NoError(t, err)
	assert.Equal(t, "elevenlabs", providerMap["provider"])

	voiceMap, err := is.ExtGet("wash", "vox", "voice_id")
	require.NoError(t, err)
	assert.Equal(t, "abc123", voiceMap["voice_id"])
}

func TestCreateFromFile_InvalidYAML(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	path := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("name: [unterminated"), 0o644))

	err := createFromFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid YAML")

	// No identity should be saved.
	is := identity.NewStore(filepath.Join(se.home, ".punt-labs", "ethos"))
	_, loadErr := is.Load("unterminated")
	assert.Error(t, loadErr)
}

func TestCreateFromFile_ValidationFailure(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	// Valid YAML but missing required "name" field.
	yamlBody := `handle: noname
kind: agent
`
	path := filepath.Join(t.TempDir(), "noname.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yamlBody), 0o644))

	err := createFromFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name")
}

func TestCreateFromFile_FileNotFound(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	err := createFromFile("/nonexistent/path.yaml")
	require.Error(t, err)
}

// --- Priority 2: Import/Export round-trip ---

func TestExportSoulSpec_InProcess(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	ethosRoot := filepath.Join(se.home, ".punt-labs", "ethos")
	seedExportIdentity(t, ethosRoot)

	// Reset the export flags used by cobra.
	exportFormat = "soulspec"
	exportDir = filepath.Join(t.TempDir(), "export")
	t.Cleanup(func() { exportFormat = ""; exportDir = "." })

	stdout := captureStdoutE(t, func() error {
		return exportSoulSpec("mal", exportDir)
	})

	// SOUL.md exists with correct content.
	soul, err := os.ReadFile(filepath.Join(exportDir, "SOUL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(soul), "# Mal Reynolds")
	assert.Contains(t, string(soul), "Steady under fire")

	// IDENTITY.md exists with correct fields.
	ident, err := os.ReadFile(filepath.Join(exportDir, "IDENTITY.md"))
	require.NoError(t, err)
	assert.Contains(t, string(ident), "Handle: mal")
	assert.Contains(t, string(ident), "Kind: human")
	assert.Contains(t, string(ident), "Email: mal@serenity.ship")
	assert.Contains(t, string(ident), "GitHub: mal")

	// STYLE.md exists with correct content.
	style, err := os.ReadFile(filepath.Join(exportDir, "STYLE.md"))
	require.NoError(t, err)
	assert.Contains(t, string(style), "Short sentences")

	// Stdout lists written files.
	assert.Contains(t, stdout, "SOUL.md")
	assert.Contains(t, stdout, "IDENTITY.md")
	assert.Contains(t, stdout, "STYLE.md")
}

func TestExportSoulSpec_MissingContent(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	ethosRoot := filepath.Join(se.home, ".punt-labs", "ethos")
	// Identity with no personality or writing style.
	require.NoError(t, identity.NewStore(ethosRoot).Save(&identity.Identity{
		Name:   "Bare Agent",
		Handle: "bare",
		Kind:   "agent",
	}))

	outDir := filepath.Join(t.TempDir(), "export")

	// Export; warnings go to stderr (not captured here — we verify
	// the side effects: which files exist vs. which are skipped).
	captureStdoutE(t, func() error {
		return exportSoulSpec("bare", outDir)
	})

	// IDENTITY.md still written.
	_, err := os.Stat(filepath.Join(outDir, "IDENTITY.md"))
	assert.NoError(t, err, "IDENTITY.md should exist")

	// SOUL.md and STYLE.md should not exist.
	_, err = os.Stat(filepath.Join(outDir, "SOUL.md"))
	assert.True(t, os.IsNotExist(err), "SOUL.md should not exist")
	_, err = os.Stat(filepath.Join(outDir, "STYLE.md"))
	assert.True(t, os.IsNotExist(err), "STYLE.md should not exist")
}

func TestExportClaudeMD_InProcess(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	ethosRoot := filepath.Join(se.home, ".punt-labs", "ethos")
	seedExportIdentity(t, ethosRoot)

	stdout := captureStdoutE(t, func() error {
		return exportClaudeMD("mal")
	})

	assert.Contains(t, stdout, "# Identity: Mal Reynolds")
	assert.Contains(t, stdout, "Handle: mal")
	assert.Contains(t, stdout, "Kind: human")
	assert.Contains(t, stdout, "## Personality")
	assert.Contains(t, stdout, "Steady under fire")
	assert.Contains(t, stdout, "## Writing Style")
	assert.Contains(t, stdout, "Short sentences")
	assert.Contains(t, stdout, "## Talents")
	assert.Contains(t, stdout, "piloting, tactics")
}

func TestImportSoulSpec_InProcess(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	srcDir := filepath.Join(t.TempDir(), "soulspec")
	require.NoError(t, os.MkdirAll(srcDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(srcDir, "SOUL.md"),
		[]byte("# Inara Serra\n\nGraceful and diplomatic. Companion.\n"),
		0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(srcDir, "IDENTITY.md"),
		[]byte("# Inara Serra\n\nHandle: inara\n"),
		0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(srcDir, "STYLE.md"),
		[]byte("# Inara Writing Style\n\nElegant prose. Measured cadence.\n"),
		0o600,
	))

	stdout := captureStdoutE(t, func() error {
		return importSoulSpec(srcDir, "")
	})

	assert.Contains(t, stdout, "imported identity")
	assert.Contains(t, stdout, "inara-serra")

	// Verify identity fields. Import writes to the repo-local store
	// (LayeredStore.writeStore prefers repo), so load from there.
	repoEthos := filepath.Join(se.repo, ".punt-labs", "ethos")
	is := identity.NewStore(repoEthos)
	id, err := is.Load("inara-serra")
	require.NoError(t, err)
	assert.Equal(t, "Inara Serra", id.Name)
	assert.Equal(t, "inara-serra", id.Handle)
	assert.Equal(t, "inara-serra", id.Personality)
	assert.Equal(t, "inara-serra", id.WritingStyle)
}

func TestImportSoulSpec_RoundTrip(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	globalRoot := filepath.Join(se.home, ".punt-labs", "ethos")
	repoRoot := filepath.Join(se.repo, ".punt-labs", "ethos")
	seedExportIdentity(t, globalRoot)

	// Export mal as soulspec.
	exportOutDir := filepath.Join(t.TempDir(), "exported")
	captureStdoutE(t, func() error {
		return exportSoulSpec("mal", exportOutDir)
	})

	// Import under a different handle. Import writes to repo-local.
	captureStdoutE(t, func() error {
		return importSoulSpec(exportOutDir, "mal-clone")
	})

	// identityStore() is a LayeredStore: Save writes to repo, attribute
	// saves go to global (layered attribute store root = global). Load
	// the clone identity from repo, attributes from global.
	repoIS := identity.NewStore(repoRoot)
	clone, err := repoIS.Load("mal-clone", identity.Reference(true))
	require.NoError(t, err)

	// The imported identity should have personality and writing style slugs.
	assert.NotEmpty(t, clone.Personality)
	assert.NotEmpty(t, clone.WritingStyle)

	// Load attribute content directly to verify round-trip fidelity.
	// The layered attribute store writes to global root.
	origPers := attribute.NewStore(globalRoot, attribute.Personalities)
	origAttr, err := origPers.Load("captain")
	require.NoError(t, err)
	clonePers := attribute.NewStore(globalRoot, attribute.Personalities)
	cloneAttr, err := clonePers.Load(clone.Personality)
	require.NoError(t, err)

	// Both should contain the same core content.
	assert.Contains(t, cloneAttr.Content, "Steady under fire")
	assert.Contains(t, origAttr.Content, "Steady under fire")

	// Writing style round-trip.
	origWS := attribute.NewStore(globalRoot, attribute.WritingStyles)
	origWSAttr, err := origWS.Load("terse")
	require.NoError(t, err)
	cloneWS := attribute.NewStore(globalRoot, attribute.WritingStyles)
	cloneWSAttr, err := cloneWS.Load(clone.WritingStyle)
	require.NoError(t, err)
	assert.Contains(t, cloneWSAttr.Content, "Short sentences")
	assert.Contains(t, origWSAttr.Content, "Short sentences")
}

func TestImportSoulSpec_MissingSoul(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	emptyDir := t.TempDir()
	err := importSoulSpec(emptyDir, "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SOUL.md not found")
}

func TestImportSoulSpec_EmptySoul(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	srcDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "SOUL.md"), []byte(""), 0o600))

	err := importSoulSpec(srcDir, "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SOUL.md is empty")
}

// --- Priority 3: Seed command ---

// execSeed runs the seed command and captures stdout (which runSeed
// writes via fmt.Printf, not cobra's cmd.OutOrStdout).
func execSeed(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()
	seedForce = false
	jsonOutput = false
	t.Cleanup(func() { seedForce = false; jsonOutput = false })
	rootCmd.SetArgs(args)
	defer rootCmd.SetArgs(nil)

	out := captureStdout(t, func() {
		err = rootCmd.Execute()
	})
	return out, err
}

func TestSeed_Fresh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	stdout, err := execSeed(t, "seed")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Seeded")
	assert.Contains(t, stdout, "deployed:")

	// Verify files were actually deployed.
	rolesDir := filepath.Join(home, ".punt-labs", "ethos", "roles")
	entries, err := os.ReadDir(rolesDir)
	require.NoError(t, err)
	assert.NotEmpty(t, entries, "roles directory should have files")
}

func TestSeed_Idempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// First run.
	_, err := execSeed(t, "seed")
	require.NoError(t, err)

	// Second run: all files should be skipped.
	stdout, err := execSeed(t, "seed")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Seeded 0 files")
	assert.Contains(t, stdout, "skipped")
}

func TestSeed_Force(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// First run: deploy.
	_, err := execSeed(t, "seed")
	require.NoError(t, err)

	// Modify a deployed file.
	rolesDir := filepath.Join(home, ".punt-labs", "ethos", "roles")
	entries, err := os.ReadDir(rolesDir)
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	var targetFile string
	for _, e := range entries {
		if !e.IsDir() {
			targetFile = filepath.Join(rolesDir, e.Name())
			break
		}
	}
	require.NotEmpty(t, targetFile)

	origData, err := os.ReadFile(targetFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(targetFile, []byte("modified"), 0o644))

	// Run seed --force: the file should be overwritten.
	stdout, err := execSeed(t, "seed", "--force")
	require.NoError(t, err)
	assert.Contains(t, stdout, "deployed:")

	restored, err := os.ReadFile(targetFile)
	require.NoError(t, err)
	assert.Equal(t, origData, restored, "force should restore original content")
}

// --- Priority 4: parseNumstat ---

func TestParseNumstat_ValidOutput(t *testing.T) {
	input := "10\t5\tinternal/mission/store.go\n3\t0\tcmd/ethos/mission.go\n"
	entries, err := parseNumstat([]byte(input))
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, numstatEntry{added: 10, removed: 5}, entries["internal/mission/store.go"])
	assert.Equal(t, numstatEntry{added: 3, removed: 0}, entries["cmd/ethos/mission.go"])
}

func TestParseNumstat_BinaryFile(t *testing.T) {
	input := "-\t-\timage.png\n5\t2\tREADME.md\n"
	entries, err := parseNumstat([]byte(input))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, numstatEntry{added: 5, removed: 2}, entries["README.md"])
	_, hasBinary := entries["image.png"]
	assert.False(t, hasBinary, "binary file should be skipped")
}

func TestParseNumstat_EmptyOutput(t *testing.T) {
	entries, err := parseNumstat([]byte(""))
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestParseNumstat_MalformedLine(t *testing.T) {
	input := "10\tfile.go\n"
	_, err := parseNumstat([]byte(input))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected 3 tab-separated fields")
}

func TestVerifyResultAgainstNumstat(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	setInProcessEnv(t, se)

	// Ensure the default branch is called "main". Use -M (rename) instead
	// of -b (create) because git init may already use "main" as default.
	gitCmd(t, se.repo, "branch", "-M", "main")

	// Create a file and commit it so we have a base.
	testFile := filepath.Join(se.repo, "hello.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("line1\n"), 0o644))

	gitCmd(t, se.repo, "add", "hello.txt")
	gitCmd(t, se.repo, "commit", "-m", "initial")

	// Create a branch and modify the file.
	gitCmd(t, se.repo, "checkout", "-b", "test-branch")
	require.NoError(t, os.WriteFile(testFile, []byte("line1\nline2\nline3\n"), 0o644))
	gitCmd(t, se.repo, "add", "hello.txt")
	gitCmd(t, se.repo, "commit", "-m", "add lines")

	// Build a result that matches the diff: 2 added, 0 removed.
	r := &mission.Result{
		Mission:    "m-2026-01-01-001",
		Round:      1,
		Author:     "bwk",
		Verdict:    "pass",
		Confidence: 0.9,
		FilesChanged: []mission.FileChange{
			{Path: "hello.txt", Added: 2, Removed: 0},
		},
		Evidence: []mission.EvidenceCheck{{Name: "test", Status: "pass"}},
	}

	// Verify against main should pass.
	err := verifyResultAgainstNumstat(r, "main")
	require.NoError(t, err)

	// Modify the result to have wrong counts — should fail.
	r.FilesChanged[0].Added = 99
	err = verifyResultAgainstNumstat(r, "main")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "added=99")
	assert.Contains(t, err.Error(), "added=2")
}

// gitCmd runs a git command in the given directory with sanitized env.
func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	home, _ := os.LookupEnv("HOME")
	cmd := execGit(dir, home, args...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

// execGit creates a git command with sanitized environment.
func execGit(dir, home string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test",
		"PATH=" + os.Getenv("PATH"),
	}
	return cmd
}

// --- Priority 5: isUsageError ---

func TestIsUsageError(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"unknown command \"foo\" for \"ethos\"", true},
		{"unknown flag: --bad", true},
		{"unknown shorthand flag: 'x' in -x", true},
		{"required flag(s) \"file\" not set", true},
		{"accepts 1 arg(s), received 0", true},
		{"requires at least 1 arg(s)", true},
		{"invalid argument \"x\" for \"--count\"", true},
		{"flag needs an argument: --file", true},
		{"connection refused", false},
		{"identity not found", false},
		{"", false},
		{"mission create: invalid YAML", false},
	}
	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			err := fmt.Errorf("%s", tt.msg)
			assert.Equal(t, tt.want, isUsageError(err),
				"isUsageError(%q) = %v, want %v", tt.msg, isUsageError(err), tt.want)
		})
	}
}

// --- Priority 6: capitalizeFirst ---

func TestCapitalizeFirst(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"a", "A"},
		{"hello world", "Hello world"},
		{"Hello", "Hello"},
		{"x", "X"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, capitalizeFirst(tt.input))
		})
	}
}

// --- Priority 4 supplement: parseNumstat edge cases ---

func TestParseNumstat_WhitespaceOnlyLines(t *testing.T) {
	input := "  \n\t\n10\t5\tfile.go\n  \n"
	entries, err := parseNumstat([]byte(input))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, numstatEntry{added: 10, removed: 5}, entries["file.go"])
}

func TestParseNumstat_NonNumericAdded(t *testing.T) {
	input := "abc\t5\tfile.go\n"
	_, err := parseNumstat([]byte(input))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "added field")
}

func TestParseNumstat_NonNumericRemoved(t *testing.T) {
	input := "5\txyz\tfile.go\n"
	_, err := parseNumstat([]byte(input))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "removed field")
}

