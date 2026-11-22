//go:build linux || darwin

package hook

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func configuredEdit(t *testing.T, file, agent string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"tool_input": map[string]string{"file_path": file}})
	require.NoError(t, err)
	cmd := exec.Command(ethosBinary, "hook", "post-edit", "--agent", agent)
	cmd.Stdin = strings.NewReader(string(payload))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return string(out)
}

func checkPolicy(t *testing.T, root, slug, checks string) {
	t.Helper()
	writeFile(t, filepath.Join(root, ".punt-labs/ethos/checks", slug+".yaml"), "name: "+slug+"\nchecks:\n"+checks)
}

func TestConfiguredEditFeedback(t *testing.T) {
	for _, tc := range []struct{ name, checks, want, absent string }{
		{"TDD advisory", "  - name: unit-tests\n    stage: edit\n    files: ['**/*.go']\n    run: [go, test, '{package_dir}']\n", "additionalContext", `"decision"`},
		{"blocking", "  - name: format\n    stage: edit\n    files: ['**/*.go']\n    run: [go, test, '{package_dir}']\n    on_fail: block\n", `"decision":"block"`, "additionalContext"},
		{"broken set", "  - name: broken\n    stage: edit\n    files: all\n    run: [go, test]\n", "post-edit policy:", `"decision"`},
		{"not edit", "  - name: later\n    stage: commit\n    files: all\n    run: [go, test]\n", "", "FAIL_TDD"},
		{"excluded", "  - name: skipped\n    stage: edit\n    files: ['**/*.go']\n    exclude: ['sub/**']\n    run: [go, test]\n", "", "FAIL_TDD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, bin := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
			checkPolicy(t, root, "project", tc.checks)
			writeFile(t, filepath.Join(root, "sub/file.go"), "package sub\n")
			postEditTool(t, bin, "go", "echo FAIL_TDD >&2; exit 1\n")
			out := configuredEdit(t, "sub/file.go", "")
			if tc.want == "" {
				require.Empty(t, out)
			} else {
				require.Contains(t, out, tc.want)
				var response map[string]any
				require.NoError(t, json.Unmarshal([]byte(out), &response))
			}
			require.NotContains(t, out, tc.absent)
		})
	}
}

func TestConfiguredEditPrecedence(t *testing.T) {
	for _, tc := range []struct{ identity, role, repo, want string }{
		{"identity-set", "role-set", "repo-set", "identity-set"},
		{"", "role-set", "repo-set", "role-set"},
		{"", "", "repo-set", "repo-set"},
		{"none", "role-set", "repo-set", ""},
		{"", "none", "repo-set", ""},
		{"", "", "none", ""},
	} {
		t.Run(tc.identity+"/"+tc.role+"/"+tc.repo, func(t *testing.T) {
			root, bin := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "team: engineering\nchecks: "+tc.repo+"\n")
			writeFile(t, filepath.Join(root, ".punt-labs/ethos/identities/bwk.yaml"), "name: Brian\nhandle: bwk\nkind: agent\nchecks: '"+tc.identity+"'\n")
			writeFile(t, filepath.Join(root, ".punt-labs/ethos/roles/worker.yaml"), "name: worker\nchecks: '"+tc.role+"'\n")
			writeFile(t, filepath.Join(root, ".punt-labs/ethos/teams/engineering.yaml"), "name: engineering\nmembers:\n  - identity: bwk\n    role: worker\n")
			for _, slug := range []string{"identity-set", "role-set", "repo-set"} {
				checkPolicy(t, root, slug, "  - name: "+slug+"\n    stage: edit\n    files: ['**/*.txt']\n    run: [probe]\n")
			}
			writeFile(t, filepath.Join(root, "file.txt"), "")
			postEditTool(t, bin, "probe", "echo failed; exit 1\n")
			out := configuredEdit(t, "file.txt", "bwk")
			if tc.want == "" {
				require.Empty(t, out)
			} else {
				require.Contains(t, out, tc.want)
			}
		})
	}
}

func TestConfiguredEditLiteralArguments(t *testing.T) {
	root, bin := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: literal\n")
	checkPolicy(t, root, "literal", "  - name: literal\n    stage: edit\n    files: ['**/*.txt']\n    cwd: file_dir\n    run: [probe, '{file}', '{files}', '{root}', '{package_dir}']\n")
	file := filepath.Join(root, "sub/-$(x) file.txt")
	writeFile(t, file, "")
	log := filepath.Join(root, "argv.json")
	t.Setenv("POST_EDIT_LOG", log)
	postEditTool(t, bin, "probe", "printf '%s\\n' \"$PWD\" \"$#\" \"$@\" > \"$POST_EDIT_LOG\"\n")
	require.Empty(t, configuredEdit(t, file, ""))
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, filepath.Dir(file)+"\n4\n"+file+"\n"+file+"\n"+root+"\n./sub\n", string(data))
}
