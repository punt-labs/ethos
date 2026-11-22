package diagnostic

import (
	"errors"
	"log/slog"
	"time"
)

// Open degrades safely on Windows, which the shared private sink does not support.
func Open(string) (*slog.Logger, func(), error) {
	return Discard(), func() {}, errors.New("private diagnostics unsupported on Windows")
}

type Command string
type Tool string
type Action string

type Invocation struct {
	Command  Command
	ExitCode int
	Duration time.Duration
}

type ToolCall struct {
	Tool     Tool
	Action   Action
	Failed   bool
	Duration time.Duration
}

// Usage is disabled on unsupported Windows hosts.
type Usage struct{}

func NewUsage(*slog.Logger, []Command) *Usage { return &Usage{} }
func (*Usage) Invocation(string, Invocation)  {}
func (*Usage) ToolCall(ToolCall)              {}
func (*Usage) Close()                         {}
