//go:build linux || darwin

package test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReleaseCandidateSmoke(t *testing.T) {
	const goodIdentity = `{"handle":"release-smoke","name":"Release Smoke","kind":"human"}`
	for _, tc := range []struct {
		name      string
		version   string
		createRC  int
		identity  string
		badDigest bool
		wantError string
	}{
		{name: "successful round trip", version: "1.2.3", identity: goodIdentity},
		{name: "wrong candidate", version: "1.2.3", identity: goodIdentity, badDigest: true, wantError: "candidate SHA-256 mismatch"},
		{name: "wrong version", version: "1.2.4", identity: goodIdentity, wantError: "expected ethos version 1.2.3"},
		{name: "operation failed", version: "1.2.3", createRC: 1, identity: goodIdentity, wantError: "identity create failed"},
		{name: "incorrect round trip", version: "1.2.3", identity: "{}", wantError: "identity round trip returned unexpected data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dist := t.TempDir()
			asset := "ethos-" + runtime.GOOS + "-" + runtime.GOARCH
			binary := fmt.Sprintf(`#!/bin/sh
case "$*" in
  "version") echo 'ethos %s' ;;
  "version --json") echo '{"version":"%s"}' ;;
  "identity create -f identity.yaml") test -r identity.yaml && exit %d ;;
  "identity get release-smoke --json") echo '%s' ;;
esac
`, tc.version, tc.version, tc.createRC, tc.identity)
			require.NoError(t, os.WriteFile(filepath.Join(dist, asset), []byte(binary), 0o700))
			digest := sha256.Sum256([]byte(binary))
			if tc.badDigest {
				digest[0] ^= 1
			}
			manifest := fmt.Sprintf("%x  %s\n", digest, asset)
			require.NoError(t, os.WriteFile(filepath.Join(dist, "checksums.txt"), []byte(manifest), 0o600))

			output, err := runReleaseSmoke(t, "candidate", "../install.sh", dist)
			if tc.wantError == "" {
				require.NoError(t, err, "%s", output)
				assert.Contains(t, output, "PASS: identity round trip")
			} else {
				require.Error(t, err, "%s", output)
				assert.Contains(t, output, tc.wantError)
			}
		})
	}
}

func TestPublishedReleaseSmokeRejectsUnprovenInstall(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fallback  bool
		wantError string
	}{
		{name: "published bytes differ", wantError: "installed SHA-256 differs from the candidate"},
		{name: "fallback with matching bytes", fallback: true, wantError: "the installer did not use the GitHub Release binary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dist := t.TempDir()
			asset := "ethos-" + runtime.GOOS + "-" + runtime.GOARCH
			installer := filepath.Join(dist, "install.sh")
			binary := "#!/bin/sh\nexit 0\n"
			candidate := binary
			warning := ""
			if tc.fallback {
				warning = "echo 'Download failed, falling back to source build' >&2\n"
			} else {
				candidate += "# Different bytes\n"
			}
			require.NoError(t, os.WriteFile(filepath.Join(dist, asset), []byte(binary), 0o600))
			digest := sha256.Sum256([]byte(candidate))
			manifest := fmt.Sprintf("%x  %s\n", digest, asset)
			require.NoError(t, os.WriteFile(filepath.Join(dist, "checksums.txt"), []byte(manifest), 0o600))
			fakeInstall := `#!/bin/sh
set -eu
test "$*" = "--no-plugin"
test -z "${ETHOS_LOCAL_BINARY:-}"
mkdir -p "$HOME/.local/bin"
cp "$(dirname "$0")/` + asset + `" "$HOME/.local/bin/ethos"
chmod +x "$HOME/.local/bin/ethos"
` + warning
			require.NoError(t, os.WriteFile(installer, []byte(fakeInstall), 0o600))

			output, err := runReleaseSmoke(t, "published", installer, dist)
			require.Error(t, err, "%s", output)
			assert.Contains(t, output, tc.wantError)
		})
	}
}

func runReleaseSmoke(t *testing.T, mode, installer, dist string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "../scripts/smoke-release.sh", mode, "1.2.3", installer, dist)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	output, err := cmd.CombinedOutput()
	return string(output), err
}
