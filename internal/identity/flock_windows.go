//go:build windows

package identity

import (
	"fmt"
	"os"
)

func flock(_ *os.File) error {
	return fmt.Errorf("file locking not supported on Windows")
}

func funlock(_ *os.File) error {
	return nil
}
