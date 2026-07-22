package enable

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "test")
	return dir
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, errb.String())
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("reading %s: %v", p, err)
	}
	return string(data)
}

func TestEnableDepositsFourArtifacts(t *testing.T) {
	dir := gitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# repo prose\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Enable(dir)
	if err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !exists(filepath.Join(dir, guideRel)) {
		t.Error("guide not deposited")
	}
	if !exists(filepath.Join(dir, manifestRel)) {
		t.Error("manifest not deposited")
	}
	if !exists(filepath.Join(dir, markerRel)) {
		t.Error("marker not written")
	}
	claude := readFile(t, filepath.Join(dir, "CLAUDE.md"))
	if strings.Count(claude, CanonicalImport) != 1 {
		t.Errorf("import line count = %d, want 1", strings.Count(claude, CanonicalImport))
	}
	if !strings.HasPrefix(claude, "# repo prose\n") {
		t.Error("host prose not preserved")
	}
	pre := readFile(t, filepath.Join(dir, ".git", "hooks", "pre-commit"))
	if !strings.Contains(pre, "# --- BEGIN "+sealTag) {
		t.Error("seal section not chained")
	}
	cm := readFile(t, filepath.Join(dir, ".git", "hooks", "commit-msg"))
	if !strings.Contains(cm, "# --- BEGIN "+trailerTag) {
		t.Error("trailer section not chained")
	}
	if rep.Hint == "" {
		t.Error("expected a setup hint when config is absent")
	}
}

func TestEnableDisableRoundTrip(t *testing.T) {
	dir := gitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Enable(dir); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if _, err := Disable(dir, false); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if exists(filepath.Join(dir, markerRel)) {
		t.Error("marker not deleted on disable")
	}
	if got := readFile(t, filepath.Join(dir, "CLAUDE.md")); strings.Contains(got, CanonicalImport) {
		t.Error("import line not removed on disable")
	}
	if !exists(filepath.Join(dir, guideRel)) {
		t.Error("guide should stay dormant after disable")
	}
	// Fresh install produced standalone marker forms; unchain removes them.
	if exists(filepath.Join(dir, ".git", "hooks", "pre-commit")) {
		t.Error("standalone pre-commit not removed on disable")
	}
	if exists(filepath.Join(dir, ".git", "hooks", "commit-msg")) {
		t.Error("standalone commit-msg not removed on disable")
	}
	// Re-enable restores all four.
	if _, err := Enable(dir); err != nil {
		t.Fatalf("re-Enable: %v", err)
	}
	if !exists(filepath.Join(dir, markerRel)) {
		t.Error("marker not restored on re-enable")
	}
	if got := readFile(t, filepath.Join(dir, "CLAUDE.md")); strings.Count(got, CanonicalImport) != 1 {
		t.Error("import line not restored on re-enable")
	}
}

func TestEnableIdempotent(t *testing.T) {
	dir := gitRepo(t)
	if _, err := Enable(dir); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if _, err := Enable(dir); err != nil {
		t.Fatalf("re-Enable: %v", err)
	}
	claude := readFile(t, filepath.Join(dir, "CLAUDE.md"))
	if strings.Count(claude, CanonicalImport) != 1 {
		t.Errorf("import line count = %d, want 1", strings.Count(claude, CanonicalImport))
	}
	pre := readFile(t, filepath.Join(dir, ".git", "hooks", "pre-commit"))
	if strings.Count(pre, "# --- BEGIN "+sealTag) != 1 {
		t.Errorf("seal BEGIN count = %d, want 1", strings.Count(pre, "# --- BEGIN "+sealTag))
	}
}

func TestEnablePreservesConfigZone(t *testing.T) {
	dir := gitRepo(t)
	zone := filepath.Join(dir, ".punt-labs", "ethos")
	fixtures := map[string]string{
		filepath.Join(zone, "identities", "mal.yaml"):                    "handle: mal\n",
		filepath.Join(zone, "teams", "crew.yaml"):                        "name: crew\n",
		filepath.Join(zone, "sessions", "2026-07-22-abc", "audit.jsonl"): "{\"ts\":\"1\"}\n",
	}
	for p, body := range fixtures {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Enable(dir); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	for p, body := range fixtures {
		if got := readFile(t, p); got != body {
			t.Errorf("%s changed: got %q, want %q", p, got, body)
		}
	}
}

func TestEnableConvergesInterimRepo(t *testing.T) {
	dir := gitRepo(t)
	// A v4.1.1-interim repo: hooks chained, but no marker and no import.
	hooksDir := filepath.Join(dir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	host := "#!/bin/sh\n" +
		"# --- BEGIN BEADS INTEGRATION ---\n" +
		"bd hooks run pre-commit || exit 1\n" +
		"# --- END BEADS INTEGRATION ---\n" +
		"# --- BEGIN " + sealTag + " ---\n" +
		"ethos audit seal || exit 2\n" +
		"# --- END " + sealTag + " ---\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(host), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Enable(dir); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !exists(filepath.Join(dir, markerRel)) {
		t.Error("marker not written on convergence")
	}
	pre := readFile(t, filepath.Join(hooksDir, "pre-commit"))
	if !strings.Contains(pre, "BEGIN BEADS INTEGRATION") {
		t.Error("beads host content lost on convergence")
	}
	if strings.Count(pre, "# --- BEGIN "+sealTag) != 1 {
		t.Errorf("seal BEGIN count = %d, want 1", strings.Count(pre, "# --- BEGIN "+sealTag))
	}
}

func TestEnableGitlinkGuard(t *testing.T) {
	dir := gitRepo(t)
	// Record .punt-labs/ethos as a gitlink (mode 160000) in the index.
	sha := "0000000000000000000000000000000000000001"
	cmd := exec.Command("git", "-C", dir, "update-index", "--add", "--cacheinfo", "160000,"+sha+",.punt-labs/ethos")
	if err := cmd.Run(); err != nil {
		t.Skipf("cannot stage a gitlink in this git: %v", err)
	}
	if _, err := Enable(dir); err == nil {
		t.Fatal("expected gitlink guard to error")
	} else if !strings.Contains(err.Error(), "ethos-e29s") {
		t.Errorf("error = %q, want vendor-first remedy ethos-e29s", err)
	}
}

func TestMarkerLastOnDepositFailure(t *testing.T) {
	dir := gitRepo(t)
	// Seed a manifest that lists only the guide, then pre-create the manifest
	// path as an unlisted, existing file → collision on the next deposit.
	if err := os.MkdirAll(filepath.Join(dir, ".punt-labs", "ethos"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestRel), []byte(guideRel+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// manifestRel now exists but is not in the previous manifest's set → the
	// deposit must collision-error before the marker is written.
	if _, err := Enable(dir); err == nil {
		t.Fatal("expected a collision error")
	}
	if exists(filepath.Join(dir, markerRel)) {
		t.Error("marker written despite a failed deposit (marker-last violated)")
	}
}

func TestDisableStrandsUnsealedLines(t *testing.T) {
	dir := gitRepo(t)
	if _, err := Enable(dir); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	live := filepath.Join(dir, ".punt-labs", "local", "ethos", "sessions")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	liveFile := filepath.Join(live, "2026-07-22-sid.audit.jsonl")
	body := "{\"ts\":\"2026-07-22T00:00:01.000000001Z\",\"seq\":1}\n" +
		"{\"ts\":\"2026-07-22T00:00:02.000000002Z\",\"seq\":2}\n"
	if err := os.WriteFile(liveFile, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Disable(dir, false)
	if err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if got := readFile(t, liveFile); got != body {
		t.Error("local-zone lines not preserved on disable")
	}
	found := false
	for _, s := range rep.Steps {
		if s.Step == "audit" && s.Status == "info" {
			found = true
		}
	}
	if !found {
		t.Error("no informational unsealed-lines notice")
	}
}

func TestDisableRefusesEnabledSiblingWorktree(t *testing.T) {
	dir := gitRepo(t)
	// A commit is needed before adding a worktree.
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "f")
	gitRun(t, dir, "commit", "-q", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	gitRun(t, dir, "worktree", "add", "-q", wt)
	if _, err := Enable(dir); err != nil {
		t.Fatalf("Enable main: %v", err)
	}
	if _, err := Enable(wt); err != nil {
		t.Fatalf("Enable worktree: %v", err)
	}
	// Disabling the main checkout must refuse — the sibling is still enabled.
	if _, err := Disable(dir, false); err == nil {
		t.Fatal("expected disable to refuse with an enabled sibling worktree")
	}
	// --force overrides.
	if _, err := Disable(dir, true); err != nil {
		t.Fatalf("Disable --force: %v", err)
	}
}

// TestMarkerGateRuntime exercises the embedded pre-commit gate: it seals only
// when the marker is present and preserves the host's fall-through status when
// dormant.
func TestMarkerGateRuntime(t *testing.T) {
	dir := gitRepo(t)
	// A host that fails by fall-through, so we can prove the gate preserves it.
	hooksDir := filepath.Join(dir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte("#!/bin/sh\nfalse\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Enable(dir); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	callLog := filepath.Join(t.TempDir(), "calls")
	stub := "#!/bin/sh\necho called >> \"" + callLog + "\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "ethos"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	runPre := func() int {
		cmd := exec.Command("sh", filepath.Join(hooksDir, "pre-commit"))
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		if err := cmd.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return ee.ExitCode()
			}
			t.Fatalf("run pre-commit: %v", err)
		}
		return 0
	}

	// Marker present: the gate passes and the seal is invoked.
	_ = runPre()
	if !exists(callLog) {
		t.Error("marker present but ethos audit seal was not invoked")
	}

	// Marker absent: the gate exits at the marker check with the host's
	// fall-through status (1 from false) and never calls ethos.
	if err := os.Remove(callLog); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, markerRel)); err != nil {
		t.Fatal(err)
	}
	code := runPre()
	if exists(callLog) {
		t.Error("marker absent but ethos was still invoked")
	}
	if code != 1 {
		t.Errorf("dormant gate exit = %d, want 1 (host fall-through preserved)", code)
	}
}
