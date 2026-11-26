//go:build !linux && !darwin

package hook

import (
	"errors"
	"os"
)

func openGateInput(_ *os.Root, _ string) (*os.File, error) {
	return nil, errors.New("snapshot input verification unavailable on this host")
}
