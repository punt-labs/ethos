//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestRepositoryLeafTypesActualCLI(t *testing.T) {
	testRepositoryLeafActualCLI(t, []string{"directory", "inside-link", "outside-link", "dangling-link"})
}

func TestRepositoryLeafSpecialActualCLI(t *testing.T) {
	kinds := []string{"fifo"}
	if os.Geteuid() != 0 {
		kinds = append(kinds, "unreadable")
	}
	testRepositoryLeafActualCLI(t, kinds)
}

func testRepositoryLeafActualCLI(t *testing.T, kinds []string) {
	t.Helper()
	require.NotEmpty(t, ethosBinary, "actual CLI must build, not skip")
	for _, mission := range []bool{false, true} {
		for _, layer := range []string{"live", "archive", "record"} {
			artifacts := []string{"legacy", "chunk", "quarantine", "corrupt"}
			if layer == "live" {
				artifacts = []string{"live"}
			}
			for _, artifact := range artifacts {
				for _, kind := range kinds {
					t.Run(layer+"/"+map[bool]string{false: "session", true: "mission"}[mission]+"/"+artifact+"/"+kind, func(t *testing.T) {
						_, root, _ := auditMigrateEnv(t)
						p, err := layout.NewRepository(root, root, layout.RecordBlame)
						require.NoError(t, err)
						stampCurrent(t, p)
						base := map[string]string{"live": p.LocalRoot(), "archive": audit.ArchiveRoot(p), "record": p.RecordRoot()}[layer]
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
						switch kind {
						case "directory":
							require.NoError(t, os.Mkdir(path, 0o700))
						case "fifo":
							out, err := exec.Command("mkfifo", path).CombinedOutput()
							require.NoError(t, err, "%s", out)
						case "unreadable":
							require.NoError(t, os.WriteFile(path, raw, 0o600))
							require.NoError(t, os.Chmod(path, 0))
							t.Cleanup(func() { require.NoError(t, os.Chmod(path, 0o600)) })
						default:
							if kind != "dangling-link" {
								require.NoError(t, os.WriteFile(target, raw, 0o600))
							}
							require.NoError(t, os.Symlink(target, path))
						}
						// A healthy independent chunk cannot hide a bad source.
						mirror := p.RecordRoot()
						if layer == "record" {
							mirror = audit.ArchiveRoot(p)
						}
						copyRel := "sessions/2026-11-04-fixture"
						if mission {
							copyRel = "missions/m-2026-11-04-001"
						}
						copyDir := filepath.Join(mirror, copyRel)
						require.NoError(t, os.MkdirAll(copyDir, 0o700))
						require.NoError(t, os.WriteFile(filepath.Join(copyDir, cn.ChunkFile()), []byte(`{"ts":"`+audit.FormatLineTS(100)+`","tool":"Read","delegation_id":"d"}`+"\n"), 0o600))
						before := auditLeafCLISnapshot(t, root)
						for _, args := range [][]string{{"audit", "show", "--delegation", "d", "--format", "json"}, {"audit", "seal"}} {
							cmd := exec.Command(ethosBinary, args...)
							cmd.Dir, cmd.Env = root, append(os.Environ(), "ETHOS_REPO_ROOT=")
							out, err := cmd.CombinedOutput()
							var exit *exec.ExitError
							if assert.ErrorAs(t, err, &exit, "%v: %s", args, out) {
								want := 1
								if args[1] == "seal" {
									want = 2
								}
								assert.Equal(t, want, exit.ExitCode(), "%v: %s", args, out)
								t.Logf("%v: exit %d; refused %s", args, exit.ExitCode(), path)
							}
							assert.Contains(t, string(out), path)
							assert.NotContains(t, string(out), "nothing pending")
							assert.NotContains(t, string(out), "audit seal: complete")
							assert.Equal(t, before, auditLeafCLISnapshot(t, root))
						}
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

func auditLeafCLISnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	// Exclude fixture Git metadata; include all product paths and link targets.
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && e.Name() == ".git" {
			return filepath.SkipDir
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if e.Type()&os.ModeSymlink != 0 {
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
