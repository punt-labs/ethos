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

func TestCLI_FetchHandoffRuntimeVerifiesPublishedBytes(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	const archive = "test published archive bytes"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(archive)))
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := archive
		if requests.Add(1) == 1 {
			body = "unexpected asset bytes"
		}
		_, _ = io.WriteString(w, body)
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
	require.Contains(t, stderr, "runtime-digest-mismatch")
	store := filepath.Join(se.home, ".punt-labs", "ethos", "runtimes")
	entries, err := os.ReadDir(store)
	require.NoError(t, err)
	require.Empty(t, entries)

	out, stderr, code := runCLI(t, se, "handoff", "fetch-runtime")
	require.Zero(t, code, stderr)
	require.Contains(t, out, digest+".tar.gz")
	path := filepath.Join(store, digest+".tar.gz")
	bytes, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, archive, string(bytes))
	_, stderr, code = runCLI(t, se, "handoff", "fetch-runtime")
	require.Zero(t, code, stderr)
	require.EqualValues(t, 2, requests.Load())

	local := filepath.Join(se.home, "local runtime.tar.gz")
	const wrong = "wrong local bytes"
	actual := fmt.Sprintf("%x", sha256.Sum256([]byte(wrong)))
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(local, []byte(wrong), 0o600))
	_, stderr, code = runCLI(t, se, "handoff", "fetch-runtime", "--file", local)
	require.NotZero(t, code)
	require.Contains(t, stderr, actual)
	require.Contains(t, stderr, digest)
	entries, err = os.ReadDir(store)
	require.NoError(t, err)
	require.Empty(t, entries)

	require.NoError(t, os.WriteFile(local, []byte(archive), 0o600))
	out, stderr, code = runCLI(t, se, "handoff", "fetch-runtime", "--file", local)
	require.Zero(t, code, stderr)
	require.Contains(t, out, path)
	bytes, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, archive, string(bytes))
	require.NoError(t, os.WriteFile(local, []byte(wrong), 0o600))
	_, stderr, code = runCLI(t, se, "handoff", "fetch-runtime", "--file", local)
	require.NotZero(t, code)
	require.Contains(t, stderr, actual)
	bytes, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, archive, string(bytes))
	require.EqualValues(t, 2, requests.Load())
}
