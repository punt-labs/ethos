//go:build !windows

// Package test exercises release scripts in isolated real Git repositories.
package test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const pluginManifest = "plugin/.claude-plugin/plugin.json"

type pluginReleaseRepo struct {
	root string
	env  []string
}

func newPluginReleaseRepo(t *testing.T, commands bool) *pluginReleaseRepo {
	t.Helper()
	r := &pluginReleaseRepo{root: filepath.Join(t.TempDir(), "repo with 'quotes'")}
	require.NoError(t, os.MkdirAll(r.root, 0o700))
	// Never inherit a caller's index, worktree, global hooks, signing, or identity.
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_") && !strings.HasPrefix(value, "HOME=") {
			r.env = append(r.env, value)
		}
	}
	r.env = append(r.env, "HOME="+r.root, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(r.root, "empty-gitconfig"), "GIT_TERMINAL_PROMPT=0")
	r.write(t, "empty-gitconfig", "")
	r.git(t, "init", "--template=", "-b", "main")
	r.git(t, "config", "user.name", "Release Fixture")
	r.git(t, "config", "user.email", "fixture@example.invalid")
	r.git(t, "config", "commit.gpgsign", "false")
	r.git(t, "config", "core.autocrlf", "false")
	r.git(t, "config", "core.hooksPath", filepath.Join(r.root, ".git", "hooks"))
	for _, name := range []string{"release-plugin.sh", "restore-dev-plugin.sh"} {
		data, err := os.ReadFile(filepath.Join("..", "scripts", name))
		require.NoError(t, err)
		r.write(t, filepath.Join("scripts", name), string(data))
	}
	r.manifest(t, "ethos-dev", "1.0.0")
	r.write(t, "unrelated.txt", "original\n")
	if commands {
		r.write(t, "plugin/commands/identity.md", "production command\n")
		r.write(t, "plugin/commands/identity-dev.md", "original dev command\n")
	}
	r.git(t, "add", ".")
	r.git(t, "commit", "-m", "initial development snapshot")
	return r
}

func (r *pluginReleaseRepo) run(t *testing.T, program string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = r.root
	cmd.Env = r.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r *pluginReleaseRepo) git(t *testing.T, args ...string) string {
	t.Helper()
	out, err := r.run(t, "git", args...)
	require.NoError(t, err, "git %q: %s", args, out)
	return strings.TrimSpace(out)
}

func (r *pluginReleaseRepo) script(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	return r.run(t, "bash", append([]string{filepath.Join("scripts", name+"-plugin.sh")}, args...)...)
}

func (r *pluginReleaseRepo) write(t *testing.T, path, content string) {
	t.Helper()
	path = filepath.Join(r.root, path)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func (r *pluginReleaseRepo) read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.root, path))
	require.NoError(t, err)
	return string(data)
}

func (r *pluginReleaseRepo) manifest(t *testing.T, name, version string) {
	t.Helper()
	data, err := json.Marshal(map[string]string{"name": name, "version": version})
	require.NoError(t, err)
	r.write(t, pluginManifest, string(data)+"\n")
}

func (r *pluginReleaseRepo) assertManifest(t *testing.T, name, version string) {
	t.Helper()
	var data map[string]string
	require.NoError(t, json.Unmarshal([]byte(r.read(t, pluginManifest)), &data))
	assert.Equal(t, name, data["name"])
	assert.Equal(t, version, data["version"])
}

func (r *pluginReleaseRepo) hook(t *testing.T, body string) {
	t.Helper()
	r.write(t, ".git/hooks/pre-commit", "#!/bin/sh\nset -eu\nprintf 'hook\\n' >> .git/hook-runs\n"+body+"\n")
	require.NoError(t, os.Chmod(filepath.Join(r.root, ".git/hooks/pre-commit"), 0o700))
}

func TestReleasePluginCommitHooks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		commands bool
		reject   bool
	}{
		{name: "rejecting hook", commands: true, reject: true},
		{name: "successful release", commands: true},
		{name: "no commands directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPluginReleaseRepo(t, tc.commands)
			body := "exit 0"
			if tc.reject {
				body = "echo 'fixture rejected commit' >&2\nexit 1"
			}
			r.hook(t, body)
			head := r.git(t, "rev-parse", "HEAD")
			out, err := r.script(t, "release")
			if tc.reject {
				require.Error(t, err, out)
				assert.Contains(t, out, "fixture rejected commit")
				assert.Equal(t, head, r.git(t, "rev-parse", "HEAD"))
				assert.NotEmpty(t, r.git(t, "diff", "--cached", "--name-only"))
			} else {
				require.NoError(t, err, out)
				assert.NotEqual(t, head, r.git(t, "rev-parse", "HEAD"))
				assert.Empty(t, r.git(t, "status", "--porcelain"))
				assert.Contains(t, r.git(t, "show", "HEAD:"+pluginManifest), `"name": "ethos"`)
			}
			r.assertManifest(t, "ethos", "1.0.0")
			assert.Equal(t, "hook\n", r.read(t, ".git/hook-runs"))
			if tc.commands {
				assert.NoFileExists(t, filepath.Join(r.root, "plugin/commands/identity-dev.md"))
				assert.Equal(t, "production command\n", r.read(t, "plugin/commands/identity.md"))
			}
		})
	}
}

func TestRestoreDevPluginAfterSquash(t *testing.T) {
	r := newPluginReleaseRepo(t, true)
	// A command-only commit must be selected even though plugin.json is unchanged.
	devCommand := "plugin/commands/nested/author's command\nnew-dev.md"
	r.git(t, "rm", "plugin/commands/identity-dev.md")
	r.write(t, devCommand, "latest development command\n")
	r.git(t, "add", "plugin/commands")
	r.git(t, "commit", "-m", "refresh development commands only")
	devHead := r.git(t, "rev-parse", "HEAD")
	r.manifest(t, "ethos", "2.0.0")
	r.git(t, "rm", devCommand)
	r.git(t, "add", pluginManifest)
	r.git(t, "commit", "-m", "chore: release v2.0.0 (#42)")
	r.write(t, "plugin/commands/identity.md", "updated production command\n")
	r.write(t, "unrelated.txt", "intervening README-style update\n")
	r.git(t, "add", "plugin/commands/identity.md", "unrelated.txt")
	r.git(t, "commit", "-m", "pin installer after release")
	head := r.git(t, "rev-parse", "HEAD")
	count := r.git(t, "rev-list", "--count", "HEAD")
	// The hook must see the engine's final version, never the old snapshot version.
	r.hook(t, `python3 -c 'import json; d=json.load(open("plugin/.claude-plugin/plugin.json")); assert d == {"name": "ethos-dev", "version": "2.0.0"}'`)
	out, err := r.script(t, "restore-dev")
	require.NoError(t, err, out)
	assert.Contains(t, out, devHead[:12])
	assert.Equal(t, head, r.git(t, "rev-parse", "HEAD"))
	assert.NoFileExists(t, filepath.Join(r.root, ".git/hook-runs"))
	r.assertManifest(t, "ethos-dev", "2.0.0")
	assert.Equal(t, "latest development command\n", r.read(t, devCommand))
	assert.NoFileExists(t, filepath.Join(r.root, "plugin/commands/identity-dev.md"))
	assert.Equal(t, "updated production command\n", r.read(t, "plugin/commands/identity.md"))
	assert.Equal(t, "intervening README-style update\n", r.read(t, "unrelated.txt"))
	assert.Empty(t, r.git(t, "diff", "--name-only"))
	assert.Equal(t, pluginManifest+"\x00"+devCommand+"\x00",
		r.git(t, "diff", "--cached", "--name-only", "-z"))

	// Mirror phase09: restamp, stage, then make one ordinary hook-checked commit.
	r.manifest(t, "ethos-dev", "2.0.0")
	r.git(t, "add", pluginManifest)
	r.git(t, "commit", "-m", "chore: restore dev plugin state")
	assert.Equal(t, "hook\n", r.read(t, ".git/hook-runs"))
	assert.Equal(t, count, r.git(t, "rev-list", "--count", "HEAD^"))
	assert.Equal(t, head, r.git(t, "rev-parse", "HEAD^"))
	assert.Empty(t, r.git(t, "status", "--porcelain"))
}

// The manifest restore is name-only: version and description bytes (including
// the escaping release-plugin.sh's json.dumps wrote) come from HEAD, not from
// the older dev snapshot.
func TestRestoreDevPluginManifestNameOnly(t *testing.T) {
	r := newPluginReleaseRepo(t, false)
	r.write(t, pluginManifest, "{\n  \"name\": \"ethos-dev\",\n  \"version\": \"4.21.0\",\n"+
		"  \"description\": \"Identity — binding\"\n}\n")
	r.git(t, "add", pluginManifest)
	r.git(t, "commit", "-m", "dev snapshot 4.21.0")
	released := "{\n  \"name\": \"ethos\",\n  \"version\": \"4.22.0\",\n" +
		"  \"description\": \"Identity \\u2014 binding\"\n}\n"
	r.write(t, pluginManifest, released)
	r.git(t, "add", pluginManifest)
	r.git(t, "commit", "-m", "chore: release v4.22.0 (#1)")
	out, err := r.script(t, "restore-dev")
	require.NoError(t, err, out)
	want := strings.Replace(released, `"name": "ethos"`, `"name": "ethos-dev"`, 1)
	assert.Equal(t, want, r.read(t, pluginManifest))
	assert.Equal(t, want, r.git(t, "show", ":"+pluginManifest)+"\n")
	assert.Contains(t, r.read(t, pluginManifest), "\\u2014")
}

func TestRestoreDevPluginFirstParent(t *testing.T) {
	r := newPluginReleaseRepo(t, true)
	r.git(t, "checkout", "-b", "side")
	r.write(t, "plugin/commands/side-dev.md", "not a first-parent dev snapshot\n")
	r.git(t, "add", "plugin/commands/side-dev.md")
	r.git(t, "commit", "-m", "side branch development")
	r.git(t, "checkout", "main")
	r.manifest(t, "ethos", "2.0.0")
	r.git(t, "rm", "plugin/commands/identity-dev.md")
	r.git(t, "add", pluginManifest)
	r.git(t, "commit", "-m", "squashed release")
	r.git(t, "merge", "--no-ff", "-m", "merge tooling after release", "side")
	head := r.git(t, "rev-parse", "HEAD")
	out, err := r.script(t, "restore-dev")
	require.NoError(t, err, out)
	assert.Equal(t, head, r.git(t, "rev-parse", "HEAD"))
	assert.Equal(t, "original dev command\n", r.read(t, "plugin/commands/identity-dev.md"))
	assert.NoFileExists(t, filepath.Join(r.root, "plugin/commands/side-dev.md"))
	assert.Equal(t, "D\tplugin/commands/side-dev.md",
		r.git(t, "diff", "--cached", "--name-status", "--", "plugin/commands/side-dev.md"))
}

func TestRestoreDevPluginSources(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "automatic without commands directory"
		if explicit {
			name = "explicit prep commit compatibility"
		}
		t.Run(name, func(t *testing.T) {
			r := newPluginReleaseRepo(t, false)
			out, err := r.script(t, "release")
			require.NoError(t, err, out)
			head := r.git(t, "rev-parse", "HEAD")
			var args []string
			if explicit {
				args = []string{head}
			}
			out, err = r.script(t, "restore-dev", args...)
			require.NoError(t, err, out)
			assert.Equal(t, head, r.git(t, "rev-parse", "HEAD"))
			r.assertManifest(t, "ethos-dev", "1.0.0")
			assert.Equal(t, pluginManifest, r.git(t, "diff", "--cached", "--name-only"))
		})
	}
}

func TestPluginScriptsProtectDirtyFiles(t *testing.T) {
	for _, script := range []string{"release", "restore-dev"} {
		for _, staged := range []bool{false, true} {
			name := script + "/unstaged"
			if staged {
				name = script + "/staged"
			}
			t.Run(name, func(t *testing.T) {
				r := newPluginReleaseRepo(t, true)
				r.write(t, "unrelated.txt", "operator change\n")
				if staged {
					r.git(t, "add", "unrelated.txt")
				}
				head := r.git(t, "rev-parse", "HEAD")
				status := r.git(t, "status", "--porcelain")
				out, err := r.script(t, script)
				require.Error(t, err, out)
				assert.Contains(t, out, "repository has uncommitted changes")
				assert.Equal(t, head, r.git(t, "rev-parse", "HEAD"))
				assert.Equal(t, status, r.git(t, "status", "--porcelain"))
				r.assertManifest(t, "ethos-dev", "1.0.0")
				assert.Equal(t, "operator change\n", r.read(t, "unrelated.txt"))
			})
		}
	}
}

func TestPluginScriptsFailLoudly(t *testing.T) {
	for _, tc := range []struct {
		name     string
		script   string
		manifest string
		args     []string
		locked   bool
		orphan   bool
		want     string
	}{
		{name: "release malformed JSON", script: "release", manifest: "{", want: "JSONDecodeError"},
		{name: "restore malformed JSON", script: "restore-dev", manifest: "{", want: "JSONDecodeError"},
		{name: "release invalid name", script: "release", manifest: `{"name":42}`, want: "plugin name"},
		{name: "restore invalid name", script: "restore-dev", manifest: `{"name":42}`, want: "plugin name"},
		{name: "already production", script: "release", manifest: `{"name":"ethos"}`, want: "no -dev suffix"},
		{name: "no dev snapshot", script: "restore-dev", manifest: `{"name":"ethos"}`, orphan: true, want: "dev plugin snapshot"},
		{name: "invalid prep ref", script: "restore-dev", args: []string{"missing-prep"}, want: "fatal:"},
		{name: "extra arguments", script: "restore-dev", args: []string{"HEAD", "HEAD"}, want: "Usage:"},
		{name: "locked index", script: "restore-dev", locked: true, want: "index.lock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPluginReleaseRepo(t, true)
			if tc.orphan {
				r.git(t, "checkout", "--orphan", "production-only")
			}
			if tc.manifest != "" {
				r.write(t, pluginManifest, tc.manifest)
				r.git(t, "add", pluginManifest)
				r.git(t, "commit", "-m", "fixture manifest")
			}
			if tc.locked {
				r.write(t, ".git/index.lock", "fixture lock\n")
			}
			head := r.git(t, "rev-parse", "HEAD")
			before := r.read(t, pluginManifest)
			out, err := r.script(t, tc.script, tc.args...)
			require.Error(t, err, out)
			assert.Contains(t, out, tc.want)
			assert.Equal(t, head, r.git(t, "rev-parse", "HEAD"))
			assert.Empty(t, r.git(t, "diff", "--cached", "--name-only"))
			assert.Equal(t, before, r.read(t, pluginManifest))
		})
	}
}
