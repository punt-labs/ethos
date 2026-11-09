//go:build linux || darwin

package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stdinWrappers are the wrappers that forward Claude Code's hook payload to
// the ethos binary, with the ethos subcommand each one runs. The list is read
// from hooks.json, the file Claude Code reads, so a wrapper registered there is
// tested and a wrapper that is not tested cannot be registered. It must also be
// every script in the directory that runs `ethos hook`, apart from none: a
// script nothing registers would never run.
func stdinWrappers(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("hooks.json")
	require.NoError(t, err)
	var cfg struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(raw, &cfg))
	registered := map[string]string{}
	for event, groups := range cfg.Hooks {
		for _, g := range groups {
			for _, h := range g.Hooks {
				const prefix = "${CLAUDE_PLUGIN_ROOT}/hooks/"
				require.True(t, strings.HasPrefix(h.Command, prefix), "%s command %q", event, h.Command)
				name := strings.TrimPrefix(h.Command, prefix)
				src, err := os.ReadFile(name)
				require.NoError(t, err, "%s registers %s", event, name)
				m := ethosHookCall.FindSubmatch(src)
				require.NotNil(t, m, "%s does not run `ethos hook <name>`", name)
				registered[name] = string(m[1])
			}
		}
	}
	require.NotEmpty(t, registered)

	scripts, err := filepath.Glob("*.sh")
	require.NoError(t, err)
	onDisk := map[string]string{}
	for _, name := range scripts {
		src, err := os.ReadFile(name)
		require.NoError(t, err)
		if m := ethosHookCall.FindSubmatch(src); m != nil {
			onDisk[name] = string(m[1])
		}
	}
	require.Equal(t, onDisk, registered, "hooks.json and the wrappers that run `ethos hook` must be the same set")
	return registered
}

// ethosHookCall finds the subcommand a wrapper pipes its payload to.
var ethosHookCall = regexp.MustCompile(`(?m)\| ethos hook ([a-z-]+) `)

// runWrapper runs hook with payload on stdin and a fake ethos that records its
// arguments, its stdin, and whether its stdin is a pipe ("pipe") or not
// ("other"). The wrapper's own stdin is a socketpair, so "pipe" means ethos
// got a fresh fd and not the inherited one (DES-029).
func runWrapper(t *testing.T, hook string, payload []byte, stall time.Duration, setup func(home string)) (stdin []byte, args, kind string) {
	t.Helper()
	stdin, args, kind, _ = runWrapperFull(t, hook, payload, stall, false, setup)
	return stdin, args, kind
}

// runWrapperFull is runWrapper that can hold the writer's end open after the
// payload, as Claude Code does when it never finishes, and also returns the
// HOME the wrapper ran with, so a test can read the log it wrote there.
func runWrapperFull(t *testing.T, hook string, payload []byte, stall time.Duration, hold bool, setup func(home string)) (stdin []byte, args, kind, home string) {
	t.Helper()
	feed := func(w *os.File) {
		// A stall after the first 8 KiB is a writer that is busy between
		// writes; the wrapper gives up on it at its deadline and keeps what it has.
		first := payload
		if stall > 0 && len(payload) > 8192 {
			first = payload[:8192]
		}
		w.Write(first) //nolint:errcheck // a short write shows up as a short stdin
		if len(first) < len(payload) {
			time.Sleep(stall)
			w.Write(payload[len(first):]) //nolint:errcheck // as above
		}
	}
	return runWrapperWith(t, "/bin/bash", hook, feed, hold, setup)
}

// runWrapperWith runs hook under the given bash, with feed writing the payload
// to the wrapper's stdin. When hold is true the writer's end stays open after
// feed returns, until the wrapper has exited.
func runWrapperWith(t *testing.T, bash, hook string, feed func(w *os.File), hold bool, setup func(home string)) (stdin []byte, args, kind, home string) {
	t.Helper()
	script, err := filepath.Abs(hook)
	require.NoError(t, err)
	home = t.TempDir()
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

	cmd := exec.Command(bash, script)
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
		feed(parent)
		if !hold {
			parent.Close()
		}
	}()
	require.NoError(t, cmd.Wait())
	if hold {
		parent.Close()
	}

	stdin, _ = os.ReadFile(out)
	a, _ := os.ReadFile(argv)
	k, _ := os.ReadFile(kindf)
	return stdin, strings.TrimSpace(string(a)), strings.TrimSpace(string(k)), home
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
	for hook, sub := range stdinWrappers(t) {
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
	for hook := range stdinWrappers(t) {
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
	for hook := range stdinWrappers(t) {
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
	for hook := range stdinWrappers(t) {
		t.Run(hook, func(t *testing.T) {
			got, _, _ := runWrapper(t, hook, payload, 0, nil)
			assert.True(t, sameJSON(payload, got), "got %q", got)
		})
	}
}

// The kill switch and the missing-binary bypass still stop the wrapper before
// it runs ethos.
func TestWrappersBypasses(t *testing.T) {
	for hook := range stdinWrappers(t) {
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

// A payload that never completes is read for 10 seconds and then forwarded as
// it stands. The wrapper logs that the read timed out, with the byte count and
// the hook, to hook-errors.log: the Go side sees only a fragment, and the log
// would otherwise show a parse error with no hint of why. The fake ethos here
// records what it was given; the log is what the test is about.
func TestWrappersLogAStdinReadTimeout(t *testing.T) {
	partial := []byte(`{"tool_name":"mcp__plugin_ethos_self__identity","tool_in`)
	for _, bash := range wrapperBashes(t) {
		for hook, sub := range stdinWrappers(t) {
			t.Run(strings.ReplaceAll(strings.TrimPrefix(bash, "/"), "/", "_")+"/"+hook, func(t *testing.T) {
				t.Parallel()
				feed := func(w *os.File) { w.Write(partial) } //nolint:errcheck // a short write shows up as a short stdin
				got, args, _, home := runWrapperWith(t, bash, hook, feed, true, nil)
				assert.Equal(t, "hook "+sub, args, "the fragment is still forwarded")
				assert.True(t, sameJSON(partial, got), "forwarded %q", got)
				log, err := os.ReadFile(filepath.Join(home, ".punt-labs", "ethos", "hook-errors.log"))
				require.NoError(t, err)
				assert.Equal(t, fmt.Sprintf("ethos %s: stdin read timed out after 10s (%d bytes)\n", sub, len(partial)), string(log))
			})
		}
	}
}

// wrapperBashes returns every bash this host has that the wrappers can meet:
// the system one (3.2 on macOS) and, where installed, Homebrew's.
func wrapperBashes(t *testing.T) []string {
	t.Helper()
	var found []string
	for _, b := range []string{"/bin/bash", "/opt/homebrew/bin/bash", "/usr/local/bin/bash"} {
		if _, err := os.Stat(b); err == nil {
			found = append(found, b)
		}
	}
	require.NotEmpty(t, found, "no bash found")
	return found
}

// A payload that arrives in pieces over several seconds is forwarded whole,
// under every bash: bash 3.2 loses what a timed-out read had collected.
func TestWrappersForwardAPayloadArrivingInPieces(t *testing.T) {
	pieces := []string{`{"tool_name":"Edit",`, `"tool_input":{"file_path":"a.go",`, `"new_string":"x"}}` + "\n"}
	want := []byte(strings.Join(pieces, ""))
	for _, bash := range wrapperBashes(t) {
		for hook, sub := range stdinWrappers(t) {
			t.Run(strings.ReplaceAll(strings.TrimPrefix(bash, "/"), "/", "_")+"/"+hook, func(t *testing.T) {
				t.Parallel()
				feed := func(w *os.File) {
					for i, p := range pieces {
						if i > 0 {
							time.Sleep(time.Second)
						}
						w.Write([]byte(p)) //nolint:errcheck // a short write shows up as a short stdin
					}
				}
				got, args, _, _ := runWrapperWith(t, bash, hook, feed, false, nil)
				assert.Equal(t, "hook "+sub, args)
				assert.True(t, sameJSON(want, got), "forwarded %q, want %q", got, want)
			})
		}
	}
}

// A payload that arrives whole and is closed writes no timeout line.
func TestWrappersLogNoTimeoutForACompletePayload(t *testing.T) {
	for hook := range stdinWrappers(t) {
		t.Run(hook, func(t *testing.T) {
			_, _, _, home := runWrapperFull(t, hook, []byte("{}\n"), 0, false, nil)
			log, err := os.ReadFile(filepath.Join(home, ".punt-labs", "ethos", "hook-errors.log"))
			if err == nil {
				assert.NotContains(t, string(log), "timed out")
			} else {
				assert.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}

// The stdin read block is copied into every wrapper, so it must not drift: one
// wrapper that kept the old read would lose a partial payload on bash 3.2.
func TestWrappersShareOneStdinReadBlock(t *testing.T) {
	blocks := map[string][]string{}
	for hook := range stdinWrappers(t) {
		src, err := os.ReadFile(hook)
		require.NoError(t, err)
		start := strings.Index(string(src), "# This block is identical")
		end := strings.Index(string(src), "# read returns >128")
		require.True(t, start >= 0 && end > start, "%s: stdin read block not found", hook)
		block := string(src)[start:end]
		blocks[block] = append(blocks[block], hook)
	}
	assert.Len(t, blocks, 1, "the wrappers carry different stdin read blocks: %v", blocks)
}
