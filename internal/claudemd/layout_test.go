package claudemd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// TestStagingNameFollowsTheLogicalPath: a CLAUDE.md symlinked to AGENTS.md is
// classified by the path the caller asked to write, so the staging file, which
// lives in the link target's directory (docs/ here, a directory no table entry names), is still class 4.
func TestStagingNameFollowsTheLogicalPath(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "docs", "AGENTS.md")
	if err := os.WriteFile(target, []byte("# agents\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "CLAUDE.md")
	if err := os.Symlink("docs/AGENTS.md", link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	real, _, err := resolveTarget(link)
	if err != nil {
		t.Fatal(err)
	}

	f, err := createTemp(real, link)
	if err != nil {
		t.Fatal(err)
	}
	name := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(name) }()

	if filepath.Dir(name) != filepath.Dir(real) {
		t.Errorf("staging file %s is not beside the link target %s", name, real)
	}
	rel, err := filepath.Rel(root, name)
	if err != nil {
		t.Fatal(err)
	}
	if c, e, ok := layout.Classify(rel); !ok || c != layout.Never {
		t.Errorf("staging file %s classified %v by %q, %v; want class 4", rel, c, e.Pattern, ok)
	}
}
