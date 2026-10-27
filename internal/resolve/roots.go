package resolve

import "errors"

// Located is where the repository is, found once by Roots: the store root, its
// .punt-labs/ethos, the checkout and the repo config, each with the error that
// says why it could not be found. A part is "" with a nil error only when it is
// genuinely absent (outside a repository, no store yet, no config yet); every
// other failure is its error.
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
// the checkout (git is the authority; ETHOS_REPO_ROOT overrides), validates the
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
	if l.StoreErr == nil && l.Store != "" {
		l.Ethos, l.EthosErr = ethosRootOf(l.Store)
		if l.EthosErr == nil {
			var cerr error
			l.Config, cerr = loadRepoConfig(l.Store)
			if cerr != nil {
				l.ConfigErr = &ConfigError{Err: cerr, What: "the repository configuration could not be read", Effect: defaultEffect, Answer: defaultAnswer}
			}
		}
	}
	l.Checkout, l.CheckoutErr = checkoutRootIfAny()
	return l
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
