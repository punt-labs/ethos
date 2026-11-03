package audit

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/redact"
)

const sessionsIgnore = ".punt-labs/ethos/sessions/\n"

// captureIgnoreNote routes the once-per-process ignore note to a buffer and
// re-arms it for this test.
func captureIgnoreNote(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldOut := ignoreNoteOut
	ignoreNoteOut, ignoreNote = &buf, sync.Once{}
	t.Cleanup(func() { ignoreNoteOut, ignoreNote = oldOut, sync.Once{} })
	return &buf
}

// ignoringRepo is a repo whose .gitignore ignores the sessions tree, as biff,
// mcp-proxy and quarry do; it returns the repo and its sealed session dir.
func ignoringRepo(t *testing.T, ignore string) (repo, sealedDir string) {
	t.Helper()
	repo = t.TempDir()
	initGitRepo(t, repo)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(ignore), 0o600); err != nil {
		t.Fatal(err)
	}
	return repo, filepath.Join(repo, ".punt-labs", "ethos", "sessions", "2026-01-01-s1")
}

func TestGitAdd_IgnoredPathIsSkippedNotAnError(t *testing.T) {
	note := captureIgnoreNote(t)
	repo, dir := ignoringRepo(t, sessionsIgnore)
	writeChunk(t, dir, SessionChunkFile(100, 200), 100, 200)
	other := filepath.Join(repo, "other.txt")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	staged, err := GitAdd(repo, filepath.Join(dir, SessionChunkFile(100, 200)), other)
	if err != nil {
		t.Fatalf("GitAdd with an ignored path: %v", err)
	}
	if len(staged) != 1 || staged[0] != other {
		t.Errorf("staged = %v, want only %s", staged, other)
	}
	tracked := runGit(t, repo, "ls-files")
	if strings.Contains(tracked, "sessions") {
		t.Errorf("ignored chunk was staged: %q", tracked)
	}
	if !strings.Contains(tracked, "other.txt") {
		t.Errorf("non-ignored path was not staged: %q", tracked)
	}
	if !strings.Contains(note.String(), "gitignores") {
		t.Errorf("no ignore note, got %q", note.String())
	}
}

func TestGitAdd_NoteIsPrintedOncePerProcess(t *testing.T) {
	note := captureIgnoreNote(t)
	repo, dir := ignoringRepo(t, sessionsIgnore)
	writeChunk(t, dir, SessionChunkFile(100, 200), 100, 200)
	writeChunk(t, dir, SessionChunkFile(201, 300), 201, 300)
	for _, n := range []string{SessionChunkFile(100, 200), SessionChunkFile(201, 300)} {
		if _, err := GitAdd(repo, filepath.Join(dir, n)); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Count(note.String(), "\n"); got != 1 {
		t.Errorf("ignore note lines = %d, want 1: %q", got, note.String())
	}
}

func TestGitAdd_NotIgnoredStagesAndStaysSilent(t *testing.T) {
	note := captureIgnoreNote(t)
	repo, dir := ignoringRepo(t, "unrelated/\n")
	writeChunk(t, dir, SessionChunkFile(100, 200), 100, 200)
	if _, err := GitAdd(repo, filepath.Join(dir, SessionChunkFile(100, 200))); err != nil {
		t.Fatalf("GitAdd: %v", err)
	}
	if !strings.Contains(runGit(t, repo, "ls-files"), "sessions/2026-01-01-s1/") {
		t.Error("chunk not staged in a repo that does not ignore sessions")
	}
	if note.Len() != 0 {
		t.Errorf("unexpected note: %q", note.String())
	}
}

func TestGitAdd_IgnoreCheckFailureIsAnError(t *testing.T) {
	captureIgnoreNote(t)
	dir := t.TempDir() // not a repository
	if _, err := GitAdd(dir, "a"); err == nil {
		t.Fatal("GitAdd outside a repository succeeded")
	}
}

// TestQuarantineInRepoThatIgnoresSessions: withholding a leaking chunk ends in
// a git add of the record. The repo ignores it, so the add is skipped: the
// quarantine completes, the original stays in the local zone, the record is on
// disk, and nothing under sessions is staged.
func TestQuarantineInRepoThatIgnoresSessions(t *testing.T) {
	captureIgnoreNote(t)
	repo, sealedDir := ignoringRepo(t, sessionsIgnore)
	name := SessionChunkFile(100, 300)
	if err := os.MkdirAll(sealedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sealedDir, name), leakyChunkBody(), 0o600); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(repo, "live.jsonl")
	writeLive(t, live, 100, 200, 300)

	cn, _ := Classify(name, SessionNS)
	if _, err := Quarantine(repo, sealedDir, cn, live, "ts mismatch", redact.Prose, keepLine, homeCheck); err != nil {
		t.Fatalf("Quarantine in a repo that ignores sessions: %v", err)
	}
	rec, err := os.ReadFile(filepath.Join(sealedDir, name+".corrupt"))
	if err != nil {
		t.Fatalf("record missing on disk: %v", err)
	}
	if bytes.Contains(rec, []byte("/home/bob")) {
		t.Errorf("record keeps the home: %q", rec)
	}
	if _, err := os.Stat(filepath.Join(sealedDir, cn.MarkerFile())); err != nil {
		t.Errorf("marker missing on disk: %v", err)
	}
	local, err := os.ReadDir(QuarantineDir(repo))
	if err != nil || len(local) == 0 {
		t.Errorf("original not kept in the local zone: %v", err)
	}
	if tracked := runGit(t, repo, "ls-files"); strings.Contains(tracked, "sessions") {
		t.Errorf("ignored artifacts were staged: %q", tracked)
	}
}

func TestIsIgnored(t *testing.T) {
	repo, dir := ignoringRepo(t, sessionsIgnore)
	cases := []struct {
		path string
		want bool
	}{
		{filepath.Join(dir, SessionChunkFile(1, 2)), true}, // need not exist
		{filepath.Join(repo, "other.txt"), false},
	}
	for _, c := range cases {
		got, err := IsIgnored(repo, c.path)
		if err != nil || got != c.want {
			t.Errorf("IsIgnored(%s) = %v, %v; want %v", c.path, got, err, c.want)
		}
	}
	if _, err := IsIgnored(t.TempDir(), "a"); err == nil {
		t.Error("IsIgnored outside a repository: want an error")
	}
}
