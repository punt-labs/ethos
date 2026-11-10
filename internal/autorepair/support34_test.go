package autorepair

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/require"
)

// recLog34 is a Logger that remembers every action a step logged.
type recLog34 struct {
	mu    sync.Mutex
	lines []string
}

func (r *recLog34) Logf(step, action string, paths []string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf("%s %s %v %v", step, action, paths, err))
}

// stepNamed34 returns the registered step called name.
func stepNamed34(t *testing.T, name string) Step {
	t.Helper()
	for _, s := range Steps {
		if s.Name == name {
			return s
		}
	}
	require.FailNowf(t, "step not registered", "no step named %q in Steps", name)
	return Step{}
}

// run34 runs the named step once under the step budget.
func run34(t *testing.T, name string, env Env) Outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), StepBudget)
	defer cancel()
	out, err := stepNamed34(t, name).Run(ctx, env)
	require.NoError(t, err, "step %s", name)
	return out
}

// stage34 copies one corpus snapshot into a fresh temporary directory, makes
// the repository a git repository whose index holds its files, and returns the
// Env a step would get. HOME points at the copied home so the redactor sees a
// home that is not the real one.
func stage34(t *testing.T, version string) Env {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	src := filepath.Join("testdata", "corpus", version)
	repo := filepath.Join(root, "repo")
	home := filepath.Join(root, "home")
	copyTree34(t, filepath.Join(src, "repo"), repo)
	copyTree34(t, filepath.Join(src, "home"), home)
	t.Setenv("HOME", home)

	for _, args := range [][]string{{"init", "-q"}, {"add", "-A", "--", "."}} {
		cmd := testhelpers.GitCommand(args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	p, err := layout.NewRepository(repo, repo, layout.RecordBlame)
	require.NoError(t, err)
	return Env{
		Roots:  resolve.Located{Store: repo, Checkout: repo, Ethos: filepath.Join(repo, ".punt-labs", "ethos")},
		Policy: p,
		Global: filepath.Join(home, ".punt-labs", "ethos"),
		Now:    time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC),
		Log:    &recLog34{},
	}
}

func copyTree34(t *testing.T, src, dst string) {
	t.Helper()
	if _, err := os.Stat(src); err != nil {
		require.NoError(t, os.MkdirAll(dst, 0o700))
		return
	}
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
		info, err := d.Info()
		if err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
	require.NoError(t, err, "copying %s", src)
}

// missionDirs34 lists the repository mission directories of a staged snapshot.
func missionDirs34(t *testing.T, env Env) []string {
	t.Helper()
	dir := filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "missions")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func read34(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func write34(t *testing.T, path, data string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(data), 0o644))
}

// provenanceOK34 reports whether a corpus snapshot's PROVENANCE.txt records
// the scenario step as ok.
func provenanceOK34(t *testing.T, version, step string) bool {
	t.Helper()
	b := read34(t, filepath.Join("testdata", "corpus", version, "PROVENANCE.txt"))
	return strings.Contains(b, step+": ok")
}

// captureStderr34 returns what fn wrote to os.Stderr.
func captureStderr34(t *testing.T, fn func()) string {
	t.Helper()
	return testhelpers.CaptureStderr(t, fn)
}
