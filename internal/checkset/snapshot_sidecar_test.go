package checkset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSnapshotSidecarFollowsWinningCheckSet(t *testing.T) {
	repo, global := t.TempDir(), t.TempDir()
	base := []byte("name: project\nchecks: [{name: full-gate, stage: handoff, files: all, adapter: make, target: check}]\n")
	sidecar := []byte(`checks:
  full-gate:
    profile: linux-worktree-v1
    epoch: test
    sources: {mode: worktree}
    runtime:
      platforms:
        linux-amd64:
          bundle: test-linux
          sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
        darwin-arm64:
          bundle: test-darwin
          sha256: "123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0"
          url: https://example.com/darwin.tar.gz
`)
	for _, root := range []string{repo, global} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "checks", "snapshots"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(root, "checks", "project.yaml"), base, 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(global, "checks", "snapshots", "project.yaml"), sidecar, 0o600))
	store := NewLayeredStoreWithBundle(repo, "", global, false)
	set, err := store.LoadForHandoff("project")
	require.NoError(t, err)
	require.Nil(t, set.Checks[0].WorktreeSnapshot)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "checks", "snapshots", "project.yaml"), sidecar, 0o600))
	set, err = store.LoadForHandoff("project")
	require.NoError(t, err)
	require.False(t, set.Checks[0].InvalidSnapshotSidecar)
	require.NotNil(t, set.Checks[0].WorktreeSnapshot)
	require.Equal(t, "test-darwin", set.Checks[0].WorktreeSnapshot.Runtime.Platforms["darwin-arm64"].Bundle)
	require.Equal(t, "https://example.com/darwin.tar.gz", set.Checks[0].WorktreeSnapshot.Runtime.Platforms["darwin-arm64"].URL)
	argv, err := set.Checks[0].Argv()
	require.NoError(t, err)
	require.Equal(t, []string{"make", "check"}, argv)
	set, err = store.Load("project")
	require.NoError(t, err)
	require.Nil(t, set.Checks[0].WorktreeSnapshot)
	names, err := store.List()
	require.NoError(t, err)
	require.Equal(t, []string{"project"}, names)
}

func TestSnapshotSidecarAcceptsJSONWithDeclaredSystemTools(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "checks")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "snapshots"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "project.yaml"), []byte("name: project\nchecks: [{name: full-gate, stage: handoff, files: all, adapter: make, target: check}]\n"), 0o600))
	path := filepath.Join(dir, "snapshots", "project.yaml")
	data := `{"checks":{"full-gate":{
		"profile":"linux-worktree-v1","epoch":"test","sources":{"mode":"worktree"},
		"runtime":{"platforms":{"linux-amd64":{"bundle":"test-linux",
		"sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}},
		"system":{"tools":["git","ffmpeg","espeak-ng"],"files":["/etc/localtime"],
		"platforms":{"linux-amd64":{"dirs":["/var/lib/texmf"]},"darwin-arm64":{"tools":["xcodegen"],"files":["/private/etc/passwd"]}}},
		"data":[{"name":"govulncheck","url":"https://vuln.go.dev/index/db.json","max_age_hours":24,"mount":"vuln/db.json"}]
	}}}`
	load := func(data string) Check {
		require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
		set, err := NewLayeredStoreWithBundle(root, "", "", true).LoadForHandoff("project")
		require.NoError(t, err)
		return set.Checks[0]
	}
	check := load(data)
	require.False(t, check.InvalidSnapshotSidecar)
	require.NotNil(t, check.WorktreeSnapshot)
	require.Equal(t, Strings{"git", "ffmpeg", "espeak-ng"}, check.WorktreeSnapshot.System.Tools)
	require.Equal(t, Strings{"/etc/localtime"}, check.WorktreeSnapshot.System.Files)
	require.Equal(t, Strings{"/var/lib/texmf"}, check.WorktreeSnapshot.System.Platforms["linux-amd64"].Dirs)
	require.Equal(t, []SnapshotData{{Name: "govulncheck", URL: "https://vuln.go.dev/index/db.json", MaxAgeHours: 24, Mount: "vuln/db.json"}}, check.WorktreeSnapshot.Data)
	check = load(strings.Replace(data, `"git"`, `"/usr/bin/git"`, 1))
	require.True(t, check.InvalidSnapshotSidecar)
}

func TestSnapshotSidecarAllowsSystemOnlyProfileWithGitIndex(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "checks", "snapshots")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "project.yaml"), []byte(`checks:
  full-gate:
    profile: linux-worktree-v1
    epoch: test
    sources: {mode: worktree}
    env: {PYTHONDONTWRITEBYTECODE: "1"}
    path: ["{scratch}/bin"]
    git: {}
`), 0o600))
	set := &Set{Checks: []Check{{Name: "full-gate", Stage: "handoff"}}}
	require.NoError(t, loadSnapshotSidecar(root, "project", set))
	snapshot := set.Checks[0].WorktreeSnapshot
	require.NotNil(t, snapshot)
	require.Nil(t, snapshot.Runtime)
	require.Equal(t, []string{"{scratch}/bin"}, snapshot.Path)
	require.Equal(t, map[string]string{"PYTHONDONTWRITEBYTECODE": "1"}, snapshot.Env)
	require.Equal(t, &SnapshotGit{}, snapshot.Git)
}

func TestLoadSnapshotSidecarRejectsInvalidPolicy(t *testing.T) {
	const valid = `checks:
  full-gate:
    profile: linux-worktree-v1
    epoch: test
    sources: {mode: worktree}
    git: {base_ref: refs/remotes/origin/main, refs: [refs/heads/release, refs/remotes/origin/release, refs/tags/v4.8.0]}
    network: loopback
    prehydrate: {run: [uv, sync, --offline]}
    runtime:
      platforms:
        linux-amd64:
          bundle: example
          sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
          url: https://example.com/runtime.tar.gz
`
	for _, tc := range []struct{ name, old, new, reason string }{
		{"unknown field", "network:", "outside_network:", "field outside_network not found"},
		{"multiple documents", "checks:", "---\n{}\n---\nchecks:", "one YAML document"},
		{"empty", valid, "checks: {}", "must name a handoff check"},
		{"unknown check", "full-gate:", "other:", "not a handoff"},
		{"commit check", "full-gate:", "commit-gate:", "not a handoff"},
		{"inline check", "full-gate:", "inline-gate:", "already has an inline snapshot"},
		{"unsupported profile", "linux-worktree-v1", "other-worktree", "needs the linux-worktree-v1 profile"},
		{"missing epoch", "epoch: test", "epoch: ''", "an epoch"},
		{"unsupported sources", "mode: worktree", "mode: directory", "worktree sources"},
		{"host network", "network: loopback", "network: host", "network must be loopback"},
		{"relative system directory", "network: loopback", "system: {dirs: [var/lib/texmf]}", "clean absolute paths"},
		{"invalid system platform", "network: loopback", "system: {platforms: {linux: {tools: [make]}}}", "invalid snapshot system platform"},
		{"invalid platform input", "network: loopback", "system: {platforms: {linux-amd64: {files: [/etc/../etc/group]}}}", "clean absolute paths"},
		{"invalid data mount", "network: loopback", "data: [{name: vuln, url: 'https://example.com/db', mount: '../db', max_age_hours: 24}]", "local name and mount"},
		{"invalid data URL", "network: loopback", "data: [{name: vuln, url: 'http://example.com/db', mount: db, max_age_hours: 24}]", "public HTTPS URL"},
		{"invalid max age", "network: loopback", "data: [{name: vuln, url: 'https://example.com/db', mount: db, max_age_hours: 0}]", "positive max_age_hours"},
		{"duplicate data mount", "network: loopback", "data: [{name: a, url: 'https://example.com/a', mount: db, max_age_hours: 24}, {name: b, url: 'https://example.com/b', mount: db, max_age_hours: 24}]", "names and mounts must be unique"},
		{"ambiguous Git base", "refs/remotes/origin/main", "origin/main", "Git base must be a full ref"},
		{"ambiguous Git ref", "refs/tags/v4.8.0", "v4.8.0", "git.refs entries must be full"},
		{"Git ref glob", "refs/tags/v4.8.0", "refs/tags/v4.*", "git.refs entries must be full"},
		{"unsupported Git ref", "refs/tags/v4.8.0", "refs/notes/review", "git.refs entries must be full"},
		{"empty prehydrate", "run: [uv, sync, --offline]", "run: []", "prehydrate needs a command"},
		{"no platforms", "runtime:", "disabled_runtime:", "field disabled_runtime not found"},
		{"empty platforms", valid, "checks: {full-gate: {profile: linux-worktree-v1, epoch: test, sources: {mode: worktree}, runtime: {platforms: {}}}}", "runtime needs at least one platform"},
		{"malformed platform", "linux-amd64", "linux.amd64", "expected GOOS-GOARCH"},
		{"invalid architecture", "linux-amd64", "linux-amd64-v1", "expected GOOS-GOARCH"},
		{"wrong digest", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "0123", "lowercase SHA-256 digest"},
		{"absolute bundle", "bundle: example", "bundle: /tmp/archive", "must be a filename hint"},
		{"authenticated URL", "https://example.com/runtime.tar.gz", "https://user@example.com/runtime.tar.gz", "must be a public HTTPS asset URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "checks", "snapshots")
			require.NoError(t, os.MkdirAll(dir, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "project.yaml"), []byte(strings.Replace(valid, tc.old, tc.new, 1)), 0o600))
			set := &Set{Checks: []Check{{Name: "full-gate", Stage: "handoff"}, {Name: "commit-gate", Stage: "commit"}, {Name: "inline-gate", Stage: "handoff", Snapshot: &Snapshot{}}}}
			require.ErrorContains(t, loadSnapshotSidecar(root, "project", set), tc.reason)
			require.Nil(t, set.Checks[0].WorktreeSnapshot)
		})
	}
}

func TestLoadSnapshotSidecarRejectsUnsafeFiles(t *testing.T) {
	storeRoot := t.TempDir()
	store := NewLayeredStoreWithBundle(storeRoot, "", "", true)
	_, err := store.LoadForHandoff("missing")
	require.ErrorIs(t, err, os.ErrNotExist)
	require.ErrorIs(t, loadSnapshotSidecar(filepath.Join(storeRoot, "missing"), "project", &Set{}), os.ErrNotExist)
	checks := filepath.Join(storeRoot, "checks")
	snapshots := filepath.Join(checks, "snapshots")
	require.NoError(t, os.Mkdir(checks, 0o700))
	require.NoError(t, os.WriteFile(snapshots, nil, 0o600))
	require.Error(t, loadSnapshotSidecar(storeRoot, "project", &Set{}))
	require.NoError(t, os.Remove(snapshots))
	require.NoError(t, os.Mkdir(snapshots, 0o700))
	path := filepath.Join(snapshots, "project.yaml")
	require.NoError(t, os.Mkdir(path, 0o700))
	require.ErrorContains(t, loadSnapshotSidecar(storeRoot, "project", &Set{}), "must be a regular file")
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat(" ", 64<<10)+"\n"), 0o600))
	require.ErrorContains(t, loadSnapshotSidecar(storeRoot, "project", &Set{}), "is too large")
}
