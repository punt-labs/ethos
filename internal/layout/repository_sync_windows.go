//go:build windows

package layout

// Go has no portable Windows directory-fsync equivalent. This matches existing
// mission/identity persistence: payload Sync is still required, but this no-op
// preserves compilation, not a claim of POSIX-equivalent crash durability.
func syncRepositoryDirectory(string) error { return nil }
