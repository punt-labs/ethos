package audit

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// Snapshot without following links: discovery must leave both source evidence
// and redirected targets alone, including on a refusal.
func discoverySnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		value := e.Type().String()
		switch {
		case e.Type()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += target
		case !e.IsDir():
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

func discoveryBadDirectory(t *testing.T, path, kind, inside string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	if kind == "regular-file" {
		require.NoError(t, os.WriteFile(path, []byte("directory evidence must survive\n"), 0o600))
		return ""
	}
	target := filepath.Join(inside, "redirect-target")
	if kind == "outside-target" {
		target = filepath.Join(t.TempDir(), "redirect-target")
	}
	if kind != "dangling-link" {
		require.NoError(t, os.MkdirAll(target, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(target, "sentinel"), []byte("target bytes must survive\n"), 0o600))
	}
	require.NoError(t, os.Symlink(target, path))
	return target
}

func TestRepositoryDiscoveryDirectoryTypes(t *testing.T) {
	for _, layer := range []string{"archive", "shared-record", "checkout-record", "local-live"} {
		positions := []string{"root", "sessions", "missions", "mission-directory"}
		if layer != "local-live" {
			positions = append(positions, "session-directory")
		}
		for _, position := range positions {
			for _, kind := range []string{"existing-target", "outside-target", "dangling-link", "regular-file"} {
				for _, validCopy := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/copy=%t", layer, position, kind, validCopy), func(t *testing.T) {
						p := repositoryPolicy(t, layout.RecordBlame)
						store := filepath.Dir(filepath.Dir(p.RecordRoot()))
						checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
						root := map[string]string{"archive": ArchiveRoot(p), "shared-record": p.RecordRoot(), "checkout-record": p.CheckoutRecordRoot(), "local-live": p.LocalRoot()}[layer]
						s := Stream{Namespace: SessionNS, Session: "fixture"}
						rel := "sessions/2026-11-04-fixture"
						if position == "missions" || position == "mission-directory" {
							s = Stream{Namespace: MissionNS, Mission: "m-2026-11-04-001", Session: "fixture"}
							rel = "missions/" + s.Mission
						}
						path := root
						switch position {
						case "sessions", "missions":
							path = filepath.Join(root, position)
						case "session-directory", "mission-directory":
							path = filepath.Join(root, rel)
						}
						if validCopy {
							copyRoot := p.CheckoutRecordRoot()
							if layer == "checkout-record" {
								copyRoot = p.RecordRoot()
							}
							dir := filepath.Join(copyRoot, rel)
							require.NoError(t, os.MkdirAll(dir, 0o700))
							cn := ChunkName{Namespace: s.Namespace, First: 100, Last: 100}
							if s.Namespace == MissionNS {
								cn.Session = s.Session
							}
							raw := []byte(`{"ts":"` + FormatLineTS(100) + `","tool":"Read"}` + "\n")
							require.NoError(t, os.WriteFile(filepath.Join(dir, cn.ChunkFile()), raw, 0o600))
						}
						target := discoveryBadDirectory(t, path, kind, store)
						beforeStore, beforeCheckout := discoverySnapshot(t, store), discoverySnapshot(t, checkout)
						var beforeTarget map[string]string
						if target != "" && kind != "dangling-link" {
							beforeTarget = discoverySnapshot(t, target)
						}
						_, err := ListStreams(p)
						assert.ErrorContains(t, err, path, "discovery must not silently skip an expected directory")
						if layer != "local-live" || position == "root" {
							_, err = StreamDirs(p, s)
							assert.ErrorContains(t, err, path, "a valid mirror cannot hide a redirected source")
						}
						_, err = ReadStream(p, s)
						assert.ErrorContains(t, err, path)
						assert.Equal(t, beforeStore, discoverySnapshot(t, store))
						assert.Equal(t, beforeCheckout, discoverySnapshot(t, checkout))
						if beforeTarget != nil {
							assert.Equal(t, beforeTarget, discoverySnapshot(t, target))
						}
					})
				}
			}
		}
	}
}

func TestRepositoryDiscoveryAbsenceAndUnrecognizedEntries(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	store := filepath.Dir(filepath.Dir(p.RecordRoot()))
	checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	beforeStore, beforeCheckout := discoverySnapshot(t, store), discoverySnapshot(t, checkout)
	streams, err := ListStreams(p)
	require.NoError(t, err)
	assert.Empty(t, streams)
	for _, s := range []Stream{{Namespace: SessionNS, Session: "fixture"}, {Namespace: MissionNS, Mission: "m-2026-11-04-001", Session: "fixture"}} {
		_, err := StreamDirs(p, s)
		require.NoError(t, err)
		data, err := ReadStream(p, s)
		require.NoError(t, err)
		assert.Empty(t, data.Lines)
	}
	assert.Equal(t, beforeStore, discoverySnapshot(t, store), "absence must not create reader directories or locks")
	assert.Equal(t, beforeCheckout, discoverySnapshot(t, checkout))
	for _, root := range []string{ArchiveRoot(p), p.RecordRoot(), p.CheckoutRecordRoot(), p.LocalRoot()} {
		for _, sub := range []string{"sessions", "missions"} {
			dir := filepath.Join(root, sub)
			require.NoError(t, os.MkdirAll(dir, 0o700))
			for _, name := range []string{"README", "2026-99-99-not-a-session", "m-not-a-mission", "m-2026-11-04-001.contract.yaml", "m-2026-11-04-001.jsonl"} {
				require.NoError(t, os.Symlink(filepath.Join(store, "missing-unused"), filepath.Join(dir, name)))
			}
		}
	}
	streams, err = ListStreams(p)
	require.NoError(t, err, "unused files and links are not expected directory candidates")
	assert.Empty(t, streams)
	// Existing physical legacy mission directories with nonstandard IDs remain
	// discoverable; this repair must not narrow Stream.Validate's compatibility.
	dir := filepath.Join(p.RecordRoot(), "missions", "legacy-id")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "log.jsonl"), nil, 0o600))
	streams, err = ListStreams(p)
	require.NoError(t, err)
	assert.Equal(t, []Stream{{Namespace: MissionNS, Mission: "legacy-id", Session: "no-session"}}, streams)
}

func TestRepositoryDiscoveryAncestorTypes(t *testing.T) {
	for _, ancestor := range []string{"shared-punt-labs", "checkout-punt-labs", "local-parent"} {
		for _, kind := range []string{"existing-target", "outside-target", "dangling-link", "regular-file"} {
			t.Run(ancestor+"/"+kind, func(t *testing.T) {
				p := repositoryPolicy(t, layout.RecordBlame)
				store := filepath.Dir(filepath.Dir(p.RecordRoot()))
				path := filepath.Dir(p.RecordRoot())
				if ancestor == "checkout-punt-labs" {
					path = filepath.Dir(p.CheckoutRecordRoot())
				} else if ancestor == "local-parent" {
					path = filepath.Dir(p.LocalRoot())
				}
				discoveryBadDirectory(t, path, kind, store)
				before := discoverySnapshot(t, store)
				_, err := ListStreams(p)
				assert.ErrorContains(t, err, path)
				for _, s := range []Stream{{Namespace: SessionNS, Session: "fixture"}, {Namespace: MissionNS, Mission: "m-2026-11-04-001", Session: "fixture"}} {
					_, err := StreamDirs(p, s)
					assert.ErrorContains(t, err, path)
					_, err = ReadStream(p, s)
					assert.ErrorContains(t, err, path)
				}
				assert.Equal(t, before, discoverySnapshot(t, store), "no traversal or creation after an invalid ancestor")
			})
		}
	}
}

func TestRepositoryDiscoveryUnreadableIsNotAbsence(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission denial requires a non-root Unix process")
	}
	for _, layer := range []string{"archive", "shared-record", "checkout-record", "local-live"} {
		t.Run(layer, func(t *testing.T) {
			p := repositoryPolicy(t, layout.RecordBlame)
			root := map[string]string{"archive": ArchiveRoot(p), "shared-record": p.RecordRoot(), "checkout-record": p.CheckoutRecordRoot(), "local-live": p.LocalRoot()}[layer]
			path := filepath.Join(root, "missions", "m-2026-11-04-001")
			require.NoError(t, os.MkdirAll(path, 0o700))
			sentinel := filepath.Join(path, "sentinel")
			require.NoError(t, os.WriteFile(sentinel, []byte("unreadable source evidence\n"), 0o600))
			require.NoError(t, os.Chmod(path, 0))
			t.Cleanup(func() { require.NoError(t, os.Chmod(path, 0o700)) })
			_, err := ListStreams(p)
			assert.ErrorContains(t, err, path)
			assert.ErrorIs(t, err, os.ErrPermission)
			s := Stream{Namespace: MissionNS, Mission: "m-2026-11-04-001", Session: "fixture"}
			_, err = ReadStream(p, s)
			assert.ErrorContains(t, err, path)
			assert.ErrorIs(t, err, os.ErrPermission)
			require.NoError(t, os.Chmod(path, 0o700))
			b, err := os.ReadFile(sentinel)
			require.NoError(t, err)
			assert.Equal(t, "unreadable source evidence\n", string(b))
		})
	}
}

func TestRepositoryDiscoverySelectedSessionIgnoresOtherNames(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	base := filepath.Join(p.RecordRoot(), "sessions")
	require.NoError(t, os.MkdirAll(base, 0o700))
	path := filepath.Join(base, "2026-11-04-other-fixture")
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "absent"), path))
	s := Stream{Namespace: SessionNS, Session: "fixture"}
	dirs, err := StreamDirs(p, s)
	require.NoError(t, err, "a suffix collision is not the selected session")
	assert.Empty(t, dirs)
	_, err = ReadStream(p, s)
	require.NoError(t, err)
	_, err = ListStreams(p)
	assert.ErrorContains(t, err, path, "global discovery must still reject that other recognized candidate")
}

func TestRepositoryDiscoveryRedirectedConflictIsNotAdmissible(t *testing.T) {
	for _, layer := range []string{"archive", "shared-record", "checkout-record", "local-live"} {
		for _, ns := range []Namespace{SessionNS, MissionNS} {
			if layer == "local-live" && ns == SessionNS {
				continue
			}
			t.Run(fmt.Sprintf("%s/%d", layer, ns), func(t *testing.T) {
				p := repositoryPolicy(t, layout.RecordBlame)
				root := map[string]string{"archive": ArchiveRoot(p), "shared-record": p.RecordRoot(), "checkout-record": p.CheckoutRecordRoot(), "local-live": p.LocalRoot()}[layer]
				s := Stream{Namespace: ns, Session: "fixture"}
				rel := "sessions/2026-11-04-fixture"
				cn := ChunkName{Namespace: ns, First: 100, Last: 100}
				if ns == MissionNS {
					s.Mission, cn.Session = "m-2026-11-04-001", s.Session
					rel = "missions/" + s.Mission
				}
				path := filepath.Join(root, rel)
				target := t.TempDir()
				targetFile := filepath.Join(target, cn.ChunkFile())
				if layer == "local-live" {
					targetFile = filepath.Join(target, s.Session+".log.jsonl")
				}
				raw := func(tool string) []byte {
					return []byte(`{"ts":"` + FormatLineTS(100) + `","tool":"` + tool + `"}` + "\n")
				}
				require.NoError(t, os.WriteFile(targetFile, raw("Write"), 0o600))
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.Symlink(target, path))
				mirror := p.CheckoutRecordRoot()
				if layer == "checkout-record" {
					mirror = p.RecordRoot()
				}
				dir := filepath.Join(mirror, rel)
				require.NoError(t, os.MkdirAll(dir, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(dir, cn.ChunkFile()), raw("Read"), 0o600))
				_, err := ReadStream(p, s)
				assert.ErrorContains(t, err, path)
				assert.ErrorContains(t, err, "not a physical directory")
				if err != nil {
					assert.NotContains(t, err.Error(), "identity conflict", "redirected bytes are never admissible union inputs")
				}
				b, err := os.ReadFile(targetFile)
				require.NoError(t, err)
				assert.Equal(t, raw("Write"), b)
				b, err = os.ReadFile(filepath.Join(dir, cn.ChunkFile()))
				require.NoError(t, err)
				assert.Equal(t, raw("Read"), b)
			})
		}
	}
}
