//go:build !windows

package hook

import (
	"errors"
	"os"
)

func syncAgentDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
