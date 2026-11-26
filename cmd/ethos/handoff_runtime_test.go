//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCLI_FetchHandoffRuntimeRefusesLoopbackAndVerifiesLocalBytes(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	const archive = "test published archive bytes"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(archive)))
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, archive)
	}))
	defer server.Close()
	cert := filepath.Join(se.home, "tls.pem")
	require.NoError(t, os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600))
	se.env = append(se.env, "SSL_CERT_FILE="+cert)
	checks := filepath.Join(se.repo, ".punt-labs", "ethos", "checks")
	require.NoError(t, os.MkdirAll(filepath.Join(checks, "snapshots"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs", "ethos.yaml"), []byte("agent: test-agent\nchecks: project\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(checks, "project.yaml"), []byte(`name: project
checks:
  - name: full-gate
    stage: handoff
    files: all
    run: [make, check]
`), 0o600))
	sidecar := fmt.Sprintf(`checks:
  full-gate:
    profile: linux-worktree-v1
    epoch: test
    sources: {mode: worktree}
    runtime:
      platforms:
        %s-%s: {bundle: demo, sha256: %q, url: %q}
`, runtime.GOOS, runtime.GOARCH, digest, server.URL+"/runtime.tar.gz")
	require.NoError(t, os.WriteFile(filepath.Join(checks, "snapshots", "project.yaml"), []byte(sidecar), 0o600))

	_, stderr, code := runCLI(t, se, "handoff", "fetch-runtime")
	require.NotZero(t, code)
	require.Contains(t, stderr, "could not fetch published runtime bundle")
	require.Zero(t, requests.Load())
	store := filepath.Join(se.home, ".punt-labs", "ethos", "runtimes")
	path := filepath.Join(store, digest+".tar.gz")
	require.NoFileExists(t, path)

	local := filepath.Join(se.home, "local runtime.tar.gz")
	const wrong = "wrong local bytes"
	actual := fmt.Sprintf("%x", sha256.Sum256([]byte(wrong)))
	require.NoError(t, os.WriteFile(local, []byte(wrong), 0o600))
	_, stderr, code = runCLI(t, se, "handoff", "fetch-runtime", "--file", local)
	require.NotZero(t, code)
	require.Contains(t, stderr, actual)
	require.Contains(t, stderr, digest)
	entries, err := os.ReadDir(store)
	require.NoError(t, err)
	require.Empty(t, entries)

	require.NoError(t, os.WriteFile(local, []byte(archive), 0o600))
	out, stderr, code := runCLI(t, se, "handoff", "fetch-runtime", "--file", local)
	require.Zero(t, code, stderr)
	require.Contains(t, out, path)
	bytes, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, archive, string(bytes))
	require.NoError(t, os.WriteFile(local, []byte(wrong), 0o600))
	_, stderr, code = runCLI(t, se, "handoff", "fetch-runtime", "--file", local)
	require.NotZero(t, code)
	require.Contains(t, stderr, actual)
	bytes, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, archive, string(bytes))
	out, stderr, code = runCLI(t, se, "handoff", "fetch-runtime")
	require.Zero(t, code, stderr)
	require.Contains(t, out, digest+".tar.gz")
	require.Zero(t, requests.Load())

	setInProcessEnv(t, se)
	command, _, err := rootCmd.Find([]string{"handoff", "fetch-runtime"})
	require.NoError(t, err)
	fileFlag := command.Flags().Lookup("file")
	t.Cleanup(func() {
		require.NoError(t, fileFlag.Value.Set(""))
		fileFlag.Changed = false
	})
	out, _, err = execHandler(t, "handoff", "fetch-runtime", "--file", local)
	require.ErrorContains(t, err, actual)
	require.Empty(t, out)
	require.NoError(t, os.WriteFile(local, []byte(archive), 0o600))
	out, _, err = execHandler(t, "handoff", "fetch-runtime", "--file", local)
	require.NoError(t, err)
	require.Equal(t, path+"\n", out)
	out, _, err = execHandler(t, "handoff", "fetch-runtime", "--file", local, "--json")
	require.NoError(t, err)
	require.JSONEq(t, fmt.Sprintf("[%q]", path), out)
	require.Zero(t, requests.Load())
}
