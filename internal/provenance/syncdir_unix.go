//go:build !windows

package provenance

import (
	"errors"
	"os"
)

const directoryDurability = "confirmed"

func syncDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
