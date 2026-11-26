//go:build darwin

package hook

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/stretchr/testify/require"
)

func TestDarwinHandoffSystemPathAllowlist(t *testing.T) {
	const checkout = "/opt/homebrew/Cellar/checkout"
	for _, path := range []string{
		"/opt/homebrew/bin/jq", "/usr/bin/xcode-select", "/Library/Developer/CommandLineTools/usr/bin/make",
		"/Applications/Xcode.app/Contents/Developer", "/Applications/Xcode-26.6.app/Contents/Info.plist",
		"/System/Library/CoreServices/SystemVersion.plist", darwinDeveloperSelector,
	} {
		t.Run(path, func(t *testing.T) {
			require.True(t, safeDarwinSystemPath(path, checkout))
		})
	}
	for _, path := range []string{
		"/Users/example/.ssh/id_ed25519", "/System/Volumes/Data/Users/example/.ssh/id_ed25519",
		"/usrx/bin/tool", "/usr/local/bin/tool", "/usr", "/opt/homebrew-evil/bin/tool",
		"/Applications/Xcode.app.backup/Contents/Developer", "/Applications/Other.app/Contents/Developer",
		"/Applications/Tools/Xcode.app/Contents/Developer", "/opt/homebrew/Cellar",
		"/opt/homebrew/Cellar/checkout/key", "/opt/homebrew/Cellar/CHECKOUT/key",
	} {
		t.Run(path, func(t *testing.T) {
			require.False(t, safeDarwinSystemPath(path, checkout))
		})
	}
}

func TestDarwinLiveRefusesDeclaredSSHKey(t *testing.T) {
	checkout, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	key := filepath.Join(dir, ".ssh", "id_ed25519")
	writeFile(t, key, "synthetic test data")
	profile := &checkset.WorktreeSnapshot{
		Isolation: checkset.IsolationLiveVerified,
		System:    checkset.SnapshotSystem{SnapshotSystemInputs: checkset.SnapshotSystemInputs{Files: []string{key}}},
	}
	plan, err := prepareHandoffSystem(t.Context(), checkout, profile)
	require.ErrorContains(t, err, "outside supported Darwin system roots")
	require.Empty(t, plan.Inputs)
	require.Empty(t, plan.Mounts)
}

func TestDarwinHandoffSystemInputRejectsSymlinkAcrossRoots(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	approved := filepath.Join(dir, "approved")
	outside := filepath.Join(dir, "outside")
	allowDarwinSystemFixtureRoot(t, approved)
	for _, root := range []string{approved, outside} {
		writeFile(t, filepath.Join(root, "input"), "synthetic test data")
	}
	for _, pair := range [][2]string{{approved, outside}, {outside, approved}} {
		path := filepath.Join(pair[0], "link")
		require.NoError(t, os.Symlink(filepath.Join(pair[1], "input"), path))
		for _, snapshot := range []bool{false, true} {
			var plan handoffSystemPlan
			_, err := addDarwinSystemInput(t.Context(), &plan, filepath.Join(dir, "checkout"), path, "", false, snapshot)
			require.ErrorContains(t, err, "outside supported Darwin system roots")
			require.Empty(t, plan.Inputs)
			require.Empty(t, plan.Mounts)
		}
	}
}

func allowDarwinSystemFixtureRoot(t *testing.T, dir string) {
	t.Helper()
	roots := darwinSystemRoots
	darwinSystemRoots = append([]string{dir}, roots...)
	t.Cleanup(func() { darwinSystemRoots = roots })
}

func TestDarwinSystemToolRefusesRejectedPATHHits(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	outside := filepath.Join(dir, "outside")
	allowed := filepath.Join(dir, "allowed")
	symlinks := filepath.Join(dir, "symlinks")
	allowDarwinSystemFixtureRoot(t, allowed)
	allowDarwinSystemFixtureRoot(t, symlinks)
	for _, bin := range []string{outside, allowed, symlinks} {
		require.NoError(t, os.Mkdir(bin, 0o700))
	}
	outsideTool := filepath.Join(outside, "git")
	allowedTool := filepath.Join(allowed, "git")
	writeFile(t, outsideTool, "#!/bin/sh\nexit 42\n")
	require.NoError(t, os.Chmod(outsideTool, 0o700))
	symlinkTool := filepath.Join(symlinks, "git")
	require.NoError(t, os.Symlink(outsideTool, symlinkTool))
	writeFile(t, allowedTool, "#!/bin/sh\nexit 0\n")
	require.NoError(t, os.Chmod(allowedTool, 0o700))
	checkout := t.TempDir()
	profile := &checkset.WorktreeSnapshot{
		Path: []string{outside, allowed},
		System: checkset.SnapshotSystem{
			SnapshotSystemInputs: checkset.SnapshotSystemInputs{Tools: []string{"git"}},
		},
	}
	ctx := t.Context()
	plan, err := prepareHandoffSystem(ctx, checkout, profile)
	require.ErrorContains(t, err, outsideTool+" is outside supported Darwin system roots")
	require.Empty(t, plan.Inputs)
	profile.Path = []string{symlinks, allowed}
	plan, err = prepareHandoffSystem(ctx, checkout, profile)
	require.ErrorContains(t, err, "resolved system input "+symlinkTool+" is outside supported Darwin system roots")
	require.Empty(t, plan.Inputs)
	profile.Path = []string{allowed}

	proof, err := digestGateInputs(ctx, checkout, "", nil)
	require.NoError(t, err)
	for _, isolation := range []string{"", checkset.IsolationLiveVerified} {
		profile.Isolation = isolation
		var previousFingerprint string
		for _, version := range []string{"one", "two"} {
			writeFile(t, allowedTool, "#!/bin/sh\nprintf '%s\\n' "+version+"\n")
			require.NoError(t, os.Chmod(allowedTool, 0o700))
			plan, err = prepareHandoffSystem(ctx, checkout, profile)
			require.NoError(t, err)
			require.NotEmpty(t, plan.Fingerprint)
			require.NotEqual(t, previousFingerprint, plan.Fingerprint)
			previousFingerprint = plan.Fingerprint
			require.Contains(t, plan.Inputs, handoffSystemInput{
				Path: allowedTool, Tool: "git", Digest: planDigest(t, ctx, allowedTool),
			})
			check := editCheck{
				name: "path-key", argv: []string{"/bin/sh", "-ec", `test "$(git)" = "$1"`, "probe", version},
				policy: &checkset.Check{WorktreeSnapshot: profile},
			}
			if isolation == checkset.IsolationLiveVerified {
				result, err := executeHandoffLive(ctx, "", checkout, nil, check, handoffWorktreePlan{system: plan})
				require.NoError(t, err)
				require.Empty(t, result)
			} else {
				result, err := executeHandoffSnapshot(ctx, "", checkout, proof, nil, check, handoffWorktreePlan{system: plan})
				require.NoError(t, err)
				require.Empty(t, result)
			}
		}
	}
}

func TestDarwinHandoffWorktreeSystemInputs(t *testing.T) {
	user, err := os.UserHomeDir()
	require.NoError(t, err)
	systemDir, err := os.MkdirTemp(user, "ethos-system-inputs-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(systemDir) })

	const declared = "/usr/share/dict"
	const data = "/System/Library/CoreServices/SystemVersion.plist"
	fixture := filepath.Join(systemDir, "data")
	secret := filepath.Join(systemDir, "unlisted")
	writeFile(t, fixture, "alpha\n")
	writeFile(t, secret, "unlisted\n")
	profile := &checkset.WorktreeSnapshot{
		System: checkset.SnapshotSystem{
			SnapshotSystemInputs: checkset.SnapshotSystemInputs{Files: []string{data}},
			Platforms: map[string]checkset.SnapshotSystemInputs{
				runtime.GOOS + "-" + runtime.GOARCH: {Tools: []string{"cat"}, Dirs: []string{declared}},
				"linux-arm64":                       {Dirs: []string{"/not-a-darwin-input"}},
			},
		},
	}
	ctx := context.Background()
	checkout := t.TempDir()
	plan, err := prepareHandoffSystem(ctx, checkout, profile)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Fingerprint)
	key, err := fingerprintDarwinSystemInput(ctx, fixture, false)
	require.NoError(t, err)
	writeFile(t, fixture, "omega\n")
	updated, err := fingerprintDarwinSystemInput(ctx, fixture, false)
	require.NoError(t, err)
	require.NotEqual(t, key, updated, "same-size content changes invalidate the cache")

	proof, err := digestGateInputs(ctx, checkout, "", nil)
	require.NoError(t, err)
	result, err := executeHandoffSnapshot(ctx, "", checkout, proof, nil, editCheck{
		name: "system-inputs",
		argv: []string{"/bin/sh", "-ec", `
test -s "$1"
test -s "$3/words"
! cat "$2"
! (echo changed > "$1")
! (echo changed > "$3/words")
`, "probe", data, secret, declared},
	}, handoffWorktreePlan{system: plan})
	require.NoError(t, err)
	require.Empty(t, result)
}

func TestDarwinHandoffWorktreeHomebrewTools(t *testing.T) {
	tools := []string{"jq", "shellcheck"}
	for _, name := range tools {
		if _, err := os.Stat(filepath.Join("/opt/homebrew/bin", name)); err != nil {
			t.Skipf("requires Homebrew %s", name)
		}
	}
	ctx := context.Background()
	checkout := t.TempDir()
	profile := &checkset.WorktreeSnapshot{
		Path: []string{"/opt/homebrew/bin"},
		System: checkset.SnapshotSystem{
			SnapshotSystemInputs: checkset.SnapshotSystemInputs{Tools: tools},
		},
	}
	plan, err := prepareHandoffSystem(ctx, checkout, profile)
	require.NoError(t, err)
	require.Greater(t, len(plan.Inputs), len(tools), "dependencies must enter the key")
	proof, err := digestGateInputs(ctx, checkout, "", nil)
	require.NoError(t, err)
	result, err := executeHandoffSnapshot(ctx, "", checkout, proof, nil, editCheck{
		name:   "tools",
		policy: &checkset.Check{WorktreeSnapshot: profile},
		argv: []string{"/bin/sh", "-ec", `
test "$(command -v jq)" = /opt/homebrew/bin/jq
jq --version
shellcheck --version
`},
	}, handoffWorktreePlan{system: plan})
	require.NoError(t, err)
	require.Empty(t, result)
}

func TestDarwinLiveUsesDeclaredToolPath(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	bin, err := os.MkdirTemp(home, "ethos-live-tools-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(bin) })
	allowDarwinSystemFixtureRoot(t, bin)
	const tool = "ethos-fixture-tool"
	install := func(binary string) {
		t.Helper()
		data, err := os.ReadFile(binary)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(bin, tool), data, 0o700))
	}
	install("/usr/bin/true")
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	checkout, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	profile := &checkset.WorktreeSnapshot{
		Isolation: checkset.IsolationLiveVerified,
		System:    checkset.SnapshotSystem{SnapshotSystemInputs: checkset.SnapshotSystemInputs{Tools: []string{tool}}},
	}
	ctx := t.Context()
	_, err = prepareHandoffSystem(ctx, checkout, profile)
	require.ErrorContains(t, err, "not available")
	for _, source := range []string{"sidecar", "runtime platform"} {
		t.Run(source, func(t *testing.T) {
			install("/usr/bin/true")
			config := *profile
			var platform checkset.SnapshotPlatform
			if source == "sidecar" {
				config.Path = []string{bin}
			} else {
				platform.Path = []string{bin}
				config.Runtime = &checkset.SnapshotRuntime{
					Platforms: map[string]checkset.SnapshotPlatform{runtime.GOOS + "-" + runtime.GOARCH: platform},
				}
			}
			plan, err := prepareHandoffSystem(ctx, checkout, &config)
			require.NoError(t, err)
			require.Contains(t, plan.Inputs, handoffSystemInput{
				Path: filepath.Join(bin, tool), Tool: tool,
				Digest: planDigest(t, ctx, filepath.Join(bin, tool)),
			})
			result, err := executeHandoffLive(ctx, "", checkout, nil, editCheck{
				name:   "declared-tool",
				argv:   []string{"/bin/sh", "-ec", "ethos-fixture-tool"},
				policy: &checkset.Check{WorktreeSnapshot: &config},
			}, handoffWorktreePlan{system: plan, runtime: handoffRuntime{platform: platform}})
			require.NoError(t, err)
			require.Empty(t, result)
			install("/usr/bin/false")
			updated, err := prepareHandoffSystem(ctx, checkout, &config)
			require.NoError(t, err)
			require.NotEqual(t, plan.Fingerprint, updated.Fingerprint)
		})
	}
}

func planDigest(t *testing.T, ctx context.Context, path string) string {
	t.Helper()
	digest, err := fingerprintDarwinSystemInput(ctx, path, false)
	require.NoError(t, err)
	return digest
}

func TestDarwinLiveKeysSelectedDeveloperDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	dir, err := os.MkdirTemp(home, "ethos-live-xcode-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	allowDarwinSystemFixtureRoot(t, dir)
	app := filepath.Join(dir, "Xcode.app")
	contents := filepath.Join(app, "Contents")
	developer := filepath.Join(contents, "Developer")
	require.NoError(t, os.MkdirAll(developer, 0o700))
	for _, name := range []string{"Info.plist", "version.plist"} {
		writeFile(t, filepath.Join(contents, name), "first")
	}
	ctx := t.Context()
	checkout, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	profile := &checkset.WorktreeSnapshot{
		Isolation: checkset.IsolationLiveVerified,
		Env:       map[string]string{"DEVELOPER_DIR": app},
	}
	plan, err := prepareHandoffSystem(ctx, checkout, profile, "/usr/bin/make")
	require.NoError(t, err)
	require.Equal(t, developer, plan.DeveloperDir)
	require.Regexp(t, `^[0-9a-f]{64}$`, plan.DeveloperFingerprint)
	result, err := executeHandoffLive(ctx, "", checkout, nil, editCheck{
		name: "selected-xcode", policy: &checkset.Check{WorktreeSnapshot: profile},
		argv: []string{"/bin/sh", "-ec", `test "$DEVELOPER_DIR" = "$1"`, "probe", developer},
	}, handoffWorktreePlan{system: plan})
	require.NoError(t, err)
	require.Empty(t, result)
	for _, name := range []string{"Info.plist", "version.plist"} {
		writeFile(t, filepath.Join(contents, name), "other")
		updated, err := prepareHandoffSystem(ctx, checkout, profile, "/usr/bin/make")
		require.NoError(t, err)
		require.NotEqual(t, plan.Fingerprint, updated.Fingerprint)
		require.NotEqual(t, plan.DeveloperFingerprint, updated.DeveloperFingerprint)
		writeFile(t, filepath.Join(contents, name), "first")
	}
}

func TestDarwinHandoffWorktreeAllowsTrustedXcodeContents(t *testing.T) {
	ctx := context.Background()
	if err := exec.CommandContext(ctx, "/usr/bin/xcodebuild", "-version").Run(); err != nil {
		t.Skipf("requires full Xcode: %v", err)
	}
	checkout := t.TempDir()
	profile := &checkset.WorktreeSnapshot{
		System: checkset.SnapshotSystem{
			SnapshotSystemInputs: checkset.SnapshotSystemInputs{Tools: []string{"xcodebuild"}},
		},
	}
	plan, err := prepareHandoffSystem(ctx, checkout, profile)
	developer, linkErr := os.Readlink(darwinDeveloperSelector)
	if linkErr != nil || !safeDarwinSystemPath(developer, checkout) || darwinTrustedToolchain(developer) != developer {
		require.ErrorIs(t, err, errHandoffXcodeUntrusted)
		require.False(t, hasDarwinDeveloperSelector(plan))
		return
	}
	require.NoError(t, err)
	require.True(t, hasDarwinDeveloperSelector(plan))
	require.Contains(t, plan.Mounts, handoffSystemMount{Source: darwinDeveloperSelector, Target: darwinDeveloperSelector})
	require.Condition(t, func() bool {
		for _, input := range plan.Mounts {
			if input.Directory && filepath.Base(input.Source) == "Contents" {
				return true
			}
		}
		return false
	})
	proof, err := digestGateInputs(ctx, checkout, "", nil)
	require.NoError(t, err)
	result, err := executeHandoffSnapshot(ctx, "", checkout, proof, nil, editCheck{
		name: "xcode-version", policy: &checkset.Check{WorktreeSnapshot: profile}, argv: []string{"xcodebuild", "-version"},
	}, handoffWorktreePlan{system: plan})
	require.NoError(t, err)
	require.Empty(t, result)
}

func TestDarwinHandoffWorktreeXcodeDeferralDoesNotBlock(t *testing.T) {
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", `  - name: xcode-profile
    stage: handoff
    files: ["**"]
    run: [/usr/bin/xcode-select, -p]
`)
	writeFile(t, filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml"), `checks:
  xcode-profile:
    profile: linux-worktree-v1
    epoch: xcode-deferral
    sources: {mode: worktree}
`)
	input := filepath.Join(root, "input")
	writeFile(t, input, "content\n")
	feedback := runStageChecks(context.Background(), root, "worker", "handoff", func() ([]string, error) {
		return []string{input}, nil
	})
	require.Empty(t, feedback.Blocking)
	require.Contains(t, feedback.Advisory, errHandoffXcodeUntrusted.Error())
}

func TestDarwinHandoffMakeWithCommandLineTools(t *testing.T) {
	ctx := context.Background()
	checkout := t.TempDir()
	var rejected handoffSystemPlan
	require.ErrorIs(t, addDarwinCLT(ctx, &rejected, checkout, filepath.Join(checkout, "missing")), errHandoffXcodeUntrusted)
	require.Empty(t, rejected.DeveloperDir)
	if !darwinMakeShim("/usr/bin/make") || !trustedDarwinCLT(darwinCLT) {
		t.Skip("requires the make shim and trusted Command Line Tools")
	}
	profile := &checkset.WorktreeSnapshot{
		Env: map[string]string{"DEVELOPER_DIR": "/invalid", "GIT_CONFIG_NOSYSTEM": "1"},
		System: checkset.SnapshotSystem{
			SnapshotSystemInputs: checkset.SnapshotSystemInputs{Tools: []string{"git"}},
		},
	}
	plan, err := prepareHandoffSystem(ctx, checkout, profile, "make", "check")
	require.NoError(t, err)
	require.Equal(t, darwinCLT, plan.DeveloperDir)
	require.NotEmpty(t, plan.Fingerprint)
	require.False(t, hasDarwinDeveloperSelector(plan))
	require.Contains(t, plan.Mounts, handoffSystemMount{Source: darwinCLT, Target: darwinCLT, Directory: true})
	withoutMake, err := prepareHandoffSystem(ctx, checkout, &checkset.WorktreeSnapshot{})
	require.NoError(t, err)
	require.NotEqual(t, withoutMake.Fingerprint, plan.Fingerprint)

	writeFile(t, filepath.Join(checkout, "Makefile"), "check:\n\t@test \"$$DEVELOPER_DIR\" = "+darwinCLT+"\n\t@! /usr/bin/readlink "+darwinDeveloperSelector+"\n\t@/usr/bin/git --version\n")
	inputs := []string{"Makefile"}
	proof, err := digestGateInputsWithMode(ctx, checkout, "", inputs, true)
	require.NoError(t, err)
	result, err := executeHandoffSnapshot(ctx, "", checkout, proof, inputs, editCheck{
		name: "make-clt", policy: &checkset.Check{WorktreeSnapshot: profile}, argv: []string{"make", "check"},
	}, handoffWorktreePlan{system: plan})
	require.NoError(t, err)
	require.Empty(t, result)
}
