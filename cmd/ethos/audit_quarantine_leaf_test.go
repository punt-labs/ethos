//go:build linux || darwin

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

func quarantineLeafCLI(t *testing.T, root, source string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ethosBinary, "audit", "quarantine", source)
	cmd.Dir, cmd.Env = root, append(os.Environ(), "ETHOS_REPO_ROOT=")
	out, err := cmd.CombinedOutput()
	require.NoError(t, ctx.Err(), "selected leaf must not block the actual CLI: %s", out)
	return out, err
}

func quarantineLeafCLIFixture(t *testing.T, namespace, layer string) (root, source, index string) {
	t.Helper()
	require.NotEmpty(t, ethosBinary, "actual CLI must build, not skip")
	p, s := repositoryAuditCLI(t, "blame")
	root = filepath.Dir(filepath.Dir(p.RecordRoot()))
	cn := audit.ChunkName{Namespace: audit.SessionNS, First: 100, Last: 100}
	if namespace == "mission" {
		s.Namespace, s.Mission = audit.MissionNS, "m-2026-11-04-001"
		cn.Namespace, cn.Session = audit.MissionNS, s.Session
	}
	dir, err := audit.ArchiveDir(p, s, "2026-11-04")
	require.NoError(t, err)
	source = filepath.Join(dir, cn.ChunkFile())
	if layer == "published" {
		rel, err := filepath.Rel(audit.ArchiveRoot(p), source)
		require.NoError(t, err)
		source = filepath.Join(p.CheckoutRecordRoot(), rel)
	}
	// Quarantine must never read or modify the index. A deliberately opaque
	// fixture catches hidden index replacement without staging any files.
	index = filepath.Join(root, ".git", "index")
	require.NoError(t, os.WriteFile(index, []byte("fixture index sentinel\n"), 0o600))
	require.NoError(t, audit.WriteLocalAtomic(p, source, []byte("initial fixture damage\n")))
	out, err := quarantineLeafCLI(t, root, source)
	require.NoError(t, err, "%s", out)
	require.Contains(t, string(out), "quarantined ")
	require.Contains(t, string(out), "nothing staged")
	_, err = os.Lstat(source)
	require.ErrorIs(t, err, os.ErrNotExist, "ordinary retirement removes the selected damaged file")
	marker, err := audit.ReadMarker(filepath.Join(dir, cn.MarkerFile()))
	require.NoError(t, err)
	require.NotEmpty(t, marker.RetiredSHA256)
	evidence, err := filepath.Glob(filepath.Join(p.LocalRoot(), "quarantine", "*", "*.corrupt"))
	require.NoError(t, err)
	require.Len(t, evidence, 1)
	body, err := os.ReadFile(evidence[0])
	require.NoError(t, err)
	require.Equal(t, "initial fixture damage\n", string(body))
	return root, source, index
}

func TestRepositoryQuarantineSelectedLeafActualCLI(t *testing.T) {
	for _, namespace := range []string{"session", "mission"} {
		for _, layer := range []string{"local", "published"} {
			for _, kind := range []string{"outside-link", "dangling-link", "directory", "fifo"} {
				t.Run(namespace+"/"+layer+"/"+kind, func(t *testing.T) {
					root, source, index := quarantineLeafCLIFixture(t, namespace, layer)
					target := filepath.Join(t.TempDir(), "harmless-sentinel")
					switch kind {
					case "directory":
						require.NoError(t, os.Mkdir(source, 0o700))
					case "fifo":
						out, err := exec.Command("mkfifo", source).CombinedOutput()
						require.NoError(t, err, "%s", out)
					default:
						if kind == "outside-link" {
							require.NoError(t, os.WriteFile(target, []byte("harmless external sentinel\n"), 0o600))
						}
						require.NoError(t, os.Symlink(target, source))
					}
					before := auditLeafCLISnapshot(t, root)
					out, err := quarantineLeafCLI(t, root, source)
					var exit *exec.ExitError
					if assert.ErrorAs(t, err, &exit, "%s", out) {
						assert.Equal(t, 2, exit.ExitCode(), "%s", out)
						t.Logf("actual CLI exit %d; refused selected %s", exit.ExitCode(), kind)
					}
					assert.Contains(t, string(out), "not a physical regular file")
					assert.Contains(t, string(out), source)
					assert.NotContains(t, string(out), "quarantined ")
					assert.Equal(t, before, auditLeafCLISnapshot(t, root), "link, markers and retained evidence must not change")
					body, err := os.ReadFile(index)
					require.NoError(t, err)
					assert.Equal(t, "fixture index sentinel\n", string(body))
					if kind == "outside-link" {
						body, err := os.ReadFile(target)
						require.NoError(t, err)
						assert.Equal(t, "harmless external sentinel\n", string(body))
					}
					if kind == "dangling-link" {
						_, err := os.Lstat(target)
						assert.ErrorIs(t, err, os.ErrNotExist)
					}
				})
			}
		}
	}
}

func TestRepositoryQuarantineRegularAndMissingActualCLI(t *testing.T) {
	for _, namespace := range []string{"session", "mission"} {
		for _, layer := range []string{"local", "published"} {
			t.Run(namespace+"/"+layer, func(t *testing.T) {
				root, source, index := quarantineLeafCLIFixture(t, namespace, layer)
				before := auditLeafCLISnapshot(t, root)
				out, err := quarantineLeafCLI(t, root, source)
				require.NoError(t, err, "missing-source completion must remain retryable: %s", out)
				assert.Contains(t, string(out), "quarantined ")
				assert.Equal(t, before, auditLeafCLISnapshot(t, root))
				body, err := os.ReadFile(index)
				require.NoError(t, err)
				assert.Equal(t, "fixture index sentinel\n", string(body))
			})
		}
	}
}
