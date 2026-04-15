package bundle

import (
	"fmt"
	"os"
	"path/filepath"
)

// Validate checks a bundle's structure:
//
//   - path exists and is a directory
//   - if bundle.yaml exists and specifies a Name, it must match the
//     directory basename
//
// Returns the first error encountered. More rigorous validation
// (dangling refs, schema of nested content) will come in a later PR.
func (b *Bundle) Validate() error {
	info, err := os.Stat(b.Path)
	if err != nil {
		return fmt.Errorf("bundle %q: stat %s: %w", b.Name, b.Path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("bundle %q: %s is not a directory", b.Name, b.Path)
	}
	if b.HasManifest && b.Manifest.Name != "" {
		base := filepath.Base(b.Path)
		if b.Manifest.Name != base {
			return fmt.Errorf("bundle %q: manifest name %q does not match directory basename %q",
				b.Name, b.Manifest.Name, base)
		}
	}
	return nil
}
