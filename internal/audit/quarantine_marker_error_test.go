package audit

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type quarantineFixtureFile struct {
	body []byte
	info os.FileInfo
}

func quarantineFixtureFiles(t *testing.T, roots ...string) map[string]quarantineFixtureFile {
	t.Helper()
	files := make(map[string]quarantineFixtureFile)
	for _, root := range roots {
		require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[path] = quarantineFixtureFile{body: body, info: info}
			return nil
		}))
	}
	return files
}

func TestRepositoryQuarantineUnreadablePriorMarkerRefusesBeforeWrites(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires Unix file permissions and a non-root user")
	}
	for _, ns := range []Namespace{SessionNS, MissionNS} {
		name := "session"
		if ns == MissionNS {
			name = "mission"
		}
		for _, location := range []string{"local", "published"} {
			for _, unreadable := range []string{"local", "published"} {
				if location == "local" && unreadable == "published" {
					continue
				}
				for _, phase := range []string{"repeated-damage", "completed-recovery"} {
					t.Run(name+"/"+location+"/"+unreadable+"-marker/"+phase, func(t *testing.T) {
						p := repositoryPolicy(t, layout.RecordBlame)
						s := Stream{Namespace: ns, Session: "s"}
						cn := ChunkName{Namespace: ns, First: 100, Last: 200}
						if ns == MissionNS {
							s.Mission, cn.Session = "m", s.Session
						}
						dir, err := ArchiveDir(p, s, "2026-11-04")
						require.NoError(t, err)
						source := filepath.Join(dir, cn.ChunkFile())
						roots := []string{p.LocalRoot()}
						if location == "published" {
							rel, err := filepath.Rel(ArchiveRoot(p), source)
							require.NoError(t, err)
							source = filepath.Join(p.CheckoutRecordRoot(), rel)
							roots = append(roots, p.CheckoutRecordRoot())
						}
						line := func(ts int64) []byte {
							return []byte(`{"ts":"` + FormatLineTS(ts) + `","tool":"Read","record":"blame"}` + "\n")
						}
						require.NoError(t, WriteLocalAtomic(p, s.LivePath(p), append(line(100), line(200)...)))
						require.NoError(t, WriteLocalAtomic(p, source, append(line(300), []byte("first damage\n")...)))
						guard := func(raw []byte) ([]byte, string, bool) { return raw, "", false }
						_, err = QuarantineLocal(p, s, source, dir, cn, "first", guard)
						require.NoError(t, err)
						markerPath := filepath.Join(dir, cn.MarkerFile())
						if unreadable == "published" {
							markerPath = filepath.Join(filepath.Dir(source), cn.MarkerFile())
						}
						prior, err := ReadMarker(markerPath)
						require.NoError(t, err)
						prior.VerifiedLast, prior.UnrecoveredFirst, prior.UnrecoveredLast = 500, 50, 600
						body, err := MarshalMarker(prior)
						require.NoError(t, err)
						require.NoError(t, replaceAuditFile(p, markerPath, body))
						if phase == "repeated-damage" {
							require.NoError(t, replaceAuditFile(p, source, []byte("second damage\n")))
						}
						before := quarantineFixtureFiles(t, roots...)
						require.NoError(t, os.Chmod(markerPath, 0))
						t.Cleanup(func() { assert.NoError(t, os.Chmod(markerPath, 0o600)) })
						_, err = ReadMarker(markerPath)
						require.ErrorIs(t, err, os.ErrPermission, "the real marker read must be denied")

						oldWrite := writeFile
						writes := 0
						writeFile = func(f *os.File, body []byte) (int, error) {
							writes++
							return oldWrite(f, body)
						}
						t.Cleanup(func() { writeFile = oldWrite })
						_, err = QuarantineLocal(p, s, source, dir, cn, "second", guard)
						writeFile = oldWrite
						assert.ErrorIs(t, err, os.ErrPermission, "an unreadable prior marker is not absent")
						if assert.Error(t, err) {
							assert.ErrorContains(t, err, markerPath)
						}
						assert.Zero(t, writes, "preflight must precede raw evidence and retirement writes")
						require.NoError(t, os.Chmod(markerPath, 0o600))
						after := quarantineFixtureFiles(t, roots...)
						assert.Len(t, after, len(before), "refusal must not create evidence or recovery files")
						for path, original := range before {
							actual, ok := after[path]
							if !assert.True(t, ok, "refusal removed %s", path) {
								continue
							}
							assert.Equal(t, original.body, actual.body, path)
							assert.True(t, os.SameFile(original.info, actual.info), "refusal replaced %s", path)
							assert.Equal(t, original.info.Mode(), actual.info.Mode(), path)
							assert.Equal(t, original.info.ModTime(), actual.info.ModTime(), path)
						}
						_, err = QuarantineLocal(p, s, source, dir, cn, "second", guard)
						assert.NoError(t, err, "restoring readability must allow recovery or its completed no-op")
					})
				}
			}
		}
	}
}

// A missing prior marker resumes; an undecodable one may have recorded a loss
// of unknown range, so the retirement refuses and leaves its bytes alone.
func TestRepositoryQuarantineMissingOrMalformedPriorMarkerResumes(t *testing.T) {
	for _, location := range []string{"local", "published"} {
		for _, content := range []struct {
			name string
			body string
		}{
			{name: "missing"},
			{name: "torn", body: `{"chunk":"`},
			{name: "malformed", body: "not JSON\n"},
			{name: "empty-chunk", body: "{}\n"},
		} {
			t.Run(location+"/"+content.name, func(t *testing.T) {
				p := repositoryPolicy(t, layout.RecordBlame)
				s := Stream{Namespace: SessionNS, Session: "s"}
				cn := ChunkName{Namespace: SessionNS, First: 100, Last: 200}
				dir, err := ArchiveDir(p, s, "2026-11-04")
				require.NoError(t, err)
				source := filepath.Join(p.CheckoutRecordRoot(), "sessions", "2026-11-04-s", cn.ChunkFile())
				require.NoError(t, WriteLocalAtomic(p, source, []byte("damage\n")))
				guard := func(raw []byte) ([]byte, string, bool) { return raw, "", false }
				_, err = QuarantineLocal(p, s, source, dir, cn, "fixture", guard)
				require.NoError(t, err)
				markerPath := filepath.Join(dir, cn.MarkerFile())
				if location == "published" {
					markerPath = filepath.Join(filepath.Dir(source), cn.MarkerFile())
				}
				if content.name == "missing" {
					require.NoError(t, os.Remove(markerPath))
				} else {
					require.NoError(t, replaceAuditFile(p, markerPath, []byte(content.body)))
				}
				if location == "published" {
					// Exercise the resume path rather than the local completion shortcut.
					localPath := filepath.Join(dir, cn.MarkerFile())
					local, err := ReadMarker(localPath)
					require.NoError(t, err)
					local.RetiredSHA256 = ""
					body, err := MarshalMarker(local)
					require.NoError(t, err)
					require.NoError(t, ReplaceLocalFile(p, localPath, body))
				}
				if content.name != "missing" {
					_, err = QuarantineLocal(p, s, source, dir, cn, "fixture", guard)
					require.ErrorIs(t, err, ErrTornMarker)
					got, rerr := os.ReadFile(markerPath)
					require.NoError(t, rerr)
					assert.Equal(t, content.body, string(got), "the undecodable marker is left byte-identical")
					return
				}
				m, err := QuarantineLocal(p, s, source, dir, cn, "fixture", guard)
				require.NoError(t, err)
				assert.NotEmpty(t, m.RetiredSHA256)
				assert.True(t, m.HasGap())
				assert.Equal(t, int64(100), m.UnrecoveredFirst)
				assert.Equal(t, int64(200), m.UnrecoveredLast)
				_, err = ReadMarker(markerPath)
				assert.NoError(t, err, "recovery must replace the absent or malformed marker")
			})
		}
	}
}
