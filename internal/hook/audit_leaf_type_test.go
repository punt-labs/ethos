package hook

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestRepositoryLeafTypesQueryAndSeal(t *testing.T) {
	for _, mission := range []bool{false, true} {
		for _, layer := range []string{"live", "archive", "shared-record", "checkout-record"} {
			artifacts := []string{"legacy", "chunk", "quarantine", "corrupt"}
			if layer == "live" {
				artifacts = []string{"live"}
			}
			for _, artifact := range artifacts {
				for _, kind := range []string{"directory", "inside-link", "outside-link", "dangling-link"} {
					t.Run(layer+"/"+map[bool]string{false: "session", true: "mission"}[mission]+"/"+artifact+"/"+kind, func(t *testing.T) {
						root, checkout := t.TempDir(), t.TempDir()
						p := hookPolicy(t, root, checkout, layout.RecordBlame)
						base := map[string]string{"live": p.LocalRoot(), "archive": audit.ArchiveRoot(p), "shared-record": p.RecordRoot(), "checkout-record": p.CheckoutRecordRoot()}[layer]
						rel, name := "sessions/2026-11-04-fixture", "audit.jsonl"
						cn := audit.ChunkName{Namespace: audit.SessionNS, First: 100, Last: 100}
						if mission {
							rel, name, cn.Namespace, cn.Session = "missions/m-2026-11-04-001", "log.jsonl", audit.MissionNS, "fixture"
						}
						raw := []byte(`{"ts":"` + audit.FormatLineTS(100) + `","tool":"Read","delegation_id":"d"}` + "\n")
						switch artifact {
						case "live":
							rel, name = "sessions", "fixture.audit.jsonl"
							if mission {
								rel, name = "missions/m-2026-11-04-001", "fixture.log.jsonl"
							}
						case "chunk":
							name = cn.ChunkFile()
						case "quarantine", "corrupt":
							name = cn.MarkerFile()
							marker, err := audit.MarshalMarker(audit.Marker{Chunk: cn.Stem(), VerifiedLast: 100})
							require.NoError(t, err)
							raw = marker
							if artifact == "corrupt" {
								dir := filepath.Join(base, rel)
								require.NoError(t, os.MkdirAll(dir, 0o700))
								require.NoError(t, os.WriteFile(filepath.Join(dir, name), marker, 0o600))
								name, raw = cn.ChunkFile()+".corrupt", []byte("retired evidence\n")
							}
						}
						path := filepath.Join(base, rel, name)
						require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
						target := filepath.Join(root, "target")
						if kind == "outside-link" {
							target = filepath.Join(t.TempDir(), "target")
						}
						if kind == "directory" {
							require.NoError(t, os.Mkdir(path, 0o700))
						} else {
							if kind != "dangling-link" {
								require.NoError(t, os.WriteFile(target, raw, 0o600))
							}
							require.NoError(t, os.Symlink(target, path))
						}
						// Always retain a healthy independent copy; none may mask the
						// source's type failure. Audit's matrix also covers no-copy cases.
						mirror := p.RecordRoot()
						if layer == "shared-record" {
							mirror = p.CheckoutRecordRoot()
						}
						copyRel := "sessions/2026-11-04-fixture"
						if mission {
							copyRel = "missions/m-2026-11-04-001"
						}
						copyDir := filepath.Join(mirror, copyRel)
						require.NoError(t, os.MkdirAll(copyDir, 0o700))
						require.NoError(t, os.WriteFile(filepath.Join(copyDir, cn.ChunkFile()), []byte(`{"ts":"`+audit.FormatLineTS(100)+`","tool":"Read","delegation_id":"d"}`+"\n"), 0o600))
						beforeS, beforeC := hookDiscoverySnapshot(t, root), hookDiscoverySnapshot(t, checkout)
						entries, err := QueryAuditByDelegationWithPolicy(p, "", "d")
						assert.ErrorContains(t, err, path)
						assert.Empty(t, entries)
						_, err = CollectAuditDiagnosticsWithPolicy(p)
						assert.ErrorContains(t, err, path)
						_, err = SealRepoWithPolicy(p, time.Now(), SealOptions{})
						assert.ErrorContains(t, err, path)
						assert.Equal(t, beforeS, hookDiscoverySnapshot(t, root), "discovery refuses before creating a stream lock")
						assert.Equal(t, beforeC, hookDiscoverySnapshot(t, checkout))
						if kind == "outside-link" {
							b, err := os.ReadFile(target)
							require.NoError(t, err)
							assert.Equal(t, raw, b)
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
}

func TestRepositoryLeafPermissionQueryAndSeal(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission denial requires a non-root Unix process")
	}
	root, checkout := t.TempDir(), t.TempDir()
	p := hookPolicy(t, root, checkout, layout.RecordBlame)
	path := filepath.Join(p.LocalRoot(), "sessions", "fixture.audit.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	raw := []byte("unreadable evidence\n")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	before := hookDiscoverySnapshot(t, root)
	require.NoError(t, os.Chmod(path, 0))
	t.Cleanup(func() { require.NoError(t, os.Chmod(path, 0o600)) })
	_, err := QueryAuditByDelegationWithPolicy(p, "", "d")
	assert.ErrorContains(t, err, path)
	assert.ErrorIs(t, err, os.ErrPermission)
	_, err = SealRepoWithPolicy(p, time.Now(), SealOptions{})
	assert.ErrorContains(t, err, path)
	assert.ErrorIs(t, err, os.ErrPermission)
	require.NoError(t, os.Chmod(path, 0o600))
	assert.Equal(t, before, hookDiscoverySnapshot(t, root))
}
