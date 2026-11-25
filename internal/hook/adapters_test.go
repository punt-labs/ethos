//go:build linux || darwin

package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/stretchr/testify/require"
)

func goAdapterTool(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	require.NoError(t, err)
	data, err := exec.Command(goBin, "env", "-json", "GOCACHE", "GOMODCACHE").Output()
	require.NoError(t, err)
	var caches map[string]string
	require.NoError(t, json.Unmarshal(data, &caches))
	for _, key := range []string{"GOCACHE", "GOMODCACHE"} {
		require.NotEmpty(t, caches[key])
		t.Setenv(key, caches[key])
	}
	return goBin
}

func TestGoAdapterCacheEnvironment(t *testing.T) {
	t.Setenv("GOCACHE", "")
	t.Setenv("GOMODCACHE", "")
	goBin, err := exec.LookPath("go")
	require.NoError(t, err)
	want, err := exec.Command(goBin, "env", "GOCACHE", "GOMODCACHE").Output()
	require.NoError(t, err)
	goAdapterTool(t)
	t.Setenv("HOME", t.TempDir())
	got, err := exec.Command(goBin, "env", "GOCACHE", "GOMODCACHE").Output()
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}

func TestGoAdapterNestedCwd(t *testing.T) {
	goBin := goAdapterTool(t)
	for _, cwd := range []string{"package_dir", "file_dir"} {
		for _, stage := range []string{"edit", "commit", "handoff"} {
			t.Run(cwd+"/"+stage, func(t *testing.T) {
				root, bin := postEditFixture(t)
				t.Setenv("HOME", t.TempDir())
				postEditTool(t, bin, "go", fmt.Sprintf("exec %q \"$@\"\n", goBin))
				writeFile(t, filepath.Join(root, "nested", "go.mod"), "module example.com/nested\n\ngo 1.26\n")
				writeFile(t, filepath.Join(root, "nested", "edited.go"), "package nested\n")
				set, err := checkset.Decode([]byte("name: nested\nchecks:\n  - name: vet\n    stage: " + stage + "\n    files: ['**/*.go']\n    cwd: " + cwd + "\n    adapter: go\n    go: vet\n"))
				require.NoError(t, err)
				checks := stageInvocations(root, []string{"nested/edited.go"}, set.Checks[0])
				require.Len(t, checks, 1)
				require.Empty(t, runEditCheck(context.Background(), root, checks[0]))
			})
		}
	}
}

func TestGoAdapterNestedPostEdit(t *testing.T) {
	goBin := goAdapterTool(t)
	for _, cwd := range []string{"package_dir", "file_dir"} {
		t.Run(cwd, func(t *testing.T) {
			root, bin := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			postEditTool(t, bin, "go", fmt.Sprintf("exec %q \"$@\"\n", goBin))
			file := filepath.Join(root, "nested", "edited.go")
			writeFile(t, filepath.Join(root, "nested", "go.mod"), "module example.com/nested\n\ngo 1.26\n")
			writeFile(t, file, "package nested\nimport \"fmt\"\nfunc bad() { fmt.Printf(\"%d\", \"wrong\") }\n")
			writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: nested\n")
			checkPolicy(t, root, "nested", "  - name: nested-vet\n    stage: edit\n    files: ['**/*.go']\n    cwd: "+cwd+"\n    adapter: go\n    go: vet\n")
			checks, selected, err := configuredEditChecks(root, root, file, "")
			require.NoError(t, err)
			require.True(t, selected)
			require.Len(t, checks, 1)
			require.Equal(t, []string{"go", "vet", "."}, checks[0].argv)
			out := postEditRun(t, file)
			require.Contains(t, out, "nested-vet")
			require.Contains(t, out, "Printf format")
			require.NotContains(t, out, "nested/nested")
		})
	}
}

func TestGoAdapterExecution(t *testing.T) {
	root, bin := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	postEditTool(t, bin, "go", "printf '%s\\n' \"$@\" > \"$PWD/args\"\n")
	file := filepath.Join(root, "sub", "edited.go")
	writeFile(t, file, "package sub\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", "  - name: vet\n    stage: edit\n    files: ['**/*.go']\n    adapter: go\n    go: vet\n")
	checks, selected, err := configuredEditChecks(root, root, file, "")
	require.NoError(t, err)
	require.True(t, selected)
	require.Len(t, checks, 1)
	require.Contains(t, runEditCheck(context.Background(), root, checks[0]), "go.mod")
	require.NoFileExists(t, filepath.Join(root, "args"))
	writeFile(t, filepath.Join(root, "go.mod"), "module test\n")
	require.Empty(t, runEditCheck(context.Background(), root, checks[0]))
	args, err := os.ReadFile(filepath.Join(root, "args"))
	require.NoError(t, err)
	require.Equal(t, "vet\n./sub\n", string(args))
}

func TestAdaptersAcrossStages(t *testing.T) {
	for _, stage := range []string{"edit", "commit", "handoff"} {
		for _, adapter := range []string{"make", "go", "uv", "npm"} {
			t.Run(stage+"/"+adapter, func(t *testing.T) {
				root, bin := postEditFixture(t)
				t.Setenv("HOME", t.TempDir())
				postEditTool(t, bin, adapter, "printf '%s\\n' \"$PWD\" \"$@\" > \"$PWD/args\"\n")
				file := filepath.Join(root, "sub", "$(edited) file.go")
				writeFile(t, file, "package sub\n")
				field := map[string]string{"make": "target: check\n    vars: {B: two, A: one}", "go": "go: vet", "uv": "uv: [lint, '{files}']", "npm": "script: lint"}[adapter]
				set, err := checkset.Decode([]byte("name: adapters\nchecks:\n  - name: probe\n    stage: " + stage + "\n    files: ['**/*.go']\n    cwd: file_dir\n    adapter: " + adapter + "\n    " + field + "\n"))
				require.NoError(t, err)
				var checks []editCheck
				if stage == "edit" {
					checkPolicy(t, root, "project", "  - name: probe\n    stage: edit\n    files: ['**/*.go']\n    cwd: file_dir\n    adapter: "+adapter+"\n    "+field+"\n")
					writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
					checks, _, err = configuredEditChecks(root, root, file, "")
					require.NoError(t, err)
				} else {
					checks = stageInvocations(root, []string{"sub/$(edited) file.go"}, set.Checks[0])
				}
				require.Len(t, checks, 1)
				marker := map[string]string{"make": "Makefile", "go": "go.mod", "uv": "pyproject.toml", "npm": "package.json"}[adapter]
				writeFile(t, filepath.Join(root, marker), `{"scripts":{"lint":"node lint.js"}}`)
				require.Contains(t, runEditCheck(context.Background(), root, checks[0]), marker, "root marker must not satisfy a directory cwd probe")
				writeFile(t, filepath.Join(root, "sub", marker), `{"scripts":{"lint":"node lint.js"}}`)
				require.Empty(t, runEditCheck(context.Background(), root, checks[0]))
				data, err := os.ReadFile(filepath.Join(root, "sub", "args"))
				require.NoError(t, err)
				lines := strings.Split(strings.TrimSpace(string(data)), "\n")
				require.Equal(t, filepath.Dir(file), lines[0])
				if adapter == "make" {
					require.Equal(t, []string{"check", "A=one", "B=two"}, lines[1:])
				}
				if adapter == "uv" || adapter == "npm" {
					require.Equal(t, file, lines[len(lines)-1])
				}
			})
		}
	}
}

func TestAdapterProbeFeedback(t *testing.T) {
	for _, mode := range []string{"advisory", "block"} {
		t.Run(mode, func(t *testing.T) {
			root, _ := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			file := filepath.Join(root, "edited.go")
			writeFile(t, file, "package test\n")
			writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
			for _, stage := range []string{"edit", "commit", "handoff"} {
				checkPolicy(t, root, "project", "  - name: probe\n    stage: "+stage+"\n    files: ['**/*.go']\n    adapter: make\n    target: check\n    on_fail: "+mode+"\n")
				if stage == "edit" {
					out := postEditRun(t, file)
					require.Contains(t, out, "Makefile")
					if mode == "block" {
						require.Contains(t, out, `"decision":"block"`)
					} else {
						require.NotContains(t, out, `"decision":"block"`)
					}
				} else {
					feedback := runStageChecks(context.Background(), root, "", stage, func() ([]string, error) { return []string{"edited.go"}, nil })
					want := "Makefile"
					if stage == "handoff" {
						want = "no declared snapshot profile. Run the explicit project gate: (cd "
						require.Contains(t, feedback.Blocking+feedback.Advisory, " && make check)")
					}
					if mode == "block" {
						require.Contains(t, feedback.Blocking, want)
						require.Empty(t, feedback.Advisory)
					} else {
						require.Contains(t, feedback.Advisory, want)
						require.Empty(t, feedback.Blocking)
					}
				}
			}
		})
	}
}
