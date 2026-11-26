package hook

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandoffWorktreeRuntime(t *testing.T) {
	root, run := handoffMetadataFixture(t, `set -eu
test "$ETHOS_HANDOFF_RUNTIME" = /runtime
test "$(runtime-proof)" = bundle
test "$(cat /runtime/hard)" = bundle
if (echo write > /runtime/hard) || touch /runtime/new; then exit 42; fi
`)
	script := "#!/bin/sh\necho bundle"
	writeRuntimeFixture(t, root, []tar.Header{
		{Name: "tools/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "tools/proof", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(script))},
		{Name: "bin/runtime-proof", Typeflag: tar.TypeSymlink, Linkname: "../tools/proof"},
		{Name: "hard", Typeflag: tar.TypeLink, Linkname: "data"},
		{Name: "data", Typeflag: tar.TypeReg, Mode: 0o644, Size: 6},
	}, map[string]string{"tools/proof": script, "data": "bundle"})
	feedback := run()
	skipIfHandoffSandboxUnavailable(t, errors.New(feedback.Blocking))
	require.Empty(t, feedback.Blocking)
}

func TestHandoffWorktreeRejectsUnsafeRuntime(t *testing.T) {
	for _, header := range []tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg},
		{Name: "/absolute", Typeflag: tar.TypeReg},
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../escape"},
		{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		{Name: "link", Typeflag: tar.TypeLink, Linkname: "../escape"},
		{Name: "link", Typeflag: tar.TypeChar},
	} {
		t.Run(fmt.Sprintf("%s-%c-%s", header.Name, header.Typeflag, header.Linkname), func(t *testing.T) {
			root, run := handoffMetadataFixture(t, "echo must-not-run; exit 42")
			writeRuntimeFixture(t, root, []tar.Header{header}, nil)
			feedback := run()
			require.Contains(t, feedback.Notice, "runtime-invalid")
			require.NotContains(t, feedback.Blocking, "must-not-run")
		})
	}
}

func writeRuntimeFixture(t *testing.T, root string, headers []tar.Header, contents map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	gzipWriter := gzip.NewWriter(&buf)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, header := range headers {
		require.NoError(t, tarWriter.WriteHeader(&header))
		_, err := tarWriter.Write([]byte(contents[header.Name]))
		require.NoError(t, err)
	}
	require.NoError(t, tarWriter.Close())
	require.NoError(t, gzipWriter.Close())
	digest := fmt.Sprintf("%x", sha256.Sum256(buf.Bytes()))
	writeFile(t, filepath.Join(os.Getenv("HOME"), ".punt-labs/ethos/runtimes", digest+".tar.gz"), buf.String())
	sidecar := filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml")
	data, err := os.ReadFile(sidecar)
	require.NoError(t, err)
	writeFile(t, sidecar, fmt.Sprintf("%s    runtime: {platforms: {%s-%s: {bundle: demo, sha256: %q, path: ['{runtime}/bin']}}}\n",
		data, runtime.GOOS, runtime.GOARCH, digest))
}
