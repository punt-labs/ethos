//go:build !linux

package hook

// lockHeldOnThisSystem cannot tell whether a process holds path on this system.
func lockHeldOnThisSystem(string) (held bool, unknown string) {
	return false, "this system cannot tell whether a process holds it"
}
