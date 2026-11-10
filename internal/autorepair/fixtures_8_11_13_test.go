package autorepair

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// The s81113 prefix keeps these helpers distinct from those other step test
// files in this package define.

// s81113Entry is one recorded Logf call.
type s81113Entry struct {
	step, action string
	paths        []string
	err          error
}

// s81113Log records every Logf call so tests can assert what was logged.
type s81113Log struct {
	mu      sync.Mutex
	entries []s81113Entry
}

func (l *s81113Log) Logf(step, action string, paths []string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, s81113Entry{step, action, append([]string(nil), paths...), err})
}

// s81113Tree is a fixture snapshot copied into a temporary directory.
type s81113Tree struct {
	Repo, Home string // Home is ~/.punt-labs/ethos
	Log        *s81113Log
}

// s81113Copy copies testdata/<fixture> (corpus/<tag> or damage/<name>) into a
// fresh physical temporary directory. A snapshot with no repo/ or home/ gets
// an empty one so steps always have both roots.
func s81113Copy(t *testing.T, fixture string) s81113Tree {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	tr := s81113Tree{
		Repo: filepath.Join(root, "repo"),
		Home: filepath.Join(root, "home", ".punt-labs", "ethos"),
		Log:  &s81113Log{},
	}
	src := filepath.Join("testdata", filepath.FromSlash(fixture))
	if _, err := os.Stat(filepath.Join(src, "repo")); err == nil {
		s81113CopyTree(t, filepath.Join(src, "repo"), tr.Repo)
	}
	if _, err := os.Stat(filepath.Join(src, "home", ".punt-labs", "ethos")); err == nil {
		s81113CopyTree(t, filepath.Join(src, "home", ".punt-labs", "ethos"), tr.Home)
	}
	require.NoError(t, os.MkdirAll(tr.Repo, 0o755))
	require.NoError(t, os.MkdirAll(tr.Home, 0o755))
	return tr
}

func s81113CopyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(to, 0o755)
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, to)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
	require.NoError(t, err)
}

// Env returns the step environment for the tree, as the engine would build it.
func (tr s81113Tree) Env(t *testing.T) Env {
	t.Helper()
	p, err := layout.NewRepository(tr.Repo, tr.Repo, layout.RecordBlame)
	require.NoError(t, err)
	// A fixture may hold a config the loader refuses; the engine passes nil.
	cfg, _ := resolve.ConfigOf(tr.Repo)
	return Env{
		Roots:  resolve.Located{Store: tr.Repo, Ethos: p.RecordRoot(), Checkout: tr.Repo, Config: cfg},
		Policy: p,
		Global: tr.Home,
		Log:    tr.Log,
	}
}

// s81113Run runs the registered step called name.
func s81113Run(t *testing.T, name string, env Env) Outcome {
	t.Helper()
	for _, s := range Steps {
		if s.Name == name {
			out, err := s.Run(context.Background(), env)
			require.NoError(t, err)
			return out
		}
	}
	t.Fatalf("step %q is not registered", name)
	return Outcome{}
}

func s81113Read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}

func s81113Exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// s81113Snapshot records every entry under the tree's roots: path, kind and
// bytes. A second run of an idempotent step must leave it identical.
func s81113Snapshot(t *testing.T, tr s81113Tree) map[string]string {
	t.Helper()
	snap := map[string]string{}
	for _, root := range []string{tr.Repo, tr.Home} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			switch {
			case d.IsDir():
				snap[p] = "dir"
			case d.Type()&fs.ModeSymlink != 0:
				target, err := os.Readlink(p)
				if err != nil {
					return err
				}
				snap[p] = "link:" + target
			default:
				b, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				snap[p] = "file:" + string(b)
			}
			return nil
		})
		require.NoError(t, err)
	}
	return snap
}

// s81113Write creates path (and parents) holding data.
func s81113Write(t *testing.T, path, data string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(data), 0o644))
}
