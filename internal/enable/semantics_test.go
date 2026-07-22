package enable

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tmpGitRepo creates a git repo under /tmp, never under this repo's TMPDIR
// (which .envrc points inside the working tree). Enable resolves the repo root
// and deposits into it; a fixture inside this repo risks contaminating it if
// resolution ever escapes, so the enable-path fixtures stay in /tmp.
func tmpGitRepo(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ethos-enable-sem-*")
	if err != nil {
		t.Skipf("cannot create an outside-repo temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "test")
	return dir
}

// TestEnableCreatesMissingHostFile pins the no-host-file semantics: a repo with
// no CLAUDE.md at all gets one created at mode 0644 whose sole content is the
// canonical import line plus a newline. The standard's write rule is append to
// the host file (§2.4); with no host file, "append" degrades to create-with-
// just-the-import, and a fresh file gets 0644. This is the chosen behavior —
// enable never refuses for a missing host, and never writes anything but the
// one import line.
func TestEnableCreatesMissingHostFile(t *testing.T) {
	dir := tmpGitRepo(t)
	claudePath := filepath.Join(dir, "CLAUDE.md")
	if exists(claudePath) {
		t.Fatal("fixture unexpectedly has a CLAUDE.md")
	}
	if _, err := Enable(dir); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	got := readFile(t, claudePath)
	if want := CanonicalImport + "\n"; got != want {
		t.Errorf("created CLAUDE.md = %q, want exactly %q", got, want)
	}
	info, err := os.Stat(claudePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("created CLAUDE.md mode = %o, want 0644", info.Mode().Perm())
	}
}

// TestEnableDoesNotWriteConfigZone pins the setup/enable separation
// (operator-ratified, design §"enable versus init and setup"): enable in a repo
// with no config writes no .punt-labs/ethos.yaml and no identities, and instead
// surfaces the "run ethos setup" hint. enable owns the Vendored and Marker
// zones only; setup owns the Config zone. Neither calls the other.
func TestEnableDoesNotWriteConfigZone(t *testing.T) {
	dir := tmpGitRepo(t)
	rep, err := Enable(dir)
	if err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if exists(filepath.Join(dir, ".punt-labs", "ethos.yaml")) {
		t.Error("enable wrote .punt-labs/ethos.yaml — config zone must be untouched")
	}
	if exists(filepath.Join(dir, ".punt-labs", "ethos", "identities")) {
		t.Error("enable created an identities/ directory — config zone must be untouched")
	}
	if rep.Hint == "" {
		t.Error("no setup hint when repo config is absent")
	}
	if !strings.Contains(rep.Hint, "setup") {
		t.Errorf("hint = %q, want a reference to ethos setup", rep.Hint)
	}
}
