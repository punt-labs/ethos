package hook

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

func hookDiscoverySnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		value := e.Type().String()
		if e.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += target
		} else if !e.IsDir() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += string(b)
		}
		out[path] = value
		return nil
	}))
	return out
}

func TestRepositoryDiscoveryTypeQueryAndSeal(t *testing.T) {
	for _, layer := range []string{"archive", "shared-record", "checkout-record", "local-live"} {
		for _, namespace := range []string{"session", "mission"} {
			if layer == "local-live" && namespace == "session" {
				continue // Live sessions are files, not dated directory candidates.
			}
			for _, kind := range []string{"existing-target", "outside-target", "dangling-link", "regular-file"} {
				t.Run(layer+"/"+namespace+"/"+kind, func(t *testing.T) {
					root, checkout := t.TempDir(), t.TempDir()
					p := hookPolicy(t, root, checkout, layout.RecordBlame)
					base := map[string]string{"archive": audit.ArchiveRoot(p), "shared-record": p.RecordRoot(), "checkout-record": p.CheckoutRecordRoot(), "local-live": p.LocalRoot()}[layer]
					rel := "sessions/2026-11-04-fixture"
					if namespace == "mission" {
						rel = "missions/m-2026-11-04-001"
					}
					path := filepath.Join(base, rel)
					require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
					target := filepath.Join(root, "target")
					if kind == "outside-target" {
						target = filepath.Join(t.TempDir(), "target")
					}
					if kind == "regular-file" {
						require.NoError(t, os.WriteFile(path, []byte("source evidence\n"), 0o600))
					} else {
						if kind != "dangling-link" {
							require.NoError(t, os.MkdirAll(target, 0o700))
							require.NoError(t, os.WriteFile(filepath.Join(target, "sentinel"), []byte("target evidence\n"), 0o600))
						}
						require.NoError(t, os.Symlink(target, path))
					}
					// An admissible mirror must not turn the bad source into success.
					mirror := p.CheckoutRecordRoot()
					if layer == "checkout-record" {
						mirror = p.RecordRoot()
					}
					dir := filepath.Join(mirror, rel)
					require.NoError(t, os.MkdirAll(dir, 0o700))
					name := "audit.jsonl"
					if namespace == "mission" {
						name = "log.jsonl"
					}
					require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(`{"ts":"2026-11-04T00:00:00Z","tool":"Read","delegation_id":"d"}`+"\n"), 0o600))
					beforeRoot, beforeCheckout := hookDiscoverySnapshot(t, root), hookDiscoverySnapshot(t, checkout)
					entries, err := QueryAuditByDelegationWithPolicy(p, "", "d")
					assert.ErrorContains(t, err, path)
					assert.Empty(t, entries)
					_, err = SealRepoWithPolicy(p, time.Now(), SealOptions{})
					assert.ErrorContains(t, err, path, "manual sealing must not say nothing pending")
					assert.Equal(t, beforeRoot, hookDiscoverySnapshot(t, root))
					assert.Equal(t, beforeCheckout, hookDiscoverySnapshot(t, checkout))
					if kind == "outside-target" {
						b, err := os.ReadFile(filepath.Join(target, "sentinel"))
						require.NoError(t, err)
						assert.Equal(t, "target evidence\n", string(b))
					}
				})
			}
		}
	}
}
