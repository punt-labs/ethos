//go:build linux || darwin

package audit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestRepositoryQuarantineSelectedLeafTypes(t *testing.T) {
	testQuarantineSelectedLeaves(t, []string{"outside-link", "dangling-link", "directory"})
}

func TestRepositoryQuarantineSelectedLeafFIFO(t *testing.T) {
	// A regression must fail within a deadline, not strand the test process
	// opening a FIFO. The child uses only fixtures under the parent's TempDir.
	if os.Getenv("ETHOS_TEST_QUARANTINE_FIFO_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRepositoryQuarantineSelectedLeafFIFO$", "-test.v")
		cmd.Env = append(os.Environ(), "ETHOS_TEST_QUARANTINE_FIFO_CHILD=1", "TMPDIR="+t.TempDir())
		out, err := cmd.CombinedOutput()
		require.NoError(t, ctx.Err(), "quarantine blocked on a selected FIFO: %s", out)
		require.NoError(t, err, "%s", out)
		return
	}
	testQuarantineSelectedLeaves(t, []string{"fifo"})
}

func testQuarantineSelectedLeaves(t *testing.T, kinds []string) {
	t.Helper()
	for _, namespace := range []string{"session", "mission"} {
		for _, layer := range []string{"local", "shared-record", "checkout-record"} {
			for _, kind := range kinds {
				t.Run(namespace+"/"+layer+"/"+kind, func(t *testing.T) {
					p := repositoryPolicy(t, layout.RecordBlame)
					s := Stream{Namespace: SessionNS, Session: "fixture"}
					cn := ChunkName{Namespace: SessionNS, First: 100, Last: 100}
					if namespace == "mission" {
						s.Namespace, s.Mission = MissionNS, "m-2026-11-04-001"
						cn.Namespace, cn.Session = MissionNS, s.Session
					}
					dir, err := ArchiveDir(p, s, "2026-11-04")
					require.NoError(t, err)
					source := filepath.Join(dir, cn.ChunkFile())
					if layer != "local" {
						rel, err := filepath.Rel(ArchiveRoot(p), source)
						require.NoError(t, err)
						root := p.RecordRoot()
						if layer == "checkout-record" {
							root = p.CheckoutRecordRoot()
						}
						source = filepath.Join(root, rel)
					}
					guard := func(raw []byte) ([]byte, string, bool) { return raw, "", false }
					// Seed real retirement evidence and completion markers. A dangling
					// link must not be mistaken for the missing-source resume below.
					require.NoError(t, WriteLocalAtomic(p, source, []byte("initial fixture damage\n")))
					prior, err := QuarantineLocal(p, s, source, dir, cn, "fixture", guard)
					require.NoError(t, err)
					require.NotEmpty(t, prior.RetiredSHA256)
					target := badAuditLeaf(t, source, kind, t.TempDir(), []byte("harmless external sentinel\n"))
					store := filepath.Dir(filepath.Dir(p.RecordRoot()))
					checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
					beforeS, beforeC := leafSnapshot(t, store), leafSnapshot(t, checkout)
					m, err := QuarantineLocal(p, s, source, dir, cn, "refused attempt", guard)
					assert.ErrorContains(t, err, "not a physical regular file")
					assert.ErrorContains(t, err, source)
					assert.Equal(t, Marker{}, m)
					assert.Equal(t, beforeS, leafSnapshot(t, store), "source, markers and evidence must remain unchanged")
					assert.Equal(t, beforeC, leafSnapshot(t, checkout))
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

func TestRepositoryQuarantineSelectedLeafBeforePreparation(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "fixture"}
	cn := ChunkName{Namespace: SessionNS, First: 100, Last: 100}
	dir, err := ArchiveDir(p, s, "2026-11-04")
	require.NoError(t, err)
	source := filepath.Join(p.CheckoutRecordRoot(), "sessions", "2026-11-04-fixture", cn.ChunkFile())
	badAuditLeaf(t, source, "outside-link", t.TempDir(), []byte("harmless external sentinel\n"))
	store := filepath.Dir(filepath.Dir(p.RecordRoot()))
	checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	beforeS, beforeC := leafSnapshot(t, store), leafSnapshot(t, checkout)
	guard := func(raw []byte) ([]byte, string, bool) { return raw, "", false }
	_, err = QuarantineLocal(p, s, source, dir, cn, "fixture", guard)
	assert.ErrorContains(t, err, "not a physical regular file")
	assert.Equal(t, beforeS, leafSnapshot(t, store), "refuse before creating the local archive or staging")
	assert.Equal(t, beforeC, leafSnapshot(t, checkout))
}
