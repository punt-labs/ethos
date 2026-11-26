//go:build !darwin

package hook

import "os"

func openHandoffRunnerExecutable() (*os.File, error) {
	return os.Open("/proc/self/exe")
}
