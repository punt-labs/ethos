//go:build behavioral

// Package behavioral provides the test harness for L4 agent behavioral
// tests. These tests spawn real Claude Code agents with persona system
// prompts, create ethos missions, and verify protocol compliance and
// constraint adherence.
//
// Run with: go test -tags behavioral -timeout 10m ./tests/behavioral/
//
// Requires:
//   - ANTHROPIC_API_KEY in environment
//   - claude CLI in PATH
//   - ethos CLI in PATH (or built via TestMain)
package behavioral

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ethosBinary is the path to the compiled ethos binary, built once by TestMain.
var ethosBinary string

// claudeBinary is the path to the claude CLI.
var claudeBinary string

// Fixture holds the filesystem layout for a behavioral test.
type Fixture struct {
	// Root is the top-level temp directory.
	Root string
	// Home is the fake HOME directory.
	Home string
	// Repo is the fixture Go repo.
	Repo string
	// EthosRoot is .punt-labs/ethos inside the repo.
	EthosRoot string
	// Env is the environment for subprocess calls.
	Env []string
}

// SetupFixture creates a minimal fixture repo with ethos installed:
//   - A git-initialized Go repo with a counter package
//   - Global identity store with worker, leader, evaluator identities
//   - Personality, writing style, talent files for the evaluator
//   - A team with both worker (eng role) and evaluator (reviewer role)
//   - Repo-local ethos config pointing to test-agent
//   - Ethos MCP config for claude --bare
func SetupFixture(t *testing.T) *Fixture {
	t.Helper()

	root := t.TempDir()
	home := filepath.Join(root, "home")
	repo := filepath.Join(root, "repo")
	ethosGlobal := filepath.Join(home, ".punt-labs", "ethos")

	// Create directory structure.
	dirs := []string{
		filepath.Join(ethosGlobal, "identities"),
		filepath.Join(ethosGlobal, "sessions"),
		filepath.Join(ethosGlobal, "missions"),
		filepath.Join(ethosGlobal, "roles"),
		filepath.Join(ethosGlobal, "teams"),
		filepath.Join(ethosGlobal, "personalities"),
		filepath.Join(ethosGlobal, "writing-styles"),
		filepath.Join(ethosGlobal, "talents"),
		filepath.Join(repo, ".punt-labs", "ethos", "identities"),
		filepath.Join(repo, "pkg", "counter"),
	}
	for _, d := range dirs {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}

	// --- Attribute content files ---

	// Personality for evaluator.
	writeFile(t, filepath.Join(ethosGlobal, "personalities", "test-reviewer.md"),
		"# Test Reviewer\n\nA test evaluator persona.\n")

	// Writing style for evaluator.
	writeFile(t, filepath.Join(ethosGlobal, "writing-styles", "test-review-prose.md"),
		"# Test Review Prose\n\nPlaceholder writing style.\n")

	// Talent for evaluator.
	writeFile(t, filepath.Join(ethosGlobal, "talents", "code-review.md"),
		"# Code Review\n\nReview code.\n")

	// Talent for worker.
	writeFile(t, filepath.Join(ethosGlobal, "talents", "go-dev.md"),
		"# Go Development\n\nWrite Go code.\n")

	// Personality for worker.
	writeFile(t, filepath.Join(ethosGlobal, "personalities", "test-worker.md"),
		"# Test Worker\n\nA test worker persona.\n")

	// Writing style for worker.
	writeFile(t, filepath.Join(ethosGlobal, "writing-styles", "test-worker-prose.md"),
		"# Test Worker Prose\n\nPlaceholder writing style.\n")

	// --- Identities ---

	// Worker identity (test-agent).
	writeFile(t, filepath.Join(ethosGlobal, "identities", "test-agent.yaml"),
		`name: Test Agent
handle: test-agent
kind: agent
personality: test-worker
writing_style: test-worker-prose
talents:
  - go-dev
`)

	// Leader identity (test-leader).
	writeFile(t, filepath.Join(ethosGlobal, "identities", "test-leader.yaml"),
		`name: Test Leader
handle: test-leader
kind: agent
`)

	// Evaluator identity (test-evaluator) — needs full content for hash.
	writeFile(t, filepath.Join(ethosGlobal, "identities", "test-evaluator.yaml"),
		`name: Test Evaluator
handle: test-evaluator
kind: agent
personality: test-reviewer
writing_style: test-review-prose
talents:
  - code-review
`)

	// --- Roles ---

	writeFile(t, filepath.Join(ethosGlobal, "roles", "eng.yaml"),
		`name: eng
responsibilities:
  - write code
permissions:
  - read
  - write
`)

	writeFile(t, filepath.Join(ethosGlobal, "roles", "reviewer.yaml"),
		`name: reviewer
responsibilities:
  - review code
permissions:
  - read
`)

	// --- Team (worker and evaluator in different roles) ---

	writeFile(t, filepath.Join(ethosGlobal, "teams", "behavioral-test.yaml"),
		`name: behavioral-test
members:
  - identity: test-agent
    role: eng
  - identity: test-evaluator
    role: reviewer
`)

	// --- Repo ethos config ---

	writeFile(t, filepath.Join(repo, ".punt-labs", "ethos.yaml"),
		"agent: test-agent\n")

	// --- Fixture Go code: a simple counter package ---

	writeFile(t, filepath.Join(repo, "pkg", "counter", "counter.go"),
		`package counter

// Counter tracks a count.
type Counter struct {
	n int
}

// Increment adds 1 to the counter.
func (c *Counter) Increment() {
	c.n++
}

// Value returns the current count.
func (c *Counter) Value() int {
	return c.n
}

// Reset sets the counter to zero.
func (c *Counter) Reset() {
	c.n = 0
}
`)

	writeFile(t, filepath.Join(repo, "go.mod"),
		"module fixture\n\ngo 1.22\n")

	// Git init the repo.
	env := fixtureEnv(home)
	gitCmd(t, repo, env, "init")
	gitCmd(t, repo, env, "config", "user.email", "test@test.local")
	gitCmd(t, repo, env, "config", "user.name", "Test Agent")
	gitCmd(t, repo, env, "add", ".")
	gitCmd(t, repo, env, "commit", "-m", "initial")

	return &Fixture{
		Root:      root,
		Home:      home,
		Repo:      repo,
		EthosRoot: filepath.Join(repo, ".punt-labs", "ethos"),
		Env:       env,
	}
}

// fixtureEnv builds the environment slice for subprocess calls.
func fixtureEnv(home string) []string {
	return []string{
		"HOME=" + home,
		"USER=test-agent",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"PATH=" + os.Getenv("PATH"),
		"ANTHROPIC_API_KEY=" + os.Getenv("ANTHROPIC_API_KEY"),
	}
}

// CreateMission writes a mission contract YAML file and calls
// ethos mission create --file --json. Returns the mission ID.
func (f *Fixture) CreateMission(t *testing.T, contractYAML string) string {
	t.Helper()

	contractPath := filepath.Join(f.Root, "contract.yaml")
	writeFile(t, contractPath, contractYAML)

	stdout, stderr, err := f.runEthos(t, "mission", "create", "--file", contractPath, "--json")
	require.NoError(t, err, "mission create failed: stdout=%s stderr=%s", stdout, stderr)

	var created map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &created),
		"mission create output is not JSON: %s", stdout)

	id, ok := created["mission_id"].(string)
	require.True(t, ok, "mission create output missing 'mission_id': %s", stdout)
	return id
}

// MissionLog returns the parsed event log for a mission.
// The JSON shape is {"events": [...], "warnings": [...]}.
func (f *Fixture) MissionLog(t *testing.T, missionID string) []map[string]interface{} {
	t.Helper()

	stdout, _, err := f.runEthos(t, "mission", "log", missionID, "--json")
	require.NoError(t, err, "mission log failed")

	var payload struct {
		Events   []map[string]interface{} `json:"events"`
		Warnings []string                 `json:"warnings"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &payload),
		"mission log output is not the expected shape: %s", stdout)
	return payload.Events
}

// MissionResults returns the parsed results for a mission.
// The JSON shape is a bare array: [...].
func (f *Fixture) MissionResults(t *testing.T, missionID string) []map[string]interface{} {
	t.Helper()

	stdout, _, err := f.runEthos(t, "mission", "results", missionID, "--json")
	require.NoError(t, err, "mission results failed")

	var results []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &results),
		"mission results output is not JSON array: %s", stdout)
	return results
}

// GitDiffFiles returns every file changed since base, including committed,
// staged, unstaged, and untracked files. This catches changes regardless
// of whether the agent committed, staged, or left files in the working tree.
func (f *Fixture) GitDiffFiles(t *testing.T, base string) []string {
	t.Helper()

	seen := make(map[string]bool)

	// Committed changes since base.
	for _, line := range f.gitLines(t, "diff", "--name-only", base+"..HEAD") {
		seen[line] = true
	}

	// Staged (index) changes.
	for _, line := range f.gitLines(t, "diff", "--name-only", "--cached") {
		seen[line] = true
	}

	// Unstaged working-tree changes.
	for _, line := range f.gitLines(t, "diff", "--name-only") {
		seen[line] = true
	}

	// Untracked new files.
	for _, line := range f.gitLines(t, "ls-files", "--others", "--exclude-standard") {
		seen[line] = true
	}

	files := make([]string, 0, len(seen))
	for f := range seen {
		files = append(files, f)
	}
	return files
}

// gitLines runs a git command and returns non-empty output lines.
func (f *Fixture) gitLines(t *testing.T, args ...string) []string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = f.Repo
	cmd.Env = f.Env
	out, err := cmd.Output()
	require.NoError(t, err, "git %s failed", strings.Join(args, " "))

	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// GitHead returns the current HEAD sha.
func (f *Fixture) GitHead(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = f.Repo
	cmd.Env = f.Env
	out, err := cmd.Output()
	require.NoError(t, err, "git rev-parse HEAD failed")
	return strings.TrimSpace(string(out))
}

// SpawnAgent runs claude --bare --print with the given system prompt and
// user prompt. Returns the agent's output and any error.
func (f *Fixture) SpawnAgent(t *testing.T, opts AgentOpts) (output string, err error) {
	t.Helper()

	args := []string{
		"--bare",
		"--print",
		"--output-format", "text",
		"--no-session-persistence",
		"--dangerously-skip-permissions",
		"--max-budget-usd", fmt.Sprintf("%.2f", opts.BudgetUSD),
	}

	if opts.SystemPrompt != "" {
		args = append(args, "--system-prompt", opts.SystemPrompt)
	}

	// Wire ethos MCP server so the agent can call mission tools.
	mcpConfig := f.writeMCPConfig(t)
	args = append(args, "--mcp-config", mcpConfig)

	args = append(args, "--add-dir", f.Repo)

	args = append(args, "-p", opts.Prompt)

	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()

	t.Logf("Spawning agent: claude %s", strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, claudeBinary, args...)
	cmd.Dir = f.Repo
	cmd.Env = f.Env

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	runErr := cmd.Run()
	t.Logf("Agent stderr (first 1000 chars): %.1000s", errBuf.String())

	if ctx.Err() == context.DeadlineExceeded {
		return outBuf.String(), fmt.Errorf("agent timed out after %s", opts.Timeout)
	}

	return outBuf.String(), runErr
}

// AgentOpts configures an agent invocation.
type AgentOpts struct {
	// SystemPrompt is an inline system prompt.
	SystemPrompt string
	// Prompt is the user-facing task prompt.
	Prompt string
	// BudgetUSD is the max spend for this invocation.
	BudgetUSD float64
	// Timeout is the max wall-clock time.
	Timeout time.Duration
}

// DefaultAgentOpts returns sensible defaults.
func DefaultAgentOpts(prompt string) AgentOpts {
	return AgentOpts{
		Prompt:    prompt,
		BudgetUSD: 0.50,
		Timeout:   3 * time.Minute,
	}
}

// Reflect writes a reflection YAML to a temp file and calls
// ethos mission reflect <id> --file <path> --json.
func (f *Fixture) Reflect(t *testing.T, missionID, reflectionYAML string) {
	t.Helper()

	path := filepath.Join(f.Root, "reflection.yaml")
	writeFile(t, path, reflectionYAML)

	stdout, stderr, err := f.runEthos(t, "mission", "reflect", missionID, "--file", path, "--json")
	require.NoError(t, err, "mission reflect failed: stdout=%s stderr=%s", stdout, stderr)
	t.Logf("Reflect output: %s", stdout)
}

// Advance calls ethos mission advance <id> --json and returns the parsed output.
func (f *Fixture) Advance(t *testing.T, missionID string) map[string]interface{} {
	t.Helper()

	stdout, stderr, err := f.runEthos(t, "mission", "advance", missionID, "--json")
	require.NoError(t, err, "mission advance failed: stdout=%s stderr=%s", stdout, stderr)

	var result map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &result),
		"mission advance output is not JSON: %s", stdout)
	return result
}

// MissionShow calls ethos mission show <id> --json and returns the parsed output.
// The JSON shape is a ShowPayload: contract fields + "results" array + optional "warnings".
func (f *Fixture) MissionShow(t *testing.T, missionID string) map[string]interface{} {
	t.Helper()

	stdout, stderr, err := f.runEthos(t, "mission", "show", missionID, "--json")
	require.NoError(t, err, "mission show failed: stdout=%s stderr=%s", stdout, stderr)

	var result map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &result),
		"mission show output is not JSON: %s", stdout)
	return result
}

// MissionReflections returns the parsed reflections for a mission.
// The JSON shape is a bare array: [...].
func (f *Fixture) MissionReflections(t *testing.T, missionID string) []map[string]interface{} {
	t.Helper()

	stdout, _, err := f.runEthos(t, "mission", "reflections", missionID, "--json")
	require.NoError(t, err, "mission reflections failed")

	var reflections []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(stdout), &reflections),
		"mission reflections output is not JSON array: %s", stdout)
	return reflections
}

// MissionClose calls ethos mission close <id> --json.
func (f *Fixture) MissionClose(t *testing.T, missionID string) {
	t.Helper()

	stdout, stderr, err := f.runEthos(t, "mission", "close", missionID, "--json")
	require.NoError(t, err, "mission close failed: stdout=%s stderr=%s", stdout, stderr)
	t.Logf("Close output: %s", stdout)
}

// --- Assertion helpers ---

// AssertFilesInWriteSet checks that all changed files are within the
// allowed write set. Returns any violations.
func AssertFilesInWriteSet(changedFiles []string, writeSet []string) []string {
	allowed := make(map[string]bool, len(writeSet))
	for _, ws := range writeSet {
		allowed[ws] = true
	}

	var violations []string
	for _, f := range changedFiles {
		if !allowed[f] {
			violations = append(violations, f)
		}
	}
	return violations
}

// --- Internal helpers ---

func (f *Fixture) runEthos(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	cmd := exec.Command(ethosBinary, args...)
	cmd.Dir = f.Repo
	cmd.Env = f.Env

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err = cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

func (f *Fixture) writeMCPConfig(t *testing.T) string {
	t.Helper()

	config := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"ethos": map[string]interface{}{
				"command": ethosBinary,
				"args":    []string{"serve"},
				"env": map[string]string{
					"HOME": f.Home,
					"USER": "test-agent",
				},
			},
		},
	}

	path := filepath.Join(f.Root, "mcp-config.json")
	data, err := json.MarshalIndent(config, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return path
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func gitCmd(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s failed: %s", strings.Join(args, " "), out)
}
