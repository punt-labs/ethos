//go:build darwin

package hook

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDarwinHandoffSnapshotIsolation(t *testing.T) {
	if !handoffSandboxSupported() {
		t.Fatal("Darwin sandbox self-test failed")
	}
	checkout, scratch, outside := t.TempDir(), t.TempDir(), t.TempDir()
	live := filepath.Join(checkout, "input")
	if err := os.WriteFile(live, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink, hardlink := filepath.Join(outside, "symlink"), filepath.Join(outside, "hardlink")
	if err := os.Symlink(live, symlink); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(live, hardlink); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	proof, err := darwinHandoffProof(ctx, checkout, []string{"input"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for _, phase := range []struct{ event, content string }{{"editing", "changed"}, {"reverting", "original"}} {
			for {
				events, globErr := filepath.Glob(filepath.Join(scratch, "*", "tmp", phase.event))
				if globErr != nil {
					done <- globErr
					return
				}
				if len(events) == 1 {
					if writeErr := os.WriteFile(live, []byte(phase.content), 0o600); writeErr != nil {
						done <- writeErr
						return
					}
					done <- os.WriteFile(events[0]+"-ack", nil, 0o600)
					break
				}
				select {
				case <-ctx.Done():
					done <- ctx.Err()
					return
				case <-time.After(10 * time.Millisecond):
				}
			}
		}
	}()
	// The denied operands are passed as live aliases by the test, not as the
	// expansion of {root} or {files}.
	physical, err := filepath.EvalSymlinks(live)
	if err != nil {
		t.Fatal(err)
	}
	script := `set -eu
	    test "$PWD/input" = "$1"
	    shift
	    for p; do
	        if /bin/cat "$p" >/dev/null 2>&1; then
	            echo "unexpected live read: $p"; exit 1
	        fi
	    done
	    if (echo corrupted > input) 2>/dev/null; then exit 1; fi
	    for phase in editing reverting; do
	        : > "$TMPDIR/$phase"
	        while [ ! -e "$TMPDIR/$phase-ack" ]; do /bin/sleep .01; done
	        test "$(/bin/cat input)" = original
	    done`
	check := editCheck{name: "isolation", cwd: checkout, timeout: 10 * time.Second,
		argv: []string{"/bin/sh", "-c", script, "sh", live, symlink, hardlink,
			"/System/Volumes/Data" + physical}}
	caseAlias := strings.Replace(physical, "/TestDarwin", "/testdarwin", 1)
	if info, err := os.Stat(caseAlias); err == nil && info.Mode().IsRegular() && caseAlias != physical {
		check.argv = append(check.argv, caseAlias)
	}
	result, err := executeHandoffSnapshot(ctx, scratch, checkout, proof, []string{"input"}, check)
	if err != nil || result != "" {
		t.Fatalf("snapshot check: result=%q err=%v", result, err)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestDarwinHandoffSnapshotDefersOnLaunchFailure(t *testing.T) {
	checkout, scratch := t.TempDir(), t.TempDir()
	proof, err := darwinHandoffProof(context.Background(), checkout, nil)
	if err != nil {
		t.Fatal(err)
	}
	check := editCheck{name: "missing executable", argv: []string{"ethos-no-such-handoff-command"}}
	got, err := executeHandoffSnapshot(context.Background(), scratch, checkout, proof, nil, check)
	var deferred *darwinHandoffDeferred
	if !errors.As(err, &deferred) || got != "" || !strings.Contains(err.Error(), "sandbox launch") {
		t.Fatalf("wanted sandbox launch deferral, got result=%q err=%v", got, err)
	}
}

func TestDarwinHandoffStripe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stripe")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	pin, pinned, err := pinHandoffStripe(ctx, path)
	if err != nil || !pinned {
		t.Fatalf("pin: pinned=%v err=%v", pinned, err)
	}
	defer pin.Close()
	second, pinned, err := pinHandoffStripe(ctx, path)
	if err != nil || !pinned {
		t.Fatalf("second pin: pinned=%v err=%v", pinned, err)
	}
	if busy, err := withUnpinnedStripe(pin, func() error {
		t.Error("pruned while another owner held a pin")
		return nil
	}); err != nil || !busy {
		t.Errorf("prune: busy=%v err=%v", busy, err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	pruneErr := errors.New("prune failed")
	if busy, err := withUnpinnedStripe(pin, func() error { return pruneErr }); busy || !errors.Is(err, pruneErr) {
		t.Errorf("prune: busy=%v err=%v", busy, err)
	}
}

func TestDarwinHandoffSnapshotDefersOnSetupFailure(t *testing.T) {
	checkout, scratch := t.TempDir(), t.TempDir()
	path := filepath.Join(checkout, "input")
	if err := os.WriteFile(path, []byte("declared"), 0o600); err != nil {
		t.Fatal(err)
	}
	proof, err := darwinHandoffProof(context.Background(), checkout, []string{"input"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	check := editCheck{name: "must not run", cwd: checkout, argv: []string{"/bin/sh", "-c", "exit 7"}}
	got, err := executeHandoffSnapshot(context.Background(), scratch, checkout, proof, []string{"input"}, check)
	var deferred *darwinHandoffDeferred
	if !errors.As(err, &deferred) || got != "" || !strings.Contains(err.Error(), "proof") {
		t.Fatalf("wanted proof deferral, got result=%q err=%v", got, err)
	}
}

func TestDarwinOpenGateInputRejectsSymlink(t *testing.T) {
	checkout := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(checkout, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "input"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("input", filepath.Join(checkout, "inside")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(checkout)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"inside", "alias/input", "../input"} {
		if file, err := openGateInput(root, name); err == nil {
			file.Close()
			t.Errorf("opened forbidden input %q", name)
		}
	}
}
