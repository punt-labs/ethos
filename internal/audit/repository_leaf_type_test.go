package audit

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// Do not follow links, open special files, or require read permission on a
// sentinel. This also snapshots lock and directory creation on a refusal.
func leafSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += target
		} else if info.Mode().IsRegular() && info.Mode().Perm()&0o400 != 0 {
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

func badAuditLeaf(t *testing.T, path, kind, root string, raw []byte) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	switch kind {
	case "directory":
		require.NoError(t, os.Mkdir(path, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(path, "sentinel"), raw, 0o600))
	case "unreadable":
		require.NoError(t, os.WriteFile(path, raw, 0o600))
		require.NoError(t, os.Chmod(path, 0))
		t.Cleanup(func() { require.NoError(t, os.Chmod(path, 0o600)) })
	case "fifo":
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("FIFO fixture requires Unix mkfifo")
		}
		out, err := exec.Command("mkfifo", path).CombinedOutput()
		require.NoError(t, err, "%s", out)
	default:
		target := filepath.Join(root, "leaf-target")
		if kind == "outside-link" {
			target = filepath.Join(t.TempDir(), "leaf-target")
		}
		if kind != "dangling-link" {
			require.NoError(t, os.WriteFile(target, raw, 0o600))
		}
		require.NoError(t, os.Symlink(target, path))
		return target
	}
	return ""
}

func leafArtifact(t *testing.T, root string, s Stream, artifact string) (path string, raw []byte) {
	t.Helper()
	rel, legacy := "sessions/2026-11-04-"+s.Session, "audit.jsonl"
	cn := ChunkName{Namespace: s.Namespace, First: 100, Last: 100}
	if s.Namespace == MissionNS {
		rel, legacy, cn.Session = "missions/"+s.Mission, "log.jsonl", s.Session
	}
	name := cn.ChunkFile()
	raw = []byte(`{"ts":"` + FormatLineTS(100) + `","tool":"Read","delegation_id":"d"}` + "\n")
	switch artifact {
	case "live":
		rel, name = "sessions", s.Session+".audit.jsonl"
		if s.Namespace == MissionNS {
			rel, name = "missions/"+s.Mission, s.Session+".log.jsonl"
		}
	case "legacy":
		name = legacy
	case "quarantine", "corrupt":
		marker, err := MarshalMarker(Marker{Chunk: cn.Stem(), VerifiedLast: 100})
		require.NoError(t, err)
		name, raw = cn.MarkerFile(), marker
		if artifact == "corrupt" {
			dir := filepath.Join(root, rel)
			require.NoError(t, os.MkdirAll(dir, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), marker, 0o600))
			name, raw = cn.ChunkFile()+".corrupt", []byte("retired bytes\n")
		}
	}
	return filepath.Join(root, rel, name), raw
}

func TestRepositoryLeafTypes(t *testing.T) {
	testRepositoryLeafTypes(t, []string{"directory", "inside-link", "outside-link", "dangling-link"})
}

func TestRepositoryLeafFIFO(t *testing.T) {
	testRepositoryLeafTypes(t, []string{"fifo"})
}

func TestRepositoryLeafPermissions(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission denial requires a non-root Unix process")
	}
	testRepositoryLeafTypes(t, []string{"unreadable"})
}

func testRepositoryLeafTypes(t *testing.T, kinds []string) {
	t.Helper()
	for _, ns := range []Namespace{SessionNS, MissionNS} {
		for _, layer := range []string{"live", "archive", "shared-record", "checkout-record"} {
			artifacts := []string{"legacy", "chunk", "quarantine", "corrupt"}
			if layer == "live" {
				artifacts = []string{"live"}
			}
			for _, artifact := range artifacts {
				for _, kind := range kinds {
					for _, mirror := range []bool{false, true} {
						t.Run(fmt.Sprintf("%d/%s/%s/%s/mirror=%t", ns, layer, artifact, kind, mirror), func(t *testing.T) {
							p := repositoryPolicy(t, layout.RecordBlame)
							store, checkout := filepath.Dir(filepath.Dir(p.RecordRoot())), filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
							s := Stream{Namespace: ns, Session: "fixture"}
							if ns == MissionNS {
								s.Mission = "m-2026-11-04-001"
							}
							root := map[string]string{"live": p.LocalRoot(), "archive": ArchiveRoot(p), "shared-record": p.RecordRoot(), "checkout-record": p.CheckoutRecordRoot()}[layer]
							path, raw := leafArtifact(t, root, s, artifact)
							if mirror {
								copyRoot := p.RecordRoot()
								if layer == "shared-record" {
									copyRoot = p.CheckoutRecordRoot()
								}
								copyArtifact := artifact
								if artifact == "live" {
									copyArtifact = "chunk"
								}
								copyPath, copyRaw := leafArtifact(t, copyRoot, s, copyArtifact)
								require.NoError(t, os.MkdirAll(filepath.Dir(copyPath), 0o700))
								require.NoError(t, os.WriteFile(copyPath, copyRaw, 0o600))
							}
							target := badAuditLeaf(t, path, kind, store, raw)
							beforeS, beforeC := leafSnapshot(t, store), leafSnapshot(t, checkout)
							_, err := ListStreams(p)
							assert.ErrorContains(t, err, path, "discovery must reject recognized leaves before locks or filtering")
							if kind == "unreadable" {
								assert.ErrorIs(t, err, os.ErrPermission)
							}
							_, err = ReadStream(p, s)
							assert.ErrorContains(t, err, path)
							if artifact == "chunk" {
								var readErr *ChunkReadError
								assert.ErrorAs(t, err, &readErr, "type failures are not quarantinable payload corruption")
							}
							if kind == "unreadable" {
								assert.ErrorIs(t, err, os.ErrPermission)
							} else {
								assert.ErrorContains(t, err, "not a physical regular file")
							}
							assert.Equal(t, beforeS, leafSnapshot(t, store))
							assert.Equal(t, beforeC, leafSnapshot(t, checkout))
							if kind == "outside-link" {
								b, err := os.ReadFile(target)
								require.NoError(t, err)
								assert.Equal(t, raw, b)
							}
							if kind == "dangling-link" {
								_, err := os.Lstat(target)
								assert.ErrorIs(t, err, os.ErrNotExist)
							}
							if kind == "unreadable" {
								require.NoError(t, os.Chmod(path, 0o600))
								b, err := os.ReadFile(path)
								require.NoError(t, err)
								assert.Equal(t, raw, b)
							}
						})
					}
				}
			}
		}
	}
}

func TestRepositoryLeafSelectedSessionIgnoresOtherArtifacts(t *testing.T) {
	kinds := []string{"directory", "inside-link", "outside-link", "dangling-link", "fifo"}
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		kinds = append(kinds, "unreadable")
	}
	for _, artifact := range []string{"live", "chunk", "quarantine", "corrupt"} {
		for _, kind := range kinds {
			t.Run(artifact+"/"+kind, func(t *testing.T) {
				p := repositoryPolicy(t, layout.RecordBlame)
				s := Stream{Namespace: MissionNS, Mission: "m-2026-11-04-001", Session: "fixture"}
				other := s
				other.Session = "other-fixture"
				root := p.RecordRoot()
				if artifact == "live" {
					root = p.LocalRoot()
				}
				path, raw := leafArtifact(t, root, other, artifact)
				badAuditLeaf(t, path, kind, filepath.Dir(filepath.Dir(p.RecordRoot())), raw)
				before := leafSnapshot(t, filepath.Dir(filepath.Dir(p.RecordRoot())))
				_, err := ScanSealedDir(filepath.Dir(path), MissionNS, s.Session)
				require.NoError(t, err, "filter exact session before validating artifact type")
				data, err := ReadStream(p, s)
				require.NoError(t, err)
				assert.Empty(t, data.Lines)
				assert.Empty(t, data.Gaps)
				_, err = ListStreams(p)
				assert.ErrorContains(t, err, path, "global discovery still validates the other session")
				assert.Equal(t, before, leafSnapshot(t, filepath.Dir(filepath.Dir(p.RecordRoot()))))
			})
		}
	}
}

func TestRepositoryLeafSelectedAuditIgnoresOtherLiveFile(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "other-fixture"}
	path := s.LivePath(p)
	badAuditLeaf(t, path, "dangling-link", t.TempDir(), nil)
	_, err := ReadStream(p, Stream{Namespace: SessionNS, Session: "fixture"})
	require.NoError(t, err)
	_, err = ListStreams(p)
	assert.ErrorContains(t, err, path)
}

func TestRepositoryLeafUnrelatedNamesAndMissingSources(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	store := filepath.Dir(filepath.Dir(p.RecordRoot()))
	before := leafSnapshot(t, store)
	for _, s := range []Stream{{Namespace: SessionNS, Session: "fixture"}, {Namespace: MissionNS, Mission: "m-2026-11-04-001", Session: "fixture"}} {
		data, err := ReadStream(p, s)
		require.NoError(t, err)
		assert.Empty(t, data.Lines)
		assert.Empty(t, data.Legacy)
	}
	_, err := ListStreams(p)
	require.NoError(t, err)
	assert.Equal(t, before, leafSnapshot(t, store), "absence creates no directory or lock")
	dir := filepath.Join(p.RecordRoot(), "sessions", "2026-11-04-fixture")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	for _, name := range []string{"README", "contract.yaml", ".audit-0000000000000000100-0000000000000000100.jsonl.tmp"} {
		require.NoError(t, os.Symlink(filepath.Join(store, "absent"), filepath.Join(dir, name)))
	}
	// Preserve compatibility for non-file malformed directory names: unlike a
	// recognized chunk leaf they are not historical input files.
	require.NoError(t, os.Mkdir(filepath.Join(dir, "audit-not-a-chunk"), 0o700))
	_, err = ScanSealedDir(dir, SessionNS, "")
	require.NoError(t, err)
	_, err = ReadStream(p, Stream{Namespace: SessionNS, Session: "fixture"})
	require.NoError(t, err)
	_, err = ListStreams(p)
	require.NoError(t, err)
	// A malformed FILE still refuses exactly as before; the type repair does not
	// turn chunk-namespace near misses into harmless siblings.
	badName := filepath.Join(dir, "audit-malformed.jsonl")
	require.NoError(t, os.WriteFile(badName, nil, 0o600))
	_, err = ScanSealedDir(dir, SessionNS, "")
	assert.ErrorContains(t, err, "malformed chunk name")
}

func TestRepositoryLeafLegacyAndCountReaders(t *testing.T) {
	for _, kind := range []string{"directory", "inside-link", "outside-link", "dangling-link", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := LiveAuditPath(root, "fixture")
			badAuditLeaf(t, path, kind, root, nil)
			_, err := MaxLegacyTS(path)
			assert.ErrorContains(t, err, path)
			_, err = SessionUnsealedCount(root, "fixture")
			assert.ErrorContains(t, err, path)
			path = LiveMissionLogPath(root, "m-2026-11-04-001", "fixture")
			// Use another root for the second link target so the test never
			// overwrites the first sentinel while constructing its fixture.
			badAuditLeaf(t, path, kind, t.TempDir(), nil)
			_, err = MissionUnsealedCount(root, root, "m-2026-11-04-001", "fixture")
			assert.ErrorContains(t, err, path)
			_, err = ListLiveLogSessions(filepath.Dir(path))
			assert.ErrorContains(t, err, path)
		})
	}
}

// ListStreamsByName names the streams without validating their artifacts: a
// directory under a chunk's name leaves ListStreams failing and the name-only
// listing intact.
func TestListStreamsByNameIgnoresArtifactTypes(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "fixture"}
	path, _ := leafArtifact(t, ArchiveRoot(p), s, "chunk")
	require.NoError(t, os.MkdirAll(path, 0o700))
	_, err := ListStreams(p)
	assert.Error(t, err)
	streams, err := ListStreamsByName(p)
	require.NoError(t, err)
	assert.Contains(t, streams, s)
}
