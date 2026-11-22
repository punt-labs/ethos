//go:build !windows

package diagnostic

import (
	"log/slog"

	"github.com/punt-labs/punt-observe-go/logging"
)

// Open returns a process logger and its cleanup, falling back to a discard sink.
func Open(role string) (*slog.Logger, func(), error) {
	h, err := logging.New("ethos", role)
	if err != nil {
		return Discard(), func() {}, err
	}
	return slog.New(h).With("component", "ethos."+role), func() { _ = h.Close() }, nil
}
