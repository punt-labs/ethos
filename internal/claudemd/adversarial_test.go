package claudemd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestRegisterDanglingSymlinkErrors pins the behavior for a CLAUDE.md that is a
// symlink to a nonexistent target. resolveTarget resolves through the link with
// filepath.EvalSymlinks, which fails on a dangling link, so Register errors and
// writes nothing rather than materializing a file at the broken target. A
// dangling host is malformed; erroring is the safe choice under §2.4.
func TestRegisterDanglingSymlinkErrors(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "CLAUDE.md")
	if err := os.Symlink(filepath.Join(dir, "nonexistent-target"), link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	wrote, err := Register(link, canonical)
	if err == nil {
		t.Fatal("Register accepted a dangling symlink host")
	}
	if wrote {
		t.Error("Register reported a write on the error path")
	}
	// The link must survive untouched and its target must still not exist.
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("dangling link removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nonexistent-target")); !os.IsNotExist(err) {
		t.Error("Register materialized the broken target")
	}
}

// TestRegisterUnwritableDirLeavesNoHalfState pins the no-half-state guarantee
// when the target's directory is unwritable: writeAtomic cannot create its temp
// file, so Register errors and the existing host file is byte-unchanged. The
// atomic temp+rename never reaches the rename, so a reader never sees a torn or
// truncated CLAUDE.md.
func TestRegisterUnwritableDirLeavesNoHalfState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory mode bits do not gate file creation on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "CLAUDE.md")
	const body = "# host prose\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o755) }()

	wrote, err := Register(p, canonical)
	if err == nil {
		t.Fatal("Register succeeded against an unwritable directory")
	}
	if wrote {
		t.Error("Register reported a write on the error path")
	}
	// Restore write access to read the file back and confirm it is intact.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != body {
		t.Errorf("host file changed on the error path: got %q, want %q", got, body)
	}
	if strings.Contains(read(t, p), canonical) {
		t.Error("a partial write leaked the import into the host file")
	}
}
