//go:build !linux && !darwin

package hook

import "errors"

func handoffLoadAverage() (float64, error) {
	return 0, errors.ErrUnsupported
}
