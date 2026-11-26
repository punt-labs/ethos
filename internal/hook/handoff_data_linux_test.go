package hook

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/stretchr/testify/require"
)

func TestHandoffWorktreeDataMustBeRefreshed(t *testing.T) {
	root, run := handoffMetadataFixture(t, "exit 97\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml"), `checks:
  metadata:
    profile: linux-worktree-v1
    epoch: data-v1
    sources: {mode: worktree}
    data: [{name: fixture, url: 'https://example.com/data', max_age_hours: 1, mount: fixture}]
`)
	feedback := run()
	skipIfHandoffSandboxUnavailable(t, errors.New(feedback.Blocking))
	require.Contains(t, feedback.Blocking, "data-unavailable")
	require.Contains(t, feedback.Notice, "ethos handoff refresh-data")
	require.Contains(t, feedback.Notice, "Run the explicit project gate")
}

func TestHandoffWorktreeDataMountAndReuse(t *testing.T) {
	root, run := handoffMetadataFixture(t, `set -eu
test -s /runtime/data/input/value
if touch /runtime/data/writable 2>/dev/null; then exit 19; fi
test "$(cat /runtime/data/input/value)" = ready
`)
	writeFile(t, filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml"), `checks:
  metadata:
    profile: linux-worktree-v1
    epoch: data-v1
    sources: {mode: worktree}
    data: [{name: fixture, url: 'https://example.com/data', max_age_hours: 1, mount: input/value}]
`)
	ctx := context.Background()
	loc := handoffDataFixtureLocation(t, root)
	policy, err := loc.Layout()
	require.NoError(t, err)
	refresh := func(contents string) {
		t.Helper()
		client := &http.Client{Transport: runtimeRoundTrip(func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "https://example.com/data", req.URL.String())
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(contents))}, nil
		})}
		names, err := refreshHandoffData(ctx, loc, "", client)
		require.NoError(t, err)
		require.Equal(t, []string{"fixture"}, names)
	}
	refresh("ready")
	pass := run()
	skipIfHandoffSandboxUnavailable(t, errors.New(pass.Blocking))
	require.Equal(t, StageFeedback{}, pass)
	require.Equal(t, StageFeedback{}, run())
	refresh("changed")
	require.Contains(t, run().Blocking, "metadata: exit status 1")

	stored, err := filepath.Glob(filepath.Join(policy.LocalRoot(), "handoff-data-v1", "*"))
	require.NoError(t, err)
	require.Len(t, stored, 1)
	record, err := os.ReadFile(stored[0])
	require.NoError(t, err)
	binary.BigEndian.PutUint64(record[:8], uint64(time.Now().Add(-2*time.Hour).Unix()))
	require.NoError(t, os.WriteFile(stored[0], record, 0o600))
	require.Contains(t, run().Blocking, "data-unavailable")

	refresh("ready")
	f, err := os.OpenFile(stored[0], os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteAt([]byte("corrupt"), handoffDataHeader)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	checkPolicy(t, root, "project", "  - name: metadata\n    stage: handoff\n    files: all\n    on_fail: advisory\n    run: [sh, gate.sh]\n")
	advisory := run()
	require.Empty(t, advisory.Blocking)
	require.Contains(t, advisory.Advisory, "data-unavailable")
	require.Contains(t, advisory.Advisory, "ethos handoff refresh-data")
	slots, err := filepath.Glob(filepath.Join(policy.LocalRoot(), "handoff-v1", "*", "*.json"))
	require.NoError(t, err)
	require.Len(t, slots, 2, "only the two gate verdicts may be cached")
}

func TestHandoffRefreshGovulndb(t *testing.T) {
	root, run := handoffMetadataFixture(t, `set -eu
test "$(cat /runtime/data/govulndb/index/db.json)" = '{}'
test -f /runtime/data/govulndb/ID/GO-2000-0001.json
`)
	writeFile(t, filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml"), `checks:
  metadata:
    profile: linux-worktree-v1
    epoch: data-v1
    sources: {mode: worktree}
    data: [{name: govulndb, url: 'https://vuln.go.dev', max_age_hours: 24, mount: govulndb}]
`)
	loc := handoffDataFixtureLocation(t, root)
	refresh := func(index string) error {
		t.Helper()
		var content bytes.Buffer
		z := zip.NewWriter(&content)
		for _, name := range []string{"index/db.json", "index/modules.json", "index/vulns.json", "ID/GO-2000-0001.json"} {
			entry, err := z.Create(name)
			require.NoError(t, err)
			_, err = io.WriteString(entry, index)
			require.NoError(t, err)
		}
		require.NoError(t, z.Close())
		client := &http.Client{Transport: runtimeRoundTrip(func(req *http.Request) (*http.Response, error) {
			require.Equal(t, "https://vuln.go.dev/vulndb.zip", req.URL.String())
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(&content)}, nil
		})}
		_, err := refreshHandoffData(context.Background(), loc, "", client)
		return err
	}
	require.NoError(t, refresh("{}"))
	require.ErrorContains(t, refresh("not JSON"), "invalid database JSON")
	pass := run()
	skipIfHandoffSandboxUnavailable(t, errors.New(pass.Blocking))
	require.Equal(t, StageFeedback{}, pass)
}

func TestHandoffRefreshDataRejectsFailedDownloads(t *testing.T) {
	root, _ := handoffMetadataFixture(t, "exit 97\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml"), `checks:
  metadata:
    profile: linux-worktree-v1
    epoch: data-v1
    sources: {mode: worktree}
    data: [{name: fixture, url: 'https://example.com/data', max_age_hours: 1, mount: fixture}]
`)
	loc := handoffDataFixtureLocation(t, root)
	policy, err := loc.Layout()
	require.NoError(t, err)
	for _, tc := range []struct {
		name, want string
		resp       *http.Response
		err        error
	}{
		{name: "transport", want: `cannot fetch handoff data "fixture"`, err: io.ErrUnexpectedEOF},
		{name: "not found", want: "HTTP 404", resp: &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody}},
		{name: "empty body", want: "within the download budget", resp: &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: runtimeRoundTrip(func(*http.Request) (*http.Response, error) {
				return tc.resp, tc.err
			})}
			names, err := refreshHandoffData(context.Background(), loc, "", client)
			require.ErrorContains(t, err, tc.want)
			require.Empty(t, names)
			records, err := filepath.Glob(filepath.Join(policy.LocalRoot(), "handoff-data-v1", "*"))
			require.NoError(t, err)
			require.Empty(t, records)
		})
	}
}

func handoffDataFixtureLocation(t *testing.T, root string) resolve.Located {
	t.Helper()
	store, err := resolve.StoreOf(root)
	require.NoError(t, err)
	cfg, err := resolve.ConfigOf(store)
	require.NoError(t, err)
	return resolve.Located{Checkout: root, Store: store, Ethos: filepath.Join(store, ".punt-labs", "ethos"), Config: cfg}
}
