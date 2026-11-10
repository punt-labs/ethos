//go:build linux || darwin

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	hooks "github.com/punt-labs/ethos/v4/plugin/hooks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func repositoryAuditCLI(t *testing.T, mode string) (layout.Policy, audit.Stream) {
	t.Helper()
	_, root, _ := auditMigrateEnv(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs", "ethos"), 0o700))
	if mode != "" {
		require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos.yaml"), []byte("record: "+mode+"\n"), 0o600))
	}
	m := layout.RecordBlame
	if mode == "full" {
		m = layout.RecordFull
	}
	p, err := layout.NewRepository(root, root, m)
	require.NoError(t, err)
	t.Cleanup(func() { auditSealCommitContext, auditSealVerbose, auditSealDryRun = false, false, false })
	auditSealCommitContext, auditSealVerbose, auditSealDryRun = false, false, false
	return p, audit.Stream{Namespace: audit.SessionNS, Session: "fixture"}
}

func TestRepositoryAuditPreCommitExitPolicy(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "acceptance requires the real CLI")
	for _, mode := range []string{"", "blame", "full", "unknown", "[malformed"} {
		t.Run(mode, func(t *testing.T) {
			p, s := repositoryAuditCLI(t, mode)
			root := filepath.Dir(filepath.Dir(p.RecordRoot()))
			require.NoError(t, os.WriteFile(filepath.Join(p.RecordRoot(), "enabled"), nil, 0o600))
			dir, err := audit.ArchiveDir(p, s, "2026-11-04")
			require.NoError(t, err)
			require.NoError(t, audit.WriteLocalAtomic(p, filepath.Join(dir, audit.SessionChunkFile(100, 100)), []byte("corrupt\n")))
			healthy := audit.Stream{Namespace: audit.SessionNS, Session: "z-new"}
			raw := []byte(`{"ts":"` + audit.FormatLineTS(1000) + `","session":"z-new","tool":"Read","record":"blame"}` + "\n")
			require.NoError(t, audit.WriteLocalAtomic(p, healthy.LivePath(p), raw))
			bin := t.TempDir()
			require.NoError(t, os.Symlink(ethosBinary, filepath.Join(bin, "ethos")))
			script := filepath.Join(t.TempDir(), "pre-commit")
			require.NoError(t, os.WriteFile(script, hooks.PreCommit, 0o700))
			cmd := exec.Command("/bin/sh", script)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if mode == "" || mode == "blame" {
				require.NoError(t, err, "%s", out)
				assert.Contains(t, string(out), "retained-data warning")
			} else {
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit, "%s", out)
				assert.Equal(t, 2, exit.ExitCode(), "%s", out)
				assert.NotContains(t, string(out), "allowing the source commit")
			}
			cmd = exec.Command(ethosBinary, "audit", "seal")
			cmd.Dir = root
			out, err = cmd.CombinedOutput()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit, "manual command: %s", out)
			assert.Equal(t, 2, exit.ExitCode(), "%s", out)

			// Run the actual Git commit entry point too. The hook executable and
			// every committed source here belong only to this disposable fixture.
			hookDir := filepath.Dir(script)
			require.NoError(t, os.WriteFile(filepath.Join(root, "source.go"), []byte("package fixture\n"), 0o600))
			git := exec.Command("git", "add", "--", "source.go")
			git.Dir = root
			out, err = git.CombinedOutput()
			require.NoError(t, err, "%s", out)
			git = exec.Command("git", "-c", "core.hooksPath="+hookDir, "-c", "commit.gpgsign=false", "-c", "user.name=fixture", "commit", "-m", "fixture source")
			git.Dir = root
			git.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			out, err = git.CombinedOutput()
			if mode == "" || mode == "blame" {
				require.NoError(t, err, "default source commit was blocked: %s", out)
				assert.Contains(t, string(out), "retained-data warning")
				git = exec.Command("git", "ls-tree", "-r", "--name-only", "HEAD")
				git.Dir = root
				out, err = git.CombinedOutput()
				require.NoError(t, err, "%s", out)
				assert.Equal(t, "source.go\n", string(out), "private audit data must not ride a blame commit")
				n, err := audit.UnarchivedCount(p, healthy)
				require.NoError(t, err)
				assert.Zero(t, n, "historical damage did not prevent new healthy archival")
				require.FileExists(t, healthy.LivePath(p))
			} else {
				require.Error(t, err, "invalid policy/full failure must block the commit: %s", out)
				assert.NotContains(t, string(out), "allowing the source commit")
			}
		})
	}
}

func TestRepositoryAuditProspectiveFullRealCommit(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "acceptance requires the real CLI")
	for _, failPublication := range []bool{false, true} {
		name := "full publication"
		if failPublication {
			name = "ignored full publication blocks"
		}
		t.Run(name, func(t *testing.T) {
			p, s := repositoryAuditCLI(t, "")
			root := filepath.Dir(filepath.Dir(p.RecordRoot()))
			require.NoError(t, os.WriteFile(filepath.Join(p.RecordRoot(), "enabled"), nil, 0o600))
			config := filepath.Join(root, ".punt-labs", "ethos.yaml")
			for _, write := range []struct{ mode, tool string }{{"blame", "Read"}, {"full", "Edit"}, {"blame", "Write"}} {
				require.NoError(t, os.WriteFile(config, []byte("record: "+write.mode+"\n"), 0o600))
				cmd := exec.Command(ethosBinary, "hook", "audit-log")
				cmd.Dir = root
				cmd.Stdin = bytes.NewBufferString(`{"session_id":"` + s.Session + `","tool_name":"` + write.tool + `","delegation_id":"d","tool_input":{"file_path":"example.go"}}`)
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", out)
			}
			require.NoError(t, os.WriteFile(config, []byte("record: full\n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(root, "source.go"), []byte("package fixture\n"), 0o600))
			if failPublication {
				require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".punt-labs/ethos/sessions/\n"), 0o600))
			}
			gitRun := func(args ...string) []byte {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = root
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", out)
				return out
			}
			gitRun("add", "--", "source.go")
			before := gitRun("diff", "--cached", "--name-status")
			bin, hookDir := t.TempDir(), t.TempDir()
			require.NoError(t, os.Symlink(ethosBinary, filepath.Join(bin, "ethos")))
			require.NoError(t, os.WriteFile(filepath.Join(hookDir, "pre-commit"), hooks.PreCommit, 0o700))
			cmd := exec.Command("git", "-c", "core.hooksPath="+hookDir, "-c", "commit.gpgsign=false", "-c", "user.name=fixture", "commit", "-m", "fixture full consent")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if failPublication {
				require.Error(t, err, "%s", out)
				assert.Contains(t, string(out), "local archive retained")
				assert.Equal(t, string(before), string(gitRun("diff", "--cached", "--name-status")), "failure preserved unrelated staged source")
			} else {
				require.NoError(t, err, "%s", out)
				tree := string(gitRun("ls-tree", "-r", "--name-only", "HEAD"))
				assert.Contains(t, tree, "source.go")
				assert.Contains(t, tree, ".punt-labs/ethos/sessions/")
				assert.NotContains(t, tree, "local/")
				files, err := filepath.Glob(filepath.Join(p.CheckoutRecordRoot(), "sessions", "*", "audit-*.jsonl"))
				require.NoError(t, err)
				require.Len(t, files, 1)
				body, err := os.ReadFile(files[0])
				require.NoError(t, err)
				assert.Contains(t, string(body), `"tool":"Edit"`)
				assert.NotContains(t, string(body), `"tool":"Read"`)
				assert.NotContains(t, string(body), `"tool":"Write"`)
			}
			data, err := audit.ReadStream(p, s)
			require.NoError(t, err)
			require.Len(t, data.Lines, 3, "live/archive union retained all consent classes")
			require.FileExists(t, s.LivePath(p), "publication never consumes the retained live source")
			n, err := audit.UnarchivedCount(p, s)
			require.NoError(t, err)
			require.Zero(t, n, "archive survived publication outcome")
		})
	}
}

func TestRepositoryAuditLinkedWorktreeWriteSurvivesRemoval(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "acceptance requires the real CLI")
	p, s := repositoryAuditCLI(t, "")
	root := filepath.Dir(filepath.Dir(p.RecordRoot()))
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	gitRun("-c", "core.hooksPath=", "-c", "commit.gpgsign=false", "-c", "user.name=fixture", "commit", "--allow-empty", "-m", "worktree fixture")
	sibling := filepath.Join(t.TempDir(), "linked")
	gitRun("worktree", "add", "--detach", sibling)
	cmd := exec.Command(ethosBinary, "hook", "audit-log")
	cmd.Dir = sibling
	// Clear an ambient override so discovery captures this linked checkout and
	// resolves S through Git's common directory. An override explicitly selects
	// a store; setting it to disposable C would test a different contract.
	cmd.Env = append(os.Environ(), "ETHOS_REPO_ROOT=")
	cmd.Stdin = bytes.NewBufferString(`{"session_id":"` + s.Session + `","tool_name":"Read","delegation_id":"linked-d","tool_input":{"file_path":"example.go"}}`)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.FileExists(t, s.LivePath(p), "the actual hook must append under S before any seal")
	require.NoDirExists(t, filepath.Join(sibling, ".punt-labs", "local", "ethos"), "no stream or lock may be owned by disposable C")
	// Removal happens before SessionEnd or seal: unsealed tails must survive.
	gitRun("worktree", "remove", "--force", sibling)
	cmd = exec.Command(ethosBinary, "audit", "seal")
	cmd.Dir = root
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	cmd = exec.Command(ethosBinary, "audit", "show", "--delegation", "linked-d", "--format", "json")
	cmd.Dir = root
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Contains(t, string(out), `"tool":"Read"`)
	n, err := audit.UnarchivedCount(p, s)
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestRepositoryAuditCLICommitContextFailurePolicy(t *testing.T) {
	for _, mode := range []string{"", "blame", "full", "unknown", "[malformed"} {
		t.Run(mode, func(t *testing.T) {
			p, s := repositoryAuditCLI(t, mode)
			dir, err := audit.ArchiveDir(p, s, "2026-11-04")
			require.NoError(t, err)
			require.NoError(t, audit.WriteLocalAtomic(p, filepath.Join(dir, audit.SessionChunkFile(100, 100)), []byte("corrupt bytes\n")))
			_, stderr, err := execHandler(t, "audit", "seal", "--commit-context")
			if mode == "" || mode == "blame" {
				require.NoError(t, err)
				assert.Contains(t, stderr, "retained-data warning")
			} else {
				require.Error(t, err)
				assert.NotContains(t, stderr, "allowing the source commit")
			}
			auditSealCommitContext = false
			_, _, err = execHandler(t, "audit", "seal")
			require.Error(t, err, "manual failure must remain visible")
			_, _, err = execHandler(t, "audit", "show", "--delegation", "d")
			require.Error(t, err, "inspection failure must remain visible")
		})
	}
}

func TestRepositoryAuditCLIArchivesWithoutStaging(t *testing.T) {
	p, s := repositoryAuditCLI(t, "")
	raw := []byte(`{"ts":"` + audit.FormatLineTS(time.Now().UnixNano()) + `","session":"fixture","tool":"Read","delegation_id":"d","record":"blame"}` + "\n")
	require.NoError(t, audit.WriteLocalAtomic(p, s.LivePath(p), raw))
	_, _, err := execHandler(t, "audit", "seal")
	require.NoError(t, err)
	files, err := filepath.Glob(filepath.Join(audit.ArchiveRoot(p), "sessions", "*", "audit-*.jsonl"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	stdout, _, err := execHandler(t, "audit", "show", "--delegation", "d", "--format", "json")
	require.NoError(t, err)
	assert.Contains(t, stdout, `"record":"blame"`)
	_, err = os.Stat(filepath.Join(filepath.Dir(filepath.Dir(p.RecordRoot())), ".git", "index"))
	assert.True(t, os.IsNotExist(err), "default seal must not create an index")
	stdout, _, err = execHandler(t, "audit", "show", "--delegation", "d", "--format", "json")
	require.NoError(t, err)
	assert.Contains(t, stdout, `"tool":"Read"`)
}
