//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file exercises the setup-consistency work end-to-end through the
// built binary — the onboarding journey exactly as a user runs it (seed,
// setup, show, whoami, doctor), plus re-run idempotence and cross-bundle
// switching. The unit tables in setup_test.go and internal/identity cover
// the same logic in-process; these tests add exit-code and parsed-output
// assertions that only a real subprocess can make, and pin the layered
// blast-radius behavior at the CLI boundary.

// freshCLIEnv builds a clean fake HOME and a git-init'd repo with no
// seeded content — the fresh-machine starting point. USER is set to the
// human handle so whoami/doctor resolve the caller without a git config.
func freshCLIEnv(t *testing.T, humanHandle string) *cliSubprocessEnv {
	t.Helper()

	home := t.TempDir()
	repo := t.TempDir()
	gitInitDir(t, repo, home)

	env := []string{
		"HOME=" + home,
		"USER=" + humanHandle,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"PATH=" + os.Getenv("PATH"),
	}
	return &cliSubprocessEnv{home: home, repo: repo, env: env}
}

// writeAnswers drops a non-interactive setup answers file in the repo and
// returns its absolute path.
func writeAnswers(t *testing.T, repo, body string) string {
	t.Helper()
	p := filepath.Join(repo, "answers.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return p
}

// showJSON runs `ethos show <handle> --json` and returns the parsed
// resolved-content fields plus the raw stderr (where warnings land).
func showJSON(t *testing.T, se *cliSubprocessEnv, handle string) (talents []string, personality string, stderr string, exit int) {
	t.Helper()
	stdout, errOut, code := runCLI(t, se, "show", handle, "--json")
	var parsed struct {
		TalentContents      []string `json:"talent_contents"`
		PersonalityContent  string   `json:"personality_content"`
		WritingStyleContent string   `json:"writing_style_content"`
	}
	if code == 0 {
		require.NoError(t, json.Unmarshal([]byte(stdout), &parsed),
			"show %s --json should emit valid JSON; stdout=%s stderr=%s", handle, stdout, errOut)
	}
	return parsed.TalentContents, parsed.PersonalityContent, errOut, code
}

// TestCLI_SetupJourney_Foundation walks the full onboarding path as a user
// runs it and asserts exit codes and parsed output at every step — the
// acceptance criterion (zero warnings, doctor clean) checked through the
// real binary rather than in-process handlers.
func TestCLI_SetupJourney_Foundation(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := freshCLIEnv(t, "tester")

	// Step 1: seed starter content into the fresh HOME.
	stdout, stderr, code := runCLI(t, se, "seed")
	require.Equal(t, 0, code, "seed should exit 0; stderr=%s", stderr)
	assert.Contains(t, stdout, "Seeded", "seed should report a deploy count")

	// Step 2: setup with the foundation bundle, non-interactively.
	answers := writeAnswers(t, se.repo, "name: Tester\nhandle: tester\nbundle: foundation\n")
	stdout, stderr, code = runCLI(t, se, "setup", "--file", answers, "--bundle", "foundation")
	require.Equal(t, 0, code, "setup should exit 0; stderr=%s", stderr)
	assert.Contains(t, stderr, `activated: bundle "foundation"`)

	// Step 3: every identity resolves with zero warnings. The default
	// agent and the human are global-stored; the bundle agents are
	// bundle-stored. All must be clean.
	for _, h := range []string{"tester", "claude", "foundation-architect", "foundation-security"} {
		_, _, errOut, showCode := showJSON(t, se, h)
		require.Equal(t, 0, showCode, "show %s should exit 0; stderr=%s", h, errOut)
		assert.NotContains(t, errOut, "warning:", "show %s should emit no warnings; stderr=%s", h, errOut)
	}

	// Step 4: the human's engineering talent resolves to foundation's copy
	// (Part A: a global identity resolves attribute content through the
	// active bundle).
	claudeTalents, _, _, _ := showJSON(t, se, "claude")
	require.NotEmpty(t, claudeTalents)
	assert.Contains(t, claudeTalents[0], "General engineering discipline",
		"claude's engineering talent should resolve foundation's text under the foundation bundle")

	// Step 5: whoami resolves the caller (USER=tester).
	stdout, stderr, code = runCLI(t, se, "whoami")
	require.Equal(t, 0, code, "whoami should exit 0; stderr=%s", stderr)
	assert.Contains(t, stdout, "tester")

	// Step 6: doctor is clean.
	stdout, stderr, code = runCLI(t, se, "doctor")
	assert.Equal(t, 0, code, "doctor should exit 0; stdout=%s stderr=%s", stdout, stderr)
}

// TestCLI_Setup_UnseededFailsLoud pins the CLI-level contract: running
// setup before seed exits non-zero and prints the actionable remedy. The
// in-process TestSetup_HardValidation_UnseededFails asserts the error
// string; this asserts the process exit code and that stderr carries the
// remedy a user would read.
func TestCLI_Setup_UnseededFailsLoud(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := freshCLIEnv(t, "nobody")

	answers := writeAnswers(t, se.repo, "name: Nobody\nhandle: nobody\nbundle: foundation\n")
	stdout, stderr, code := runCLI(t, se, "setup", "--file", answers, "--bundle", "foundation")

	require.NotEqual(t, 0, code, "unseeded setup must exit non-zero; stdout=%s", stdout)
	assert.Contains(t, stderr, `principal-engineer`)
	assert.Contains(t, stderr, `run "ethos seed" first`)

	// Nothing dangling: no global identities directory was written.
	assert.NoFileExists(t, filepath.Join(se.home, ".punt-labs", "ethos", "identities", "nobody.yaml"))
	assert.NoFileExists(t, filepath.Join(se.home, ".punt-labs", "ethos", "identities", "claude.yaml"))
}

// TestCLI_Setup_ReRunIdempotent pins the semantic of a second setup on an
// already-configured repo: it exits 0 and reports skips rather than
// erroring or duplicating. Idempotence is the contract — re-running setup
// is safe.
func TestCLI_Setup_ReRunIdempotent(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := freshCLIEnv(t, "tester")

	_, stderr, code := runCLI(t, se, "seed")
	require.Equal(t, 0, code, "seed: %s", stderr)

	answers := writeAnswers(t, se.repo, "name: Tester\nhandle: tester\nbundle: foundation\n")

	// First run creates everything.
	_, stderr, code = runCLI(t, se, "setup", "--file", answers, "--bundle", "foundation")
	require.Equal(t, 0, code, "first setup: %s", stderr)

	// Second run is idempotent: exit 0, identities and bundle reported as
	// already present rather than re-created.
	_, stderr, code = runCLI(t, se, "setup", "--file", answers, "--bundle", "foundation")
	require.Equal(t, 0, code, "re-run setup should exit 0; stderr=%s", stderr)
	assert.Contains(t, stderr, `skipped: identity "tester" already exists`)
	assert.Contains(t, stderr, `skipped: identity "claude" already exists`)
	assert.Contains(t, stderr, `bundle "foundation" already active`)
}

// TestCLI_CrossBundleSwitch pins the Part A blast radius at the CLI: a
// global identity's resolved attribute *content* follows the active
// bundle. After switching active_bundle from foundation to gstack in
// ethos.yaml, claude's engineering talent resolves gstack's text instead
// of foundation's — decided behavior under R1a, verified end-to-end.
func TestCLI_CrossBundleSwitch(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := freshCLIEnv(t, "tester")

	_, stderr, code := runCLI(t, se, "seed")
	require.Equal(t, 0, code, "seed: %s", stderr)

	answers := writeAnswers(t, se.repo, "name: Tester\nhandle: tester\nbundle: foundation\n")
	_, stderr, code = runCLI(t, se, "setup", "--file", answers, "--bundle", "foundation")
	require.Equal(t, 0, code, "setup: %s", stderr)

	// Foundation active → foundation's engineering text.
	talents, _, _, _ := showJSON(t, se, "claude")
	require.NotEmpty(t, talents)
	assert.Contains(t, talents[0], "General engineering discipline",
		"under foundation, claude resolves foundation's engineering copy")

	// Switch the active bundle to gstack in the repo config.
	cfgPath := filepath.Join(se.repo, ".punt-labs", "ethos.yaml")
	require.NoError(t, os.WriteFile(cfgPath,
		[]byte("agent: claude\nactive_bundle: gstack\nteam: gstack\n"), 0o644))

	// gstack active → gstack's engineering text, from the same global
	// identity, no re-setup.
	talents, _, errOut, showCode := showJSON(t, se, "claude")
	require.Equal(t, 0, showCode, "show after switch: %s", errOut)
	require.NotEmpty(t, talents)
	assert.Contains(t, talents[0], "gstack builder framework",
		"after switching to gstack, claude resolves gstack's engineering copy")
	assert.NotContains(t, talents[0], "General engineering discipline",
		"foundation's copy must no longer resolve once gstack is active")
	assert.NotContains(t, errOut, "warning:", "switch must not produce warnings; stderr=%s", errOut)
}
