package main

import "context"

func runPostEdit(parent context.Context, run func(context.Context)) { run(parent) }
