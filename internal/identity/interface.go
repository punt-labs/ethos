package identity

// IdentityStore defines the contract for identity storage operations.
// Both Store (single-root) and LayeredStore (repo + global) implement this.
type IdentityStore interface {
	Load(handle string, opts ...LoadOption) (*Identity, error)
	Save(id *Identity) error
	List() (*ListResult, error)
	FindBy(field, value string) (*Identity, error)
	Exists(handle string) bool
	Update(handle string, fn func(*Identity) error) error
	ValidateRefs(id *Identity) error
	Root() string
	IdentitiesDir() string
	Path(handle string) string
	ExtDir(handle string) string
	ExtGet(handle, namespace, key string) (map[string]string, error)
	ExtSet(handle, namespace, key, value string) error
	ExtDel(handle, namespace, key string) error
	ExtList(handle string) ([]string, error)
}

// Compile-time check: *Store satisfies IdentityStore.
var _ IdentityStore = (*Store)(nil)
