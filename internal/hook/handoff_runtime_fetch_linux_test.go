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
	"strings"
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
	const secondURL = "https://github.com/example/project/releases/download/v1/runtime.tar.gz"
	withURL := strings.Replace(selected, "}}}", ", url: "+secondURL+"}}}", 1)
	writeFile(t, sidecar, "checks:\n  first:\n"+selected+"  second:\n"+withURL)
	bundles, err := handoffRuntimeBundles(loc, "worker")
	require.NoError(t, err)
	require.Len(t, bundles, 1)
	require.Equal(t, secondURL, bundles[0].platform.URL)

	writeFile(t, sidecar, "checks:\n  first:\n"+selected+"  second:\n"+selected)
	requireInvalid("worker", "--agent=worker")
	path := filepath.Join(home, ".punt-labs/ethos/runtimes", digest+".tar.gz")
	writeFile(t, path, archive)
	paths, err := FetchHandoffRuntimes(ctx, loc, "worker")
	require.NoError(t, err)
	require.Equal(t, []string{path}, paths)

	local := filepath.Join(home, "local.tar.gz")
	_, err = InstallHandoffRuntimeFile(ctx, loc, "bad/agent", local)
	require.ErrorContains(t, err, "slug")
	_, err = InstallHandoffRuntimeFile(ctx, loc, "worker", local)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = InstallHandoffRuntimeFile(ctx, loc, "worker", home)
	require.ErrorContains(t, err, "requires a regular runtime archive")
	writeFile(t, local, "wrong")
	_, err = InstallHandoffRuntimeFile(ctx, loc, "worker", local)
	require.ErrorContains(t, err, digest)
	require.ErrorContains(t, err, fmt.Sprintf("%x", sha256.Sum256([]byte("wrong"))))
	writeFile(t, local, archive)
	require.NoError(t, os.Remove(path))
	paths, err = InstallHandoffRuntimeFile(ctx, loc, "worker", local)
	require.NoError(t, err)
	require.Equal(t, []string{path}, paths)
	other := strings.Replace(selected, digest, fmt.Sprintf("%x", sha256.Sum256([]byte("other"))), 1)
	writeFile(t, sidecar, "checks:\n  first:\n"+selected+"  second:\n"+other)
	_, err = InstallHandoffRuntimeFile(ctx, loc, "worker", local)
	require.ErrorContains(t, err, "single distinct runtime digest")
}

type runtimeRoundTrip func(*http.Request) (*http.Response, error)

func (f runtimeRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestHandoffRuntimeFetchGitHubRelease(t *testing.T) {
	const (
		archive = "published runtime bytes"
		browser = "https://github.com/example/project/releases/download/test%2Fv1/demo.tar.gz"
		release = "https://api.github.com/repos/example/project/releases/tags/test%2Fv1"
		asset   = "https://api.github.com/repos/example/project/releases/assets/42"
		foreign = "https://foreign.api.github.com/asset"
	)
	for _, tc := range []struct{ name, gh, github, cli, want string }{
		{"GH_TOKEN first", "gh-example", "github-example", "cli-example", "gh-example"},
		{"GITHUB_TOKEN next", "", "github-example", "cli-example", "github-example"},
		{"gh fallback", "", "", "cli-example", "cli-example"},
		{"public without token", "", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GH_TOKEN", tc.gh)
			t.Setenv("GITHUB_TOKEN", tc.github)
			bin := t.TempDir()
			t.Setenv("PATH", bin)
			if tc.cli != "" {
				postEditTool(t, bin, "gh", "[ \"$*\" = 'auth token --hostname github.com' ] || exit 1\nprintf '%s\\n' "+tc.cli+"\n")
			}
			var visited []string
			client := handoffRuntimeHTTPClient()
			client.Transport = runtimeRoundTrip(func(req *http.Request) (*http.Response, error) {
				url := req.URL.String()
				visited = append(visited, url)
				h := make(http.Header)
				status, body := http.StatusOK, archive
				switch url {
				case release:
					require.Equal(t, "Bearer "+tc.want, req.Header.Get("Authorization"))
					require.Equal(t, "application/vnd.github+json", req.Header.Get("Accept"))
					body = `{"assets":[{"name":"other.tar.gz","url":"https://example.invalid/other"},{"name":"demo.tar.gz","url":"` + asset + `"}]}`
				case asset:
					require.Equal(t, "Bearer "+tc.want, req.Header.Get("Authorization"))
					require.Equal(t, "application/octet-stream", req.Header.Get("Accept"))
					status, body = http.StatusFound, ""
					h.Set("Location", foreign)
				case foreign, browser:
					require.Empty(t, req.Header.Get("Authorization"))
				default:
					t.Fatalf("unexpected runtime request: %s", url)
				}
				return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})
			bundle := newHandoffRuntime(t.TempDir(), checkset.SnapshotPlatform{
				Bundle: "demo", URL: browser, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(archive))),
			}, "")
			require.NoError(t, bundle.fetch(context.Background(), client))
			if tc.want == "" {
				require.Equal(t, []string{browser}, visited)
			} else {
				require.Equal(t, []string{release, asset, foreign}, visited)
			}
			bytes, err := os.ReadFile(bundle.archive)
			require.NoError(t, err)
			require.Equal(t, archive, string(bytes))
		})
	}
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

func TestHandoffRuntimeFetchRefusesLoopback(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "fixture runtime")
	}))
	defer server.Close()
	client := handoffRuntimeHTTPClient()
	transport, _ := client.Transport.(*http.Transport)
	if transport == nil {
		transport = http.DefaultTransport.(*http.Transport).Clone()
		client.Transport = transport
	}
	transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig
	bundle := newHandoffRuntime(t.TempDir(), checkset.SnapshotPlatform{
		Bundle: "fixture", SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("fixture runtime"))), URL: server.URL,
	}, "")
	require.ErrorContains(t, bundle.fetch(context.Background(), client), "could not fetch published runtime bundle")
	require.NoFileExists(t, bundle.archive)
}
