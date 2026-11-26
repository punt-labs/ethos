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

func TestDarwinHandoffWorktreeSystemInputs(t *testing.T) {
	user, err := os.UserHomeDir()
	require.NoError(t, err)
	systemDir, err := os.MkdirTemp(user, "ethos-system-inputs-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(systemDir) })

	declared := filepath.Join(systemDir, "declared")
	require.NoError(t, os.Mkdir(declared, 0o700))
	data := filepath.Join(systemDir, "data")
	secret := filepath.Join(systemDir, "unlisted")
	writeFile(t, filepath.Join(declared, "member"), "allowed\n")
	writeFile(t, data, "alpha\n")
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
	key := plan.Fingerprint
	require.NotEmpty(t, key)
	writeFile(t, data, "omega\n")
	plan, err = prepareHandoffSystem(ctx, checkout, profile)
	require.NoError(t, err)
	require.NotEqual(t, key, plan.Fingerprint, "same-size content changes invalidate the cache")

	proof, err := digestGateInputs(ctx, checkout, "", nil)
	require.NoError(t, err)
	result, err := executeHandoffSnapshot(ctx, "", checkout, proof, nil, editCheck{
		name: "system-inputs",
		argv: []string{"/bin/sh", "-ec", `
test "$(cat "$1")" = omega
test "$(cat "$3/member")" = allowed
! cat "$2"
! (echo changed > "$1")
! (echo changed > "$3/member")
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
	if linkErr != nil || darwinTrustedToolchain(developer) == "" {
		require.ErrorIs(t, err, errHandoffXcodeUntrusted)
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
