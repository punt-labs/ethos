package main

import "context"

// postEditCancels is false: post-edit installs no signal handling here, so the
// generated hook must not signal it.
const postEditCancels = false

func runPostEdit(parent context.Context, run func(context.Context)) { run(parent) }
