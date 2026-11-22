// Package diagnostic configures process-owned private diagnostic logging.
package diagnostic

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// Discard returns a logger with no output.
func Discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type loggerKey struct{}

// WithLogger carries a process logger across an operation boundary.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, logger)
}

// FromContext returns the injected logger or a silent logger for library callers.
func FromContext(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return logger
	}
	return Discard()
}

// ErrorType returns only the innermost error's Go type, never its message.
func ErrorType(err error) string {
	if err == nil {
		return ""
	}
	for errors.Unwrap(err) != nil {
		err = errors.Unwrap(err)
	}
	return fmt.Sprintf("%T", err)
}
