package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/spf13/cobra"
)

// A command locates the repository ONCE — in the root PersistentPreRunE
// (establishRoots) — and everything it builds reads that one answer
// (repoRoots). No constructor looks the repository up again, so there is no
// second lookup that can disagree with the first after the resolver's cache
// expires, and no failure is turned into "" on the way down.
//
// A lookup that failed is an error in the located value. A store command is
// refused before it runs. A command whose class works without a store
// (storeGlobalOnly: it touches only global stores; storeHandlesError: it
// reports on or repairs the store itself) runs on the global stores: that is
// an explicit choice of its class (repoRoots.Global), never a fallback, and
// it is the only way a failure leaves a constructor with "".

// repoRoots is where the repository is, as located once for a command: the
// resolve.Located (store, .punt-labs/ethos, checkout, config, and each one's
// error) plus whether the command's class runs on the global stores.
type repoRoots struct {
	resolve.Located

	// Global: the command's class runs on the global stores when a lookup
	// failed (see above). False means a failure here is a bug: the command
	// should have been refused first.
	Global bool
}

// locateRoots looks the repository up, once, through resolve.Roots — the one
// place ethos finds it, which validates the whole .punt-labs/ethos path and
// the config. "Outside a repository" is "", nil; any other failure is the error.
func locateRoots() *repoRoots {
	return &repoRoots{Located: resolve.Roots()}
}

// err is the first failure to locate anything a command needs.
func (r *repoRoots) err() error { return r.Located.Err() }

// cmdRoots is the located repository for the running command, set by
// establishRoots and by ethos serve for each handler it builds.
var cmdRoots atomic.Pointer[repoRoots]

// refreshRoots locates the repository again, for a command that has just
// created what it located (setup writes the repo config and the store, enable
// deposits it): the stores built after that point must see it. It is the one
// deliberate second lookup, made by the command that changed the answer.
func refreshRoots() {
	old := cmdRoots.Load()
	r := locateRoots()
	r.Global = old == nil || old.Global
	cmdRoots.Store(r)
}

func init() {
	// The located repository belongs to one command: forget it when the
	// command finishes, so the next one (in a test, or a second Execute in the
	// same process) locates for itself.
	cobra.OnFinalize(func() { cmdRoots.Store(nil) })
}

// currentRoots is the located repository the constructors read. Outside a
// command (a test calling a constructor directly) nothing was established, so
// it locates now, without remembering the answer.
func currentRoots() *repoRoots {
	if r := cmdRoots.Load(); r != nil {
		return r
	}
	r := locateRoots()
	r.Global = true
	return r
}

// storeRoot is the store root for a constructor. A failed lookup is only
// tolerated for a Global command, which gets the global stores ("").
func (r *repoRoots) storeRoot() string {
	r.mustBeUsable(r.StoreErr)
	return r.Store
}

// ethosRoot is .punt-labs/ethos for a constructor; see storeRoot.
func (r *repoRoots) ethosRoot() string {
	if r.StoreErr == nil {
		r.mustBeUsable(r.EthosErr)
	} else {
		r.mustBeUsable(r.StoreErr)
	}
	return r.Ethos
}

// checkoutRoot is the checkout for a constructor; see storeRoot.
func (r *repoRoots) checkoutRoot() string {
	r.mustBeUsable(r.CheckoutErr)
	return r.Checkout
}

// mustBeUsable returns when err is nil or the command is Global (the global
// stores are its explicit choice). Otherwise a store command reached a
// constructor with a failed lookup: it exits 2 naming the cause rather than
// use the global store.
func (r *repoRoots) mustBeUsable(err error) {
	if err == nil || r.Global {
		return
	}
	fmt.Fprintf(os.Stderr, "ethos: %v; refusing to use the global store instead\n", err)
	os.Exit(2)
}

func storeRootForStores() string    { return currentRoots().storeRoot() }
func ethosRootForStores() string    { return currentRoots().ethosRoot() }
func checkoutRootForStores() string { return currentRoots().checkoutRoot() }

// requireCheckout is the checkout root for a command that must run inside a
// repository. Outside one it prints "<name> must run inside a repo" and
// returns usageError (exit 2). Any other failure prints the cause and returns
// failClosed: the command refuses rather than act on a guess.
func requireCheckout(errOut io.Writer, name string) (string, error) {
	r := currentRoots()
	switch {
	case r.CheckoutErr != nil:
		err := r.CheckoutErr
		if errors.Is(err, gitenv.ErrTimeout) && name == "audit seal" {
			err = hook.SealGitError(cwdOrDot(), "locating the checkout (git rev-parse)", err, true)
		}
		fmt.Fprintf(errOut, "ethos: %s: %v\n", name, err)
		return "", failClosed{}
	case r.Checkout == "":
		fmt.Fprintf(errOut, "ethos: %s must run inside a repo\n", name)
		return "", usageError{}
	}
	return r.Checkout, nil
}

// requireStore is requireCheckout for the store root.
func requireStore(errOut io.Writer, name string) (string, error) {
	r := currentRoots()
	switch {
	case r.StoreFailure() != nil:
		fmt.Fprintf(errOut, "ethos: %s: %v\n", name, r.StoreFailure())
		return "", failClosed{}
	case r.Store == "":
		fmt.Fprintf(errOut, "ethos: %s must run inside a repo\n", name)
		return "", usageError{}
	}
	return r.Store, nil
}

func cwdOrDot() string {
	if d, err := os.Getwd(); err == nil {
		return d
	}
	return "."
}
