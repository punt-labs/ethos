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

func resolveEthosBinary(envPath string, build func() (string, error)) (string, error) {
	envPath = strings.TrimSpace(envPath)
	if envPath != "" {
		abs, err := filepath.Abs(envPath)
		if err != nil {
			return "", fmt.Errorf("resolving ETHOS_BINARY %q: %w", envPath, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return "", fmt.Errorf("ETHOS_BINARY %q: %w", abs, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("ETHOS_BINARY %q is a directory", abs)
		}
		if info.Mode()&0o111 == 0 {
			return "", fmt.Errorf("ETHOS_BINARY %q is not executable", abs)
		}
		return abs, nil
	}
	return build()
}

func runBehavioralTests(m *testing.M) int {
	testhelpers.MustIsolateGit() // no inherited GIT_DIR; temp dirs stop at the temp root
	var buildDir string
	defer func() {
		if buildDir != "" {
			os.RemoveAll(buildDir)
		}
	}()

	bin, err := resolveEthosBinary(os.Getenv("ETHOS_BINARY"), func() (string, error) {
		dir, mkErr := os.MkdirTemp("", "ethos-behavioral-*")
		if mkErr != nil {
			return "", fmt.Errorf("creating temp dir: %w", mkErr)
		}
		buildDir = dir
		target := filepath.Join(dir, "ethos")
		wd, wdErr := os.Getwd()
		if wdErr != nil {
			return "", fmt.Errorf("getting working directory: %w", wdErr)
		}
		root := filepath.Join(wd, "..", "..")
		cmd := exec.Command("go", "build", "-o", target, "./cmd/ethos")
		cmd.Dir = root
		out, buildErr := cmd.CombinedOutput()
		if buildErr != nil {
			return "", fmt.Errorf("go build failed: %w\n%s", buildErr, out)
		}
		return target, nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	ethosBinary = bin

	return m.Run()
}
