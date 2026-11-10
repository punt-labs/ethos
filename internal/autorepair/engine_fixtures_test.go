package autorepair

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func repairLogLines(t *testing.T, global string) []string {
	raw, err := os.ReadFile(LogPath(global))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	s := strings.TrimSuffix(string(raw), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// captureStd redirects os.Stdout and os.Stderr around f and returns what was written.
func captureStd(t *testing.T, f func()) string {
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout, os.Stderr = w, w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	f()
	os.Stdout, os.Stderr = oldOut, oldErr
	_ = w.Close()
	return <-done
}

// (a) global step + repository step deferring once.
func TestEnsureDeferOnceThenConverge(t *testing.T) {
	r := newRig(t, true)
	r.step("g", ScopeGlobal, Outcome{Items: 1}, nil)
	var calls atomic.Int32
	r.opts.steps = append(r.opts.steps, Step{Name: "rp", Scope: ScopeRepository, Run: func(context.Context, Env) (Outcome, error) {
		if calls.Add(1) == 1 {
			return Outcome{Deferred: true}, nil
		}
		return Outcome{Items: 2}, nil
	}})
	r.opts.order = append(r.opts.order, "rp")
	var out string
	out = captureStd(t, func() { r.ensure() })
	assert.Empty(t, out)
	_, err := r.repoStamp()
	assert.ErrorIs(t, err, os.ErrNotExist, "run1: no repo stamp")
	_, err = r.globalStamp()
	assert.NoError(t, err, "run1: global stamp")
	t.Logf("run1 log:\n%s", strings.Join(repairLogLines(t, r.global), "\n"))

	out = captureStd(t, func() { r.ensure() })
	assert.Empty(t, out)
	_, err = r.repoStamp()
	assert.NoError(t, err, "run2: repo stamp")
	g2, _ := os.ReadFile(GlobalStampPath(r.global))
	rp := filepath.Join(r.store, ".punt-labs", "local", "ethos", StampName)
	r2, _ := os.ReadFile(rp)
	l2 := repairLogLines(t, r.global)
	t.Logf("run2 log:\n%s", strings.Join(l2, "\n"))
	t.Logf("ran: %v", r.ran)

	out = captureStd(t, func() { r.ensure() })
	assert.Empty(t, out)
	g3, _ := os.ReadFile(GlobalStampPath(r.global))
	r3, _ := os.ReadFile(rp)
	assert.Equal(t, string(g2), string(g3))
	assert.Equal(t, string(r2), string(r3))
	assert.Equal(t, l2, repairLogLines(t, r.global), "run3 adds no log line")
	assert.Equal(t, int32(2), calls.Load())
	t.Logf("global stamp:\n%s\nrepo stamp:\n%s", g3, r3)
}

// (b) residual is recorded and not retried.
// (b2) where does a ScopeBoth residual land?
// (c) panic: recovered, no stamp, one log line, nothing printed.
func TestEnsureStepPanicIsLoggedOnce(t *testing.T) {
	r := newRig(t, false)
	r.opts.steps = []Step{{Name: "p", Scope: ScopeGlobal, Run: func(context.Context, Env) (Outcome, error) { panic("kaboom") }}}
	r.opts.order = []string{"p"}
	var res Result
	out := captureStd(t, func() { res = r.ensure() })
	assert.Empty(t, out)
	assert.Equal(t, "", res.Line())
	_, err := r.globalStamp()
	assert.ErrorIs(t, err, os.ErrNotExist)
	lines := repairLogLines(t, r.global)
	t.Logf("log:\n%s", strings.Join(lines, "\n"))
	assert.Len(t, lines, 1)
}

// (d) global lock held by a separate process.
func TestEnsureGlobalLockHeldByChildProcess(t *testing.T) {
	r := newRig(t, false)
	r.opts.lockWait = LockWait
	r.step("s", ScopeGlobal, Outcome{Items: 1}, nil)
	lp := r.opts.globalLockPath(r.global)
	require.NoError(t, os.MkdirAll(filepath.Dir(lp), 0o700))
	ready := filepath.Join(r.global, "ready")
	cmd := exec.Command("perl", "-e", `use Fcntl ":flock"; open(F, ">>", $ARGV[0]) or die; flock(F, LOCK_EX) or die; open(R, ">", $ARGV[1]); close(R); sleep 6;`, lp, ready)
	require.NoError(t, cmd.Start())
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	for i := 0; i < 300; i++ {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	before := treeOf(t, r.global)
	start := time.Now()
	var res Result
	out := captureStd(t, func() { res = r.ensure() })
	el := time.Since(start)
	t.Logf("elapsed=%s res=%+v", el, res)
	assert.Empty(t, out)
	assert.Equal(t, Result{}, res)
	assert.Empty(t, r.ran)
	assert.GreaterOrEqual(t, el, LockWait)
	assert.Less(t, el, LockWait+500*time.Millisecond)
	assert.Equal(t, before, treeOf(t, r.global), "nothing written")
}

// (e) 5 goroutines.
func TestEnsureFiveConcurrentRunOnce(t *testing.T) {
	r := newRig(t, true)
	var runs atomic.Int32
	r.opts.steps = []Step{{Name: "once", Scope: ScopeBoth, Run: func(_ context.Context, e Env) (Outcome, error) {
		runs.Add(1)
		for i := 0; i < 50; i++ {
			e.Log.Logf("once", "probe", []string{strings.Repeat("x", 200)}, nil)
		}
		time.Sleep(100 * time.Millisecond)
		return Outcome{Items: 1}, nil
	}}}
	r.opts.order = []string{"once"}
	var wg sync.WaitGroup
	var repaired atomic.Int32
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, _ := ensure(context.Background(), r.roots, r.global, r.opts)
			if res.Repaired {
				repaired.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, runs.Load())
	assert.EqualValues(t, 1, repaired.Load())
	f, err := os.Open(LogPath(r.global))
	require.NoError(t, err)
	defer f.Close()
	sc := bufio.NewScanner(f)
	n, ran := 0, 0
	for sc.Scan() {
		var m map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m), "torn line: %q", sc.Text())
		n++
		if m["action"] == "ran" {
			ran++
		}
	}
	assert.Equal(t, 1, ran, "one ran line")
	t.Logf("log lines=%d ran=%d", n, ran)
}

// (f) cancelled ctx.
func TestEnsureCancelledCtxLeavesTreesIdentical(t *testing.T) {
	r := newRig(t, true)
	r.step("s", ScopeBoth, Outcome{Items: 1}, nil)
	require.NoError(t, os.MkdirAll(r.global, 0o700))
	gb, rb := treeOf(t, r.global), treeOf(t, r.store)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var res Result
	out := captureStd(t, func() { res, _ = ensure(ctx, r.roots, r.global, r.opts) })
	assert.Empty(t, out)
	assert.Equal(t, Result{}, res)
	assert.Empty(t, r.ran)
	_, err := r.globalStamp()
	assert.ErrorIs(t, err, os.ErrNotExist)
	ga, ra := treeOf(t, r.global), treeOf(t, r.store)
	t.Logf("global before=%v after=%v", gb, ga)
	t.Logf("repo before=%v after=%v", rb, ra)
	t.Logf("log:\n%s", strings.Join(repairLogLines(t, r.global), "\n"))
	assert.Equal(t, gb, ga, "global untouched")
	assert.Equal(t, rb, ra, "repo untouched")
}

// (g) a step that prints: the engine does not stop it.
// (5) redaction of a step error.
// (5) rotation with two FileLog values in two goroutines.
// (4) ScopeBoth step running for the global pass when the repository stamp is current.
