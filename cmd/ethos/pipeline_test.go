package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/internal/mission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// seedPipelineYAML writes a pipeline YAML file into the global pipelines
// directory under home. The caller supplies raw YAML content so tests
// control the exact shape.
func seedPipelineYAML(t *testing.T, home, name, content string) {
	t.Helper()
	dir := filepath.Join(home, ".punt-labs", "ethos", "pipelines")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, name+".yaml"),
		[]byte(content), 0o644))
}

// seedArchetypeYAML writes an archetype YAML file into the global
// archetypes directory under home. Like seedPipelineYAML, the caller
// controls content.
func seedArchetypeYAML(t *testing.T, home, name, content string) {
	t.Helper()
	dir := filepath.Join(home, ".punt-labs", "ethos", "archetypes")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, name+".yaml"),
		[]byte(content), 0o644))
}

// pipelineTestEnv creates a temp HOME seeded with:
//   - djb evaluator identity (personality, writing-style, talent)
//   - "implement" and "review" archetypes
//
// Returns the HOME path. Pipeline and archetype YAML files for
// specific tests are seeded by the caller.
func pipelineTestEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, ".punt-labs", "ethos")
	seedEvaluator(t, root) // djb identity + implement archetype
	seedArchetypeYAML(t, home, "review", `name: review
description: test review archetype
budget_default:
  rounds: 1
  reflection_after_each: true
`)
	return home
}

// pipelineInProcessEnv sets HOME to a temp directory seeded with the
// djb evaluator, archetypes, and a git-initialized repo with
// ethos.yaml. It resets all pipeline and mission flag state so tests
// don't leak into each other. Returns the HOME path.
func pipelineInProcessEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, ".punt-labs", "ethos")
	seedEvaluator(t, root)
	seedArchetypeYAML(t, home, "review", "name: review\ndescription: test review archetype\nbudget_default:\n  rounds: 1\n  reflection_after_each: true\n")

	// Create repo with ethos.yaml and git init.
	repo := filepath.Join(home, "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".punt-labs"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(repo, ".punt-labs", "ethos.yaml"),
		[]byte("agent: claude\n"), 0o644))
	cmd := exec.Command("git", "init", repo)
	cmd.Env = []string{
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"PATH=" + os.Getenv("PATH"),
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init: %s", out)

	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Chdir(repo)

	resetPipelineFlags(t)
	return home
}

// resetPipelineFlags zeroes all package-level flag state that pipeline
// and mission commands bind via cobra. Without this, a --json or
// --dry-run from one test leaks into the next.
func resetPipelineFlags(t *testing.T) {
	t.Helper()
	jsonOutput = false
	pipelineInstVars = nil
	pipelineInstLeader = ""
	pipelineInstEvaluator = ""
	pipelineInstWorker = ""
	pipelineInstID = ""
	pipelineInstDryRun = false
	missionListStatus = "open"
	missionListPipeline = ""
	missionCreateFile = ""
	missionCloseStatus = mission.StatusClosed
	missionResultFile = ""
	missionResultVerify = false
	missionResultBase = "main"
	missionExportDir = ".ethos/missions"
	t.Cleanup(func() {
		jsonOutput = false
		pipelineInstVars = nil
		pipelineInstLeader = ""
		pipelineInstEvaluator = ""
		pipelineInstWorker = ""
		pipelineInstID = ""
		pipelineInstDryRun = false
		missionListStatus = "open"
		missionListPipeline = ""
		missionCreateFile = ""
		missionCloseStatus = mission.StatusClosed
		missionResultFile = ""
		missionResultVerify = false
		missionResultBase = "main"
		missionExportDir = ".ethos/missions"
	})
}

// execPipelineHandler runs a cobra command in-process, capturing
// os.Stdout (which pipeline handlers write to directly via
// fmt.Println and printJSON) as well as rootCmd.SetErr. Returns
// captured stdout, the cobra error message (if any), and the error.
func execPipelineHandler(t *testing.T, args ...string) (stdout string, errMsg string, err error) {
	t.Helper()
	resetPipelineFlags(t)

	// Capture os.Stdout — pipeline handlers write there directly.
	oldStdout := os.Stdout
	pr, pw, pipeErr := os.Pipe()
	require.NoError(t, pipeErr)
	os.Stdout = pw

	var errBuf bytes.Buffer
	rootCmd.SetErr(&errBuf)
	rootCmd.SetArgs(args)
	defer func() {
		os.Stdout = oldStdout
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	}()

	err = rootCmd.Execute()

	pw.Close()
	var outBuf bytes.Buffer
	_, copyErr := io.Copy(&outBuf, pr)
	require.NoError(t, copyErr)

	return outBuf.String(), errBuf.String(), err
}

// =====================================================================
// In-process handler tests — visible to go test -coverprofile
// =====================================================================

// --- pipeline list (in-process) ---

func TestPipelineHandler_List(t *testing.T) {
	home := pipelineInProcessEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)
	seedPipelineYAML(t, home, "docs-test", docsPipeline)

	stdout, _, err := execPipelineHandler(t, "mission", "pipeline", "list")
	require.NoError(t, err)
	assert.Contains(t, stdout, "quick-test")
	assert.Contains(t, stdout, "docs-test")
}

func TestPipelineHandler_ListJSON(t *testing.T) {
	home := pipelineInProcessEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)
	seedPipelineYAML(t, home, "docs-test", docsPipeline)

	stdout, _, err := execPipelineHandler(t, "mission", "pipeline", "list", "--json")
	require.NoError(t, err)

	var entries []struct {
		Name   string `json:"name"`
		Stages int    `json:"stages"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &entries))
	assert.Len(t, entries, 2)

	names := map[string]int{}
	for _, e := range entries {
		names[e.Name] = e.Stages
	}
	assert.Equal(t, 2, names["quick-test"])
	assert.Equal(t, 1, names["docs-test"])
}

// --- pipeline show (in-process) ---

func TestPipelineHandler_Show(t *testing.T) {
	home := pipelineInProcessEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)

	stdout, _, err := execPipelineHandler(t, "mission", "pipeline", "show", "quick-test")
	require.NoError(t, err)
	assert.Contains(t, stdout, "implement")
	assert.Contains(t, stdout, "review")
	assert.Contains(t, stdout, "Quick test pipeline")
}

func TestPipelineHandler_ShowNotFound(t *testing.T) {
	_ = pipelineInProcessEnv(t)

	_, _, err := execPipelineHandler(t, "mission", "pipeline", "show", "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// --- pipeline instantiate (in-process) ---

func TestPipelineHandler_InstantiateDryRun(t *testing.T) {
	home := pipelineInProcessEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)

	stdout, _, err := execPipelineHandler(t,
		"mission", "pipeline", "instantiate", "quick-test",
		"--leader", "claude",
		"--worker", "bwk",
		"--evaluator", "djb",
		"--var", "target=internal/foo/",
		"--dry-run",
	)
	require.NoError(t, err)
	assert.Contains(t, stdout, "Dry run")
	assert.Contains(t, stdout, "implement")
	assert.Contains(t, stdout, "review")

	// No mission files should exist.
	missionsDir := filepath.Join(home, ".punt-labs", "ethos", "missions")
	entries, dirErr := os.ReadDir(missionsDir)
	if dirErr == nil {
		var yamls []string
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".yaml" {
				yamls = append(yamls, e.Name())
			}
		}
		assert.Empty(t, yamls, "dry-run must not create mission files")
	}
}

func TestPipelineHandler_InstantiateReal(t *testing.T) {
	home := pipelineInProcessEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)

	stdout, _, err := execPipelineHandler(t,
		"mission", "pipeline", "instantiate", "quick-test",
		"--leader", "claude",
		"--worker", "bwk",
		"--evaluator", "djb",
		"--var", "target=internal/foo/",
	)
	require.NoError(t, err)
	assert.Contains(t, stdout, "Created pipeline")

	// Verify mission files on disk.
	missionsDir := filepath.Join(home, ".punt-labs", "ethos", "missions")
	entries, err := os.ReadDir(missionsDir)
	require.NoError(t, err)

	var yamls []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".yaml" {
			yamls = append(yamls, e.Name())
		}
	}
	require.Len(t, yamls, 2, "instantiate should create 2 mission YAML files")
}

func TestPipelineHandler_InstantiateMissingVar(t *testing.T) {
	home := pipelineInProcessEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)

	_, _, err := execPipelineHandler(t,
		"mission", "pipeline", "instantiate", "quick-test",
		"--leader", "claude",
		"--worker", "bwk",
		"--evaluator", "djb",
		// omit --var target=...
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown template variable")
}

func TestPipelineHandler_InstantiateMissingWorker(t *testing.T) {
	home := pipelineInProcessEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)

	_, _, err := execPipelineHandler(t,
		"mission", "pipeline", "instantiate", "quick-test",
		"--leader", "claude",
		"--evaluator", "djb",
		"--var", "target=internal/foo/",
		// omit --worker
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no worker")
}

func TestPipelineHandler_InstantiateInvalidID(t *testing.T) {
	home := pipelineInProcessEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)

	_, _, err := execPipelineHandler(t,
		"mission", "pipeline", "instantiate", "quick-test",
		"--leader", "claude",
		"--worker", "bwk",
		"--evaluator", "djb",
		"--var", "target=internal/foo/",
		"--id", "has spaces",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must match")
}

// --- mission list --pipeline (in-process) ---

func TestPipelineHandler_MissionListByPipeline(t *testing.T) {
	home := pipelineInProcessEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)

	// Instantiate to create 2 missions.
	stdout, _, err := execPipelineHandler(t,
		"mission", "pipeline", "instantiate", "quick-test",
		"--leader", "claude",
		"--worker", "bwk",
		"--evaluator", "djb",
		"--var", "target=internal/foo/",
		"--id", "test-filter-ip",
		"--json",
	)
	require.NoError(t, err)

	var instOut struct {
		Pipeline string `json:"pipeline"`
		Missions []struct {
			ID string `json:"id"`
		} `json:"missions"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &instOut))
	require.Len(t, instOut.Missions, 2)
	pipelineID := instOut.Pipeline

	// List with pipeline filter — should match 2.
	filtered, _, err := execPipelineHandler(t,
		"mission", "list", "--json", "--pipeline", pipelineID,
	)
	require.NoError(t, err)
	var filteredEntries []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(filtered), &filteredEntries))
	assert.Len(t, filteredEntries, 2)

	// Non-matching pipeline — should match 0.
	empty, _, err := execPipelineHandler(t,
		"mission", "list", "--json", "--pipeline", "nonexistent-pipeline",
	)
	require.NoError(t, err)
	var emptyEntries []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(empty), &emptyEntries))
	assert.Empty(t, emptyEntries)
}

// countMissionFiles returns the number of .yaml contract files in the
// missions directory under home. Counts only contract files — not
// reflections, results, or dotfiles.
func countMissionFiles(home string) int {
	dir := filepath.Join(home, ".punt-labs", "ethos", "missions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) == ".yaml" &&
			!strings.HasPrefix(name, ".") &&
			!strings.HasSuffix(name, ".reflections.yaml") &&
			!strings.HasSuffix(name, ".results.yaml") {
			n++
		}
	}
	return n
}

// badArchPipeline is a two-stage pipeline where the second stage
// references an archetype that does not exist, causing validation
// failure. The first stage uses a valid archetype.
const badArchPipeline = `name: bad-arch-test
description: "Pipeline with invalid archetype on stage 2"
stages:
  - name: implement
    archetype: implement
    write_set:
      - "internal/foo/"
    success_criteria:
      - "make check passes"
    context: "Implement the thing"
    budget:
      rounds: 2
      reflection_after_each: true
  - name: bogus
    archetype: does-not-exist
    write_set:
      - ".tmp/review.md"
    success_criteria:
      - "findings reported"
    context: "This stage should fail validation"
    inputs_from: implement
    budget:
      rounds: 1
      reflection_after_each: true
`

func TestPipelineHandler_Instantiate_AtomicRollbackOnValidationFailure(t *testing.T) {
	home := pipelineInProcessEnv(t)
	seedPipelineYAML(t, home, "bad-arch-test", badArchPipeline)

	assert.Equal(t, 0, countMissionFiles(home), "precondition: no missions")

	_, _, err := execPipelineHandler(t,
		"mission", "pipeline", "instantiate", "bad-arch-test",
		"--leader", "claude",
		"--worker", "bwk",
		"--evaluator", "djb",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown mission type")

	// No missions should exist — stage 1 must not be orphaned.
	assert.Equal(t, 0, countMissionFiles(home),
		"atomic instantiate must leave zero missions on validation failure")
}

func TestPipelineHandler_Instantiate_AllStagesCreatedOnSuccess(t *testing.T) {
	home := pipelineInProcessEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)

	stdout, _, err := execPipelineHandler(t,
		"mission", "pipeline", "instantiate", "quick-test",
		"--leader", "claude",
		"--worker", "bwk",
		"--evaluator", "djb",
		"--var", "target=internal/foo/",
	)
	require.NoError(t, err)
	assert.Contains(t, stdout, "Created pipeline")
	assert.Equal(t, 2, countMissionFiles(home),
		"successful instantiate must create all stages")
}

// =====================================================================
// Subprocess tests (not visible to coverprofile — kept for exit-code
// and stderr assertions that require a real process boundary)
// =====================================================================

const quickPipeline = `name: quick-test
description: "Quick test pipeline"
stages:
  - name: implement
    archetype: implement
    write_set:
      - "{target}"
    success_criteria:
      - "make check passes"
    context: "Implement in {target}"
    budget:
      rounds: 2
      reflection_after_each: true
  - name: review
    archetype: review
    write_set:
      - ".tmp/review.md"
    success_criteria:
      - "findings reported"
    context: "Review changes"
    inputs_from: implement
    budget:
      rounds: 1
      reflection_after_each: true
`

const docsPipeline = `name: docs-test
description: "Docs test pipeline"
stages:
  - name: write-docs
    archetype: implement
    write_set:
      - "docs/"
    success_criteria:
      - "docs updated"
    context: "Write documentation"
    budget:
      rounds: 1
      reflection_after_each: true
`

// --- pipeline list ---

func TestPipelineCLI_List(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	home := pipelineTestEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)
	seedPipelineYAML(t, home, "docs-test", docsPipeline)

	env := append(os.Environ(), "HOME="+home)
	stdout, _, exitCode := runCLI(t, &cliSubprocessEnv{home: home, env: env}, "mission", "pipeline", "list")

	assert.Equal(t, 0, exitCode)
	assert.Contains(t, stdout, "quick-test")
	assert.Contains(t, stdout, "docs-test")
}

func TestPipelineCLI_ListJSON(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	home := pipelineTestEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)
	seedPipelineYAML(t, home, "docs-test", docsPipeline)

	env := append(os.Environ(), "HOME="+home)
	stdout, _, exitCode := runCLI(t, &cliSubprocessEnv{home: home, env: env}, "mission", "pipeline", "list", "--json")

	assert.Equal(t, 0, exitCode)
	var entries []struct {
		Name   string `json:"name"`
		Stages int    `json:"stages"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &entries))
	assert.Len(t, entries, 2)

	names := map[string]int{}
	for _, e := range entries {
		names[e.Name] = e.Stages
	}
	assert.Equal(t, 2, names["quick-test"])
	assert.Equal(t, 1, names["docs-test"])
}

func TestPipelineCLI_ListEmpty(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	home := pipelineTestEnv(t)
	// No pipelines seeded.

	env := append(os.Environ(), "HOME="+home)
	stdout, _, exitCode := runCLI(t, &cliSubprocessEnv{home: home, env: env}, "mission", "pipeline", "list")

	assert.Equal(t, 0, exitCode)
	assert.Contains(t, stdout, "No pipelines found.")
}

// --- pipeline show ---

func TestPipelineCLI_Show(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	home := pipelineTestEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)

	env := append(os.Environ(), "HOME="+home)
	stdout, _, exitCode := runCLI(t, &cliSubprocessEnv{home: home, env: env}, "mission", "pipeline", "show", "quick-test")

	assert.Equal(t, 0, exitCode)
	assert.Contains(t, stdout, "implement")
	assert.Contains(t, stdout, "review")
	assert.Contains(t, stdout, "Quick test pipeline")
}

func TestPipelineCLI_ShowJSON(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	home := pipelineTestEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)

	env := append(os.Environ(), "HOME="+home)
	stdout, _, exitCode := runCLI(t, &cliSubprocessEnv{home: home, env: env}, "mission", "pipeline", "show", "quick-test", "--json")

	assert.Equal(t, 0, exitCode)
	var p struct {
		Name   string `json:"name"`
		Stages []struct {
			Name string `json:"name"`
		} `json:"stages"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &p))
	assert.Equal(t, "quick-test", p.Name)
	require.Len(t, p.Stages, 2)
	assert.Equal(t, "implement", p.Stages[0].Name)
	assert.Equal(t, "review", p.Stages[1].Name)
}

func TestPipelineCLI_ShowNotFound(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	home := pipelineTestEnv(t)

	env := append(os.Environ(), "HOME="+home)
	_, stderr, exitCode := runCLI(t, &cliSubprocessEnv{home: home, env: env}, "mission", "pipeline", "show", "nonexistent")

	assert.Equal(t, 1, exitCode)
	assert.Contains(t, stderr, "not found")
}

// --- pipeline instantiate ---

func TestPipelineCLI_InstantiateDryRun(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	home := pipelineTestEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)

	env := append(os.Environ(), "HOME="+home)
	stdout, _, exitCode := runCLI(t, &cliSubprocessEnv{home: home, env: env},
		"mission", "pipeline", "instantiate", "quick-test",
		"--leader", "claude",
		"--worker", "bwk",
		"--evaluator", "djb",
		"--var", "target=internal/foo/",
		"--dry-run",
	)

	assert.Equal(t, 0, exitCode)
	assert.Contains(t, stdout, "Dry run")
	assert.Contains(t, stdout, "implement")
	assert.Contains(t, stdout, "review")
	assert.Contains(t, stdout, "m-placeholder")

	// No mission files should exist on disk.
	missionsDir := filepath.Join(home, ".punt-labs", "ethos", "missions")
	entries, err := os.ReadDir(missionsDir)
	if err == nil {
		// Filter out non-YAML files (counter, lock files, etc).
		var yamls []string
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".yaml" {
				yamls = append(yamls, e.Name())
			}
		}
		assert.Empty(t, yamls, "dry-run must not create mission files")
	}
}

func TestPipelineCLI_InstantiateDryRunJSON(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	home := pipelineTestEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)

	env := append(os.Environ(), "HOME="+home)
	stdout, _, exitCode := runCLI(t, &cliSubprocessEnv{home: home, env: env},
		"mission", "pipeline", "instantiate", "quick-test",
		"--leader", "claude",
		"--worker", "bwk",
		"--evaluator", "djb",
		"--var", "target=internal/foo/",
		"--dry-run",
		"--json",
	)

	assert.Equal(t, 0, exitCode)
	var out struct {
		Pipeline string `json:"pipeline"`
		DryRun   bool   `json:"dry_run"`
		Missions []struct {
			Stage     string   `json:"stage"`
			ID        string   `json:"id"`
			Type      string   `json:"type"`
			DependsOn []string `json:"depends_on"`
		} `json:"missions"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &out))
	assert.NotEmpty(t, out.Pipeline)
	assert.True(t, out.DryRun)
	require.Len(t, out.Missions, 2)
	assert.Equal(t, "implement", out.Missions[0].Stage)
	assert.Equal(t, "review", out.Missions[1].Stage)
	assert.Equal(t, "implement", out.Missions[0].Type)
	assert.Equal(t, "review", out.Missions[1].Type)
}

func TestPipelineCLI_InstantiateReal(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	home := pipelineTestEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)

	env := append(os.Environ(), "HOME="+home)
	stdout, stderr, exitCode := runCLI(t, &cliSubprocessEnv{home: home, env: env},
		"mission", "pipeline", "instantiate", "quick-test",
		"--leader", "claude",
		"--worker", "bwk",
		"--evaluator", "djb",
		"--var", "target=internal/foo/",
	)
	t.Logf("stdout: %s", stdout)
	t.Logf("stderr: %s", stderr)

	assert.Equal(t, 0, exitCode)
	assert.Contains(t, stdout, "Created pipeline")

	// Verify mission files on disk.
	missionsDir := filepath.Join(home, ".punt-labs", "ethos", "missions")
	entries, err := os.ReadDir(missionsDir)
	require.NoError(t, err)

	var yamls []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".yaml" {
			yamls = append(yamls, e.Name())
		}
	}
	require.Len(t, yamls, 2, "instantiate should create 2 mission YAML files")

	// Parse each and verify Pipeline field and DependsOn wiring.
	var pipelineIDs []string
	var dependsOns [][]string
	for _, f := range yamls {
		data, readErr := os.ReadFile(filepath.Join(missionsDir, f))
		require.NoError(t, readErr)
		var c map[string]interface{}
		require.NoError(t, yaml.Unmarshal(data, &c))

		pid, _ := c["pipeline"].(string)
		assert.NotEmpty(t, pid, "mission file %s must have a pipeline field", f)
		pipelineIDs = append(pipelineIDs, pid)

		mid, _ := c["mission_id"].(string)
		assert.NotEmpty(t, mid, "mission file %s must have a mission_id", f)

		// DependsOn is []interface{} from YAML.
		if dep, ok := c["depends_on"].([]interface{}); ok {
			var ds []string
			for _, d := range dep {
				ds = append(ds, d.(string))
			}
			dependsOns = append(dependsOns, ds)
		} else {
			dependsOns = append(dependsOns, nil)
		}
	}

	// All missions share the same pipeline ID.
	assert.Equal(t, pipelineIDs[0], pipelineIDs[1],
		"both missions must share the same pipeline ID")

	// Exactly one mission should depend on the other (review depends
	// on implement via inputs_from).
	hasDep := false
	for _, dep := range dependsOns {
		if len(dep) > 0 {
			hasDep = true
			// The dependency should reference a real mission ID, not a placeholder.
			assert.NotContains(t, dep[0], "placeholder",
				"real instantiation must not use placeholder IDs")
		}
	}
	assert.True(t, hasDep, "review stage must depend on implement stage")
}

func TestPipelineCLI_InstantiateMissingVar(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	home := pipelineTestEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)

	env := append(os.Environ(), "HOME="+home)
	_, stderr, exitCode := runCLI(t, &cliSubprocessEnv{home: home, env: env},
		"mission", "pipeline", "instantiate", "quick-test",
		"--leader", "claude",
		"--worker", "bwk",
		"--evaluator", "djb",
		// omit --var target=...
	)

	assert.Equal(t, 1, exitCode)
	assert.Contains(t, stderr, "unknown template variable")
}

func TestPipelineCLI_InstantiateMissingWorker(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	home := pipelineTestEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)

	env := append(os.Environ(), "HOME="+home)
	_, stderr, exitCode := runCLI(t, &cliSubprocessEnv{home: home, env: env},
		"mission", "pipeline", "instantiate", "quick-test",
		"--leader", "claude",
		"--evaluator", "djb",
		"--var", "target=internal/foo/",
		// omit --worker
	)

	assert.Equal(t, 1, exitCode)
	assert.Contains(t, stderr, "no worker")
}

func TestPipelineCLI_InstantiateMissingLeader(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	home := pipelineTestEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)

	env := append(os.Environ(), "HOME="+home)
	_, stderr, exitCode := runCLI(t, &cliSubprocessEnv{home: home, env: env},
		"mission", "pipeline", "instantiate", "quick-test",
		"--worker", "bwk",
		"--evaluator", "djb",
		"--var", "target=internal/foo/",
		// omit --leader
	)

	// Cobra returns exit code 2 for usage errors (missing required flags).
	assert.Equal(t, 2, exitCode)
	assert.Contains(t, stderr, "required flag")
}

func TestPipelineCLI_InstantiateInvalidID(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	home := pipelineTestEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)

	env := append(os.Environ(), "HOME="+home)
	_, stderr, exitCode := runCLI(t, &cliSubprocessEnv{home: home, env: env},
		"mission", "pipeline", "instantiate", "quick-test",
		"--leader", "claude",
		"--worker", "bwk",
		"--evaluator", "djb",
		"--var", "target=internal/foo/",
		"--id", "has spaces",
	)

	assert.Equal(t, 1, exitCode)
	assert.Contains(t, stderr, "must match")
}

// --- mission list --pipeline ---

func TestPipelineCLI_ListByPipeline(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	home := pipelineTestEnv(t)
	seedPipelineYAML(t, home, "quick-test", quickPipeline)

	env := append(os.Environ(), "HOME="+home)

	// Instantiate a pipeline to create 2 missions with the same pipeline ID.
	stdout, stderr, exitCode := runCLI(t, &cliSubprocessEnv{home: home, env: env},
		"mission", "pipeline", "instantiate", "quick-test",
		"--leader", "claude",
		"--worker", "bwk",
		"--evaluator", "djb",
		"--var", "target=internal/foo/",
		"--id", "test-pipeline-filter",
		"--json",
	)
	t.Logf("instantiate stdout: %s", stdout)
	t.Logf("instantiate stderr: %s", stderr)
	require.Equal(t, 0, exitCode)

	var instOut struct {
		Pipeline string `json:"pipeline"`
		Missions []struct {
			ID string `json:"id"`
		} `json:"missions"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &instOut))
	require.Len(t, instOut.Missions, 2)
	pipelineID := instOut.Pipeline

	// List all missions (no filter) — should have 2.
	allOut, _, allCode := runCLI(t, &cliSubprocessEnv{home: home, env: env},
		"mission", "list", "--json",
	)
	require.Equal(t, 0, allCode)
	var allEntries []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(allOut), &allEntries))
	assert.Len(t, allEntries, 2)

	// List with --pipeline filter — should have exactly 2.
	filteredOut, _, filteredCode := runCLI(t, &cliSubprocessEnv{home: home, env: env},
		"mission", "list", "--json", "--pipeline", pipelineID,
	)
	require.Equal(t, 0, filteredCode)
	var filteredEntries []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(filteredOut), &filteredEntries))
	assert.Len(t, filteredEntries, 2)

	// List with a non-matching pipeline — should have 0.
	emptyOut, _, emptyCode := runCLI(t, &cliSubprocessEnv{home: home, env: env},
		"mission", "list", "--json", "--pipeline", "nonexistent-pipeline",
	)
	require.Equal(t, 0, emptyCode)
	var emptyEntries []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(emptyOut), &emptyEntries))
	assert.Empty(t, emptyEntries)
}
