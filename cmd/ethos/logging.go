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
	"github.com/spf13/cobra"
)

var processLogger = diagnostic.Discard()
var processUsage *diagnostic.Usage
var finishProcess = func(int, error) {}

func startLogging() {
	command := rootCmd
	if found, _, _ := rootCmd.Find(os.Args[1:]); found != nil {
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
	var commands []diagnostic.Command
	var collect func(*cobra.Command)
	collect = func(cmd *cobra.Command) {
		commands = append(commands, diagnostic.Command(strings.TrimPrefix(cmd.CommandPath(), "ethos ")))
		for _, child := range cmd.Commands() {
			collect(child)
		}
	}
	collect(rootCmd)
	processUsage = diagnostic.NewUsage(logger.With("component", "ethos.telemetry"), commands)
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
			if role != "mcp" {
				processUsage.Invocation(role, diagnostic.Invocation{Command: diagnostic.Command(path), ExitCode: code, Duration: time.Since(started)})
			}
			processUsage.Close()
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
