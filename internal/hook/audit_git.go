package hook

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// gitAdd stages one path (absolute or repo-relative) from repoRoot. A
// git-add failure staging a new or orphan chunk is fail-closed (exit 2)
// per §Seal failure policy, so the error is returned, never swallowed.
func gitAdd(repoRoot string, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"add", "--"}, paths...)
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git add: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// isGitlinkMount reports whether <repoRoot>/.punt-labs/ethos is a
// gitlink (submodule) rather than regular tracked files. In that
// configuration the sealed-chunk target tree is unreachable and the seal
// defers (§Seal failure policy). A file with git mode 160000 is a
// gitlink; regular tracked files are 100644/100755.
func isGitlinkMount(repoRoot string) bool {
	rel := filepath.Join(".punt-labs", "ethos")
	cmd := exec.Command("git", "ls-files", "--stage", "--", rel)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "160000") {
			return true
		}
	}
	return false
}
