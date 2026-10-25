package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// The repository lookups a command makes never read a failure as "not in a
// repository". resolve.StoreRootIfAny, CheckoutRootIfAny and EthosRootIfAny
// return "" only outside a repository; every other failure is an error.
//
// The *OrGlobal helpers below are for store constructors, which cannot return
// an error. They degrade to the global stores on a failure, say so on stderr
// (once per cause), and are safe only because the failure is already handled
// where it matters: a command that touches the store is refused before it
// runs (refuseUnlocatableStore), hook and doctor paths carry the error
// (deps.StoreErr, doctor's "Repository store" row), and ethos serve guards
// every tool. A path that must not degrade calls resolve.StoreRootIfAny or
// requireStore and returns the error.

// rootNotices holds the failures noteRootFailure has already printed.
var rootNotices sync.Map

// noteRootFailure says once per cause that the repository store could not be
// located and the global stores are in use.
func noteRootFailure(err error) {
	if _, dup := rootNotices.LoadOrStore(err.Error(), true); !dup {
		fmt.Fprintf(os.Stderr, "ethos: %v; using the global stores only\n", err)
	}
}

// storeRootOrGlobal is the store root for a store constructor: "" outside a
// repository and, with a notice, when it could not be located.
func storeRootOrGlobal() string {
	root, err := resolve.StoreRootIfAny()
	if err != nil {
		noteRootFailure(err)
		return ""
	}
	return root
}

// ethosRootOrGlobal is storeRootOrGlobal for the .punt-labs/ethos directory
// ("" outside a repository, before the directory exists, or on a failure).
func ethosRootOrGlobal() string {
	root, err := resolve.EthosRootIfAny()
	if err != nil {
		noteRootFailure(err)
		return ""
	}
	return root
}

// checkoutRootOrGlobal is the checkout root for a store constructor.
func checkoutRootOrGlobal() string {
	root, err := resolve.CheckoutRootIfAny()
	if err != nil {
		noteRootFailure(err)
		return ""
	}
	return root
}

// requireCheckout is the checkout root for a command that must run inside a
// repository. Outside one it prints "<name> must run inside a repo" and
// returns usageError (exit 2). Any other failure prints the cause and returns
// failClosed: the command refuses rather than act on a guess.
func requireCheckout(errOut io.Writer, name string) (string, error) {
	root, err := resolve.LocateRepoRoot()
	switch {
	case errors.Is(err, resolve.ErrNotInRepo):
		fmt.Fprintf(errOut, "ethos: %s must run inside a repo\n", name)
		return "", usageError{}
	case err != nil:
		if errors.Is(err, gitenv.ErrTimeout) && name == "audit seal" {
			err = hook.SealGitError(cwdOrDot(), err)
		}
		fmt.Fprintf(errOut, "ethos: %s: %v\n", name, err)
		return "", failClosed{}
	}
	return root, nil
}

// requireStore is requireCheckout for the store root.
func requireStore(errOut io.Writer, name string) (string, error) {
	root, err := resolve.LocateStoreRoot()
	switch {
	case errors.Is(err, resolve.ErrNotInRepo):
		fmt.Fprintf(errOut, "ethos: %s must run inside a repo\n", name)
		return "", usageError{}
	case err != nil:
		fmt.Fprintf(errOut, "ethos: %s: %v\n", name, err)
		return "", failClosed{}
	}
	return root, nil
}

func cwdOrDot() string {
	if d, err := os.Getwd(); err == nil {
		return d
	}
	return "."
}
