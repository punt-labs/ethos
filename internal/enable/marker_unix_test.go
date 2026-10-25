//go:build unix

package enable

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A marker that exists but is not a regular file is refused before anything is
// written: no write through a link to nothing, no hang opening a FIFO.
func TestEnableRefusesAMarkerThatIsNotAFile(t *testing.T) {
	cases := map[string]func(t *testing.T, marker string){
		"dangling link": func(t *testing.T, marker string) {
			if err := os.Symlink(filepath.Join(filepath.Dir(marker), "nowhere"), marker); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
		},
		"fifo":      func(t *testing.T, marker string) { mustNil(t, syscall.Mkfifo(marker, 0o644)) },
		"directory": func(t *testing.T, marker string) { mustNil(t, os.Mkdir(marker, 0o755)) },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			dir := gitRepo(t)
			marker := filepath.Join(dir, markerRel)
			mustNil(t, os.MkdirAll(filepath.Dir(marker), 0o755))
			mk(t, marker)

			done := make(chan error, 1)
			go func() { _, err := Enable(dir); done <- err }()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "not a regular file") {
					t.Fatalf("Enable error = %v, want a refusal naming the marker", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Enable hung on the marker")
			}
			if exists(filepath.Join(dir, ".punt-labs", "ethos", "ETHOS-SETUP.md")) {
				t.Error("Enable deposited files before refusing")
			}
			if li, err := os.Lstat(marker); err != nil || li.Mode().IsRegular() {
				t.Errorf("the marker was replaced: %v %v", li, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "nowhere")); err == nil {
				t.Error("a file was written through the link")
			}
		})
	}
}

func mustNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
