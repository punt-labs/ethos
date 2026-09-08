//go:build windows

package mission

// syncDir is a deliberate no-op on Windows: NTFS does not support
// FlushFileBuffers on a plain directory handle the way POSIX fsync(2)
// supports it on a directory fd (see syncdir_unix.go), so there is no
// portable os-package equivalent to call here. Windows is not a
// supported/shipped target for this module (no release binary, no CI
// job — see CHANGELOG) — this keeps the module cross-compiling
// (GOOS=windows GOARCH=amd64 go build ./...) without claiming a
// durability guarantee this platform's build cannot deliver via this
// mechanism.
var syncDir = func(dir string) error { return nil }
