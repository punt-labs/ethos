//go:build behavioral

package behavioral

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

func TestMain(m *testing.M) {
	testhelpers.MustIsolateGit() // no inherited GIT_DIR; temp dirs stop at the temp root
	// Verify ANTHROPIC_API_KEY is set.
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY not set; skipping behavioral tests")
		os.Exit(0)
	}

	// Find claude CLI.
	claude, err := exec.LookPath("claude")
	if err != nil {
		fmt.Fprintln(os.Stderr, "claude CLI not found in PATH; skipping behavioral tests")
		os.Exit(0)
	}
	claudeBinary = claude

	// Build ethos binary.
	dir, err := os.MkdirTemp("", "ethos-behavioral-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	bin := filepath.Join(dir, "ethos")
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getting working directory: %v\n", err)
		os.Exit(1)
	}
	root := filepath.Join(wd, "..", "..")

	cmd := exec.Command("go", "build", "-o", bin, "./cmd/ethos")
	cmd.Dir = root
	out, buildErr := cmd.CombinedOutput()
	if buildErr != nil {
		fmt.Fprintf(os.Stderr, "go build failed: %v\n%s\n", buildErr, out)
		os.Exit(1)
	}
	ethosBinary = bin

	os.Exit(m.Run())
}
