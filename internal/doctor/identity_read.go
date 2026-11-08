package doctor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/identity"
)

// listDiagnosticLayer reuses the bundle layer's nonmigrating reference listing.
// Load, unlike List/FindBy, has a WithoutMigration option and stays on the
// original store so attribute, extension and repository-policy semantics remain.
func listDiagnosticLayer(root string) (*identity.ListResult, error) {
	if err := checkDiagnosticDirectory(root, "identities"); err != nil {
		return nil, err
	}
	// Reference listing also loads extension namespaces. Check their files,
	// not just the identity directory's immediate entries, before listing.
	entries, err := os.ReadDir(filepath.Join(root, "identities"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasSuffix(entry.Name(), ".ext") {
			if err := checkDiagnosticDirectory(root, filepath.Join("identities", entry.Name())); err != nil {
				return nil, err
			}
		}
	}
	return identity.NewLayeredStoreWithBundle(nil, identity.NewStore(root), nil, true).List()
}

func checkDiagnosticDirectory(root, name string) error {
	dir := filepath.Join(root, name)
	if err := hook.CheckAgentPath(root, dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading diagnostic directory %s: %w", dir, err)
	}
	for _, entry := range entries {
		if err := hook.CheckAgentPath(root, filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

type referenceView struct {
	identity.IdentityStore
	roots  []string
	global string
	caller bool
	known  bool
}

var _ identity.IdentityStore = (*referenceView)(nil)
var _ hook.AgentIdentityReader = (*referenceView)(nil)

func references(s identity.IdentityStore) *referenceView {
	v := &referenceView{IdentityStore: s}
	if caller, ok := s.(*identity.CallerStore); ok {
		v = references(caller.IdentityStore)
		v.IdentityStore = caller
		v.caller = true
		return v
	}
	switch s := s.(type) {
	case *identity.LayeredStore:
		v.known = true
		for _, root := range []string{s.RepoRoot(), s.BundleRoot()} {
			if root != "" {
				v.roots = append(v.roots, root)
			}
		}
		v.global = s.GlobalRoot()
		if !s.RepoAuthoritative() && v.global != "" {
			v.roots = append(v.roots, v.global)
		}
	case *identity.Store:
		v.known = true
		v.roots = []string{s.Root()}
	}
	return v
}

func (v *referenceView) readRoots() []string {
	roots := append([]string(nil), v.roots...)
	if v.caller && v.global != "" && !slices.Contains(roots, v.global) {
		roots = append(roots, v.global)
	}
	return roots
}

func (v *referenceView) Load(handle string, opts ...identity.LoadOption) (*identity.Identity, error) {
	// Reject redirected identity and extension inputs before the store reads
	// them. This is a diagnostic, not a sandbox against concurrent path swaps.
	for _, root := range v.readRoots() {
		store := identity.NewStore(root)
		if err := hook.CheckAgentPath(root, store.Path(handle)); err != nil {
			return nil, err
		}
		dir := store.ExtDir(handle)
		if err := hook.CheckAgentPath(root, dir); err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, entry := range entries {
			if err := hook.CheckAgentPath(root, filepath.Join(dir, entry.Name())); err != nil {
				return nil, err
			}
		}
	}
	return v.IdentityStore.Load(handle, append(opts, identity.WithoutMigration())...)
}

func (v *referenceView) LoadAgentReference(handle string) (*identity.Identity, error) {
	return v.Load(handle, identity.Reference(true))
}

func (v *referenceView) List() (*identity.ListResult, error) {
	if !v.known {
		return v.IdentityStore.List() // preserve injected-store errors and semantics
	}
	result := &identity.ListResult{}
	seen := make(map[string]bool)
	for _, root := range v.readRoots() {
		list, err := listDiagnosticLayer(root)
		if err != nil {
			return nil, err
		}
		for _, id := range list.Identities {
			if !seen[id.Handle] {
				seen[id.Handle] = true
				result.Identities = append(result.Identities, id)
			}
		}
		result.Warnings = append(result.Warnings, list.Warnings...)
	}
	return result, nil
}

func (v *referenceView) FindBy(field, value string) (*identity.Identity, error) {
	if !v.known {
		return v.IdentityStore.FindBy(field, value)
	}
	if field != "handle" && field != "email" && field != "github" {
		return nil, fmt.Errorf("unsupported FindBy field: %q", field)
	}
	if value == "" {
		return nil, nil
	}
	for _, root := range v.readRoots() {
		list, err := listDiagnosticLayer(root)
		if err != nil {
			return nil, err
		}
		var found *identity.Identity
		for _, id := range list.Identities {
			if identityValue(id, field) != value {
				continue
			}
			if found != nil {
				return nil, fmt.Errorf("ambiguous identity: 2 matches for %s %q: %s, %s", field, value, found.Handle, id.Handle)
			}
			found = id
		}
		if found != nil {
			if v.caller && root == v.global {
				local, err := v.LoadAgentReference(found.Handle)
				if err != nil {
					return nil, err
				}
				for _, key := range []string{"email", "github"} {
					a, b := identityValue(local, key), identityValue(found, key)
					equal := a == b || key == "email" && strings.EqualFold(a, b)
					if a != "" && b != "" && !equal {
						return nil, fmt.Errorf("global and local identities collide on handle %q (%s differs)", found.Handle, key)
					}
				}
			}
			return found, nil
		}
		if v.caller && len(list.Warnings) > 0 {
			return nil, fmt.Errorf("cannot verify caller identity in %s: %s", root, strings.Join(list.Warnings, "; "))
		}
	}
	return nil, nil
}

func identityValue(id *identity.Identity, field string) string {
	switch field {
	case "handle":
		return id.Handle
	case "email":
		return id.Email
	case "github":
		return id.GitHub
	}
	return ""
}

func (v *referenceView) LoadAgentIdentity(handle string) (*identity.Identity, error) {
	id, err := v.LoadAgentReference(handle)
	if err != nil {
		return nil, err
	}
	check := func(kind attribute.Kind, slug string) error {
		for _, root := range v.roots {
			path, err := attribute.NewStore(root, kind).Path(slug)
			if err != nil {
				return err
			}
			if err := hook.CheckAgentPath(root, path); err != nil {
				return err
			}
		}
		return nil
	}
	if id.Personality != "" {
		if err := check(attribute.Personalities, id.Personality); err != nil {
			return nil, err
		}
	}
	if id.WritingStyle != "" {
		if err := check(attribute.WritingStyles, id.WritingStyle); err != nil {
			return nil, err
		}
	}
	for _, slug := range id.Talents {
		if err := check(attribute.Talents, slug); err != nil {
			return nil, err
		}
	}
	return v.Load(handle)
}
