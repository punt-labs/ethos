package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

// A real failed fsync occurs after the policy appender's timestamp allocation,
// sink guard and write. An unwritable parent would fail too early to cover the
// noticeStoredLine ordering at the repository entry point.
func TestRepositoryAuditNoticeAfterConfirmedAppend(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("post-write syscall fault acceptance requires Linux strace")
	}
	strace, err := exec.LookPath("strace")
	if err != nil {
		t.Skip("post-write notice acceptance requires strace; not established")
	}
	for _, fail := range []bool{false, true} {
		name := "confirmed"
		if fail {
			name = "fsync failure after guarded write"
		}
		t.Run(name, func(t *testing.T) {
			root, home := t.TempDir(), t.TempDir()
			trace := filepath.Join(t.TempDir(), "syscalls")
			args := []string{"-f", "-yy", "-s", "8192", "-o", trace, "-e", "trace=write,fsync,ftruncate"}
			if fail {
				args = append(args, "-e", "inject=fsync:error=EIO")
			}
			args = append(args, os.Args[0], "-test.run=^TestRepositoryAuditNoticeProcess$", "-test.v")
			cmd := exec.Command(strace, args...)
			// TestMain builds a fixture CLI before m.Run. Keep its existing Go
			// caches, then isolate HOME inside the opted-in test itself.
			cmd.Env = append(os.Environ(), "ETHOS_NOTICE_TEST_HOME="+home, "ETHOS_NOTICE_TEST_ROOT="+root, "ETHOS_NOTICE_TEST_FAIL="+name)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			calls, err := os.ReadFile(trace)
			require.NoError(t, err)
			guardedWrite := false
			for _, line := range strings.Split(string(calls), "\n") {
				if strings.Contains(line, "write(") && strings.Contains(line, "s.audit.jsonl>") {
					guardedWrite = true
					assert.Contains(t, line, `\"record\":\"blame\"`, "actual guarded/stamped append reached the kernel")
					assert.NotContains(t, line, "/home/notice-fixture")
					t.Log(line)
				}
			}
			assert.True(t, guardedWrite, "fault must follow guarding, not prevent entry")
			if fail {
				assert.Contains(t, string(calls), "EIO (Input/output error) (INJECTED)")
				assert.Contains(t, string(calls), "ftruncate(", "failed append attempts rollback")
			}
			t.Logf("child assertions: %s", out)
		})
	}
}

// Only the parent above opts in; normal test runs do not spawn recursively.
func TestRepositoryAuditNoticeProcess(t *testing.T) {
	root := os.Getenv("ETHOS_NOTICE_TEST_ROOT")
	if root == "" {
		return
	}
	t.Setenv("HOME", os.Getenv("ETHOS_NOTICE_TEST_HOME"))
	p := hookPolicy(t, root, root, layout.RecordBlame)
	// An enrichment field reaches the sink guard unchanged by tool-input
	// redaction, so this tests the guard report as well as the initial rewrite.
	input := `{"session_id":"s","tool_name":"Read","agent_type":"/home/notice-fixture/private","tool_input":{"file_path":"` + filepath.Join(os.Getenv("HOME"), "private.txt") + `"}}`
	var appendErr error
	notice := testhelpers.CaptureStderr(t, func() {
		appendErr = HandleAuditLogWithPolicy(strings.NewReader(input), p, "")
	})
	live, err := os.ReadFile((audit.Stream{Namespace: audit.SessionNS, Session: "s"}).LivePath(p))
	require.NoError(t, err)
	if os.Getenv("ETHOS_NOTICE_TEST_FAIL") != "confirmed" {
		require.ErrorIs(t, appendErr, ErrAuditDropped)
		assert.Empty(t, live, "failed fsync truncates the unconfirmed append")
		assert.Contains(t, notice, "lost session=s")
		assertNoRepositoryStoredNotice(t, notice)
	} else {
		require.NoError(t, appendErr)
		assert.Equal(t, 1, strings.Count(notice, "was rewritten before it was stored"))
		assert.NotContains(t, string(live), "/home/notice-fixture")
		assert.NotContains(t, string(live), os.Getenv("HOME"))
		assert.Contains(t, string(live), `"record":"blame"`)
	}
}

func assertNoRepositoryStoredNotice(t *testing.T, notice string) {
	t.Helper()
	for _, phrase := range []string{"was rewritten", "was replaced by a loss marker", "rewritten before sealing", "was withheld"} {
		assert.NotContains(t, notice, phrase, "unconfirmed persistence must not announce stored changes")
	}
}

func TestRepositoryAuditNoticeDirectoryDurabilityUnconfirmed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix directory read/search permissions")
	}
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	p := hookPolicy(t, root, root, layout.RecordBlame)
	s := audit.Stream{Namespace: audit.SessionNS, Session: "s"}
	dir := filepath.Dir(s.LivePath(p))
	require.NoError(t, os.MkdirAll(dir, 0o700))
	// Search permits the append; opening the directory for the subsequent sync
	// fails. Verify the fault really applies (privileged hosts cannot accept it).
	require.NoError(t, os.Chmod(dir, 0o300))
	t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0o700)) })
	if f, err := os.Open(dir); err == nil {
		require.NoError(t, f.Close())
		t.Skip("host privileges bypass the directory-sync permission fault")
	} else {
		require.ErrorIs(t, err, os.ErrPermission)
	}
	var appendErr error
	notice := testhelpers.CaptureStderr(t, func() {
		appendErr = HandleAuditLogWithPolicy(strings.NewReader(`{"session_id":"s","tool_name":"Read","agent_type":"/home/notice-fixture/private"}`), p, "")
	})
	require.ErrorIs(t, appendErr, ErrAuditDurabilityUnconfirmed)
	assert.NotErrorIs(t, appendErr, ErrAuditDropped)
	live, err := os.ReadFile(s.LivePath(p))
	require.NoError(t, err)
	assert.NotEmpty(t, live, "applied append is retained, not reported as rolled back")
	assert.NotContains(t, string(live), "/home/notice-fixture")
	assert.NotContains(t, notice, "lost session=")
	assertNoRepositoryStoredNotice(t, notice)
}

func TestRepositorySealNoticeImmutablePayload(t *testing.T) {
	for _, tc := range []struct {
		name, raw, wantError string
		redacts              bool
	}{
		{"unsafe live payload", `{"ts":"2026-11-04T10:00:00.000000100Z","session":"s","tool":"Read","cwd":"/home/notice-fixture/private"}`, "", true},
		{"unchanged valid payload", `{"ts":"2026-11-04T10:00:00.000000100Z","session":"s","tool":"Read"}`, "", false},
		{"malformed payload", `not JSON`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			root := t.TempDir()
			p := hookPolicy(t, root, root, layout.RecordBlame)
			s := audit.Stream{Namespace: audit.SessionNS, Session: "s"}
			require.NoError(t, os.MkdirAll(filepath.Dir(s.LivePath(p)), 0o700))
			raw := []byte(tc.raw + "\n")
			require.NoError(t, os.WriteFile(s.LivePath(p), raw, 0o600))
			var res SealResult
			var sealErr error
			var out bytes.Buffer
			notice := testhelpers.CaptureStderr(t, func() {
				res, sealErr = SealRepoWithPolicy(p, time.Now(), SealOptions{Verbose: true, Out: &out})
			})
			retained, err := os.ReadFile(s.LivePath(p))
			require.NoError(t, err)
			assertNoRepositoryStoredNotice(t, notice)
			paths, err := filepath.Glob(filepath.Join(audit.ArchiveRoot(p), "sessions", "*", "audit-*.jsonl"))
			require.NoError(t, err)
			if tc.redacts {
				// A live line that predates redaction is rewritten in place, then archived.
				require.NoError(t, sealErr)
				assert.NotContains(t, string(retained), "/home/notice-fixture")
				assert.Equal(t, 1, res.LinesSealed)
				require.Len(t, paths, 1)
				archived, err := os.ReadFile(paths[0])
				require.NoError(t, err)
				assert.Equal(t, retained, archived, "the archive holds the redacted live line")
				return
			}
			assert.Equal(t, raw, retained, "archival never rewrites or consumes the raw source")
			if !json.Valid([]byte(tc.raw)) {
				require.Error(t, sealErr)
				if tc.wantError != "" {
					assert.ErrorContains(t, sealErr, tc.wantError)
					assert.ErrorContains(t, sealErr, "original source retained")
				}
				assert.Zero(t, res.LinesSealed)
				assert.Empty(t, paths)
				assert.NotContains(t, out.String(), "archived 1 line")
			} else {
				require.NoError(t, sealErr)
				assert.Equal(t, 1, res.LinesSealed)
				assert.Zero(t, res.ChunksStaged)
				require.Len(t, paths, 1)
				archived, err := os.ReadFile(paths[0])
				require.NoError(t, err)
				assert.Equal(t, raw, archived)
				assert.Contains(t, out.String(), "archived 1 line(s)")
			}
		})
	}
}
