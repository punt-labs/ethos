//go:build windows

package hook

// Go has no portable Windows directory-fsync equivalent. As with identity and
// mission persistence, this preserves compilation, not a claim of equivalent
// crash durability on native Windows.
func syncAgentDirectory(string) error { return nil }
