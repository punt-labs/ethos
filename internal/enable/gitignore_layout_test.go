package enable

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// TestGitignoreStagingNameFollowsTheLogicalPath: a .gitignore linked to a file
// with another name is classified by the path the caller asked to write.
func TestGitignoreStagingNameFollowsTheLogicalPath(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "shared-ignore")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, ".gitignore")
	if err := os.Symlink("shared-ignore", link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	f, err := createGitignoreTemp(target, link)
	if err != nil {
		t.Fatal(err)
	}
	name := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(name) }()

	rel, err := filepath.Rel(root, name)
	if err != nil {
		t.Fatal(err)
	}
	if c, e, ok := layout.Classify(rel); !ok || c != layout.Never {
		t.Errorf("staging file %s classified %v by %q, %v; want class 4", rel, c, e.Pattern, ok)
	}
}
