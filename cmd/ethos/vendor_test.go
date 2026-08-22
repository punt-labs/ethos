//go:build linux || darwin

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runVendorCmd(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	rootCmd.SetOut(&outBuf)
	rootCmd.SetErr(&errBuf)
	rootCmd.SetArgs(append([]string{"vendor"}, args...))
	defer func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		vendorTeam = ""
		vendorAll = false
		vendorTo = ""
		vendorPrune = false
		vendorDryRun = false
		vendorApply = false
		vendorAllowExtKeys = nil
		// pflag remembers Changed across Execute() calls on the same
		// Flag object, so a mutual-exclusion check tripped in one test
		// would otherwise leak into the next.
		for _, name := range []string{
			"team", "all", "to", "prune", "dry-run", "apply", "allow-ext-key", "no-teams", "from",
		} {
			if f := vendorCmd.Flags().Lookup(name); f != nil {
				f.Changed = false
			}
		}
	}()
	err := rootCmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

func TestVendorAllAndTeamAreMutuallyExclusive(t *testing.T) {
	dir := enableGitRepo(t)
	t.Chdir(dir)

	_, _, err := runVendorCmd(t, "--all", "--team", "engineering")
	if err == nil {
		t.Fatal("expected an error combining --all with --team, got nil")
	}
	if !strings.Contains(err.Error(), "all") || !strings.Contains(err.Error(), "team") {
		t.Errorf("error = %q, want mention of the conflicting --all/--team flags", err.Error())
	}
}

func TestVendorAllAndPositionalHandlesIsAnError(t *testing.T) {
	dir := enableGitRepo(t)
	t.Chdir(dir)

	_, _, err := runVendorCmd(t, "bwk", "--all")
	if err == nil {
		t.Fatal("expected an error combining --all with a positional handle, got nil")
	}
	if !strings.Contains(err.Error(), "--all is not supported with explicit handles") {
		t.Errorf("error = %q, want the --all/explicit-handles usage error", err.Error())
	}
}

// TestVendorAllAloneStillWorks pins that the new checks reject only the
// contradictory combinations, not --all by itself: the run must reach
// past flag validation into the closure walk (which then reports its own,
// unrelated outcome for an empty identity store).
func TestVendorAllAloneStillWorks(t *testing.T) {
	dir := enableGitRepo(t)
	t.Chdir(dir)

	_, _, err := runVendorCmd(t, "--all")
	if err != nil && strings.Contains(err.Error(), "not supported") {
		t.Errorf("--all alone was rejected by the new exclusivity checks: %v", err)
	}
}

// TestCLI_VendorApplyWithoutPruneReportsWouldRemove pins the bug: --apply
// alone (no --prune) must never claim it deleted files. internal/vendor's
// prune() only runs when Prune is set, so a status line keyed on p.Applied
// alone described a deletion that never happened — a user could run
// --apply, read "Removed 53 file(s)", and believe stale files were gone
// when every one of them was still on disk.
func TestCLI_VendorApplyWithoutPruneReportsWouldRemove(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	se := setupCLISubprocessEnv(t)

	// First apply vendors test-agent into the repo layer.
	_, stderr, exit := runCLI(t, se, "vendor", "test-agent", "--apply")
	require.Equal(t, 0, exit, "initial vendor --apply: stderr=%s", stderr)

	// A stale file the closure no longer contains.
	stale := filepath.Join(se.repo, ".punt-labs", "ethos", "identities", "stale.yaml")
	require.NoError(t, os.WriteFile(stale,
		[]byte("name: Stale\nhandle: stale\nkind: human\n"), 0o644))

	stdout, stderr, exit := runCLI(t, se, "vendor", "test-agent", "--apply")
	require.Equal(t, 0, exit, "vendor --apply without --prune: stderr=%s", stderr)

	assert.Contains(t, stdout, "Would remove 1 file(s)",
		"an --apply run with no --prune must not claim it removed anything")
	assert.NotContains(t, stdout, "Removed 1 file(s)")
	assert.Contains(t, stdout, "(with --prune)",
		"the hint must appear on --apply alone, not only on a plain dry run")
	assert.FileExists(t, stale, "no --prune means the stale file must survive --apply")
}

// TestVendorApplyJSONWithoutPruneReportsPruneFalse pins that a --json plan
// carries the actual --prune setting rather than letting a reader infer
// removal from "applied" alone. Applied is true whenever --apply ran; it
// says nothing about whether files were pruned.
func TestVendorApplyJSONWithoutPruneReportsPruneFalse(t *testing.T) {
	dir := enableGitRepo(t)
	t.Chdir(dir)

	home := os.Getenv("HOME")
	idDir := filepath.Join(home, ".punt-labs", "ethos", "identities")
	if err := os.MkdirAll(idDir, 0o755); err != nil {
		t.Fatalf("mkdir identities: %v", err)
	}
	id := "name: Brian K\nhandle: bwk\nkind: agent\n"
	if err := os.WriteFile(filepath.Join(idDir, "bwk.yaml"), []byte(id), 0o644); err != nil {
		t.Fatalf("write identity: %v", err)
	}

	jsonOutput = true
	defer func() { jsonOutput = false }()

	out, _, err := runVendorCmd(t, "bwk", "--apply", "--json")
	if err != nil {
		t.Fatalf("vendor --apply --json: %v", err)
	}
	if !strings.Contains(out, `"prune": false`) {
		t.Errorf("output = %s, want \"prune\": false", out)
	}
}
