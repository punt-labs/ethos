//go:build unix

package resolve

import "syscall"

// statDevice returns the device number of the filesystem holding p.
func statDevice(p string) (uint64, bool) {
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		return 0, false
	}
	return uint64(st.Dev), true // Dev is uint64 on linux, int32 on darwin
}
