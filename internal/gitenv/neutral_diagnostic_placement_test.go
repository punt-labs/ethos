package gitenv

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNeutralDiagnosticRepository(t *testing.T) {
	git, err := exec.LookPath("git")
	require.NoError(t, err, "actual Git is required")
	for _, linked := range []bool{false, true} {
		for _, mode := range []layout.RecordMode{layout.RecordBlame, layout.RecordFull} {
			base, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			store, checkout := filepath.Join(base, "store"), filepath.Join(base, "store")
			if linked {
				checkout = filepath.Join(base, "checkout")
			}
			require.NoError(t, os.MkdirAll(store, 0o700))
			record := filepath.Join(checkout, ".punt-labs", "ethos")
			require.NoError(t, os.MkdirAll(record, 0o700))
			t.Setenv("TMPDIR", record)
			t.Setenv("GIT_DIR", filepath.Join(base, "unrelated-git"))
			t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(base, "global"))
			t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
			t.Setenv("GIT_CONFIG_PARAMETERS", "")
			t.Setenv("GIT_CONFIG_COUNT", "1")
			t.Setenv("GIT_CONFIG_KEY_0", "user.name")
			t.Setenv("GIT_CONFIG_VALUE_0", "command")
			require.NoError(t, os.WriteFile(filepath.Join(base, "global"), []byte("[include]\n path = included\n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(base, "included"), []byte("[user]\n name = included\n"), 0o600))
			p, err := layout.NewRepository(store, checkout, mode)
			require.NoError(t, err)
			dir, env, cleanup, err := NeutralInRepository(p)
			require.NoError(t, err)
			t.Cleanup(cleanup)
			assert.Equal(t, p.TempDir(), filepath.Dir(dir))
			assert.Contains(t, filepath.Base(dir), layout.TmpMarker)
			info, err := os.Stat(dir)
			require.NoError(t, err)
			if runtime.GOOS != "windows" {
				assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
			}
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			assert.Empty(t, entries, "retain an empty neutral cwd, not C")
			assert.NotContains(t, env, "GIT_DIR="+filepath.Join(base, "unrelated-git"))
			assert.Contains(t, env, "GIT_CEILING_DIRECTORIES="+p.TempDir())
			// A broken .git above the fresh directory must be outside Git's and
			// the runner's discovery boundary, not a reason to fall back to C.
			require.NoError(t, os.Symlink(filepath.Join(base, "missing"), filepath.Join(p.TempDir(), ".git")))
			out, err := Run(Spec{Git: git, Dir: dir, Env: env, Args: []string{"config", "user.name"}})
			require.NoError(t, err)
			assert.Equal(t, "command\n", string(out))
			cleanup()
			assert.NoDirExists(t, dir)
			entries, err = os.ReadDir(record)
			require.NoError(t, err)
			assert.Empty(t, entries)
		}
	}
}

func TestNeutralDiagnosticRefusesUnsafeLocal(t *testing.T) {
	_, _, _, err := NeutralInRepository(layout.Policy{})
	require.Error(t, err, "an uninitialized policy must not use ambient TMPDIR")
	for _, component := range []string{".punt-labs", ".punt-labs/local", ".punt-labs/local/ethos", ".punt-labs/local/ethos/tmp"} {
		t.Run(component, func(t *testing.T) {
			base, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			store := filepath.Join(base, "store")
			redirect := filepath.Join(base, "record")
			require.NoError(t, os.MkdirAll(redirect, 0o700))
			link := filepath.Join(store, filepath.FromSlash(component))
			require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o700))
			require.NoError(t, os.Symlink(redirect, link))
			p, err := layout.NewRepository(store, store, layout.RecordBlame)
			require.NoError(t, err)
			dir, env, cleanup, err := NeutralInRepository(p)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "symlink")
			assert.Empty(t, dir)
			assert.Nil(t, env)
			assert.Nil(t, cleanup)
			entries, err := os.ReadDir(redirect)
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}

func TestNeutralDiagnosticConcurrentDirectories(t *testing.T) {
	store, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	p, err := layout.NewRepository(store, store, layout.RecordBlame)
	require.NoError(t, err)
	var wg sync.WaitGroup
	paths := make(chan string, 8)
	errors := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dir, _, cleanup, err := NeutralInRepository(p)
			if err != nil {
				errors <- err
				return
			}
			defer cleanup()
			paths <- dir
		}()
	}
	wg.Wait()
	close(paths)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	seen := map[string]bool{}
	for dir := range paths {
		assert.False(t, seen[dir], "each diagnostic must own its cwd")
		seen[dir] = true
		assert.True(t, strings.HasPrefix(dir, p.TempDir()+string(filepath.Separator)))
		assert.NoDirExists(t, dir)
	}
	assert.Len(t, seen, 8)
}

func TestNeutralDiagnosticInterrupted(t *testing.T) {
	store, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	exe, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestNeutralDiagnosticInterruptedChild$")
	cmd.Env = append(os.Environ(), "ETHOS_TEST_NEUTRAL_DIAGNOSTIC_STORE="+store)
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	line, readErr := bufio.NewReader(out).ReadString('\n')
	// Kill before the child's deferred cleanup can run. Interrupted scratch
	// may remain, but only under the same canonical local root, never records.
	killErr := cmd.Process.Kill()
	waitErr := cmd.Wait()
	require.NoError(t, readErr)
	require.NoError(t, killErr)
	require.Error(t, waitErr)
	p, err := layout.NewRepository(store, store, layout.RecordBlame)
	require.NoError(t, err)
	dir := strings.TrimSpace(line)
	assert.Equal(t, p.TempDir(), filepath.Dir(dir))
	assert.DirExists(t, dir)
	assert.NoDirExists(t, p.RecordRoot())
	assert.NoError(t, ctx.Err(), "child must reach allocation before its bound")
}

func TestNeutralDiagnosticInterruptedChild(t *testing.T) {
	store := os.Getenv("ETHOS_TEST_NEUTRAL_DIAGNOSTIC_STORE")
	if store == "" {
		return // Only the parent's killed child executes the scenario.
	}
	p, err := layout.NewRepository(store, store, layout.RecordBlame)
	require.NoError(t, err)
	dir, _, cleanup, err := NeutralInRepository(p)
	require.NoError(t, err)
	defer cleanup()
	fmt.Println(dir)
	time.Sleep(time.Hour)
}

func TestNeutralDiagnosticCeilingSeparator(t *testing.T) {
	for _, linked := range []bool{false, true} {
		for _, mode := range []layout.RecordMode{layout.RecordBlame, layout.RecordFull} {
			t.Run(fmt.Sprintf("linked=%t/mode=%d", linked, mode), func(t *testing.T) {
				base, err := filepath.EvalSymlinks(t.TempDir())
				require.NoError(t, err)
				store := filepath.Join(base, "store"+string(os.PathListSeparator)+"separator")
				checkout := store
				if linked {
					checkout = filepath.Join(base, "checkout")
				}
				p, err := layout.NewRepository(store, checkout, mode)
				require.NoError(t, err, "a native path-list separator can be a valid filename component")
				require.Contains(t, p.TempDir(), string(os.PathListSeparator))
				before := map[string][]byte{
					filepath.Join(store, ".git", "config"):           []byte("[core]\n bare = false\n"),
					filepath.Join(store, ".punt-labs", "ethos.yaml"): []byte("record: blame\n"),
					filepath.Join(p.RecordRoot(), "payload.yaml"):    []byte("fixture: unchanged\n"),
				}
				for path, data := range before {
					require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
					require.NoError(t, os.WriteFile(path, data, 0o600))
				}
				ambient := filepath.Join(checkout, ".punt-labs", "ethos")
				require.NoError(t, os.MkdirAll(ambient, 0o700))
				t.Setenv("TMPDIR", ambient)
				dir, env, cleanup, err := NeutralInRepository(p)
				t.Logf("S=%s C=%s separator=%q dir=%q err=%v", store, checkout, os.PathListSeparator, dir, err)
				if cleanup != nil {
					t.Cleanup(cleanup) // RED must also leave its fixture disposable.
				}
				assert.ErrorContains(t, err, "path-list separator")
				assert.Empty(t, dir)
				assert.Nil(t, env)
				assert.Nil(t, cleanup)
				assert.NoDirExists(t, filepath.Join(store, ".punt-labs", "local"), "reject before EnsureLocalDir or MkdirTemp")
				for path, data := range before {
					after, err := os.ReadFile(path)
					require.NoError(t, err)
					assert.Equal(t, data, after, path)
				}
				entries, err := os.ReadDir(ambient)
				require.NoError(t, err)
				if linked {
					assert.Empty(t, entries, "no C/global/ambient fallback")
				} else {
					require.Len(t, entries, 1)
					assert.Equal(t, "payload.yaml", entries[0].Name())
				}
			})
		}
	}
}
