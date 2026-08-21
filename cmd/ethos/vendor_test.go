package main

import (
	"bytes"
	"strings"
	"testing"
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
