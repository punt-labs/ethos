package hook

import (
	"encoding/binary"
	"errors"

	"golang.org/x/sys/unix"
)

func handoffLoadAverage() (float64, error) {
	data, err := unix.SysctlRaw("vm.loadavg")
	if err != nil {
		return 0, err
	}
	if len(data) != 24 {
		return 0, errors.New("unexpected vm.loadavg size")
	}
	scale := binary.NativeEndian.Uint64(data[16:])
	if scale == 0 {
		return 0, errors.New("invalid vm.loadavg scale")
	}
	return float64(binary.NativeEndian.Uint32(data)) / float64(scale), nil
}
