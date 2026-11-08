//go:build windows

package adr

// Go does not expose a portable directory flush on Windows. Like mission's
// syncDir, this is deliberately a no-op: file contents are synced, but directory
// entry crash durability is not confirmed on this unsupported runtime target.
func syncDirectory(string) error { return nil }
