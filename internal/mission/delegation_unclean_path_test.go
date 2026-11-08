//go:build !windows

package mission

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDelegationUncleanRepositoryPath(t *testing.T) {
	for _, tt := range []struct {
		name     string
		relative bool
		parents  int
	}{
		{name: "absolute", parents: 1},
		{name: "relative", relative: true, parents: 1},
		{name: "repository markers erased", parents: 6},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, external := t.TempDir(), t.TempDir()
			parent := filepath.Dir(DelegationDir(root, "M", "d-link"))
			require.NoError(t, os.MkdirAll(parent, 0o700))
			child := external
			for i := 0; i < tt.parents; i++ {
				child = filepath.Join(child, "child")
			}
			require.NoError(t, os.MkdirAll(child, 0o700))
			target := filepath.Join(external, "d-e", "record.yaml")
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o700))
			before := []byte("id: [unclean-path-payload]\nverdict: open\n")
			require.NoError(t, os.WriteFile(target, before, 0o640))
			info, err := os.Stat(target)
			require.NoError(t, err)
			require.NoError(t, os.Symlink(child, filepath.Join(parent, "d-link")))
			if tt.relative {
				cwd, err := os.Getwd()
				require.NoError(t, err)
				parent, err = filepath.Rel(cwd, parent)
				require.NoError(t, err)
			}
			// Join would erase the counterexample. The kernel follows d-link
			// before applying each '..'; lexical cleaning removes it instead.
			sep := string(filepath.Separator)
			raw := parent + sep + "d-link" + sep + strings.Repeat(".."+sep, tt.parents) + "d-e" + sep + "record.yaml"
			require.NotEqual(t, raw, filepath.Clean(raw))
			require.NoFileExists(t, filepath.Clean(raw))
			d, err := LoadDelegation(raw)
			t.Logf("direct LoadDelegation: %v", err)
			assert.Nil(t, d, "no redirected payload may be returned")
			assert.ErrorContains(t, err, "repository delegation path must be clean")
			if assert.Error(t, err) {
				assert.NotContains(t, err.Error(), "decoding delegation record")
				assert.NotContains(t, err.Error(), "unclean-path-payload")
			}
			after, err := os.ReadFile(target)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			afterInfo, err := os.Stat(target)
			require.NoError(t, err)
			assert.Equal(t, info.Mode(), afterInfo.Mode())
			assert.True(t, os.SameFile(info, afterInfo))
		})
	}
}

func TestLoadDelegationUncleanRepositorySpelling(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(DelegationDir(root, "M", "d-e"), "record.yaml")
	sep := string(filepath.Separator)
	for _, tt := range []struct{ name, path string }{
		{"dot", filepath.Dir(path) + sep + "." + sep + "record.yaml"},
		{"repeated separator", filepath.Dir(path) + sep + sep + "record.yaml"},
		{"trailing separator", path + sep},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadDelegation(tt.path)
			assert.ErrorContains(t, err, "repository delegation path must be clean")
		})
	}
}

func TestLoadDelegationUncleanPathControls(t *testing.T) {
	const body = "id: d-control\nverdict: open\n"
	root := filepath.Join(t.TempDir(), ".punt-labs", "ethos", "missions", "outer", "delegations", "inner", "repository")
	path := filepath.Join(DelegationDir(root, "M", "d-e"), "record.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	cwd, err := os.Getwd()
	require.NoError(t, err)
	relative, err := filepath.Rel(cwd, path)
	require.NoError(t, err)
	for _, tt := range []struct{ name, path string }{
		{"absolute nested markers", path},
		{"relative nested markers", relative},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, err := LoadDelegation(tt.path)
			require.NoError(t, err)
			assert.Equal(t, "d-control", d.ID)
		})
	}
	t.Run("missing repository record", func(t *testing.T) {
		_, err := LoadDelegation(filepath.Join(DelegationDir(root, "M", "missing"), "record.yaml"))
		assert.True(t, errors.Is(err, fs.ErrNotExist), "got %v", err)
	})
	for _, tt := range []struct{ name, path string }{
		{"standalone", filepath.Join(t.TempDir(), "record.yaml")},
		{"global delegation", filepath.Join(t.TempDir(), ".punt-labs", "ethos", "delegations", "d-e", "record.yaml")},
		{"exported", filepath.Join(t.TempDir(), "exported-delegation.yaml")},
		{"backslash component", filepath.Join(DelegationDir(root, "M", `d-link\..\d-e`), "record.yaml")},
		{"backslash marker is a filename", filepath.Join(t.TempDir(), `.punt-labs\ethos\missions\M\delegations\d-link\..\d-e\record.yaml`)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, os.MkdirAll(filepath.Dir(tt.path), 0o700))
			require.NoError(t, os.WriteFile(tt.path, []byte(body), 0o600))
			d, err := LoadDelegation(tt.path)
			require.NoError(t, err)
			assert.Equal(t, "d-control", d.ID)
		})
	}
	t.Run("standalone unclean traversal retains original meaning", func(t *testing.T) {
		standalone, external := t.TempDir(), t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(external, "child"), 0o700))
		require.NoError(t, os.Mkdir(filepath.Join(external, "d-e"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(external, "d-e", "record.yaml"), []byte(body), 0o600))
		require.NoError(t, os.Symlink(filepath.Join(external, "child"), filepath.Join(standalone, "link")))
		raw := standalone + "/link/../d-e/record.yaml"
		require.NoFileExists(t, filepath.Clean(raw))
		d, err := LoadDelegation(raw)
		require.NoError(t, err)
		assert.Equal(t, "d-control", d.ID)
	})
}

func TestLoadDelegationUncleanCWD(t *testing.T) {
	const childCase = "ETHOS_TEST_DELEGATION_CWD_CASE"
	name := os.Getenv(childCase)
	if name == "" {
		binary, err := os.Executable()
		require.NoError(t, err)
		for _, name := range []string{"logical relative", "physical relative", "unclean base", "absolute", "standalone", "standalone record"} {
			t.Run(name, func(t *testing.T) {
				// Only the isolated child changes process-wide cwd/PWD. No
				// package tests run alongside those mutations in that child.
				cmd := exec.Command(binary, "-test.run=^TestLoadDelegationUncleanCWD$", "-test.v")
				cmd.Env = append(os.Environ(), childCase+"="+name)
				output, err := cmd.CombinedOutput()
				t.Logf("isolated %s:\n%s", name, output)
				assert.NoError(t, err)
			})
		}
		return
	}

	root := t.TempDir()
	a, b := filepath.Join(root, "A"), filepath.Join(root, "B")
	physical := filepath.Join(b, "child")
	logical := filepath.Join(a, "link")
	require.NoError(t, os.MkdirAll(physical, 0o700))
	require.NoError(t, os.Mkdir(a, 0o700))
	require.NoError(t, os.Symlink(physical, logical))
	aPath := filepath.Join(DelegationDir(a, "M", "d-e"), "record.yaml")
	bPath := filepath.Join(DelegationDir(b, "M", "d-e"), "record.yaml")
	paths := []string{aPath, bPath, filepath.Join(b, "standalone.yaml"), filepath.Join(b, "exported", "record.yaml")}
	before := make([][]byte, len(paths))
	infos := make([]fs.FileInfo, len(paths))
	for i, path := range paths {
		id := "physical-B"
		if path == aPath {
			id = "logical-A"
		}
		before[i] = []byte("id: " + id + "\nverdict: open\n")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, before[i], 0o640))
		info, err := os.Stat(path)
		require.NoError(t, err)
		infos[i] = info
	}

	// Cleanup restores cwd and PWD even on failure. A valid alias is required:
	// this is Go's documented Getwd behavior, not an invented shell shortcut.
	t.Chdir(physical)
	pwd := logical
	if name == "physical relative" {
		pwd = physical
	} else if name == "unclean base" {
		pwd = physical + "/."
	}
	t.Setenv("PWD", pwd)
	dot, err := os.Stat(".")
	require.NoError(t, err)
	alias, err := os.Stat(pwd)
	require.NoError(t, err)
	require.True(t, os.SameFile(dot, alias), "PWD must identify the actual cwd")
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.Equal(t, pwd, cwd, "Getwd must actually preserve the supplied alias")

	relative := "../.punt-labs/ethos/missions/M/delegations/d-e/record.yaml"
	require.Equal(t, relative, filepath.Clean(relative))
	physicalBytes, err := os.ReadFile(relative)
	require.NoError(t, err)
	require.Equal(t, before[1], physicalBytes, "the raw OS path must identify B")
	require.NotEqual(t, before[0], physicalBytes)
	path := relative
	switch name {
	case "absolute":
		path = bPath // A bad cwd must not invalidate a clean absolute input.
	case "standalone":
		path = "../standalone.yaml"
	case "standalone record":
		path = "../exported/record.yaml"
	}
	d, loadErr := LoadDelegation(path)
	id := ""
	if d != nil {
		id = d.ID
	}
	t.Logf("Getwd=%q; raw relative identifies B; LoadDelegation id=%q err=%v", cwd, id, loadErr)
	switch name {
	case "logical relative":
		assert.Nil(t, d, "must refuse, not return A or adopt B")
		assert.ErrorContains(t, loadErr, "delegation working directory")
		assert.ErrorContains(t, loadErr, "refusing to follow symlink")
	case "unclean base":
		assert.Nil(t, d)
		assert.ErrorContains(t, loadErr, "delegation working directory")
		assert.ErrorContains(t, loadErr, "must be clean and absolute")
	default:
		if assert.NoError(t, loadErr) && assert.NotNil(t, d) {
			assert.Equal(t, "physical-B", d.ID)
		}
	}
	for i, path := range paths {
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, before[i], after)
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, infos[i].Mode(), info.Mode())
		assert.True(t, os.SameFile(infos[i], info))
	}
}
