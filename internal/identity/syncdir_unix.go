//go:build !windows

package identity

import "os"

// syncDirectory confirms directory-entry durability after a publication, using
// the same POSIX mechanism as internal/mission. Close errors are secondary to
// fsync errors and do not themselves provide a durability signal.
func syncDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
