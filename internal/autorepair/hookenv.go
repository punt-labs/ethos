package autorepair

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// HookEnv is the Env a hook passes to the cheap pass: the located roots and
// policy (zero unless a repository that has used ethos was located cleanly),
// the global root, the clock and the repair log. It writes nothing.
//
// It has the signature of the engine's HookEnv; the engine's engine.go
// replaces this file at the merge.
func HookEnv(roots resolve.Located, global string) (Env, error) {
	if global == "" {
		return Env{}, errors.New("building the hook environment: no global store root")
	}
	var store, checkout string
	if roots.Err() == nil {
		store, checkout = roots.Store, roots.Checkout
	}
	env := Env{Global: global, Now: time.Now(), Log: newFileLog(global, store, checkout, "")}
	if store == "" {
		return env, nil
	}
	if info, err := os.Stat(filepath.Join(store, ".punt-labs")); err != nil || !info.IsDir() {
		return env, nil
	}
	p, err := roots.Layout()
	if err != nil {
		return env, nil
	}
	env.Roots, env.Policy = roots, p
	return env, nil
}
