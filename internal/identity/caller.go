package identity

import (
	"errors"
	"os"
)

// CallerStore reads the identity of whoever is running ethos. Under DES-057
// repo-only resolution the repo's store holds the team and agent identities
// the repo commits, and nothing else; the caller is a person, not repo data,
// so a caller the repo does not hold is looked up in the global store.
type CallerStore struct {
	IdentityStore        // the repo's store, for everything but the caller lookups
	global        *Store // the global store, consulted after the repo misses
}

// ForCaller returns s with the global fallback for the caller's identity when s
// is a repo-only LayeredStore, and s itself otherwise: a layered store already
// reads global.
func ForCaller(s IdentityStore) IdentityStore {
	ls, ok := s.(*LayeredStore)
	if !ok || !ls.RepoAuthoritative() {
		return s
	}
	return &CallerStore{IdentityStore: s, global: NewStore(ls.GlobalRoot())}
}

// FindBy searches the repo's store, then the global store.
func (c *CallerStore) FindBy(field, value string) (*Identity, error) {
	id, err := c.IdentityStore.FindBy(field, value)
	if err != nil || id != nil {
		return id, err
	}
	return c.global.FindBy(field, value)
}

// Load reads handle from the repo's store, then the global store.
func (c *CallerStore) Load(handle string, opts ...LoadOption) (*Identity, error) {
	id, _, err := c.LoadCaller(handle, opts...)
	return id, err
}

// LoadCaller is Load that also reports whether the identity came from the
// global store. Only a miss falls through; any other error from the repo's
// store (a damaged file, an incomplete repo-only set) is returned as it is.
func (c *CallerStore) LoadCaller(handle string, opts ...LoadOption) (id *Identity, fromGlobal bool, err error) {
	id, err = c.IdentityStore.Load(handle, opts...)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return id, false, err
	}
	id, gerr := c.global.Load(handle, opts...)
	if gerr != nil {
		return nil, false, err // the repo's miss names where ethos looked
	}
	return id, true, nil
}

// Exists reports whether handle is in the repo's store or the global store.
func (c *CallerStore) Exists(handle string) bool {
	return c.IdentityStore.Exists(handle) || c.global.Exists(handle)
}
