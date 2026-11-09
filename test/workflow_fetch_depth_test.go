package test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// plugin/hooks builds old ethos binaries (93c9f82, v4.8.0) with `git archive`
// and, when CI is set, fails instead of skipping if that history is missing.
// Any workflow job that runs the Go test suite (directly or through make) must
// therefore check the repo out with full history. v4.22.0 was tagged but never
// published because release.yml kept the default depth 1.

type workflowFile struct {
	Jobs map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	Steps []workflowStep `yaml:"steps"`
}

type workflowStep struct {
	Uses string         `yaml:"uses"`
	Run  string         `yaml:"run"`
	With map[string]any `yaml:"with"`
}

// testRunLine returns the first line of a run script that invokes the Go test
// suite: `go test`, or `make` with a test, check, or test-e2e* target.
func testRunLine(run string) (string, bool) {
	for _, line := range strings.Split(run, "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "go" && i+1 < len(fields) && fields[i+1] == "test" {
				return strings.TrimSpace(line), true
			}
			if f != "make" {
				continue
			}
			for _, target := range fields[i+1:] {
				if target == "test" || target == "check" || strings.HasPrefix(target, "test-e2e") {
					return strings.TrimSpace(line), true
				}
			}
		}
	}
	return "", false
}

// checksOutThisRepo reports whether a step is actions/checkout of punt-labs/ethos.
func checksOutThisRepo(s workflowStep) bool {
	if !strings.HasPrefix(s.Uses, "actions/checkout@") {
		return false
	}
	repo, ok := s.With["repository"]
	return !ok || repo == "punt-labs/ethos"
}

// workflowFiles returns every workflow file in dir. GitHub loads both .yml and
// .yaml, so both extensions are scanned.
func workflowFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	for _, ext := range []string{"*.yml", "*.yaml"} {
		matches, err := filepath.Glob(filepath.Join(dir, ext))
		require.NoError(t, err)
		files = append(files, matches...)
	}
	return files
}

// fetchDepthViolations returns one message per job in the workflow file that
// runs tests without a full-history checkout of this repo.
func fetchDepthViolations(t *testing.T, file string) []string {
	t.Helper()
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	var wf workflowFile
	require.NoError(t, yaml.Unmarshal(data, &wf), file)

	var out []string
	for id, job := range wf.Jobs {
		var runLine string
		for _, s := range job.Steps {
			if line, ok := testRunLine(s.Run); ok {
				runLine = line
				break
			}
		}
		if runLine == "" {
			continue
		}
		var checkouts int
		for _, s := range job.Steps {
			if !checksOutThisRepo(s) {
				continue
			}
			checkouts++
			if depth, ok := s.With["fetch-depth"]; !ok || depth != 0 {
				out = append(out, fmt.Sprintf(
					"%s: job %q runs %q but its actions/checkout step does not set fetch-depth: 0 "+
						"(plugin/hooks tests build old binaries from git history; a depth-1 clone fails them in CI)",
					filepath.Base(file), id, runLine))
			}
		}
		if checkouts == 0 {
			out = append(out, fmt.Sprintf(
				"%s: job %q runs %q but has no actions/checkout step for this repo",
				filepath.Base(file), id, runLine))
		}
	}
	return out
}

func TestWorkflowsFetchFullHistoryWhenRunningTests(t *testing.T) {
	files := workflowFiles(t, filepath.Join("..", ".github", "workflows"))
	require.NotEmpty(t, files, "no workflow files found")
	for _, file := range files {
		require.Empty(t, fetchDepthViolations(t, file))
	}
}

// TestFetchDepthGuardScansYamlExtension proves the guard sees a .yaml workflow:
// a depth-1 job running make test, written with the .yaml extension, must be
// discovered by workflowFiles and rejected by fetchDepthViolations.
func TestFetchDepthGuardScansYamlExtension(t *testing.T) {
	dir := t.TempDir()
	workflow := `name: shallow
on: push
jobs:
  shallow:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683 # v4
      - run: make test
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "shallow.yaml"), []byte(workflow), 0o600))

	files := workflowFiles(t, dir)
	require.Len(t, files, 1, ".yaml workflow was not discovered")
	violations := fetchDepthViolations(t, files[0])
	require.Len(t, violations, 1)
	require.Contains(t, violations[0], "does not set fetch-depth: 0")
	require.Contains(t, violations[0], `job "shallow"`)
	require.Contains(t, violations[0], "shallow.yaml")
}
