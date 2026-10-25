package audit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// writeTemp creates a chunk temp file and stamps its mtime.
func writeTemp(t *testing.T, dir, name string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func TestSweepStaleTempsMissionScoping(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	ownFresh := MissionTempFile("A", 100, 200)
	ownStale := MissionTempFile("A", 300, 400)
	foreignFresh := MissionTempFile("B", 100, 200)
	foreignStale := MissionTempFile("B", 300, 400)

	writeTemp(t, dir, ownFresh, now)
	writeTemp(t, dir, ownStale, now.Add(-time.Hour))
	writeTemp(t, dir, foreignFresh, now)                                // B mid-write → must survive
	writeTemp(t, dir, foreignStale, now.Add(-staleTempAge-time.Minute)) // B crash orphan → sweep

	if err := SweepStaleTemps(dir, MissionNS, "A", now); err != nil {
		t.Fatal(err)
	}

	if exists(dir, ownFresh) {
		t.Error("own session's temp must be swept regardless of age (fresh)")
	}
	if exists(dir, ownStale) {
		t.Error("own session's temp must be swept regardless of age (stale)")
	}
	if !exists(dir, foreignFresh) {
		t.Error("a foreign session's in-flight temp must survive the sweep")
	}
	if exists(dir, foreignStale) {
		t.Error("a foreign session's crash-orphaned temp must be swept")
	}
}

func TestSweepStaleTempsSessionNamespaceSweepsAll(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	fresh := SessionTempFile(100, 200)
	writeTemp(t, dir, fresh, now)
	// The session dir is single-session, so every temp is this session's.
	if err := SweepStaleTemps(dir, SessionNS, "", now); err != nil {
		t.Fatal(err)
	}
	if exists(dir, fresh) {
		t.Error("session-namespace sweep must remove all temps")
	}
}

func TestSweepStaleTempsMissingDir(t *testing.T) {
	if err := SweepStaleTemps(filepath.Join(t.TempDir(), "nope"), MissionNS, "A", time.Now()); err != nil {
		t.Fatalf("missing dir must be a no-op, got %v", err)
	}
}

// TestSweepStaleTempsLegacyName: a temp left by a crash of a version that
// staged with ".tmp" is still recognised and swept.
func TestSweepStaleTempsLegacyName(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	own := "." + MissionChunkFile("A", 100, 200) + layout.LegacyTmpSuffix
	foreign := "." + MissionChunkFile("B", 100, 200) + layout.LegacyTmpSuffix
	writeTemp(t, dir, own, now)
	writeTemp(t, dir, foreign, now.Add(-staleTempAge-time.Minute))

	if err := SweepStaleTemps(dir, MissionNS, "A", now); err != nil {
		t.Fatal(err)
	}
	if exists(dir, own) {
		t.Error("own session's legacy temp must be swept")
	}
	if exists(dir, foreign) {
		t.Error("a foreign session's stale legacy temp must be swept")
	}
}

// A failed write whose temp chunk cannot be removed says so: the leftover is
// named, not hidden.
func TestWriteChunkAtomicReportsAFailedTempRemoval(t *testing.T) {
	dir := t.TempDir()
	orig := removeFile
	t.Cleanup(func() { removeFile = orig })
	removeFile = func(string) error { return errors.New("read-only file system") }

	// The rename fails: its target directory does not exist.
	chunk := filepath.Join("missing", "c.jsonl")
	err := WriteChunkAtomic(dir, "t.tmp", chunk, [][]byte{[]byte("x")})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"renaming temp chunk", "removing the temp chunk " + filepath.Join(dir, "t.tmp") + " also failed", "read-only file system"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}

	removeFile = orig
	err = WriteChunkAtomic(dir, "t.tmp", chunk, [][]byte{[]byte("x")})
	if err == nil || strings.Contains(err.Error(), "also failed") {
		t.Errorf("a removable temp: err = %v", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "t.tmp")); serr == nil {
		t.Error("the temp chunk was not removed")
	}
}
