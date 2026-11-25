package hook

import "golang.org/x/sys/unix"

func handoffLoadAverage() (float64, error) {
	var info unix.Sysinfo_t
	if err := unix.Sysinfo(&info); err != nil {
		return 0, err
	}
	return float64(info.Loads[0]) / (1 << 16), nil
}
