package hook

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/checkset"
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

func TestHandoffRuntimeExtractionNeverPublishesUnverifiedTree(t *testing.T) {
	ctx := context.Background()
	var archive bytes.Buffer
	zw := gzip.NewWriter(&archive)
	tw := tar.NewWriter(zw)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "valid-directory", Typeflag: tar.TypeDir, Mode: 0o755}))
	require.NoError(t, tw.Close())
	require.NoError(t, zw.Close())
	base := t.TempDir()
	dest := filepath.Join(base, "runtime")
	bundle := newHandoffRuntime(base, checkset.SnapshotPlatform{
		Bundle: "demo", SHA256: fmt.Sprintf("%x", sha256.Sum256(archive.Bytes())),
	}, "")
	writeFile(t, bundle.archive, archive.String())

	busy, err := audit.TryLock(bundle.archive+".lock", func() error {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		require.ErrorContains(t, bundle.extract(canceled, dest), "runtime extraction interrupted")
		require.NoDirExists(t, dest)
		return nil
	})
	require.NoError(t, err)
	require.False(t, busy)

	changed := bundle
	changed.platform.SHA256 = fmt.Sprintf("%064d", 1)
	require.ErrorContains(t, changed.verify(ctx), "runtime-digest-mismatch")
	require.ErrorContains(t, changed.extract(ctx, dest), "runtime-digest-mismatch")
	require.NoDirExists(t, dest)
	require.ErrorContains(t, bundle.extract(ctx, filepath.Join(dest, "missing-parent")), "could not stage")
	writeFile(t, dest, "previous destination")
	require.ErrorContains(t, bundle.extract(ctx, dest), "could not publish")
	content, err := os.ReadFile(dest)
	require.NoError(t, err)
	require.Equal(t, "previous destination", string(content))
	entries, err := os.ReadDir(base)
	require.NoError(t, err)
	require.Len(t, entries, 2)
}

func TestHandoffRuntimeArchiveRejectsConflictingMembers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		members []tar.Header
		reason  string
	}{
		{"duplicate", []tar.Header{{Name: "a"}, {Name: "./a"}}, "duplicate archive entry"},
		{"missing-hardlink", []tar.Header{{Name: "a", Typeflag: tar.TypeLink, Linkname: "absent"}}, "does not target an archived regular file"},
		{"link-cycle", []tar.Header{{Name: "a", Typeflag: tar.TypeSymlink, Linkname: "b"}, {Name: "b", Typeflag: tar.TypeSymlink, Linkname: "a"}}, "does not resolve inside the archive"},
		{"file-parent", []tar.Header{{Name: "a"}, {Name: "a/b"}}, "file exists"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var archive bytes.Buffer
			zw := gzip.NewWriter(&archive)
			tw := tar.NewWriter(zw)
			for _, member := range tc.members {
				require.NoError(t, tw.WriteHeader(&member))
			}
			require.NoError(t, tw.Close())
			require.NoError(t, zw.Close())
			root, err := os.OpenRoot(t.TempDir())
			require.NoError(t, err)
			defer root.Close()
			require.ErrorContains(t, extractHandoffRuntime(root, &archive), tc.reason)
		})
	}
}

func TestHandoffRuntimeRejectsUnsafeStoreEntries(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	bundle := newHandoffRuntime(base, checkset.SnapshotPlatform{Bundle: "demo", SHA256: fmt.Sprintf("%064d", 1)}, "")
	require.ErrorContains(t, bundle.verify(ctx), "runtime store unavailable")
	require.NoError(t, os.MkdirAll(bundle.archive, 0o700))
	require.ErrorContains(t, bundle.verify(ctx), "not a regular file")
	require.NoError(t, os.Remove(bundle.archive))
	source := filepath.Join(base, "elsewhere")
	writeFile(t, source, "archive bytes")
	require.NoError(t, os.Symlink(source, bundle.archive))
	require.ErrorContains(t, bundle.verify(ctx), "cannot safely open")
	require.NoError(t, os.Remove(bundle.archive))
	writeFile(t, bundle.archive, "not gzip")
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorContains(t, bundle.verify(canceled), "runtime verification interrupted")
	require.ErrorContains(t, bundle.extract(canceled, filepath.Join(base, "runtime")), "runtime extraction interrupted")
	require.ErrorContains(t, bundle.extract(ctx, filepath.Join(base, "runtime")), "runtime-invalid")
	require.NoDirExists(t, filepath.Join(base, "runtime"))
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
