//go:build !windows

package main

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
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
	caught := []os.Signal{syscall.SIGTERM}
	if w.role != "ui" {
		caught = append(caught, os.Interrupt)
	}
	for _, received := range caught {
		if !signal.Ignored(received) {
			signal.Notify(w.signals, received)
		}
	}
	go func() {
		select {
		case received := <-w.signals:
			code := 128 + int(received.(syscall.Signal))
			logServerTermination()
			finish(code, nil)
			signal.Stop(w.signals)
			if err := syscall.Kill(os.Getpid(), received.(syscall.Signal)); err != nil {
				os.Exit(code)
			}
			// Allow gitenv's 1s WaitDelay, 1s groupGrace and 250ms dieWait.
			time.Sleep(3 * time.Second)
			os.Exit(code)
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
