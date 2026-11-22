package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/punt-labs/ethos/v4/internal/diagnostic"
)

var processLogger = diagnostic.Discard()
var finishProcess = func(int, error) {}

func startLogging() {
	command := rootCmd
	if found, _, err := rootCmd.Find(os.Args[1:]); err == nil {
		command = found
	}
	path := strings.TrimPrefix(command.CommandPath(), "ethos ")
	role := "cli"
	top := command
	for top.HasParent() && top.Parent().HasParent() {
		top = top.Parent()
	}
	switch top.Name() {
	case "serve":
		role = "mcp"
	case "hook":
		role = "hook"
	case "ui":
		role = "ui"
	}
	logger, closeLog, err := diagnostic.Open(role)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ethos: private diagnostics unavailable")
	}
	processLogger = logger
	rootCmd.SetContext(diagnostic.WithLogger(context.Background(), logger))
	started := time.Now()
	var once sync.Once
	finishProcess = func(code int, err error) {
		once.Do(func() {
			if role == "cli" || role == "hook" {
				outcome := "completed"
				if code != 0 {
					outcome = "failed"
				}
				attrs := []any{"command", path, "outcome", outcome, "exit_code", code, "duration_ms", time.Since(started).Milliseconds()}
				if err != nil {
					attrs = append(attrs, "error_type", diagnostic.ErrorType(err))
				}
				logger.Info("Invocation finished", attrs...)
			}
			closeLog()
		})
	}
}

func exitProcess(code int, err error) {
	finishProcess(code, err)
	os.Exit(code)
}

func logServerStop(logger *slog.Logger, started time.Time, err error) {
	reason := "completed"
	if err != nil {
		reason = "failed"
	}
	logger.Info("Server stopped", "reason", reason, "error_type", diagnostic.ErrorType(err), "duration_ms", time.Since(started).Milliseconds())
}
