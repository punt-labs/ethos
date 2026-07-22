package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runEnableCmd(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	rootCmd.SetOut(&outBuf)
	rootCmd.SetErr(&errBuf)
	rootCmd.SetArgs(args)
	defer func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		jsonOutput = false
		disableForce = false
	}()
	err := rootCmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

func enableGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Isolate HOME so claudemd's per-user lock dir lands in a temp dir.
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	cmd := exec.Command("git", "-C", dir, "init", "-q")
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	return dir
}

func TestEnableDisableCommandRoundTrip(t *testing.T) {
	dir := enableGitRepo(t)
	t.Chdir(dir)

	out, _, err := runEnableCmd(t, "enable")
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !strings.Contains(out, "ethos enabled in") {
		t.Errorf("enable output = %q", out)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".punt-labs", "ethos", "enabled")); statErr != nil {
		t.Error("marker not written by enable command")
	}

	out, _, err = runEnableCmd(t, "disable")
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if !strings.Contains(out, "ethos disabled in") {
		t.Errorf("disable output = %q", out)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".punt-labs", "ethos", "enabled")); !os.IsNotExist(statErr) {
		t.Error("marker not deleted by disable command")
	}
}

func TestEnableCommandJSON(t *testing.T) {
	dir := enableGitRepo(t)
	t.Chdir(dir)

	out, _, err := runEnableCmd(t, "enable", "--json")
	if err != nil {
		t.Fatalf("enable --json: %v", err)
	}
	var rep struct {
		RepoRoot string `json:"repo_root"`
		Steps    []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("enable --json output not valid JSON: %v\n%s", err, out)
	}
	if len(rep.Steps) == 0 {
		t.Error("no steps in JSON report")
	}
}

func TestEnableSurfacesWarningsOnErrorPath(t *testing.T) {
	// Repro: a hand-written guide (grandfather-overwritten with a warning) plus
	// an open-fence CLAUDE.md (the import step fails). The deposit warning must
	// still reach stderr even though a later step errors (R1).
	dir := enableGitRepo(t)
	zone := filepath.Join(dir, ".punt-labs", "ethos")
	if err := os.MkdirAll(zone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zone, "CLAUDE.md"), []byte("hand-written guide\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A CLAUDE.md ending inside an open code fence makes the import step error.
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# repo\n```\nunclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	_, errOut, err := runEnableCmd(t, "enable")
	if err == nil {
		t.Fatal("expected enable to fail on the open-fence CLAUDE.md")
	}
	if !strings.Contains(errOut, "overwritten") || !strings.Contains(errOut, ".punt-labs/ethos/CLAUDE.md") {
		t.Errorf("deposit overwrite warning not surfaced on the error path; stderr = %q", errOut)
	}
}

func TestEnableNotInRepoExits2(t *testing.T) {
	// TMPDIR points inside this repo (per .envrc), so t.TempDir would have a
	// .git ancestor. Use /tmp to land genuinely outside any git work tree —
	// the same escape the doctor tests use.
	dir, err := os.MkdirTemp("/tmp", "ethos-enable-*")
	if err != nil {
		t.Skipf("cannot create an outside-repo temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Chdir(dir)
	_, errOut, err := runEnableCmd(t, "enable")
	if err == nil {
		t.Fatal("expected an error outside a git repo")
	}
	if _, ok := err.(failClosed); !ok {
		t.Errorf("error type = %T, want failClosed (exit 2)", err)
	}
	if !strings.Contains(errOut, "not in a git repository") {
		t.Errorf("stderr = %q", errOut)
	}
}
