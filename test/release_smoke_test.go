//go:build linux || darwin

package test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReleaseCandidateSmoke(t *testing.T) {
	const goodIdentity = `{"handle":"release-smoke","name":"Release Smoke","kind":"human"}`
	for _, tc := range []struct {
		name           string
		version        string
		installVersion string
		createRC       int
		identity       string
		badDigest      bool
		wantError      string
	}{
		{name: "successful round trip", version: "1.2.3", identity: goodIdentity},
		{name: "wrong candidate", version: "1.2.3", identity: goodIdentity, badDigest: true, wantError: "candidate SHA-256 mismatch"},
		{name: "wrong version", version: "1.2.4", identity: goodIdentity, wantError: "expected ethos version 1.2.3"},
		{name: "stale installer", version: "1.2.3", installVersion: "1.2.2", identity: goodIdentity, wantError: "installer must target ethos version 1.2.3"},
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

			version := tc.installVersion
			if version == "" {
				version = "1.2.3"
			}
			installer := releaseSmokeInstaller(t, dist, version)
			var pythonCalls string
			if tc.wantError == "" {
				pythonCalls = useReleaseSmokePython(t)
			}
			output, err := runReleaseSmoke(t, "candidate", installer, dist)
			if tc.wantError == "" {
				require.NoError(t, err, "%s", output)
				assert.Contains(t, output, "PASS: identity round trip")
				calls, err := os.ReadFile(pythonCalls)
				require.NoError(t, err)
				assert.Equal(t, "python3\npython3\n", string(calls))
			} else {
				require.Error(t, err, "%s", output)
				assert.Contains(t, output, tc.wantError)
			}
		})
	}
}

func useReleaseSmokePython(t *testing.T) string {
	t.Helper()
	python, err := exec.CommandContext(t.Context(), "python3", "-I", "-c", "import sys; print(sys.executable)").Output()
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.Symlink(strings.TrimSpace(string(python)), filepath.Join(dir, "real-python3")))
	wrapper := filepath.Join(dir, "python3")
	require.NoError(t, os.WriteFile(wrapper, []byte(`#!/bin/sh
if [ "$1" = "-I" ]; then
  printf '%s\n' "$0"
  exit
fi
echo python3 >> "$0.called"
exec "${0%/*}/real-python3" "$@"
`), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return wrapper + ".called"
}

func releaseSmokeInstaller(t *testing.T, dir, version string) string {
	t.Helper()
	source, err := os.ReadFile("../install.sh")
	require.NoError(t, err)
	pin := regexp.MustCompile(`(?m)^VERSION="[^"]+"$`)
	require.Len(t, pin.FindAll(source, -1), 1)
	installer := filepath.Join(dir, "install.sh")
	require.NoError(t, os.WriteFile(installer, pin.ReplaceAll(source, []byte(`VERSION="`+version+`"`)), 0o600))
	return installer
}

func TestPublishedReleaseSmokeRejectsUnprovenInstall(t *testing.T) {
	for _, tc := range []struct {
		name            string
		fallbackWarning string
		wantError       string
	}{
		{name: "published bytes differ", wantError: "installed SHA-256 differs from the candidate"},
		{name: "failed download triggers fallback", fallbackWarning: "Download failed, falling back to source build", wantError: "the installer did not use the GitHub Release binary"},
		{name: "source build with matching bytes", fallbackWarning: "Pre-built binary not available, building from source...", wantError: "the installer did not use the GitHub Release binary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dist := t.TempDir()
			asset := "ethos-" + runtime.GOOS + "-" + runtime.GOARCH
			installer := filepath.Join(dist, "install.sh")
			binary := "#!/bin/sh\nexit 0\n"
			candidate := binary
			warning := ""
			if tc.fallbackWarning != "" {
				warning = "echo '" + tc.fallbackWarning + "' >&2\n"
			} else {
				candidate += "# Different bytes\n"
			}
			require.NoError(t, os.WriteFile(filepath.Join(dist, asset), []byte(binary), 0o600))
			digest := sha256.Sum256([]byte(candidate))
			manifest := fmt.Sprintf("%x  %s\n", digest, asset)
			require.NoError(t, os.WriteFile(filepath.Join(dist, "checksums.txt"), []byte(manifest), 0o600))
			fakeInstall := `#!/bin/sh
set -eu
VERSION="1.2.3"
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
