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

func fakeEthosBinary(version string, createRC int, identity, tracePath string, mcpError bool, hookStderr string) string {
	var traceLine string
	if tracePath != "" {
		traceLine = fmt.Sprintf("printf '%%s\\n' \"$*\" >> %q\n", tracePath)
	}
	mcpCallResp := `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"{\"handle\":\"release-smoke\",\"name\":\"Release Smoke\",\"kind\":\"human\"}"}]}}`
	if mcpError {
		mcpCallResp = `{"jsonrpc":"2.0","id":3,"result":{"isError":true,"content":[{"type":"text","text":"mcp boom"}]}}`
	}
	hookErrLine := ""
	if hookStderr != "" {
		hookErrLine = fmt.Sprintf("printf '%%s\\n' %q >&2;", hookStderr)
	}
	return fmt.Sprintf(`#!/bin/sh
%scase "$*" in
  "version") echo 'ethos %s' ;;
  "version --json") echo '{"version":"%s"}' ;;
  "seed"|"doctor"|"enable") exit 0 ;;
  "identity create -f identity.yaml") test -r identity.yaml && exit %d ;;
  "identity get release-smoke --json") echo '%s' ;;
  "session show release-smoke-session --json") echo '{"session":"release-smoke-session","participants":[{"agent_id":"release-smoke"}]}' ;;
  "hook session-start") cat >/dev/null; %s exit 0 ;;
  "hook pre-tool-use"|"hook audit-log"|"hook format-output"|"hook session-end") cat >/dev/null; exit 0 ;;
  "serve")
    while IFS= read -r line; do
      case "$line" in
        *'"method":"initialize"'*) echo '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"ethos","version":"%s"}}}' ;;
        *'"method":"tools/list"'*) echo '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"identity"},{"name":"session"},{"name":"mission"},{"name":"doctor"}]}}' ;;
        *'"method":"tools/call"'*) echo '%s' ;;
      esac
    done
    ;;
esac
`, traceLine, version, version, createRC, identity, hookErrLine, version, mcpCallResp)
}

func fakeClaudeBinary(t *testing.T, dir, tracePath string, listOutput string, validateRC int) string {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
printf 'claude %%s\n' "$*" >> %q
case "$*" in
  "plugin validate "*) exit %d ;;
  "plugin list") printf '%%s\n' %q ;;
  *) exit 0 ;;
esac
`, tracePath, validateRC, listOutput)
	path := filepath.Join(dir, "claude")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o700))
	return path
}

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
			binary := fakeEthosBinary(tc.version, tc.createRC, tc.identity, "", false, "")
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
				assert.Contains(t, output, "PASS:")
				calls, err := os.ReadFile(pythonCalls)
				require.NoError(t, err)
				assert.GreaterOrEqual(t, strings.Count(string(calls), "python3\n"), 2)
			} else {
				require.Error(t, err, "%s", output)
				assert.Contains(t, output, tc.wantError)
			}
		})
	}
}

func TestReleaseCandidateSmokeExercisesPluginMCPAndHooks(t *testing.T) {
	const goodIdentity = `{"handle":"release-smoke","name":"Release Smoke","kind":"human"}`
	for _, tc := range []struct {
		name          string
		mcpError      bool
		hookStderr    string
		validateRC    int
		requirePlugin bool
		withClaude    bool
		wantError     string
	}{
		{name: "full candidate flow", withClaude: true, requirePlugin: true},
		{name: "MCP server error fails smoke", withClaude: true, mcpError: true, wantError: "MCP"},
		{name: "hook error log fails smoke", withClaude: true, hookStderr: "hook boom", wantError: "hook-errors.log"},
		{name: "claude plugin validate failure fails smoke", withClaude: true, validateRC: 1, wantError: "claude plugin validate failed"},
		{name: "missing claude fails when plugin required", withClaude: false, requirePlugin: true, wantError: "claude CLI is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dist := t.TempDir()
			tracePath := filepath.Join(dist, "trace.log")
			asset := "ethos-" + runtime.GOOS + "-" + runtime.GOARCH
			binary := fakeEthosBinary("1.2.3", 0, goodIdentity, tracePath, tc.mcpError, tc.hookStderr)
			require.NoError(t, os.WriteFile(filepath.Join(dist, asset), []byte(binary), 0o700))
			digest := sha256.Sum256([]byte(binary))
			manifest := fmt.Sprintf("%x  %s\n", digest, asset)
			require.NoError(t, os.WriteFile(filepath.Join(dist, "checksums.txt"), []byte(manifest), 0o600))
			installer := releaseSmokeInstaller(t, dist, "1.2.3")

			extraEnv := []string{}
			if tc.requirePlugin {
				extraEnv = append(extraEnv, "ETHOS_SMOKE_REQUIRE_PLUGIN=1")
			}
			var pathEnv string
			if tc.withClaude {
				claudeDir := t.TempDir()
				fakeClaudeBinary(t, claudeDir, tracePath, "ethos@punt-labs", tc.validateRC)
				pathEnv = claudeDir + string(os.PathListSeparator) + "/usr/bin:/bin"
			} else {
				pathEnv = "/usr/bin:/bin"
			}
			output, err := runReleaseSmokeWithEnv(t, "candidate", installer, dist, pathEnv, extraEnv...)
			if tc.wantError == "" {
				require.NoError(t, err, "%s", output)
				assert.Contains(t, output, "PASS:")
				traceBytes, readErr := os.ReadFile(tracePath)
				require.NoError(t, readErr)
				trace := string(traceBytes)
				assert.Contains(t, trace, "claude plugin validate ")
				assert.Contains(t, trace, "serve\n")
				assert.Contains(t, trace, "enable\n")
				assert.Contains(t, trace, "hook session-start\n")
				assert.Contains(t, trace, "hook pre-tool-use\n")
				assert.Contains(t, trace, "hook audit-log\n")
				assert.Contains(t, trace, "hook format-output\n")
				assert.Contains(t, trace, "hook session-end\n")
			} else {
				require.Error(t, err, "%s", output)
				assert.Contains(t, output, tc.wantError)
			}
		})
	}
}

func TestPublishedReleaseSmokeExercisesMarketplacePlugin(t *testing.T) {
	const goodIdentity = `{"handle":"release-smoke","name":"Release Smoke","kind":"human"}`
	repoPlugin, err := filepath.Abs("../plugin")
	require.NoError(t, err)

	for _, tc := range []struct {
		name                 string
		skipPluginLog        bool
		claudeList           string
		cacheVersion         string
		requirePluginVersion bool
		wantError            string
	}{
		{name: "marketplace plugin install and flows", claudeList: "ethos@punt-labs", cacheVersion: "1.2.3"},
		{name: "marketplace older version allowed during release CI", claudeList: "ethos@punt-labs", cacheVersion: "1.2.2"},
		{name: "exact plugin version mismatch rejected when required", claudeList: "ethos@punt-labs", cacheVersion: "1.2.2", requirePluginVersion: true, wantError: "expected marketplace plugin version 1.2.3"},
		{name: "skipped plugin install rejected", skipPluginLog: true, claudeList: "ethos@punt-labs", cacheVersion: "1.2.3", wantError: "plugin install was skipped"},
		{name: "missing plugin from claude plugin list rejected", claudeList: "", cacheVersion: "1.2.3", wantError: "ethos@punt-labs not found in claude plugin list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dist := t.TempDir()
			tracePath := filepath.Join(dist, "trace.log")
			asset := "ethos-" + runtime.GOOS + "-" + runtime.GOARCH
			binary := fakeEthosBinary("1.2.3", 0, goodIdentity, tracePath, false, "")
			require.NoError(t, os.WriteFile(filepath.Join(dist, asset), []byte(binary), 0o700))
			digest := sha256.Sum256([]byte(binary))
			manifest := fmt.Sprintf("%x  %s\n", digest, asset)
			require.NoError(t, os.WriteFile(filepath.Join(dist, "checksums.txt"), []byte(manifest), 0o600))

			skipLine := ""
			if tc.skipPluginLog {
				skipLine = "echo 'Skipping plugin install'\n"
			}
			fakeInstall := fmt.Sprintf(`#!/bin/sh
set -eu
VERSION="1.2.3"
test "$#" -eq 0
test -z "${ETHOS_LOCAL_BINARY:-}"
mkdir -p "$HOME/.local/bin" "$HOME/.punt-labs/ethos"
cp "$(dirname "$0")/%s" "$HOME/.local/bin/ethos"
chmod +x "$HOME/.local/bin/ethos"
%smkdir -p "$HOME/.claude/plugins/cache/punt-labs/ethos/%s"
cp -R %q/. "$HOME/.claude/plugins/cache/punt-labs/ethos/%s/"
`, asset, skipLine, tc.cacheVersion, repoPlugin, tc.cacheVersion)
			installer := filepath.Join(dist, "install.sh")
			require.NoError(t, os.WriteFile(installer, []byte(fakeInstall), 0o600))

			claudeDir := t.TempDir()
			fakeClaudeBinary(t, claudeDir, tracePath, tc.claudeList, 0)
			pathEnv := claudeDir + string(os.PathListSeparator) + "/usr/bin:/bin"
			extraEnv := []string{"ETHOS_SMOKE_REQUIRE_PLUGIN=1"}
			if tc.requirePluginVersion {
				extraEnv = append(extraEnv, "ETHOS_SMOKE_REQUIRE_PLUGIN_VERSION=1")
			}

			output, runErr := runReleaseSmokeWithEnv(t, "published", installer, dist, pathEnv, extraEnv...)
			if tc.wantError == "" {
				require.NoError(t, runErr, "%s", output)
				assert.Contains(t, output, "PASS:")
				traceBytes, readErr := os.ReadFile(tracePath)
				require.NoError(t, readErr)
				trace := string(traceBytes)
				assert.Contains(t, trace, "claude plugin list\n")
				assert.Contains(t, trace, "serve\n")
				assert.Contains(t, trace, "hook session-start\n")
			} else {
				require.Error(t, runErr, "%s", output)
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
test -z "$*" || test "$*" = "--no-plugin"
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
	return runReleaseSmokeWithEnv(t, mode, installer, dist, os.Getenv("PATH"))
}

func runReleaseSmokeWithEnv(t *testing.T, mode, installer, dist, pathEnv string, extraEnv ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "../scripts/smoke-release.sh", mode, "1.2.3", installer, dist)
	cmd.Env = append([]string{"PATH=" + pathEnv}, extraEnv...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}
