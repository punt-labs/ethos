package resolve

import "path/filepath"

// CheckPolicyOf keeps a repository check reference with its checkout's registry.
// Identity, role, bundle and storage configuration still use the canonical store.
func CheckPolicyOf(loc Located) (ref, root string, err error) {
	if err := loc.StoreFailure(); err != nil {
		return "", "", err
	}
	if loc.CheckoutErr != nil {
		return "", "", loc.CheckoutErr
	}
	root = loc.Ethos
	if root == "" && loc.Store != "" {
		root = filepath.Join(loc.Store, ".punt-labs", "ethos")
	}
	if loc.Config != nil {
		ref = loc.Config.Checks
	}
	if loc.Checkout == "" || loc.Checkout == loc.Store {
		return ref, root, nil
	}
	cfg, err := ConfigOf(loc.Checkout)
	if err != nil {
		return "", "", err
	}
	if cfg != nil && cfg.Checks != "" {
		return cfg.Checks, filepath.Join(loc.Checkout, ".punt-labs", "ethos"), nil
	}
	return ref, root, nil
}
