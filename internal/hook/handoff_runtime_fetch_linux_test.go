package hook

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/stretchr/testify/require"
)

func TestFetchHandoffRuntimesSelectsPinnedPlatform(t *testing.T) {
	root, _ := postEditFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(root, ".punt-labs/ethos")
	loc := resolve.Located{Checkout: root, Store: root, Ethos: repo, Config: &resolve.RepoConfig{Checks: "project"}}
	ctx := context.Background()
	requireInvalid := func(agent, message string) {
		t.Helper()
		paths, err := FetchHandoffRuntimes(ctx, loc, agent)
		require.ErrorContains(t, err, message)
		require.Empty(t, paths)
	}
	const gate = "    stage: handoff\n    files: all\n    run: [make, check]\n"
	checkPolicy(t, root, "project", "  - name: first\n"+gate+"  - name: second\n"+gate)
	requireInvalid("", "declares no runtime bundles")
	requireInvalid("invalid/agent", "slug")
	broken := loc
	broken.StoreErr = errors.New("store unavailable")
	_, err := FetchHandoffRuntimes(ctx, broken, "")
	require.ErrorIs(t, err, broken.StoreErr)
	sidecar := filepath.Join(repo, "checks/snapshots/project.yaml")
	writeFile(t, sidecar, "checks: [\n")
	requireInvalid("", "invalid snapshot sidecar")

	const archive = "published runtime bytes"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(archive)))
	profile := func(platform string) string {
		return fmt.Sprintf("    profile: linux-worktree-v1\n    epoch: test\n    sources: {mode: worktree}\n"+
			"    runtime: {platforms: {%s: {bundle: demo, sha256: %q}}}\n", platform, digest)
	}
	writeFile(t, sidecar, "checks:\n  first:\n"+profile("linux-ppc64le"))
	if runtime.GOARCH != "ppc64le" {
		requireInvalid("", "has no pinned bundle")
	}
	selected := profile(runtime.GOOS + "-" + runtime.GOARCH)
	writeFile(t, sidecar, "checks:\n  first:\n"+selected+"  second:\n"+selected)
	requireInvalid("worker", "--agent=worker")
	path := filepath.Join(home, ".punt-labs/ethos/runtimes", digest+".tar.gz")
	writeFile(t, path, archive)
	paths, err := FetchHandoffRuntimes(ctx, loc, "worker")
	require.NoError(t, err)
	require.Equal(t, []string{path}, paths)
}

func TestHandoffRuntimeFetchOnlyPublishesCompleteVerifiedBytes(t *testing.T) {
	const archive = "published runtime bytes"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/bundle":
			if req.Header.Get("Accept-Encoding") != "identity" {
				http.Error(w, "identity encoding required", http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, archive)
		case "/wrong":
			_, _ = io.WriteString(w, "wrong bytes")
		case "/short":
			w.Header().Set("Content-Length", "100")
			_, _ = io.WriteString(w, archive)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(server.Close)
	ctx := context.Background()
	bundle := newHandoffRuntime(t.TempDir(), checkset.SnapshotPlatform{
		Bundle: "demo", SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(archive))),
	}, "")
	writeFile(t, bundle.archive, "previous corrupt archive")
	for _, tc := range []struct{ path, reason string }{
		{"/missing", "HTTP 404"},
		{"/wrong", "runtime-digest-mismatch"},
		{"/short", "could not read published runtime bundle"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			bundle.platform.URL = server.URL + tc.path
			require.ErrorContains(t, bundle.fetch(ctx, server.Client()), tc.reason)
			body, err := os.ReadFile(bundle.archive)
			require.NoError(t, err)
			require.Equal(t, "previous corrupt archive", string(body))
			entries, err := os.ReadDir(filepath.Dir(bundle.archive))
			require.NoError(t, err)
			require.Len(t, entries, 1)
		})
	}
	bundle.platform.URL = server.URL + "/bundle"
	blocked := newHandoffRuntime(t.TempDir(), bundle.platform, "")
	writeFile(t, filepath.Dir(blocked.archive), "not a store directory")
	require.Error(t, blocked.fetch(ctx, server.Client()))
	require.NoError(t, bundle.fetch(ctx, server.Client()))
	bytes, err := os.ReadFile(bundle.archive)
	require.NoError(t, err)
	require.Equal(t, archive, string(bytes))
	server.Close()
	require.NoError(t, bundle.fetch(ctx, server.Client()))
	require.NoError(t, os.Remove(bundle.archive))
	require.ErrorContains(t, bundle.fetch(ctx, server.Client()), "could not fetch published runtime bundle")
	require.NoFileExists(t, bundle.archive)
}
