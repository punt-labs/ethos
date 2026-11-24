//go:build behavioral

package behavioral

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
)

func TestMain(m *testing.M) {
	os.Exit(runBehavioralTests(m))
}

func runBehavioralTests(m *testing.M) int {
	testhelpers.MustIsolateGit() // no inherited GIT_DIR; temp dirs stop at the temp root
	for _, name := range []string{"ANTHROPIC_API_KEY", judgeModelEnv} {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			fmt.Fprintf(os.Stderr, "%s is required for behavioral tests\n", name)
			return 1
		}
	}

	// Find claude CLI.
	claude, err := exec.LookPath("claude")
	if err != nil {
		fmt.Fprintln(os.Stderr, "claude CLI is required for behavioral tests and was not found in PATH")
		return 1
	}
	claudeBinary = claude

	// Build ethos binary.
	dir, err := os.MkdirTemp("", "ethos-behavioral-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating temp dir: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)

	bin := filepath.Join(dir, "ethos")
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getting working directory: %v\n", err)
		return 1
	}
	root := filepath.Join(wd, "..", "..")

	cmd := exec.Command("go", "build", "-o", bin, "./cmd/ethos")
	cmd.Dir = root
	out, buildErr := cmd.CombinedOutput()
	if buildErr != nil {
		fmt.Fprintf(os.Stderr, "go build failed: %v\n%s\n", buildErr, out)
		return 1
	}
	ethosBinary = bin

	return m.Run()
}
