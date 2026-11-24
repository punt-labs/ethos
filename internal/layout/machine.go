package layout

import (
	"errors"
	"os"
	"path/filepath"
)

// MachineRoot returns the absolute location of Ethos's global state.
func MachineRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return MachineRootFromHome(home)
}

// MachineRootFromHome resolves the same root when a caller also needs home
// for files owned by another application.
func MachineRootFromHome(home string) (string, error) {
	if !filepath.IsAbs(home) {
		return "", errors.New("home directory must be absolute")
	}
	return filepath.Join(home, ".punt-labs", "ethos"), nil
}
