package resolve

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// Located is where the repository is, found once by Roots: the store root, its
// .punt-labs/ethos, the checkout and the repo config, each with the error that
// says why it could not be found. A part is "" with a nil error only when it is
// genuinely absent (outside a repository, no store yet, no config yet); every
// other failure is its error. Store and Checkout are absolute physical paths,
// so a root alias never redirects the local paths derived from this snapshot.
type Located struct {
	Store    string // the repository whose .punt-labs/ethos is the store; "" outside a repository
	Ethos    string // <Store>/.punt-labs/ethos; "" when there is no store yet
	Checkout string // the current work tree; "" outside a repository
	Config   *RepoConfig

	StoreErr    error
	EthosErr    error
	CheckoutErr error
	ConfigErr   error
}

// Roots is the one place ethos finds the repository. It locates the store and
// the checkout (git is the authority; ETHOS_REPO_ROOT overrides), resolves both
// to absolute physical roots before deriving paths or loading config, validates the
// whole .punt-labs/ethos path (a dangling symlink at any level, a
// non-directory or a permission error is an error; only an absent component is
// "no store yet"), and reads the repo config (a dangling ethos.yaml is an
// error, not "no config"). Everything else — the CLI, the hooks, ethos serve,
// doctor, setup, the UI — takes its answer from Roots, or from a value built
// from it. The locating functions are unexported, so code outside this
// package cannot locate on its own.
func Roots() Located {
	var l Located
	l.Store, l.StoreErr = storeRootIfAny()
	if l.StoreErr == nil {
		l.Store, l.StoreErr = physicalRoot(l.Store, "store")
	}
	l.Checkout, l.CheckoutErr = checkoutRootIfAny()
	if l.CheckoutErr == nil {
		l.Checkout, l.CheckoutErr = physicalRoot(l.Checkout, "checkout")
	}
	if l.StoreErr == nil && l.Store != "" {
		l.Ethos, l.EthosErr = ethosRootOf(l.Store)
		if l.EthosErr == nil {
			var cerr error
			l.Config, cerr = loadRepoConfig(l.Store)
			if cerr != nil {
				var field *layout.ValidationError
				invalidRecord := errors.As(cerr, &field) && field.Field == "record"
				l.ConfigErr = &ConfigError{Err: cerr, What: "the repository configuration could not be read", Effect: defaultEffect, Answer: defaultAnswer, Policy: invalidRecord}
			}
		}
	}
	return l
}

// physicalRoot normalizes a located root, not its descendants. An absent root
// remains absent; any normalization failure refuses that part of the snapshot.
func physicalRoot(root, part string) (string, error) {
	if root == "" {
		return "", nil
	}
	abs := root
	if !filepath.IsAbs(root) {
		var err error
		if filepath.VolumeName(root) != "" || os.IsPathSeparator(root[0]) {
			// Windows drive-relative and rooted paths need the OS's drive context.
			abs, err = filepath.Abs(root)
		} else {
			var cwd string
			cwd, err = os.Getwd()
			if err == nil {
				cwd, err = filepath.EvalSymlinks(cwd)
			}
			// Do not clean before resolving links: link/.. and a logical PWD
			// can otherwise select a different directory from os.Stat(root).
			abs = cwd + string(filepath.Separator) + root
		}
		if err != nil {
			return "", fmt.Errorf("making %s root %q absolute: %w", part, root, err)
		}
	}
	physical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolving physical %s root %q: %w", part, root, err)
	}
	return physical, nil
}

// StoreFailure is the first failure to find the store itself: the store root,
// its .punt-labs/ethos path, or the config in it. The checkout is not part of
// it.
func (l Located) StoreFailure() error {
	for _, e := range []error{l.StoreErr, l.EthosErr, l.ConfigErr} {
		if e != nil {
			return e
		}
	}
	return nil
}

// Err is the first failure to find anything a store-reading command needs: the
// store, its .punt-labs/ethos path, the config, or the checkout.
func (l Located) Err() error {
	for _, e := range []error{l.StoreErr, l.EthosErr, l.ConfigErr, l.CheckoutErr} {
		if e != nil {
			return e
		}
	}
	return nil
}

// Layout consumes this already-loaded location/configuration snapshot, without
// discovering roots or rereading configuration. A missing store directory is
// fine, but an absent repository is not: callers choose global storage explicitly.
func (l Located) Layout() (layout.Policy, error) {
	if err := l.Err(); err != nil {
		return layout.Policy{}, err
	}
	if l.Store == "" {
		return layout.Policy{}, ErrNotInRepo
	}
	mode := layout.RecordBlame
	if l.Config != nil {
		var err error
		mode, err = l.Config.RecordingMode()
		if err != nil {
			return layout.Policy{}, err
		}
	}
	return layout.NewRepository(l.Store, l.Checkout, mode)
}

const (
	defaultEffect = "so this repo's agent persona and team were not loaded."
	defaultAnswer = "this answer is from the global store only"
)

// ConfigError is a failure in a repository's configuration, as opposed to the
// location of its store: an unreadable or malformed ethos.yaml, an unusable
// bundle or resolution policy. It says how to put itself in a sentence, so a
// notice never claims the store was not located when it was, nor that only the
// global store was used when the repository layer was.
type ConfigError struct {
	Err    error
	What   string // "the repository configuration could not be read"
	Effect string // what it cost a session: "so this repo's agent persona ..."
	Answer string // what an MCP answer was built from
	// Policy marks a bundle or resolution-policy failure: the repository's
	// store was located and read, so its layer is used, without the policy.
	Policy bool
}

func (e *ConfigError) Error() string { return e.Err.Error() }
func (e *ConfigError) Unwrap() error { return e.Err }

// Describe returns how to say err in a notice: what failed, what it cost a
// session, and what an answer given despite it was built from. A ConfigError
// says it itself; any other error is the store not being located.
func Describe(err error) (what, effect, answer string) {
	var ce *ConfigError
	if errors.As(err, &ce) {
		return ce.What, ce.Effect, ce.Answer
	}
	return "the repository store could not be located", defaultEffect, defaultAnswer
}
