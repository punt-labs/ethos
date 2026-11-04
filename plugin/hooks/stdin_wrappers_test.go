//go:build linux || darwin

package hooks

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stdinWrappers are the wrappers that forward Claude Code's hook payload to
// the ethos binary, with the ethos subcommand each one runs.
var stdinWrappers = map[string]string{
	"audit-log.sh":       "audit-log",
	"pre-compact.sh":     "pre-compact",
	"pre-tool-use.sh":    "pre-tool-use",
	"session-end.sh":     "session-end",
	"session-start.sh":   "session-start",
	"subagent-start.sh":  "subagent-start",
	"subagent-stop.sh":   "subagent-stop",
	"suppress-output.sh": "format-output",
}

// runWrapper runs hook with payload on stdin and a fake ethos that records its
// arguments, its stdin, and whether its stdin is a pipe ("pipe") or not
// ("other"). The wrapper's own stdin is a socketpair, so "pipe" means ethos
// got a fresh fd and not the inherited one (DES-029).
func runWrapper(t *testing.T, hook string, payload []byte, stall time.Duration, setup func(home string)) (stdin []byte, args, kind string) {
	t.Helper()
	script, err := filepath.Abs(hook)
	require.NoError(t, err)
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".punt-labs", "ethos"), 0o755))
	if setup != nil {
		setup(home)
	}
	bin := t.TempDir()
	out := filepath.Join(t.TempDir(), "stdin")
	argv := filepath.Join(t.TempDir(), "args")
	kindf := filepath.Join(t.TempDir(), "kind")
	fake := "#!/bin/sh\necho \"$@\" > '" + argv + "'\n" +
		"if [ -p /dev/stdin ]; then echo pipe; else echo other; fi > '" + kindf + "'\n" +
		"cat > '" + out + "'\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ethos"), []byte(fake), 0o755))

	cmd := exec.Command("/bin/bash", script)
	cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}
	// Claude Code is a Node process, and Node gives a child's stdin a
	// socketpair, not a pipe; the 8 KiB truncation appears on that fd.
	// Close-on-exec, under ForkLock as os.Pipe does: a concurrent test's child
	// must not inherit these ends, or this child never sees EOF.
	syscall.ForkLock.RLock()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fds[0])
		syscall.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
	require.NoError(t, err)
	parent := os.NewFile(uintptr(fds[0]), "stdin-parent")
	child := os.NewFile(uintptr(fds[1]), "stdin-child")
	cmd.Stdin = child
	require.NoError(t, cmd.Start())
	require.NoError(t, child.Close())
	go func() {
		// A stall after the first 8 KiB is a writer that is busy between
		// writes; bash read -t 1 gives up on it and keeps what it has.
		first := payload
		if stall > 0 && len(payload) > 8192 {
			first = payload[:8192]
		}
		parent.Write(first) //nolint:errcheck // a short write shows up as a short stdin
		if len(first) < len(payload) {
			time.Sleep(stall)
			parent.Write(payload[len(first):]) //nolint:errcheck // as above
		}
		parent.Close()
	}()
	require.NoError(t, cmd.Wait())

	stdin, _ = os.ReadFile(out)
	a, _ := os.ReadFile(argv)
	k, _ := os.ReadFile(kindf)
	return stdin, strings.TrimSpace(string(a)), strings.TrimSpace(string(k))
}

// sameJSON reports whether got is want with at most one extra trailing
// newline: the wrapper re-sends the payload with printf '%s\n'.
func sameJSON(want, got []byte) bool {
	return bytes.Equal(bytes.TrimRight(want, "\n"), bytes.TrimRight(got, "\n"))
}

// largePayload is one line of JSON well past the 8 KiB a bash read -t
// delivers and the 64 KiB of a pipe buffer, as an Edit of a large file is.
func largePayload() []byte {
	return []byte(`{"tool_name":"Edit","tool_input":{"new_string":"` + strings.Repeat("x", 100*1024) + `"}}` + "\n")
}

// A payload larger than one read must reach ethos whole: a truncated payload
// is invalid JSON, and the hook then acts on an empty input.
func TestWrappersForwardLargePayloadIntact(t *testing.T) {
	payload := largePayload()
	for hook, sub := range stdinWrappers {
		t.Run(hook, func(t *testing.T) {
			got, args, _ := runWrapper(t, hook, payload, 0, nil)
			assert.Equal(t, "hook "+sub, args)
			assert.True(t, sameJSON(payload, got), "stdin forwarded whole (%d of %d bytes)", len(got), len(payload))
		})
	}
}

// ethos must read a fresh pipe, not the fd 0 Claude Code's /bin/sh handed the
// wrapper: Go's read of that fd hung on Linux in production (DES-029) and no
// test harness reproduces it.
func TestWrappersGiveEthosAFreshPipe(t *testing.T) {
	for hook := range stdinWrappers {
		t.Run(hook, func(t *testing.T) {
			_, _, kind := runWrapper(t, hook, []byte("{}\n"), 0, nil)
			assert.Equal(t, "pipe", kind, "ethos stdin")
		})
	}
}

// A writer that stalls past one second after the first 8 KiB (Claude Code
// serializing a large Edit) is what truncated payloads in production:
// hook-errors.log held "invalid JSON input (8192 bytes)" when the wrapper
// read with -t 1. The wrapper waits up to 10s for the whole payload.
func TestWrappersForwardStalledPayloadIntact(t *testing.T) {
	payload := largePayload()
	for hook := range stdinWrappers {
		t.Run(hook, func(t *testing.T) {
			t.Parallel()
			got, _, _ := runWrapper(t, hook, payload, 1300*time.Millisecond, nil)
			assert.True(t, sameJSON(payload, got), "stdin forwarded whole (%d of %d bytes)", len(got), len(payload))
		})
	}
}

// A multi-line payload is forwarded as sent, not cut at the first newline.
func TestWrappersForwardMultilinePayload(t *testing.T) {
	payload := []byte("{\"a\":\n\"b\"}\n")
	for hook := range stdinWrappers {
		t.Run(hook, func(t *testing.T) {
			got, _, _ := runWrapper(t, hook, payload, 0, nil)
			assert.True(t, sameJSON(payload, got), "got %q", got)
		})
	}
}

// The kill switch and the missing-binary bypass still stop the wrapper before
// it runs ethos.
func TestWrappersBypasses(t *testing.T) {
	for hook := range stdinWrappers {
		t.Run(hook+"/kill switch", func(t *testing.T) {
			got, args, _ := runWrapper(t, hook, []byte("{}\n"), 0, func(home string) {
				require.NoError(t, os.WriteFile(filepath.Join(home, ".punt-hooks-kill"), nil, 0o644))
			})
			assert.Empty(t, got)
			assert.Empty(t, args)
		})
		t.Run(hook+"/no ethos config", func(t *testing.T) {
			got, args, _ := runWrapper(t, hook, []byte("{}\n"), 0, func(home string) {
				require.NoError(t, os.Remove(filepath.Join(home, ".punt-labs", "ethos")))
			})
			assert.Empty(t, got)
			assert.Empty(t, args)
		})
	}
}
