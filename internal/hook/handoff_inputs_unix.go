//go:build linux || darwin

package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openGateInput(root *os.Root, input string) (*os.File, error) {
	if !filepath.IsLocal(input) || filepath.Clean(input) != input || input == "." {
		return nil, fmt.Errorf("invalid gate input %q", input)
	}
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	parts := strings.Split(input, "/")
	for i, part := range parts {
		flags := unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		fd, err := unix.Openat(int(dir.Fd()), part, flags, 0)
		_ = dir.Close()
		if err != nil {
			return nil, err
		}
		dir = os.NewFile(uintptr(fd), input)
	}
	return dir, nil
}
