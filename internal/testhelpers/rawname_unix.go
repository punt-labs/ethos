//go:build unix

package testhelpers

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// RequireNonUTF8Names skips t when the temp volume refuses a file name that is
// not valid UTF-8 (macOS APFS answers EILSEQ), and fails t on any other error:
// a full disk or a read-only directory must not hide the hazard the caller
// wants tested. Where names are accepted, as on Linux, it returns and the
// caller goes on to use a raw byte in its names.
func RequireNonUTF8Names(t testing.TB) {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "probe\xff")
	err := os.WriteFile(probe, nil, 0o600)
	switch {
	case err == nil:
		return
	case errors.Is(err, syscall.EILSEQ):
		t.Skip("filesystem rejects non-UTF-8 names (EILSEQ); bead ethos-oq01")
	default:
		t.Fatalf("probing for non-UTF-8 file names: %v", err)
	}
}
