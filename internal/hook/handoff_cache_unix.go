//go:build linux || darwin

package hook

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func pinHandoffStripe(ctx context.Context, path string) (*os.File, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	waited := false
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_SH|unix.LOCK_NB)
		if err == nil {
			return f, waited, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return nil, waited, errors.Join(err, f.Close())
		}
		waited = true
		select {
		case <-ctx.Done():
			return nil, waited, errors.Join(ctx.Err(), f.Close())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func withUnpinnedStripe(pin *os.File, prune func() error) (busy bool, err error) {
	fd := int(pin.Fd())
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return true, nil
		}
		return false, err
	}
	defer func() { err = errors.Join(err, unix.Flock(fd, unix.LOCK_SH)) }()
	return false, prune()
}
