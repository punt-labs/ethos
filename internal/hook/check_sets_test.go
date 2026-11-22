//go:build linux || darwin

package hook

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/role"

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

func TestConfiguredEditKillSwitch(t *testing.T) {
	root, bin := postEditFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(home, ".punt-hooks-kill"), "")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	writeFile(t, filepath.Join(root, "file.txt"), "")
	checkPolicy(t, root, "project", "  - name: configured\n    stage: edit\n    files: ['**']\n    run: [probe]\n    on_fail: block\n")
	marker := filepath.Join(root, "must-not-run")
	t.Setenv("POST_EDIT_MARKER", marker)
	postEditTool(t, bin, "probe", "printf ran > \"$POST_EDIT_MARKER\"; exit 1\n")
	require.Empty(t, configuredEdit(t, "file.txt", ""))
	require.NoFileExists(t, marker)
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: [")
	require.Empty(t, configuredEdit(t, "file.txt", ""))
}

func TestConfiguredEditMainStoreFromLinkedSession(t *testing.T) {
	for _, tc := range []struct{ name, agent, config, identity, role string }{
		{"repository", "", "checks: project\n", "", ""},
		{"identity", "worker", "", "checks: project\n", ""},
		{"role", "worker", "team: crew\n", "", "checks: project\n"},
		{"bundle", "", "active_bundle: ops\nchecks: project\n", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, bin := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			worktree := filepath.Join(t.TempDir(), "linked")
			cmd := exec.Command("git", "worktree", "add", "--detach", worktree)
			cmd.Dir = root
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), tc.config)
			writeFile(t, filepath.Join(root, ".punt-labs/ethos/identities/worker.yaml"), "name: Worker\nhandle: worker\nkind: agent\n"+tc.identity)
			writeFile(t, filepath.Join(root, ".punt-labs/ethos/teams/crew.yaml"), "name: crew\nmembers: [{identity: worker, role: writer}]\n")
			writeFile(t, filepath.Join(root, ".punt-labs/ethos/roles/writer.yaml"), "name: writer\ntools: [Edit]\n"+tc.role)
			policyRoot := root
			if tc.name == "bundle" {
				policyRoot = filepath.Join(root, ".punt-labs/ethos-bundles/ops")
				writeFile(t, filepath.Join(policyRoot, "bundle.yaml"), "name: ops\n")
			}
			if tc.name == "bundle" {
				writeFile(t, filepath.Join(policyRoot, "checks/project.yaml"), "name: project\nchecks:\n  - name: main-only-check\n    stage: edit\n    files: ['**']\n    run: [probe]\n")
			} else {
				checkPolicy(t, root, "project", "  - name: main-only-check\n    stage: edit\n    files: ['**']\n    run: [probe]\n")
			}
			writeFile(t, filepath.Join(worktree, "file.txt"), "")
			postEditTool(t, bin, "probe", "echo failed; exit 1\n")
			t.Setenv("CLAUDE_PROJECT_DIR", worktree)
			require.Contains(t, configuredEdit(t, filepath.Join(worktree, "file.txt"), tc.agent), "main-only-check")
			writeFile(t, filepath.Join(worktree, ".punt-labs/ethos.yaml"), "checks: none\n")
			require.Contains(t, configuredEdit(t, filepath.Join(worktree, "file.txt"), tc.agent), "main-only-check")
		})
	}
}

func TestConfiguredEditEnginePrecedence(t *testing.T) {
	for _, tc := range []struct{ name, idRef, roleRef, repoRef, want string }{
		{"identity", "id-policy", "role-policy", "repo-policy", "id-policy"},
		{"role", "", "role-policy", "repo-policy", "role-policy"},
		{"repository", "", "", "repo-policy", "repo-policy"},
		{"identity none", "none", "role-policy", "repo-policy", "none"},
		{"role none", "", "none", "repo-policy", "none"},
		{"repository none", "", "", "none", "none"},
		{"defaults", "", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "team: crew\nchecks: '"+tc.repoRef+"'\n")
			writeFile(t, filepath.Join(root, ".punt-labs/ethos/identities/worker.yaml"), "name: Worker\nhandle: worker\nkind: agent\nchecks: '"+tc.idRef+"'\n")
			writeFile(t, filepath.Join(root, ".punt-labs/ethos/teams/crew.yaml"), "name: crew\nmembers:\n  - identity: worker\n    role: writer\n")
			writeFile(t, filepath.Join(root, ".punt-labs/ethos/roles/writer.yaml"), "name: writer\ntools: [Edit]\nchecks: '"+tc.roleRef+"'\n")
			for _, slug := range []string{"id-policy", "role-policy", "repo-policy"} {
				checkPolicy(t, root, slug, "  - name: "+slug+"\n    stage: edit\n    files: ['**/*.go']\n    run: [probe, '{file}', '{files}', '{root}', '{package_dir}']\n    cwd: package_dir\n    on_fail: block\n    fail_on_output: true\n  - name: later\n    stage: commit\n    files: all\n    run: [probe]\n  - name: unmatched\n    stage: edit\n    files: ['*.sh']\n    run: [probe]\n")
			}
			file := filepath.Join(root, "pkg/- $(x).go")
			checks, selected, err := configuredEditChecks(root, root, file, "worker")
			require.NoError(t, err)
			require.Equal(t, tc.want != "", selected)
			if tc.want == "none" || tc.want == "" {
				require.Empty(t, checks)
				return
			}
			require.Len(t, checks, 1)
			require.Equal(t, tc.want, checks[0].name)
			require.Equal(t, []string{"probe", file, file, root, "./pkg"}, checks[0].argv)
			require.Equal(t, filepath.Dir(file), checks[0].cwd)
			require.Equal(t, "block", checks[0].onFail)
			require.True(t, checks[0].failOnOutput)
			require.Equal(t, 10*time.Second, checks[0].timeout)
		})
	}
}

func TestConfiguredEditEngineErrors(t *testing.T) {
	for _, tc := range []struct{ name, cfg, id, team string }{
		{"config", "checks: [", "", ""},
		{"bundle", "active_bundle: absent\n", "", ""},
		{"identity", "checks: project\n", "[", ""},
		{"team", "team: crew\n", "name: Worker\nhandle: worker\nkind: agent\n", ""},
		{"role", "team: crew\n", "name: Worker\nhandle: worker\nkind: agent\n", "name: crew\nmembers: [{identity: worker, role: missing}]\n"},
		{"set", "checks: missing\n", "", ""},
		{"repo-only store", "resolution: repo-only\n", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), tc.cfg)
			if tc.id != "" {
				writeFile(t, filepath.Join(root, ".punt-labs/ethos/identities/worker.yaml"), tc.id)
			}
			if tc.team != "" {
				writeFile(t, filepath.Join(root, ".punt-labs/ethos/teams/crew.yaml"), tc.team)
			}
			_, _, err := configuredEditChecks(root, root, filepath.Join(root, "file.go"), "worker")
			require.Error(t, err)
		})
	}
}

func TestConfiguredEditEngineBundleIdentity(t *testing.T) {
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "resolution: repo-only\nactive_bundle: ops\nchecks: missing\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos-bundles/ops/identities/worker.yaml"), "name: Worker\nhandle: worker\nkind: agent\nchecks: none\n")
	checks, selected, err := configuredEditChecks(root, root, filepath.Join(root, "file.go"), "worker")
	require.NoError(t, err)
	require.True(t, selected)
	require.Empty(t, checks)
}

func TestConfiguredEditFeedback(t *testing.T) {
	for _, tc := range []struct{ name, checks, want, absent string }{
		{"TDD advisory", "  - name: unit-tests\n    stage: edit\n    files: ['**/*.go']\n    run: [go, test, '{package_dir}']\n", "additionalContext", `"decision"`},
		{"blocking", "  - name: format\n    stage: edit\n    files: ['**/*.go']\n    run: [go, test, '{package_dir}']\n    on_fail: block\n", `"decision":"block"`, "additionalContext"},
		{"broken set", "  - name: broken\n    stage: edit\n    files: all\n    run: [go, test]\n", "post-edit policy:", `"decision"`},
		{"not edit", "  - name: later\n    stage: commit\n    files: all\n    run: [go, test]\n", "", "FAIL_TDD"},
		{"excluded", "  - name: skipped\n    stage: edit\n    files: ['**/*.go']\n    exclude: ['sub/**']\n    run: [go, test]\n", "", "FAIL_TDD"},
		{"no glob match", "  - name: shell\n    stage: edit\n    files: ['**/*.sh']\n    run: [go, test]\n", "", "FAIL_TDD"},
		{"mixed", "  - name: advisory\n    stage: edit\n    files: ['**/*.go']\n    run: [go, test]\n  - name: blocking\n    stage: edit\n    files: ['**/*.go']\n    run: [go, test]\n    on_fail: block\n", `"decision":"block"`, "unused-marker"},
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
				if tc.name == "mixed" {
					require.Contains(t, out, "additionalContext")
					require.Contains(t, out, "blocking:")
					require.Contains(t, out, "advisory:")
				}
			}
			require.NotContains(t, out, tc.absent)
		})
	}
}

func TestConfiguredEditTimeoutAndPass(t *testing.T) {
	for _, tc := range []struct{ name, script, want string }{
		{"pass", "exit 0\n", ""},
		{"output", "echo needs-formatting\n", "non-empty formatting output"},
		{"whitespace output", "printf ' \\n'\n", "non-empty formatting output"},
		{"timeout", "exec /bin/sleep 30\n", "timed out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, bin := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
			checkPolicy(t, root, "project", "  - name: probe\n    stage: edit\n    files: ['**']\n    run: [probe]\n    fail_on_output: true\n    timeout: 100ms\n")
			writeFile(t, filepath.Join(root, "file.txt"), "")
			postEditTool(t, bin, "probe", tc.script)
			start := time.Now()
			out := configuredEdit(t, "file.txt", "")
			if tc.want == "" {
				require.Empty(t, out)
			} else {
				require.Contains(t, out, tc.want)
			}
			require.Less(t, time.Since(start), 2*time.Second)
		})
	}
}

func TestCheckPolicyDoesNotChangeGeneratedBytes(t *testing.T) {
	id := &identity.Identity{Name: "Agent", Handle: "agent", Kind: "agent"}
	r := &role.Role{Name: "developer", Tools: []string{"Edit"}}
	before := buildAgentFile(id, r, nil, nil, nil)
	id.Checks, r.Checks = "identity-set", "role-set"
	require.Equal(t, before, buildAgentFile(id, r, nil, nil, nil))
}

func TestConfiguredEditRealTDD(t *testing.T) {
	goBin, err := exec.LookPath("go")
	require.NoError(t, err)
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/tdd\n\ngo 1.25\n")
	writeFile(t, filepath.Join(root, "file.go"), "package tdd\n")
	writeFile(t, filepath.Join(root, "file_test.go"), "package tdd\nimport \"testing\"\nfunc TestRed(t *testing.T) { t.Fatal(\"TDD_RED\") }\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: tdd\n")
	checkPolicy(t, root, "tdd", "  - name: red-tests\n    stage: edit\n    files: ['**/*.go']\n    run: ['"+goBin+"', test, '{package_dir}']\n    timeout: 60s\n")
	out := configuredEdit(t, "file.go", "")
	require.Contains(t, out, "additionalContext")
	require.Contains(t, out, "TDD_RED")
	require.NotContains(t, out, `"decision"`)
}

func TestConfiguredEditPolicyScope(t *testing.T) {
	root, bin := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: session\n")
	checkPolicy(t, root, "session", "  - name: session-check\n    stage: edit\n    files: ['**/*.txt']\n    run: [probe]\n")
	marker := filepath.Join(root, "probe-cwd")
	t.Setenv("POST_EDIT_LOG", marker)
	postEditTool(t, bin, "probe", "printf '%s' \"$PWD\" > \"$POST_EDIT_LOG\"; echo failed; exit 1\n")
	worktree := filepath.Join(root, ".claude/worktrees/worker")
	cmd := exec.Command("git", "worktree", "add", "--detach", worktree)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	writeFile(t, filepath.Join(worktree, ".punt-labs/ethos.yaml"), "checks: none\n")
	writeFile(t, filepath.Join(worktree, "file.txt"), "")
	require.Contains(t, configuredEdit(t, filepath.Join(worktree, "file.txt"), ""), "session-check")
	data, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, worktree, string(data))
	require.NoError(t, os.Remove(marker))
	foreign := t.TempDir()
	initGitRepo(t, foreign)
	writeFile(t, filepath.Join(foreign, "file.txt"), "")
	writeFile(t, filepath.Join(foreign, ".punt-labs/ethos.yaml"), "checks: session\n")
	checkPolicy(t, foreign, "session", "  - name: hostile\n    stage: edit\n    files: ['**']\n    run: [probe]\n")
	require.Empty(t, configuredEdit(t, filepath.Join(foreign, "file.txt"), ""))
	_, err = os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestConfiguredEditMissingReferenceAndUnknownAgent(t *testing.T) {
	root, bin := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, "file.txt"), "")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: missing\n")
	out := configuredEdit(t, "file.txt", "unknown")
	require.Contains(t, out, "additionalContext")
	require.Contains(t, out, "not found")
	require.NotContains(t, out, `"decision"`)
	checkPolicy(t, root, "missing", "  - name: project-check\n    stage: edit\n    files: ['**']\n    run: [probe]\n")
	postEditTool(t, bin, "probe", "echo failed; exit 1\n")
	require.Contains(t, configuredEdit(t, "file.txt", "unknown"), "project-check")
}

func TestConfiguredEditLayeredResolution(t *testing.T) {
	root, bin := postEditFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(root, "file.txt"), "")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "active_bundle: ops\nchecks: project\n")
	bundleRoot := filepath.Join(root, ".punt-labs/ethos-bundles/ops")
	globalRoot := filepath.Join(home, ".punt-labs/ethos")
	for _, layer := range []struct{ root, name string }{
		{globalRoot, "global-check"}, {bundleRoot, "bundle-check"}, {filepath.Join(root, ".punt-labs/ethos"), "repo-check"},
	} {
		writeFile(t, filepath.Join(layer.root, "checks/project.yaml"), "name: project\nchecks:\n  - name: "+layer.name+"\n    stage: edit\n    files: ['**']\n    run: [probe]\n")
	}
	postEditTool(t, bin, "probe", "exit 1\n")
	for _, layer := range []struct{ root, name string }{
		{filepath.Join(root, ".punt-labs/ethos"), "repo-check"}, {bundleRoot, "bundle-check"}, {globalRoot, "global-check"},
	} {
		require.Contains(t, configuredEdit(t, "file.txt", ""), layer.name)
		require.NoError(t, os.Remove(filepath.Join(layer.root, "checks/project.yaml")))
	}
}

func TestConfiguredEditRepoOnlyBundle(t *testing.T) {
	root, bin := postEditFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(root, "file.txt"), "")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "resolution: repo-only\nactive_bundle: ops\nchecks: project\n")
	globalBundle := filepath.Join(home, ".punt-labs/ethos/bundles/ops")
	policy := "name: project\nchecks:\n  - name: bundle-check\n    stage: edit\n    files: ['**']\n    run: [probe]\n    on_fail: block\n"
	writeFile(t, filepath.Join(globalBundle, "checks/project.yaml"), policy)
	marker := filepath.Join(root, "probe-marker")
	t.Setenv("POST_EDIT_LOG", marker)
	postEditTool(t, bin, "probe", "printf executed > \"$POST_EDIT_LOG\"; exit 1\n")
	out := configuredEdit(t, "file.txt", "")
	require.NoFileExists(t, marker)
	require.Contains(t, out, "additionalContext")
	require.Contains(t, out, "repo-only cannot use the global bundle")
	require.NotContains(t, out, `"decision"`)
	writeFile(t, filepath.Join(root, ".punt-labs/ethos-bundles/ops/checks/project.yaml"), policy)
	out = configuredEdit(t, "file.txt", "")
	require.FileExists(t, marker)
	require.Contains(t, out, `"decision":"block"`)
	require.Contains(t, out, "bundle-check")
}

func TestConfiguredEditRepoOnlyMissingStore(t *testing.T) {
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, "file.txt"), "")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "resolution: repo-only\nchecks: none\n")
	out := configuredEdit(t, "file.txt", "")
	require.Contains(t, out, "additionalContext")
	require.Contains(t, out, "no identity store")
	require.NotContains(t, out, `"decision"`)
}

func TestConfiguredEditUsesGeneratorRoleBinding(t *testing.T) {
	root, bin := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, "file.txt"), "")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "team: engineering\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos/identities/bwk.yaml"), "name: Agent\nhandle: bwk\nkind: agent\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos/teams/engineering.yaml"), "name: engineering\nmembers:\n  - identity: bwk\n    role: writer\n  - identity: bwk\n    role: metadata\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos/roles/writer.yaml"), "name: writer\ntools: [Edit]\nchecks: writer\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos/roles/metadata.yaml"), "name: metadata\nchecks: none\n")
	checkPolicy(t, root, "writer", "  - name: writer-check\n    stage: edit\n    files: ['**']\n    run: [probe]\n")
	postEditTool(t, bin, "probe", "exit 1\n")
	require.Contains(t, configuredEdit(t, "file.txt", "bwk"), "writer-check")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "agent: bwk\nteam: engineering\n")
	require.Empty(t, configuredEdit(t, "file.txt", "bwk"))
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
			writeFile(t, filepath.Join(root, ".punt-labs/ethos/roles/worker.yaml"), "name: worker\ntools: [Edit]\nchecks: '"+tc.role+"'\n")
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
