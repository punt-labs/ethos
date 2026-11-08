//go:build !windows

package layout

import (
	"errors"
	"os"
)

func syncRepositoryDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
