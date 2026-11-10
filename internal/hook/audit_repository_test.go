package hook

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/stretchr/testify/require"
)

func policyGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func TestRepositoryConcurrentSiblingAppendAndLocalSeal(t *testing.T) {
	root, sibling := t.TempDir(), t.TempDir()
	policyGit(t, root, "init", "-q")
	blame := hookPolicy(t, root, root, layout.RecordBlame)
	full := hookPolicy(t, root, sibling, layout.RecordFull)
	errors := make(chan error, 5)
	var wg sync.WaitGroup
	for worker := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := blame
			if worker%2 == 0 {
				p = full
			}
			for range 8 {
				input := `{"session_id":"s","tool_name":"Read","delegation_id":"d","tool_input":{"file_path":"example.go"}}`
				if err := HandleAuditLogWithPolicy(strings.NewReader(input), p, ""); err != nil {
					errors <- err
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 8 {
			if _, err := SealRepoWithPolicy(blame, time.Now(), SealOptions{}); err != nil {
				errors <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	_, err := SealRepoWithPolicy(blame, time.Now(), SealOptions{})
	require.NoError(t, err)
	data, err := audit.ReadStream(blame, audit.Stream{Namespace: audit.SessionNS, Session: "s"})
	require.NoError(t, err)
	require.Len(t, data.Lines, 32)
	for i := 1; i < len(data.Lines); i++ {
		require.Greater(t, data.Lines[i].TS, data.Lines[i-1].TS)
	}
	// A later full operation publishes only the 16 writes made under full
	// consent, regardless of the mode of the racing archival operations.
	p := hookPolicy(t, root, root, layout.RecordFull)
	_, err = SealRepoWithPolicy(p, time.Now(), SealOptions{})
	require.NoError(t, err)
	paths, err := filepath.Glob(filepath.Join(p.RecordRoot(), "sessions", "*", "audit-*.jsonl"))
	require.NoError(t, err)
	count := 0
	for _, path := range paths {
		body, err := os.ReadFile(path)
		require.NoError(t, err)
		for _, raw := range audit.SplitLines(body) {
			require.True(t, audit.Eligible(audit.Stream{}, audit.Line{Raw: raw}))
			count++
		}
	}
	require.Equal(t, 16, count)
}

func TestRepositoryLegacyMigrationRetainsLateWriterAndPrivateMultiset(t *testing.T) {
	root := t.TempDir()
	p := hookPolicy(t, root, root, layout.RecordFull)
	global := t.TempDir()
	dir := repoSessionDir(t, root, "2026-11-04", "s")
	legacy := filepath.Join(global, "s.audit.jsonl")
	loss := `{"ts":"2026-11-04T10:00:00Z","session":"s","delegation_id":"d","audit_error":"lost","future":9007199254740993}` + "\n"
	hashed := `{"ts":"2026-11-04T10:00:00Z","session":"s","delegation_id":"d","tool":"Read","tool_input_hash":"h1","record":"full"}` + "\n"
	require.NoError(t, os.WriteFile(legacy, []byte(loss+loss+hashed), 0o600))
	// A pre-existing frozen record mirror must consolidate locally even when
	// migration has no new lines to add. Neither copy grants publication consent.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "audit.jsonl"), []byte(loss+loss+hashed), 0o600))
	w, err := os.OpenFile(legacy, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	var out bytes.Buffer
	require.NoError(t, MigrateAuditWithPolicy(p, global, false, &out))
	localDir, err := audit.ArchiveDir(p, audit.Stream{Namespace: audit.SessionNS, Session: "s"}, "2026-11-04")
	require.NoError(t, err)
	b, err := os.ReadFile(filepath.Join(localDir, "audit.jsonl"))
	require.NoError(t, err)
	require.Contains(t, string(b), "9007199254740993")
	_, err = w.WriteString(loss)
	require.NoError(t, err)
	require.NoError(t, w.Sync())
	for range 2 {
		require.NoError(t, MigrateAuditWithPolicy(p, global, false, &out))
	}
	entries, err := QueryAuditByDelegationWithPolicy(p, global, "d")
	require.NoError(t, err)
	require.Len(t, entries, 4, "three identical losses and one hashed call survive mirror/retry")
	// Full mode does not publish or consult git for frozen legacy, including
	// a legacy payload with a record field it cannot authenticate as prospective.
	t.Setenv("PATH", t.TempDir())
	res, err := SealRepoWithPolicy(p, time.Now(), SealOptions{})
	require.NoError(t, err)
	require.Zero(t, res.ChunksStaged)
	record, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	require.NoError(t, err)
	require.Equal(t, loss+loss+hashed, string(record))
	original, err := os.ReadFile(legacy)
	require.NoError(t, err)
	require.Equal(t, loss+loss+hashed+loss, string(original))
}

func hookPolicy(t *testing.T, root, checkout string, mode layout.RecordMode) layout.Policy {
	t.Helper()
	p, err := layout.NewRepository(root, checkout, mode)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func policyAppend(t *testing.T, p layout.Policy, tool string) {
	t.Helper()
	input := `{"session_id":"s","tool_name":"` + tool + `","delegation_id":"d","tool_input":{"file_path":"example.go"}}`
	if err := HandleAuditLogWithPolicy(strings.NewReader(input), p, ""); err != nil {
		t.Fatal(err)
	}
}

func TestRepositorySealBlameDoesNotUseGitIndex(t *testing.T) {
	root := t.TempDir()
	policyGit(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "source"), []byte("source staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	policyGit(t, root, "add", "source")
	index := filepath.Join(root, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index+".lock", []byte("foreign lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := hookPolicy(t, root, root, layout.RecordBlame)
	policyAppend(t, p, "Read")
	// A PATH containing no git proves default archival cannot even issue an
	// index/status/ignore query. Already-resolved policy is the operation input.
	t.Setenv("PATH", t.TempDir())
	res, err := SealRepoWithPolicy(p, time.Now(), SealOptions{})
	if err != nil || res.LinesSealed != 1 || res.ChunksStaged != 0 {
		t.Fatalf("seal=%+v %v", res, err)
	}
	after, err := os.ReadFile(index)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("source index changed: %v", err)
	}
	if _, err := os.Stat(p.RecordRoot()); !os.IsNotExist(err) {
		t.Fatalf("blame seal created record tree: %v", err)
	}
	s := audit.Stream{Namespace: audit.SessionNS, Session: "s"}
	if n, err := audit.UnarchivedCount(p, s); err != nil || n != 0 {
		t.Fatalf("unarchived=%d %v", n, err)
	}
	if _, err := os.Stat(s.LivePath(p)); err != nil {
		t.Fatalf("live payload not retained: %v", err)
	}
}

func TestRepositoryBlameArchivesHealthyStreamDespiteHistoricalError(t *testing.T) {
	root := t.TempDir()
	p := hookPolicy(t, root, root, layout.RecordBlame)
	bad := audit.Stream{Namespace: audit.SessionNS, Session: "a-old"}
	dir, err := audit.ArchiveDir(p, bad, "2026-11-04")
	require.NoError(t, err)
	require.NoError(t, audit.WriteLocalAtomic(p, filepath.Join(dir, audit.SessionChunkFile(100, 100)), []byte("historical corruption\n")))
	input := `{"session_id":"z-new","tool_name":"Read","delegation_id":"d","tool_input":{"file_path":"example.go"}}`
	require.NoError(t, HandleAuditLogWithPolicy(strings.NewReader(input), p, ""))
	t.Setenv("PATH", t.TempDir())
	res, err := SealRepoWithPolicy(p, time.Now(), SealOptions{})
	require.ErrorContains(t, err, "a-old", "manual command still reports damaged history")
	require.Equal(t, 1, res.LinesSealed, "independent healthy stream was not skipped")
	healthy := audit.Stream{Namespace: audit.SessionNS, Session: "z-new"}
	n, err := audit.UnarchivedCount(p, healthy)
	require.NoError(t, err)
	require.Zero(t, n)
	require.FileExists(t, healthy.LivePath(p))
}

func TestRepositorySealMixedEligibilityAndModeSwitch(t *testing.T) {
	root := t.TempDir()
	policyGit(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "source"), []byte("source staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	policyGit(t, root, "add", "source")
	blame := hookPolicy(t, root, root, layout.RecordBlame)
	full := hookPolicy(t, root, root, layout.RecordFull)
	policyAppend(t, blame, "Read")
	policyAppend(t, full, "Edit")
	policyAppend(t, blame, "Write")
	res, err := SealRepoWithPolicy(full, time.Now(), SealOptions{})
	if err != nil || res.LinesSealed != 3 || res.ChunksStaged != 1 {
		t.Fatalf("seal=%+v %v", res, err)
	}
	staged := policyGit(t, root, "diff", "--cached", "--name-only")
	if !strings.Contains(staged, "source") || !strings.Contains(staged, ".punt-labs/ethos/sessions/") || strings.Contains(staged, "local/") {
		t.Fatalf("staged selection: %s", staged)
	}
	files, err := filepath.Glob(filepath.Join(full.CheckoutRecordRoot(), "sessions", "*", "audit-*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("published=%v %v", files, err)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(`"Read"`)) || bytes.Contains(b, []byte(`"Write"`)) || !bytes.Contains(b, []byte(`"Edit"`)) {
		t.Fatalf("private prefix/suffix published: %s", b)
	}
	entries, err := QueryAuditByDelegationWithPolicy(blame, "", "d")
	if err != nil || len(entries) != 3 {
		t.Fatalf("mode-independent query=%d %v", len(entries), err)
	}
	if res, err := SealRepoWithPolicy(blame, time.Now(), SealOptions{}); err != nil || res.LinesSealed != 0 || res.ChunksStaged != 0 {
		t.Fatalf("blame retry=%+v %v", res, err)
	}
}

func TestRepositoryFullSealFailureRetainsArchiveForLaterFullRetry(t *testing.T) {
	root := t.TempDir()
	policyGit(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "source"), []byte("source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	policyGit(t, root, "add", "source")
	index := filepath.Join(root, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	p := hookPolicy(t, root, root, layout.RecordFull)
	policyAppend(t, p, "Read")
	if err := os.WriteFile(index+".lock", []byte("foreign lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SealRepoWithPolicy(p, time.Now(), SealOptions{}); err == nil {
		t.Fatal("full staging failure hidden")
	}
	after, err := os.ReadFile(index)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed full seal changed source index: %v", err)
	}
	if n, err := audit.UnarchivedCount(p, audit.Stream{Namespace: audit.SessionNS, Session: "s"}); err != nil || n != 0 {
		t.Fatalf("archive rolled back after publication failure: %d %v", n, err)
	}
	if err := os.Remove(index + ".lock"); err != nil {
		t.Fatal(err)
	}
	blame := hookPolicy(t, root, root, layout.RecordBlame)
	if _, err := SealRepoWithPolicy(blame, time.Now(), SealOptions{}); err != nil {
		t.Fatal(err)
	}
	after, err = os.ReadFile(index)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("switching to blame retried publication")
	}
	if res, err := SealRepoWithPolicy(p, time.Now(), SealOptions{}); err != nil || res.LinesSealed != 0 || res.ChunksStaged != 1 {
		t.Fatalf("full retry=%+v %v", res, err)
	}
}

func TestRepositoryLiveSurvivesLinkedCheckoutRemoval(t *testing.T) {
	root := t.TempDir()
	policyGit(t, root, "init", "-q")
	policyGit(t, root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "fixture")
	checkout := filepath.Join(t.TempDir(), "linked")
	policyGit(t, root, "worktree", "add", "--detach", checkout)
	p := hookPolicy(t, root, checkout, layout.RecordBlame)
	policyAppend(t, p, "Read")
	policyGit(t, root, "worktree", "remove", checkout)
	if _, err := os.Stat((audit.Stream{Namespace: audit.SessionNS, Session: "s"}).LivePath(p)); err != nil {
		t.Fatalf("checkout deletion lost live data: %v", err)
	}
	main := hookPolicy(t, root, root, layout.RecordBlame)
	if res, err := SealRepoWithPolicy(main, time.Now(), SealOptions{}); err != nil || res.LinesSealed != 1 {
		t.Fatalf("survivor seal=%+v %v", res, err)
	}
}

func TestRepositoryQuarantineIsLocalAndResumable(t *testing.T) {
	root := t.TempDir()
	p := hookPolicy(t, root, root, layout.RecordBlame)
	policyAppend(t, p, "Read")
	if _, err := SealRepoWithPolicy(p, time.Now(), SealOptions{}); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(audit.ArchiveRoot(p), "sessions", "*", "audit-*.jsonl"))
	if len(paths) != 1 {
		t.Fatalf("archive=%v", paths)
	}
	if err := os.WriteFile(paths[0], []byte("raw corrupt bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SealRepoWithPolicy(p, time.Now(), SealOptions{}); err == nil {
		t.Fatal("manual corruption failure hidden")
	}
	if _, err := QueryAuditByDelegationWithPolicy(p, "", "d"); err == nil {
		t.Fatal("inspection corruption failure hidden")
	}
	marker, err := QuarantineChunkWithPolicy(p, paths[0], "fixture corruption")
	if err != nil || !marker.HasGap() {
		t.Fatalf("quarantine=%+v %v: the undecodable bytes are a recorded loss", marker, err)
	}
	if _, err := QuarantineChunkWithPolicy(p, paths[0], "fixture corruption"); err != nil {
		t.Fatal(err)
	}
	if entries, err := QueryAuditByDelegationWithPolicy(p, "", "d"); err != nil || len(entries) != 1 {
		t.Fatalf("recovered query=%d %v", len(entries), err)
	}
	if _, err := os.Stat(p.RecordRoot()); !os.IsNotExist(err) {
		t.Fatalf("quarantine created record tree: %v", err)
	}
}
