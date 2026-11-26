package hook

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandoffWorktreePrehydrate(t *testing.T) {
	root, run := handoffMetadataFixture(t, `set -eu
test "$(cat /scratch/hydrated)" = ready
test -e "/proc/$(cat /scratch/child)"
python3 -c 'import socket; s = socket.create_connection(("127.0.0.1", int(open("/scratch/port").read())), timeout=2); assert s.recv(20) == b"fixture"'
`)
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip(err)
	}
	token := filepath.Base(filepath.Dir(root))
	writeFile(t, filepath.Join(root, "listener.py"), `import socket
import sys
s = socket.socket()
s.bind(("127.0.0.1", 0))
s.listen()
with open("/scratch/port", "w") as port:
    port.write(str(s.getsockname()[1]))
while True:
    conn, _ = s.accept()
    with conn:
        print("service-output" * 8192, file=sys.stderr, flush=True)
        conn.sendall(b"fixture")
`)
	writeFile(t, filepath.Join(root, "hydrate.sh"), `set -eu
test "$PWD" = /checkout
test "$(cat /runtime/value)" = runtime
test "$(cat /scratch/input)" = checkout
test "$#" = 2
test "$1" != failure || exit 19
python3 listener.py "$2" &
printf '%s' "$!" > /scratch/child
while test ! -s /scratch/port; do sleep 0.01; done
printf '%s' "$1" > /scratch/hydrated
`)
	writeFile(t, filepath.Join(root, "input"), "checkout\n")
	writeProfile := func(arg string) {
		t.Helper()
		writeHandoffScratchProfile(t, root, "    network: loopback\n    scratch: {seed: {'{scratch}/input': '{root}/input'}}\n"+
			"    prehydrate: {run: [sh, '{root}/hydrate.sh', "+arg+", "+token+"]}\n", map[string]string{"value": "runtime\n"})
	}
	writeProfile("ready")
	pass := run()
	skipIfHandoffSandboxUnavailable(t, errors.New(pass.Blocking))
	require.Equal(t, StageFeedback{}, pass)
	processes, err := filepath.Glob("/proc/[0-9]*/cmdline")
	require.NoError(t, err)
	for _, process := range processes {
		if argv, err := os.ReadFile(process); err == nil {
			require.NotContains(t, string(argv), "listener.py\x00"+token+"\x00")
		}
	}

	writeProfile("failure")
	for range 2 {
		failure := run()
		require.Contains(t, failure.Blocking, "prehydrate-failed")
		require.Contains(t, failure.Notice, "Run the explicit project gate")
		require.NotContains(t, failure.Blocking, "metadata: exit status")
	}
	require.NoFileExists(t, filepath.Join(root, "hydrated"))
}
