//go:build !windows

package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uiServer is a Server over temp roots with the real templates.
func uiServer(t *testing.T, repoRoot, storeRoot string) *Server {
	t.Helper()
	srv, err := NewServer(repoRoot, storeRoot)
	require.NoError(t, err)
	srv.globalRoot = t.TempDir()
	return srv
}

// fakeGitBody puts a git running body first on PATH (the -c options Read
// passes are dropped, as the real git consumes them).
func fakeGitBody(t *testing.T, body string) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\nwhile [ \"$1\" = -c ]; do shift 2; done\n" + body
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755))
	t.Setenv("PATH", bin)
}

// A file whose blame fails is still shown, with a notice on the page that says
// attribution is missing and why.
func TestBrowseFileShowsANoticeWhenBlameFails(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte("hello\n"), 0o644))
	fakeGitBody(t, "echo 'fatal: no such path a.txt in HEAD' >&2\nexit 128\n")
	srv := uiServer(t, repo, repo)

	rec := httptest.NewRecorder()
	testhelpers.CaptureStderr(t, func() {
		srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/browse/a.txt", nil))
	})
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "hello", "the file is still shown")
	assert.Contains(t, body, "git blame failed")
	assert.Contains(t, body, "no such path a.txt in HEAD")
}

// A commit whose lookup fails leaves its lines unattributed, and the page says
// how many commits could not be looked up and why.
func TestBrowseFileShowsANoticeWhenACommitLookupFails(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644))
	sha := strings.Repeat("a", 40)
	fakeGitBody(t, `case "$1" in
blame) printf '`+sha+` 1 1 1\nauthor A\n\tone\n' ;;
log) echo 'fatal: bad object' >&2; exit 128 ;;
esac
`)
	srv := uiServer(t, repo, repo)
	rec := httptest.NewRecorder()
	testhelpers.CaptureStderr(t, func() {
		srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/browse/a.txt", nil))
	})
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "could not be looked up")
	assert.Contains(t, body, "bad object")
}

// A git that answers without incident shows no notice.
func TestBrowseFileShowsNoNoticeWhenGitAnswers(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644))
	sha := strings.Repeat("b", 40)
	fakeGitBody(t, `case "$1" in
blame) printf '`+sha+` 1 1 1\nauthor A\n\tone\n' ;;
log) echo '' ;;
esac
`)
	srv := uiServer(t, repo, repo)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/browse/a.txt", nil))
	assert.NotContains(t, rec.Body.String(), "ethos-notice")
}

// missions.jsonl: a file that cannot be opened, a line that does not parse and
// a line longer than the scanner allows are each reported on the dashboard;
// a file that is simply not there yet is not.
func TestReadMissionsJSONLReportsWhatItCouldNotRead(t *testing.T) {
	store := t.TempDir()
	path := mission.RepoStatePath(store, "missions.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	srv := uiServer(t, store, store)

	t.Run("absent is not a problem", func(t *testing.T) {
		rows, notices := srv.readMissionsJSONL()
		assert.Empty(t, rows)
		assert.Empty(t, notices)
	})
	t.Run("bad line", func(t *testing.T) {
		good := `{"id":"m-1","status":"open"}`
		require.NoError(t, os.WriteFile(path, []byte(good+"\nnot json\n"+good+"\n"), 0o644))
		rows, notices := srv.readMissionsJSONL()
		assert.Len(t, rows, 2, "the good lines are kept")
		require.Len(t, notices, 1)
		assert.Contains(t, notices[0], "line 2")
	})
	t.Run("line too long", func(t *testing.T) {
		long := `{"id":"` + strings.Repeat("x", 2<<20) + `"}`
		require.NoError(t, os.WriteFile(path, []byte(`{"id":"m-1","status":"open"}`+"\n"+long+"\n"), 0o644))
		rows, notices := srv.readMissionsJSONL()
		assert.Len(t, rows, 1)
		require.NotEmpty(t, notices)
		assert.Contains(t, strings.Join(notices, " "), "after line 1")
	})
	t.Run("unreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads any file")
		}
		require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o644))
		require.NoError(t, os.Chmod(path, 0o000))
		t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
		rows, notices := srv.readMissionsJSONL()
		assert.Empty(t, rows)
		require.Len(t, notices, 1)
		assert.Contains(t, notices[0], "missions.jsonl")
	})
}

// The dashboard shows what readMissionsJSONL could not read.
func TestDashboardShowsMissionsJSONLNotices(t *testing.T) {
	store := t.TempDir()
	path := mission.RepoStatePath(store, "missions.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`{"id":"m-1","status":"open"}`+"\ngarbage\n"), 0o644))
	srv := uiServer(t, store, store)
	rec := httptest.NewRecorder()
	srv.handleDashboard(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "ethos-notice")
	assert.Contains(t, rec.Body.String(), "line 2")
}

// The UI's git runs in the located checkout in the Parsed environment: the C
// locale, and no GIT_DIR from the process that started it.
func TestServerGitUsesTheParsedEnvironment(t *testing.T) {
	repo := t.TempDir()
	fakeGitBody(t, `echo "$LC_ALL|$GIT_DIR|$(pwd -P)"`+"\n")
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("GIT_DIR", "/elsewhere/.git")
	srv := uiServer(t, repo, repo)
	out, err := srv.git("status")
	require.NoError(t, err)
	real, rerr := filepath.EvalSymlinks(repo)
	require.NoError(t, rerr)
	assert.Equal(t, "C||"+real+"\n", string(out))
}

// writeDelegation writes a delegation record (and optionally a prompt).
func writeDelegation(t *testing.T, store, missionID, id, record string) string {
	t.Helper()
	dir := mission.RepoStatePath(store, "missions", missionID, "delegations", id)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "record.yaml"), []byte(record), 0o644))
	return dir
}

const goodRecord = "id: d-1\ntier: B\nagent_type: bwk\ncreated_at: 2026-07-24T00:00:00Z\nverdict: pass\n"

// A mission page says which delegation records it could not read, and which
// audit queries failed.
func TestMissionPageShowsNoticesForUnreadableDelegationsAndAudit(t *testing.T) {
	store := t.TempDir()
	id := "m-2026-07-24-120"
	require.NoError(t, mission.NewStoreWithRoots(store, t.TempDir()).WithCheckoutRoot(store).Create(uiTestContract(id)))
	writeDelegation(t, store, id, "d-1", goodRecord)
	writeDelegation(t, store, id, "d-bad", "id: [unclosed\n")
	srv := uiServer(t, store, store)
	notAFile := filepath.Join(t.TempDir(), "sessions")
	require.NoError(t, os.WriteFile(notAFile, []byte("x"), 0o644))
	srv.globalSessionsDir = notAFile // the legacy audit query cannot read it

	rec := httptest.NewRecorder()
	srv.handleMission(rec, httptest.NewRequest(http.MethodGet, "/missions/"+id, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "ethos-notice")
	assert.Contains(t, body, "delegation d-bad")
	assert.Contains(t, body, "audit entries for delegation d-1 could not be queried")
}

// A delegation page says its prompt could not be read, and that the audit
// query failed; a prompt that was simply never recorded is not a notice.
func TestDelegationPageShowsNotices(t *testing.T) {
	store := t.TempDir()
	id := "m-2026-07-24-121"
	dir := writeDelegation(t, store, id, "d-1", goodRecord)
	srv := uiServer(t, store, store)
	get := func() string {
		rec := httptest.NewRecorder()
		srv.handleDelegation(rec, httptest.NewRequest(http.MethodGet, "/delegations/"+id+"/d-1", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		return rec.Body.String()
	}

	assert.NotContains(t, get(), "ethos-notice", "no prompt recorded, no audit trouble: nothing to say")

	if os.Geteuid() != 0 {
		prompt := filepath.Join(dir, "prompt.md")
		require.NoError(t, os.WriteFile(prompt, []byte("p"), 0o644))
		require.NoError(t, os.Chmod(prompt, 0o000))
		t.Cleanup(func() { _ = os.Chmod(prompt, 0o644) })
		assert.Contains(t, get(), "the prompt of d-1 could not be read")
		require.NoError(t, os.Chmod(prompt, 0o644))
	}

	notAFile := filepath.Join(t.TempDir(), "sessions")
	require.NoError(t, os.WriteFile(notAFile, []byte("x"), 0o644))
	srv.globalSessionsDir = notAFile
	assert.Contains(t, get(), "audit entries for delegation d-1 could not be queried")
}

// The file fallback of the browser says when reading stopped early.
func TestBrowseFallbackReportsAScanError(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repo, "long.txt"), []byte("one\n"+strings.Repeat("x", 2<<20)+"\n"), 0o644))
	fakeGitBody(t, "echo 'fatal: no such path' >&2\nexit 128\n") // blame fails: the fallback runs
	srv := uiServer(t, repo, repo)
	rec := httptest.NewRecorder()
	testhelpers.CaptureStderr(t, func() {
		srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/browse/long.txt", nil))
	})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "reading long.txt stopped after line 1")
}
