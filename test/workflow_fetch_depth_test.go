package test

import (
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

func TestWorkflowsFetchFullHistoryWhenRunningTests(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", ".github", "workflows", "*.yml"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "no workflow files found")

	for _, file := range files {
		data, err := os.ReadFile(file)
		require.NoError(t, err)
		var wf workflowFile
		require.NoError(t, yaml.Unmarshal(data, &wf), file)

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
				depth, ok := s.With["fetch-depth"]
				require.Truef(t, ok && depth == 0,
					"%s: job %q runs %q but its actions/checkout step does not set fetch-depth: 0 "+
						"(plugin/hooks tests build old binaries from git history; a depth-1 clone fails them in CI)",
					filepath.Base(file), id, runLine)
			}
			require.NotZerof(t, checkouts,
				"%s: job %q runs %q but has no actions/checkout step for this repo",
				filepath.Base(file), id, runLine)
		}
	}
}
