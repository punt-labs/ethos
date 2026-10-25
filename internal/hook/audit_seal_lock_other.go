//go:build !linux

package hook

// lockHeld cannot tell whether a process holds path on this system.
func lockHeld(string) (held, known bool) { return false, false }
