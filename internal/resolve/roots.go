package resolve

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
			l.Config, l.ConfigErr = loadRepoConfig(l.Store)
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
