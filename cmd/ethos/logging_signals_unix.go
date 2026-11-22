//go:build !windows

package main

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

type terminationWatcher struct {
	role    string
	signals chan os.Signal
	done    chan struct{}
	once    sync.Once
}

func newTerminationWatcher(role string) *terminationWatcher {
	return &terminationWatcher{role: role, signals: make(chan os.Signal, 1), done: make(chan struct{})}
}

func (w *terminationWatcher) Start(finish func(int, error)) {
	if w.role == "mcp" {
		return // ServeStdio already handles SIGINT and SIGTERM gracefully.
	}
	if w.role == "ui" {
		signal.Notify(w.signals, syscall.SIGTERM)
	} else {
		signal.Notify(w.signals, os.Interrupt, syscall.SIGTERM)
	}
	go func() {
		select {
		case received := <-w.signals:
			code := 128 + int(received.(syscall.Signal))
			logServerTermination()
			finish(code, nil)
			signal.Reset(received)
			if err := syscall.Kill(os.Getpid(), received.(syscall.Signal)); err != nil {
				os.Exit(code)
			}
		case <-w.done:
		}
	}()
}

func (w *terminationWatcher) Close() {
	w.once.Do(func() {
		signal.Stop(w.signals)
		close(w.done)
	})
}
