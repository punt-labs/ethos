package audit

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepositoryQuarantineSameByteRetirementResumes(t *testing.T) {
	for _, boundary := range []string{"invalidation-file-sync", "invalidation-directory-sync", "removal-sync", "final-marker"} {
		t.Run(boundary, func(t *testing.T) {
			p := repositoryPolicy(t, layout.RecordBlame)
			s := Stream{Namespace: SessionNS, Session: "s"}
			cn := ChunkName{Namespace: SessionNS, First: 100, Last: 200}
			dir, err := ArchiveDir(p, s, "2026-11-04")
			require.NoError(t, err)
			source := filepath.Join(dir, cn.ChunkFile())
			line := func(ts int64) []byte {
				return []byte(`{"ts":"` + FormatLineTS(ts) + `","tool":"Read","record":"blame"}` + "\n")
			}
			live := append(line(100), line(200)...)
			damage := []byte("damage\n")
			require.NoError(t, WriteLocalAtomic(p, s.LivePath(p), live))
			require.NoError(t, WriteLocalAtomic(p, source, damage))
			guard := func(raw []byte) ([]byte, string, bool) { return raw, "", false }
			prior, err := QuarantineLocal(p, s, source, dir, cn, "first", guard)
			require.NoError(t, err)
			require.False(t, prior.HasGap())
			require.NotEmpty(t, prior.RetiredSHA256)
			recovered, err := os.ReadFile(source)
			require.NoError(t, err)
			require.Equal(t, live, recovered)
			require.NoError(t, ReplaceLocalFile(p, source, damage))
			oldSync, oldFsync := syncAuditDirectory, fsyncFile
			t.Cleanup(func() { syncAuditDirectory, fsyncFile = oldSync, oldFsync })
			injected := errors.New("injected same-byte " + boundary)
			removed, failed := false, false
			syncAuditDirectory = func(path string) error {
				if path == dir {
					if boundary == "invalidation-directory-sync" {
						m, err := ReadMarker(filepath.Join(dir, cn.MarkerFile()))
						if err == nil && m.RetiredSHA256 == "" {
							failed = true
							return injected
						}
					}
					if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
						removed = true
						if boundary == "removal-sync" {
							failed = true
							return injected
						}
					}
				}
				return oldSync(path)
			}
			fsyncFile = func(f *os.File) error {
				if removed && boundary == "final-marker" || boundary == "invalidation-file-sync" {
					body, err := os.ReadFile(f.Name())
					if err != nil {
						return err
					}
					var m Marker
					if json.Unmarshal(body, &m) == nil && m.Chunk == cn.Stem() &&
						(boundary != "invalidation-file-sync" || m.RetiredSHA256 == "") {
						failed = true
						return injected
					}
				}
				return oldFsync(f)
			}
			_, err = QuarantineLocal(p, s, source, dir, cn, "second", guard)
			syncAuditDirectory, fsyncFile = oldSync, oldFsync
			require.True(t, failed, "the selected interruption boundary must execute")
			require.ErrorIs(t, err, injected)
			if boundary == "removal-sync" || boundary == "final-marker" {
				require.True(t, removed)
			} else {
				require.False(t, removed, "failed invalidation must not remove the source")
				retained, err := os.ReadFile(source)
				require.NoError(t, err)
				require.Equal(t, damage, retained)
			}
			_, err = ScanSealedDir(dir, SessionNS, "")
			if boundary != "invalidation-file-sync" {
				assert.ErrorContains(t, err, "quarantine incomplete", "an identical evidence digest is not completion of a new retirement")
			}
			_, err = ReadStream(p, s)
			assert.Error(t, err, "healthy live lines cannot hide an incomplete or still-corrupt archive")
			if boundary != "invalidation-file-sync" {
				assert.ErrorContains(t, err, "quarantine incomplete")
			}
			m, err := QuarantineLocal(p, s, source, dir, cn, "second", guard)
			require.NoError(t, err)
			require.False(t, m.HasGap(), "pending retirement must not invent a permanent gap")
			require.Equal(t, int64(200), m.VerifiedLast)
			require.Equal(t, prior.RetiredSHA256, m.RetiredSHA256, "evidence remains content-addressed")
			recovered, err = os.ReadFile(source)
			assert.NoError(t, err, "retry must recreate the removed archive, not accept the old completion")
			assert.Equal(t, live, recovered)
			data, err := ReadStream(p, s)
			require.NoError(t, err)
			require.Len(t, data.Lines, 2)
			require.Empty(t, data.Gaps)
			files, err := filepath.Glob(filepath.Join(p.LocalRoot(), "quarantine", "*", "*"))
			require.NoError(t, err)
			require.Empty(t, files, "the damaged bytes are deleted once recovery completes")
		})
	}
}

func TestRepositoryQuarantineRepeatedRetirementResumesAfterOldCompletion(t *testing.T) {
	for _, ns := range []Namespace{SessionNS, MissionNS} {
		namespace := "session"
		if ns == MissionNS {
			namespace = "mission"
		}
		for _, published := range []bool{false, true} {
			for _, boundary := range []string{"removal-sync", "final-marker"} {
				name := namespace + "/local/" + boundary
				if published {
					name = namespace + "/published/" + boundary
				}
				t.Run(name, func(t *testing.T) {
					p := repositoryPolicy(t, layout.RecordBlame)
					s := Stream{Namespace: ns, Session: "s"}
					cn := ChunkName{Namespace: ns, First: 100, Last: 200}
					if ns == MissionNS {
						s.Mission, cn.Session = "m", s.Session
					}
					dir, err := ArchiveDir(p, s, "2026-11-04")
					require.NoError(t, err)
					source := filepath.Join(dir, cn.ChunkFile())
					if published {
						rel, err := filepath.Rel(ArchiveRoot(p), source)
						require.NoError(t, err)
						source = filepath.Join(p.CheckoutRecordRoot(), rel)
					}
					line := func(ts int64) []byte {
						return []byte(`{"ts":"` + FormatLineTS(ts) + `","tool":"Read","record":"blame"}` + "\n")
					}
					require.NoError(t, WriteLocalAtomic(p, s.LivePath(p), append(line(100), line(200)...)))
					firstDamage := []byte("first damage\n")
					require.NoError(t, WriteLocalAtomic(p, source, firstDamage))
					guard := func(raw []byte) ([]byte, string, bool) { return raw, "", false }
					prior, err := QuarantineLocal(p, s, source, dir, cn, "first", guard)
					require.NoError(t, err)
					require.False(t, prior.HasGap())
					require.Equal(t, int64(200), prior.VerifiedLast)
					secondDamage := append(line(300), []byte("second corrupt tail\n")...)
					if published {
						require.NoError(t, WriteLocalAtomic(p, source, secondDamage))
					} else {
						require.NoError(t, ReplaceLocalFile(p, source, secondDamage))
					}
					oldSync, oldFsync := syncAuditDirectory, fsyncFile
					t.Cleanup(func() { syncAuditDirectory, fsyncFile = oldSync, oldFsync })
					injected := errors.New("injected " + boundary)
					removed, failed := false, false
					syncAuditDirectory = func(path string) error {
						if path == filepath.Dir(source) {
							if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
								removed = true
								if boundary == "removal-sync" {
									failed = true
									return injected
								}
							}
						}
						return oldSync(path)
					}
					fsyncFile = func(f *os.File) error {
						if removed && boundary == "final-marker" {
							b, err := os.ReadFile(f.Name())
							if err != nil {
								return err
							}
							var m Marker
							if json.Unmarshal(b, &m) == nil && m.Chunk == cn.Stem() {
								failed = true
								return injected
							}
						}
						return oldFsync(f)
					}
					_, err = QuarantineLocal(p, s, source, dir, cn, "second", guard)
					syncAuditDirectory, fsyncFile = oldSync, oldFsync
					require.True(t, removed)
					require.True(t, failed, "the selected interruption boundary must execute")
					require.ErrorIs(t, err, injected)
					_, err = ScanSealedDir(dir, ns, cn.Session)
					require.ErrorContains(t, err, "quarantine incomplete", "old completion cannot cover new retirement metadata")
					_, err = ReadStream(p, s)
					require.ErrorContains(t, err, "quarantine incomplete", "readers must not accept the old floor")
					// The decodable ts-300 line of the second damage is recovered
					// alongside the live 100 and 200: no gap, three lines.
					m, err := QuarantineLocal(p, s, source, dir, cn, "second", guard)
					require.NoError(t, err)
					require.Equal(t, int64(300), m.VerifiedLast)
					require.False(t, m.HasGap())
					data, err := ReadStream(p, s)
					require.NoError(t, err)
					require.Len(t, data.Lines, 3)
					files, err := filepath.Glob(filepath.Join(p.LocalRoot(), "quarantine", "*", "*"))
					require.NoError(t, err)
					require.Empty(t, files, "the damaged bytes are deleted once recovery completes")
				})
			}
		}
	}
}

func TestRepositoryQuarantineLegacyCompletionRebindsRetirement(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	cn := ChunkName{Namespace: SessionNS, First: 100, Last: 100}
	dir, err := ArchiveDir(p, s, "2026-11-04")
	require.NoError(t, err)
	source := filepath.Join(dir, cn.ChunkFile())
	line := []byte(`{"ts":"` + FormatLineTS(100) + `","tool":"Read"}` + "\n")
	require.NoError(t, WriteLocalAtomic(p, s.LivePath(p), line))
	require.NoError(t, WriteLocalAtomic(p, source, []byte("damage\n")))
	guard := func(raw []byte) ([]byte, string, bool) { return raw, "", false }
	m, err := QuarantineLocal(p, s, source, dir, cn, "fixture", guard)
	require.NoError(t, err)
	// A pre-generation marker is not proof that its retirement pointer completed.
	m.RetiredSHA256 = ""
	body, err := MarshalMarker(m)
	require.NoError(t, err)
	require.NoError(t, ReplaceLocalFile(p, filepath.Join(dir, cn.MarkerFile()), body))
	_, err = ReadStream(p, s)
	require.ErrorContains(t, err, "quarantine incomplete")
	m, err = QuarantineLocal(p, s, source, dir, cn, "fixture", guard)
	require.NoError(t, err)
	require.NotEmpty(t, m.RetiredSHA256)
	require.False(t, m.HasGap())
	data, err := ReadStream(p, s)
	require.NoError(t, err)
	require.Equal(t, int64(100), data.Floor)
	require.Len(t, data.Lines, 1)
}

func TestRepositoryQuarantineRepeatedRetirementKeepsHighestMarkerBounds(t *testing.T) {
	for _, higher := range []string{"local", "published"} {
		t.Run(higher, func(t *testing.T) {
			p := repositoryPolicy(t, layout.RecordBlame)
			s := Stream{Namespace: SessionNS, Session: "s"}
			cn := ChunkName{Namespace: SessionNS, First: 100, Last: 200}
			dir, err := ArchiveDir(p, s, "2026-11-04")
			require.NoError(t, err)
			source := filepath.Join(p.CheckoutRecordRoot(), "sessions", "2026-11-04-s", cn.ChunkFile())
			local := Marker{Chunk: cn.Stem(), VerifiedLast: 300, UnrecoveredFirst: 100, UnrecoveredLast: 300}
			shared := local
			if higher == "local" {
				local.VerifiedLast, local.UnrecoveredFirst, local.UnrecoveredLast = 500, 50, 600
			} else {
				shared.VerifiedLast, shared.UnrecoveredFirst, shared.UnrecoveredLast = 500, 50, 600
			}
			for path, m := range map[string]Marker{
				filepath.Join(dir, cn.MarkerFile()):                  local,
				filepath.Join(filepath.Dir(source), cn.MarkerFile()): shared,
			} {
				body, err := MarshalMarker(m)
				require.NoError(t, err)
				require.NoError(t, WriteLocalAtomic(p, path, body))
			}
			require.NoError(t, WriteLocalAtomic(p, source, []byte("no parseable timestamp\n")))
			guard := func(raw []byte) ([]byte, string, bool) { return raw, "", false }
			m, err := QuarantineLocal(p, s, source, dir, cn, "fixture", guard)
			require.NoError(t, err)
			shared, err = ReadMarker(filepath.Join(filepath.Dir(source), cn.MarkerFile()))
			require.NoError(t, err)
			for _, marker := range []Marker{m, shared} {
				require.Equal(t, int64(500), marker.VerifiedLast, "preserve verified timestamps, not unverified gap endpoints")
				require.Equal(t, int64(50), marker.UnrecoveredFirst)
				require.Equal(t, int64(600), marker.UnrecoveredLast)
			}
		})
	}
}

// Recovery redacts a live line it re-seals instead of refusing the quarantine,
// and deletes the damaged bytes once it completes.
func TestQuarantineLocalRedactsRecoveredLine(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	cn := ChunkName{Namespace: SessionNS, First: 100, Last: 100}
	dir, err := ArchiveDir(p, s, "2026-11-04")
	require.NoError(t, err)
	source := filepath.Join(dir, cn.ChunkFile())
	require.NoError(t, WriteLocalAtomic(p, s.LivePath(p), []byte(`{"ts":"`+FormatLineTS(100)+`","tool":"Secret"}`+"\n")))
	require.NoError(t, WriteLocalAtomic(p, source, []byte("damage\n")))

	m, err := QuarantineLocal(p, s, source, dir, cn, "fixture", maskGuard)
	require.NoError(t, err, "a line the guard changes no longer refuses the recovery")
	assert.False(t, m.HasGap(), "the named range is recovered whole from the live file")
	b, err := os.ReadFile(source)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"tool":"Masked"`)
	assert.NotContains(t, string(b), "Secret")
	files, err := filepath.Glob(filepath.Join(p.LocalRoot(), "quarantine", "*", "*"))
	require.NoError(t, err)
	assert.Empty(t, files, "no raw copy of the damaged bytes remains")
}

// A recovered line that contradicts another surviving chunk is not installed.
func TestQuarantineLocalRefusesLineConflictingWithSurvivor(t *testing.T) {
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	dir, err := ArchiveDir(p, s, "2026-11-04")
	require.NoError(t, err)
	survivor := ChunkName{Namespace: SessionNS, First: 100, Last: 100}
	require.NoError(t, WriteLocalAtomic(p, filepath.Join(dir, survivor.ChunkFile()), []byte(`{"ts":"`+FormatLineTS(100)+`","tool":"Read"}`+"\n")))
	cn := ChunkName{Namespace: SessionNS, First: 100, Last: 200}
	source := filepath.Join(dir, cn.ChunkFile())
	require.NoError(t, WriteLocalAtomic(p, source, []byte("damage\n")))
	// The only live line for ts 100 contradicts the surviving chunk.
	require.NoError(t, ReplaceLocalFile(p, s.LivePath(p), []byte(`{"ts":"`+FormatLineTS(100)+`","tool":"Bash"}`+"\n")))

	m, err := QuarantineLocal(p, s, source, dir, cn, "fixture", func(raw []byte) ([]byte, string, bool) { return raw, "", false })
	require.NoError(t, err)
	assert.True(t, m.HasGap(), "the refused line is a recorded gap, not an installed conflict")
	_, statErr := os.Stat(source)
	assert.True(t, os.IsNotExist(statErr), "no recovered chunk holds the conflicting line")
}
