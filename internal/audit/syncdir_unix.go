//go:build !windows

package audit

import (
	"errors"
	"os"
)

func syncAuditDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
