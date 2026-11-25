package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/stretchr/testify/require"
)

type countedAppendTail struct {
	io.ReaderAt
	requested int
}

func (r *countedAppendTail) ReadAt(p []byte, off int64) (int, error) {
	r.requested += len(p)
	return r.ReaderAt.ReadAt(p, off)
}

func floorTestRow(ts int64) []byte {
	return []byte(fmt.Sprintf(`{"ts":%q,"tool":"Read"}`, FormatLineTS(ts)) + "\n")
}

func TestAppendTailReadBudgetAndValidation(t *testing.T) {
	for _, size := range []int{appendAnchorBytes, 8 << 20} {
		prefix := bytes.Repeat([]byte("\n"), size)
		cp := appendFloor{End: int64(size), Floor: 100, Anchor: sha256.Sum256(prefix[size-appendAnchorBytes:])}
		for _, test := range []struct {
			name   string
			suffix []byte
			ok     bool
			read   bool
		}{
			{"complete and torn", append(floorTestRow(101), '{'), true, true},
			{"backwards", floorTestRow(99), false, true},
			{"duplicate key", []byte(`{"ts":"1970-01-01T00:00:00.000000101Z","x":1,"x":2}` + "\n"), false, true},
			{"oversize", bytes.Repeat([]byte("x"), appendSuffixBytes+1), false, false},
		} {
			data := append(bytes.Clone(prefix), test.suffix...)
			r := &countedAppendTail{ReaderAt: bytes.NewReader(data)}
			end, floor, ok := checkAppendTail(r, int64(len(data)), cp)
			require.Equal(t, test.ok, ok, test.name)
			want := 0
			if test.read {
				want = appendAnchorBytes + len(test.suffix)
			}
			require.Equal(t, want, r.requested, test.name)
			if ok {
				require.EqualValues(t, len(data)-1, end)
				require.EqualValues(t, 101, floor)
			}
		}
	}
}

func TestAppendSessionRechecksNewHistory(t *testing.T) {
	for _, kind := range []string{"local archive", "checkout publication"} {
		t.Run(kind, func(t *testing.T) {
			p := repositoryPolicy(t, layout.RecordBlame)
			s := Stream{Namespace: SessionNS, Session: "s"}
			appendLine := func(want int64) {
				t.Helper()
				require.NoError(t, WithStreamLock(p, s, func() error {
					return AppendSession(p, s, time.Unix(0, 100), func(ts int64) ([]byte, error) {
						require.Equal(t, want, ts)
						return bytes.TrimSuffix(floorTestRow(ts), []byte("\n")), nil
					})
				}))
			}
			published := filepath.Join(p.CheckoutRecordRoot(), "sessions")
			require.NoError(t, os.MkdirAll(published, 0o700))
			appendLine(100)
			dir := filepath.Join(published, "2026-11-04-s")
			if kind == "local archive" {
				var err error
				dir, err = ArchiveDir(p, s, "2026-11-04")
				require.NoError(t, err)
			}
			require.NoError(t, WriteLocalAtomic(p, filepath.Join(dir, SessionChunkFile(200, 200)), floorTestRow(200)))
			appendLine(201)
			appendLine(202)
		})
	}
}

func TestAppendSessionDoesNotAdvanceCheckpointUntilDirectorySync(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bounded appends require a stable file identity")
	}
	p := repositoryPolicy(t, layout.RecordBlame)
	s := Stream{Namespace: SessionNS, Session: "s"}
	appendLine := func() error {
		return WithStreamLock(p, s, func() error {
			return AppendSession(p, s, time.Unix(0, 100), func(ts int64) ([]byte, error) {
				return bytes.TrimSuffix(floorTestRow(ts), []byte("\n")), nil
			})
		})
	}
	require.NoError(t, appendLine())
	cp, ok := readAppendFloor(p, s)
	require.True(t, ok)
	oldSync := syncAuditDirectory
	t.Cleanup(func() { syncAuditDirectory = oldSync })
	syncAuditDirectory = func(string) error { return errors.New("directory sync failed") }
	require.ErrorIs(t, appendLine(), ErrAppendDurabilityUnconfirmed)
	got, ok := readAppendFloor(p, s)
	require.True(t, ok)
	require.Equal(t, cp, got)
	data, err := ReadStream(p, s)
	require.NoError(t, err)
	require.EqualValues(t, 101, data.Floor, "the append was applied, not rolled back")
	syncAuditDirectory = oldSync
	require.NoError(t, appendLine())
	got, ok = readAppendFloor(p, s)
	require.True(t, ok)
	require.EqualValues(t, 102, got.Floor)
}

func TestAppendFloorInvalidationSyncBoundaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bounded appends require a stable file identity")
	}
	for failAt := 0; failAt <= 6; failAt++ {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			root := t.TempDir()
			p, err := layout.NewRepository(root, root, layout.RecordBlame)
			require.NoError(t, err)
			s := Stream{Namespace: SessionNS, Session: "s"}
			now := time.Unix(0, 100)
			require.NoError(t, WithStreamLock(p, s, func() error {
				return AppendSession(p, s, now, func(ts int64) ([]byte, error) {
					return bytes.TrimSuffix(floorTestRow(ts), []byte("\n")), nil
				})
			}))
			old, err := os.ReadFile(s.LivePath(p))
			require.NoError(t, err)
			_, valid := readAppendFloor(p, s)
			require.True(t, valid)
			oldFile, oldDir := fsyncFile, syncAuditDirectory
			t.Cleanup(func() { fsyncFile, syncAuditDirectory = oldFile, oldDir })
			fault := errors.New("interrupted invalidation sync")
			at := 0
			next := func() error {
				at++
				if at == failAt {
					return fault
				}
				return nil
			}
			fsyncFile = func(f *os.File) error {
				if err := next(); err != nil {
					return err
				}
				return oldFile(f)
			}
			syncAuditDirectory = func(dir string) error {
				if err := next(); err != nil {
					return err
				}
				return oldDir(dir)
			}
			err = WithStreamLock(p, s, func() error {
				_, err := RedactLive(context.Background(), p, s, func(b []byte) ([]byte, string, bool) {
					return bytes.Replace(b, []byte("Read"), []byte("Bash"), 1), "", false
				}, 0)
				return err
			})
			got, readErr := os.ReadFile(s.LivePath(p))
			require.NoError(t, readErr)
			_, valid = readAppendFloor(p, s)
			if failAt != 0 {
				require.ErrorIs(t, err, fault)
				require.Equal(t, old, got, "history must not mutate before marker and every ancestor are durable")
			} else {
				require.NoError(t, err)
				require.False(t, valid, "mutated history must not retain the old floor")
				require.Contains(t, string(got), "Bash")
			}
		})
	}
}

func TestWarmAppendRollsBackFailedSyncWithoutMovingFloor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bounded appends require a stable file identity")
	}
	root := t.TempDir()
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	s := Stream{Namespace: SessionNS, Session: "s"}
	now := time.Unix(0, 100)
	appendLine := func() error {
		return WithStreamLock(p, s, func() error {
			return AppendSession(p, s, now, func(ts int64) ([]byte, error) {
				return bytes.TrimSuffix(floorTestRow(ts), []byte("\n")), nil
			})
		})
	}
	require.NoError(t, appendLine())
	cp, ok := readAppendFloor(p, s)
	require.True(t, ok)
	before, err := os.ReadFile(s.LivePath(p))
	require.NoError(t, err)
	oldSync := fsyncFile
	t.Cleanup(func() { fsyncFile = oldSync })
	at := 0
	fault := errors.New("append sync failed")
	fsyncFile = func(f *os.File) error {
		at++
		if at == 1 {
			return fault
		}
		return oldSync(f)
	}
	require.ErrorIs(t, appendLine(), fault)
	require.Equal(t, 2, at, "rollback must sync")
	after, err := os.ReadFile(s.LivePath(p))
	require.NoError(t, err)
	require.Equal(t, before, after)
	got, ok := readAppendFloor(p, s)
	require.True(t, ok)
	require.Equal(t, cp, got)
	require.NoError(t, appendLine())
	got, ok = readAppendFloor(p, s)
	require.True(t, ok)
	require.Equal(t, cp.Floor+1, got.Floor)
}

func TestAppendFloorRecoveryAfterProcessCrash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bounded appends require a stable file identity")
	}
	const testRoot = "ETHOS_APPEND_CRASH_ROOT"
	const testStep = "ETHOS_APPEND_CRASH_STEP"
	const crash = 89
	s := Stream{Namespace: SessionNS, Session: "s"}
	if root := os.Getenv(testRoot); root != "" {
		p, err := layout.NewRepository(root, root, layout.RecordBlame)
		require.NoError(t, err)
		stop, err := strconv.Atoi(os.Getenv(testStep))
		require.NoError(t, err)
		at := 0
		after := func(err error) error {
			at++
			if at == stop && err == nil {
				os.Exit(crash)
			}
			return err
		}
		oldFile, oldDir := fsyncFile, syncAuditDirectory
		fsyncFile = func(f *os.File) error { return after(oldFile(f)) }
		syncAuditDirectory = func(dir string) error { return after(oldDir(dir)) }
		require.NoError(t, WithStreamLock(p, s, func() error {
			_, err := RedactLive(context.Background(), p, s, func(b []byte) ([]byte, string, bool) {
				return bytes.Replace(b, []byte("Read"), []byte("Bash"), 1), "", false
			}, 0)
			return err
		}))
		t.Fatal("child reached the end without crashing")
	}
	// The first six syncs are the invalidation data and five ancestors.
	// The next two are the replacement data and its directory installation.
	for step := 1; step <= 8; step++ {
		t.Run(strconv.Itoa(step), func(t *testing.T) {
			root := t.TempDir()
			p, err := layout.NewRepository(root, root, layout.RecordBlame)
			require.NoError(t, err)
			require.NoError(t, WithStreamLock(p, s, func() error {
				return AppendSession(p, s, time.Unix(0, 100), func(ts int64) ([]byte, error) {
					return bytes.TrimSuffix(floorTestRow(ts), []byte("\n")), nil
				})
			}))
			cmd := exec.Command(os.Args[0], "-test.run=^TestAppendFloorRecoveryAfterProcessCrash$")
			cmd.Env = append(os.Environ(), testRoot+"="+root, testStep+"="+strconv.Itoa(step))
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit, "%s", out)
			require.Equal(t, crash, exit.ExitCode(), "%s", out)
			line, err := os.ReadFile(s.LivePath(p))
			require.NoError(t, err)
			_, floorStillValid := readAppendFloor(p, s)
			if step == 8 {
				require.Contains(t, string(line), "Bash")
			}
			if !bytes.Equal(line, floorTestRow(100)) {
				require.False(t, floorStillValid)
			}
			require.NoError(t, WithStreamLock(p, s, func() error {
				return AppendSession(p, s, time.Unix(0, 99), func(ts int64) ([]byte, error) {
					require.EqualValues(t, 101, ts)
					return bytes.TrimSuffix(floorTestRow(ts), []byte("\n")), nil
				})
			}))
		})
	}
}
