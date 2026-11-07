package repair

import (
	"errors"
	"os"
)

const supportsDeletion = false

func tryLock(*os.File) error {
	return errors.New("repair deletion is unsupported on Windows; use a Unix host with all writers stopped")
}
