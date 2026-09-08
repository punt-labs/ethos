//go:build windows

package mission

// syncDir is a deliberate no-op on Windows. The reason is mechanical,
// not a policy choice: NTFS does not support FlushFileBuffers on a
// plain directory handle the way POSIX fsync(2) supports it on a
// directory fd (see syncdir_unix.go), and Go's stdlib exposes no
// portable equivalent to call in its place — there is nothing for
// this function to do on this platform. The practical consequence is
// that a rename's directory-entry durability is never confirmed on a
// Windows build; the file's own contents are still synced before the
// rename (writeContractFile, store.go), only the directory-metadata
// half of F2's fix (PR #508 round 2) is unavailable here.
//
// This module DOES cross-compile for Windows
// (`GOOS=windows GOARCH=amd64 go build ./...` succeeds — verified
// manually per review, not by a CI job; grep .github/workflows/ and
// find none) but Windows itself is, per CHANGELOG.md, "still not a
// supported/shipped target (no release binary, no CI job)" — no
// release artifact targets it and nothing tests it automatically. The
// no-op above exists so the build keeps succeeding without this
// function claiming a guarantee the platform cannot deliver through
// this mechanism; it is not evidence of Windows support one way or
// the other, and the CHANGELOG line above is the citable source of
// truth on that question, not this comment.
var syncDir = func(dir string) error { return nil }
