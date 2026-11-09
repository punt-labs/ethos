//go:build unix

package testhelpers

import (
	"os"
	"testing"
)

// A temp directory that cannot be written is a failure, not a volume that
// refuses the name: the probe must not turn a full or read-only disk into a skip.
func TestRequireNonUTF8Names_FailsOnOtherErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	t.Setenv("TMPDIR", ro)

	ft := &failRecorder{TB: t}
	func() {
		defer func() { _ = recover() }() // Fatalf on the recorder panics to stop the probe
		RequireNonUTF8Names(ft)
	}()
	if !ft.fataled {
		t.Fatal("a write error other than EILSEQ must fail the test")
	}
	if ft.skipped {
		t.Fatal("a write error other than EILSEQ must not skip")
	}
}

type failRecorder struct {
	testing.TB
	fataled, skipped bool
}

func (f *failRecorder) TempDir() string { return os.Getenv("TMPDIR") }
func (f *failRecorder) Fatalf(string, ...any) {
	f.fataled = true
	panic("fatal")
}
func (f *failRecorder) Skip(...any) { f.skipped = true; panic("skip") }
