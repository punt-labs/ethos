package main

type terminationWatcher struct{}

func newTerminationWatcher(string) *terminationWatcher { return &terminationWatcher{} }
func (*terminationWatcher) Start(func(int, error))     {}
func (*terminationWatcher) Close()                     {}
