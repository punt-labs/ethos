//go:build linux

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCLI_FetchHandoffRuntimeDownloadsViaProxy(t *testing.T) {
	const publicIP, archive = "192.0.2.1", "test published archive bytes"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(archive)))
	se := setupCLISubprocessEnv(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP(publicIP)},
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	require.NoError(t, err)
	certFile := filepath.Join(se.home, "origin.pem")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	var requests atomic.Int32
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/redirect" {
			http.Redirect(w, req, "https://10.0.0.1/runtime.tar.gz", http.StatusFound)
			return
		}
		if req.Host != publicIP || req.URL.Path != "/runtime.tar.gz" {
			http.Error(w, "wrong origin request", http.StatusBadRequest)
			return
		}
		requests.Add(1)
		_, _ = io.WriteString(w, archive)
	}))
	origin.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	origin.StartTLS()
	defer origin.Close()
	var proxyRequests atomic.Int32
	var tunnels atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		proxyRequests.Add(1)
		if req.Method != http.MethodConnect || req.Host != publicIP+":443" && req.Host != "192.0.2.2:443" {
			http.Error(w, "target was not the pinned public endpoint", http.StatusBadRequest)
			return
		}
		upstream, err := net.DialTimeout("tcp", origin.Listener.Addr().String(), time.Second)
		if err != nil {
			http.Error(w, "origin unavailable", http.StatusBadGateway)
			return
		}
		defer upstream.Close()
		client, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer client.Close()
		tunnels.Add(1)
		if _, err := rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil || rw.Flush() != nil {
			return
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = io.Copy(upstream, rw)
			_ = upstream.Close()
		}()
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
		<-done
	}))
	defer proxy.Close()
	se.env = append(se.env, "SSL_CERT_FILE="+certFile, "HTTPS_PROXY="+proxy.URL)
	badDigest := fmt.Sprintf("%x", sha256.Sum256([]byte("not installed")))
	for i, asset := range []string{"https://10.0.0.1/runtime.tar.gz", "https://192.0.2.2/runtime.tar.gz", "https://" + publicIP + "/redirect"} {
		path := writeHandoffRuntimeFixture(t, se, asset, badDigest)
		_, stderr, code := runCLI(t, se, "handoff", "fetch-runtime")
		require.NotZero(t, code)
		require.Contains(t, stderr, "could not fetch published runtime bundle")
		require.NoFileExists(t, path)
		require.EqualValues(t, i, proxyRequests.Load(), asset)
		require.Zero(t, requests.Load())
	}
	path := writeHandoffRuntimeFixture(t, se, "https://"+publicIP+"/runtime.tar.gz", digest)
	require.NoFileExists(t, path)
	out, stderr, code := runCLI(t, se, "handoff", "fetch-runtime")
	require.Zero(t, code, stderr)
	require.Contains(t, out, path)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, archive, string(content))
	require.EqualValues(t, 3, tunnels.Load())
	require.EqualValues(t, 3, proxyRequests.Load())
	require.EqualValues(t, 1, requests.Load())
}

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
	path := writeHandoffRuntimeFixture(t, se, server.URL+"/runtime.tar.gz", digest)
	store := filepath.Dir(path)
	_, stderr, code := runCLI(t, se, "handoff", "fetch-runtime")
	require.NotZero(t, code)
	require.Contains(t, stderr, "could not fetch published runtime bundle")
	require.Zero(t, requests.Load())
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

func writeHandoffRuntimeFixture(t *testing.T, se *cliSubprocessEnv, asset, digest string) string {
	t.Helper()
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
`, runtime.GOOS, runtime.GOARCH, digest, asset)
	require.NoError(t, os.WriteFile(filepath.Join(checks, "snapshots", "project.yaml"), []byte(sidecar), 0o600))

	return filepath.Join(se.home, ".punt-labs", "ethos", "runtimes", digest+".tar.gz")
}
