package mission

import (
	"fmt"
	"path/filepath"
	"strings"
)

// enforceArchetypeConstraints checks the contract against its archetype's
// constraints. Called by Store.Create after Validate passes.
//
// Three constraint families:
//   - AllowEmptyWriteSet: handled by ValidateWithArchetype (rule 11).
//   - WriteSetConstraints: every non-directory write_set entry must match
//     at least one glob pattern.
//   - RequiredFields: named fields must be non-empty.
func enforceArchetypeConstraints(c *Contract, a *Archetype) error {
	if err := enforceWriteSetConstraints(c, a); err != nil {
		return err
	}
	if err := enforceRequiredFields(c, a); err != nil {
		return err
	}
	return nil
}

// enforceWriteSetConstraints checks that every non-directory write_set
// entry matches at least one of the archetype's glob patterns.
//
// Directory entries (trailing slash) are exempt. They are scope markers,
// not file claims, and cannot match file-glob patterns like "*_test.go".
//
// Two matching strategies per pattern:
//  1. filepath.Match against the base name — handles patterns like
//     "*_test.go" and "*.md".
//  2. Prefix match for "dir/**" patterns — filepath.Match does not
//     support "**", so we check whether the entry starts with the
//     directory prefix.
func enforceWriteSetConstraints(c *Contract, a *Archetype) error {
	if len(a.WriteSetConstraints) == 0 || len(c.WriteSet) == 0 {
		return nil
	}
	for _, entry := range c.WriteSet {
		if strings.HasSuffix(entry, "/") {
			continue // directory envelope — not subject to file-pattern constraints
		}
		ok, err := matchesAnyConstraint(entry, a.WriteSetConstraints)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("write_set entry %q does not match any constraint: %v",
				entry, a.WriteSetConstraints)
		}
	}
	return nil
}

// matchesAnyConstraint reports whether entry matches at least one of the
// constraint patterns. Returns an error if any pattern is malformed so
// archetype configuration bugs surface immediately.
func matchesAnyConstraint(entry string, constraints []string) (bool, error) {
	base := filepath.Base(entry)
	for _, pattern := range constraints {
		// Strategy 1: glob match against the base name.
		ok, err := filepath.Match(pattern, base)
		if err != nil {
			return false, fmt.Errorf("invalid constraint pattern %q: %w", pattern, err)
		}
		if ok {
			return true, nil
		}
		// Strategy 2: "dir/**" prefix match against the full path.
		if strings.HasSuffix(pattern, "/**") {
			prefix := strings.TrimSuffix(pattern, "/**")
			if entry == prefix || strings.HasPrefix(entry, prefix+"/") {
				return true, nil
			}
		}
	}
	return false, nil
}

// enforceRequiredFields checks that every field named in
// a.RequiredFields is non-empty on the contract.
func enforceRequiredFields(c *Contract, a *Archetype) error {
	for _, field := range a.RequiredFields {
		switch field {
		case "context":
			if strings.TrimSpace(c.Context) == "" {
				return fmt.Errorf("required field %q is empty", field)
			}
		case "inputs.files":
			if len(c.Inputs.Files) == 0 {
				return fmt.Errorf("required field %q is empty", field)
			}
		case "inputs.ticket":
			if strings.TrimSpace(c.Inputs.Ticket) == "" {
				return fmt.Errorf("required field %q is empty", field)
			}
		case "success_criteria":
			if len(c.SuccessCriteria) == 0 {
				return fmt.Errorf("required field %q is empty", field)
			}
		default:
			return fmt.Errorf("unknown required field %q in archetype", field)
		}
	}
	return nil
}
