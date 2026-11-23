//go:build !windows

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// postEditOrphanPoll is how often post-edit checks whether its hook shell
// has died.
const postEditOrphanPoll = 100 * time.Millisecond

// runPostEdit runs post-edit under a context that ends when the process is
// signalled or its parent, the hook shell, dies. Ending it stops in-flight
// edit checks, which run in process groups of their own that a signal to the
// hook's group does not reach. After a signal, post-edit ends itself with it
// once run returns, so its exit status still reports the signal. See DES-029.
func runPostEdit(parent context.Context, run func(context.Context)) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var caught []os.Signal
	for _, s := range []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
		if !signal.Ignored(s) {
			caught = append(caught, s)
		}
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, caught...)
	defer signal.Stop(sig)
	received := make(chan os.Signal, 1)
	done := make(chan struct{})
	watched := make(chan struct{})
	ppid := os.Getppid()
	go func() {
		defer close(watched)
		tick := time.NewTicker(postEditOrphanPoll)
		defer tick.Stop()
		for {
			select {
			case s := <-sig:
				received <- s
				cancel()
				return
			case <-tick.C:
				if os.Getppid() != ppid {
					cancel()
					return
				}
			case <-done:
				return
			}
		}
	}()
	run(ctx)
	close(done)
	<-watched
	select {
	case s := <-received:
		dieOf(s.(syscall.Signal))
	default:
	}
}

// dieOf records the invocation's end, restores sig's default disposition and
// raises it on the process.
func dieOf(sig syscall.Signal) {
	finishProcess(128+int(sig), nil)
	signal.Reset(sig)
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		os.Exit(128 + int(sig))
	}
	time.Sleep(250 * time.Millisecond)
	os.Exit(128 + int(sig))
}
