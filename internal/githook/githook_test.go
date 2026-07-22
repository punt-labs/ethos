package githook

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	tag   = "ETHOS DES-058 SEAL"
	ident = "hooks/pre-commit.sh — Seal pending live audit lines"
)

// src is a stand-in for the embedded pre-commit script: a shebang, the ident
// header on line 2, a $? capture, and a seal call that preserves host status.
var src = []byte(`#!/bin/sh
# ` + ident + `
_host_status=$?
if command -v ethos >/dev/null 2>&1; then
  ethos audit seal || exit 2
fi
exit "$_host_status"
`)

func chainPre(t *testing.T, dest string) Result {
	t.Helper()
	res, err := Chain(dest, src, tag, ident)
	if err != nil {
		t.Fatalf("Chain(%s): %v", dest, err)
	}
	return res
}

func read(t *testing.T, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("reading %s: %v", p, err)
	}
	return string(data)
}

func countBegin(body string) int {
	return strings.Count(body, "# --- BEGIN "+tag)
}

func isExec(t *testing.T, p string) bool {
	t.Helper()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	return info.Mode().Perm()&0o111 != 0
}

func TestChainFreshInstall(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "pre-commit")
	res := chainPre(t, dest)
	if res.Action != "installed" {
		t.Errorf("action = %q, want installed", res.Action)
	}
	body := read(t, dest)
	if !isExec(t, dest) {
		t.Error("hook not executable")
	}
	// A brand-new hook gets 0755.
	if info, err := os.Stat(dest); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o755 {
		t.Errorf("new hook mode = %o, want 0755", info.Mode().Perm())
	}
	if !strings.HasPrefix(body, "#!/bin/sh\n") {
		t.Error("first line is not a shebang")
	}
	if countBegin(body) != 1 {
		t.Errorf("BEGIN markers = %d, want 1", countBegin(body))
	}
	if !strings.Contains(body, "# --- END "+tag) {
		t.Error("END marker missing")
	}
	if !strings.Contains(body, "audit seal") {
		t.Error("seal body missing")
	}
}

func TestChainPreservesExistingHookMode(t *testing.T) {
	// Chaining into an existing 0700 hook must not widen it to 0755 (C2).
	dest := filepath.Join(t.TempDir(), "pre-commit")
	if err := os.WriteFile(dest, []byte("#!/bin/sh\nrun_lint || exit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	chainPre(t, dest)
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("hook mode = %o, want 0700 (chaining must not widen it)", info.Mode().Perm())
	}
}

func TestChainForeignHostPreserved(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "pre-commit")
	host := "#!/bin/sh\n# a foreign hook\nrun_something || exit 1\n"
	if err := os.WriteFile(dest, []byte(host), 0o755); err != nil {
		t.Fatal(err)
	}
	res := chainPre(t, dest)
	if res.Action != "chained" {
		t.Errorf("action = %q, want chained", res.Action)
	}
	body := read(t, dest)
	if !strings.Contains(body, "run_something") {
		t.Error("host content lost")
	}
	if countBegin(body) != 1 {
		t.Errorf("BEGIN markers = %d, want 1", countBegin(body))
	}
	if strings.Count(body, "#!/bin/sh") != 1 {
		t.Error("shebang duplicated inside the section")
	}
}

func TestChainIdempotent(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "pre-commit")
	host := "#!/bin/sh\nrun_something || exit 1\n"
	if err := os.WriteFile(dest, []byte(host), 0o755); err != nil {
		t.Fatal(err)
	}
	chainPre(t, dest)
	before := read(t, dest)
	res := chainPre(t, dest)
	if res.Action != "refreshed" {
		t.Errorf("action = %q, want refreshed", res.Action)
	}
	after := read(t, dest)
	if before != after {
		t.Errorf("content changed on re-run:\nbefore=%q\nafter=%q", before, after)
	}
	if countBegin(after) != 1 {
		t.Errorf("BEGIN markers = %d, want 1", countBegin(after))
	}
}

func TestChainInterimVersionedMarkerReplaced(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "pre-commit")
	// A real v4.1.0 section carried the whole pre-commit script, so its first
	// body line is the ident header — the fingerprint stripSection checks.
	host := "#!/usr/bin/env sh\n" +
		"# --- BEGIN BEADS INTEGRATION v1.0.4 ---\n" +
		"bd hooks run pre-commit \"$@\" || true\n" +
		"# --- END BEADS INTEGRATION v1.0.4 ---\n\n" +
		"# --- BEGIN ETHOS DES-058 SEAL v4.1.0 ---\n" +
		"# " + ident + "\n" +
		"ethos audit seal || exit 2\n" +
		"# --- END ETHOS DES-058 SEAL ---\n"
	if err := os.WriteFile(dest, []byte(host), 0o755); err != nil {
		t.Fatal(err)
	}
	chainPre(t, dest)
	body := read(t, dest)
	if !strings.Contains(body, "BEGIN BEADS INTEGRATION") {
		t.Error("beads section lost")
	}
	if strings.Contains(body, "v4.1.0") {
		t.Error("versioned ethos marker not replaced")
	}
	if countBegin(body) != 1 {
		t.Errorf("BEGIN markers = %d, want 1", countBegin(body))
	}
}

func TestChainStandaloneConvertedToMarkerForm(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "pre-commit")
	body := append([]byte{}, src...)
	body = append(body, "# stale trailing edit\n"...)
	if err := os.WriteFile(dest, body, 0o755); err != nil {
		t.Fatal(err)
	}
	res := chainPre(t, dest)
	if res.Action != "refreshed" {
		t.Errorf("action = %q, want refreshed", res.Action)
	}
	out := read(t, dest)
	if countBegin(out) != 1 {
		t.Errorf("BEGIN markers = %d, want 1", countBegin(out))
	}
	if strings.Contains(out, "stale trailing edit") {
		t.Error("stale trailing edit not dropped")
	}
}

func TestChainCatAppendHybridNotOurs(t *testing.T) {
	// Host lint, then our ENTIRE standalone appended (header mid-file). The
	// positional line-2 check must judge it NOT ours and strip-and-append.
	dest := filepath.Join(t.TempDir(), "pre-commit")
	host := []byte("#!/bin/sh\n# my precious lint hook\nmy-lint --strict\n")
	host = append(host, src...)
	if err := os.WriteFile(dest, host, 0o755); err != nil {
		t.Fatal(err)
	}
	chainPre(t, dest)
	body := read(t, dest)
	if !strings.Contains(body, "my-lint --strict") {
		t.Error("host lint line lost")
	}
	if countBegin(body) != 1 {
		t.Errorf("BEGIN markers = %d, want 1", countBegin(body))
	}
}

func TestChainWarnsUnconditionalTail(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		wantSub string
	}{
		{"plain exit", "#!/bin/sh\ndo_stuff\nexit 0\n", "unconditional 'exit'"},
		{"indented exit", "#!/bin/sh\ndo_stuff\n    exit 0\n", "unconditional 'exit'"},
		{"exit then comment", "#!/bin/sh\ndo_stuff\nexit 1\n# note\n", "unconditional 'exit'"},
		{"exit trailing tab", "#!/bin/sh\ndo_stuff\nexit\t\n", "unconditional 'exit'"},
		{"exit semicolon", "#!/bin/sh\ndo_stuff\nexit;\n", "unconditional 'exit'"},
		{"exec program", "#!/bin/sh\nexec lefthook run pre-commit \"$@\"\n", "unconditional 'exec'"},
		{"exec quoted var", "#!/bin/sh\ndo_stuff\nexec \"$RUNNER\" pre-commit\n", "unconditional 'exec'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "pre-commit")
			if err := os.WriteFile(dest, []byte(tt.host), 0o755); err != nil {
				t.Fatal(err)
			}
			res := chainPre(t, dest)
			if !hasWarning(res.Warnings, tt.wantSub) {
				t.Errorf("warnings = %v, want one containing %q", res.Warnings, tt.wantSub)
			}
		})
	}
}

func TestChainDoesNotWarnFdRedirection(t *testing.T) {
	tests := []struct {
		name string
		host string
	}{
		{"exec fd dup", "#!/bin/sh\ndo_stuff\nexec 3>&1\n"},
		{"exec redirect", "#!/bin/sh\ndo_stuff\nexec >log 2>&1\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "pre-commit")
			if err := os.WriteFile(dest, []byte(tt.host), 0o755); err != nil {
				t.Fatal(err)
			}
			res := chainPre(t, dest)
			if hasWarning(res.Warnings, "exec") {
				t.Errorf("warned on fd redirection: %v", res.Warnings)
			}
		})
	}
}

func TestChainNonShellHostSkipped(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "pre-commit")
	host := "#!/usr/bin/env python3\nimport sys\nprint('lint')\n"
	if err := os.WriteFile(dest, []byte(host), 0o755); err != nil {
		t.Fatal(err)
	}
	res := chainPre(t, dest)
	if res.Action != "skipped-non-shell" {
		t.Errorf("action = %q, want skipped-non-shell", res.Action)
	}
	if read(t, dest) != host {
		t.Error("non-shell host was modified")
	}
	if !hasWarning(res.Warnings, "non-shell") {
		t.Errorf("no non-shell warning: %v", res.Warnings)
	}
}

func TestChainUnterminatedMarkerAborts(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "pre-commit")
	host := "#!/bin/sh\nrun_host || exit 1\n# --- BEGIN ETHOS DES-058 SEAL ---\nethos audit seal || exit 2\n"
	if err := os.WriteFile(dest, []byte(host), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Chain(dest, src, tag, ident); err == nil {
		t.Fatal("expected abort on unterminated marker")
	}
	if read(t, dest) != host {
		t.Error("host content changed on abort")
	}
}

func TestChainSymlinkTargetUpdated(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real-hook")
	if err := os.WriteFile(real, []byte("#!/bin/sh\nrun || exit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "pre-commit")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	res := chainPre(t, link)
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Error("link no longer a symlink")
	}
	if !strings.Contains(read(t, real), "# --- BEGIN "+tag) {
		t.Error("link target did not receive the section")
	}
	if !hasWarning(res.Warnings, "symlink") {
		t.Errorf("no symlink warning: %v", res.Warnings)
	}
}

func TestChainMktempFailureAborts(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	dir := t.TempDir()
	dest := filepath.Join(dir, "pre-commit")
	if err := os.WriteFile(dest, []byte("#!/bin/sh\nbd || exit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()
	if _, err := Chain(dest, src, tag, ident); err == nil {
		t.Fatal("expected abort on unwritable hooks dir")
	}
}

func TestChainPreservesHostStatus(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEthos := func(code int) {
		body := "#!/bin/sh\nexit " + strconv.Itoa(code) + "\n"
		if err := os.WriteFile(filepath.Join(bin, "ethos"), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run := func(hook string) int {
		cmd := exec.Command("sh", hook)
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		cmd.Stdin = nil
		if err := cmd.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				return ee.ExitCode()
			}
			t.Fatalf("run %s: %v", hook, err)
		}
		return 0
	}

	writeEthos(0)
	failHost := filepath.Join(dir, "fail")
	if err := os.WriteFile(failHost, []byte("#!/bin/sh\nfalse\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	chainPre(t, failHost)
	if run(failHost) == 0 {
		t.Error("host failure fell through as success after chaining")
	}

	okHost := filepath.Join(dir, "ok")
	if err := os.WriteFile(okHost, []byte("#!/bin/sh\ntrue\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	chainPre(t, okHost)
	if run(okHost) != 0 {
		t.Error("host success did not exit 0 after chaining")
	}

	writeEthos(2)
	if run(okHost) != 2 {
		t.Error("seal failure did not exit 2 even though host passed")
	}
}

func TestUnchainRoundTripForeignHost(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "pre-commit")
	host := "#!/bin/sh\n# a foreign hook\nrun_something || exit 1\n"
	if err := os.WriteFile(dest, []byte(host), 0o755); err != nil {
		t.Fatal(err)
	}
	chainPre(t, dest)
	res, err := Unchain(dest, tag, ident)
	if err != nil {
		t.Fatalf("Unchain: %v", err)
	}
	if res.Action != "reduced" {
		t.Errorf("action = %q, want reduced", res.Action)
	}
	if got := read(t, dest); got != host {
		t.Errorf("host not restored byte-for-byte:\nwant=%q\ngot =%q", host, got)
	}
}

func TestUnchainStandaloneRemovesFile(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "pre-commit")
	chainPre(t, dest) // fresh install → standalone marker form
	res, err := Unchain(dest, tag, ident)
	if err != nil {
		t.Fatalf("Unchain: %v", err)
	}
	if res.Action != "removed" {
		t.Errorf("action = %q, want removed", res.Action)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("standalone hook file was not removed")
	}
}

func TestUnchainNoSectionNoop(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "pre-commit")
	host := "#!/bin/sh\nrun_something\n"
	if err := os.WriteFile(dest, []byte(host), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := Unchain(dest, tag, ident)
	if err != nil {
		t.Fatalf("Unchain: %v", err)
	}
	if res.Action != "noop" {
		t.Errorf("action = %q, want noop", res.Action)
	}
	if read(t, dest) != host {
		t.Error("host changed on a no-op unchain")
	}
}

func TestUnchainAbsent(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "pre-commit")
	res, err := Unchain(dest, tag, ident)
	if err != nil {
		t.Fatalf("Unchain: %v", err)
	}
	if res.Action != "absent" {
		t.Errorf("action = %q, want absent", res.Action)
	}
}

func TestChainPreservesHeredocFakeMarker(t *testing.T) {
	// A host documenting the marker format inside a heredoc must not have its
	// heredoc body deleted: the fake BEGIN/END inside it are host-owned text,
	// not real section boundaries.
	dest := filepath.Join(t.TempDir(), "pre-commit")
	host := "#!/bin/sh\n" +
		"cat <<'EOF'\n" +
		"# --- BEGIN " + tag + " ---\n" +
		"example documentation, not a real marker\n" +
		"# --- END " + tag + " ---\n" +
		"EOF\n" +
		"echo done\n"
	if err := os.WriteFile(dest, []byte(host), 0o755); err != nil {
		t.Fatal(err)
	}
	res := chainPre(t, dest)
	if res.Action != "chained" {
		t.Errorf("action = %q, want chained", res.Action)
	}
	body := read(t, dest)
	// The entire host prefix (heredoc and all) must survive verbatim.
	if !strings.HasPrefix(body, host) {
		t.Errorf("host heredoc content not preserved:\n%q", body)
	}
	if !strings.Contains(body, "example documentation, not a real marker") {
		t.Error("heredoc documentation line was deleted")
	}
	// Exactly one real section — our appended one — must exist.
	if countBegin(body) != 2 {
		// two textual BEGINs: the heredoc's fake one + our real one.
		t.Errorf("BEGIN text count = %d, want 2 (1 fake in heredoc + 1 real)", countBegin(body))
	}

	// Unchain must remove only our real section and restore the host exactly.
	if _, err := Unchain(dest, tag, ident); err != nil {
		t.Fatalf("Unchain: %v", err)
	}
	if got := read(t, dest); got != host {
		t.Errorf("Unchain did not restore the host byte-for-byte:\nwant=%q\ngot =%q", host, got)
	}
}

func TestChainCRLFHostGetsCRLFSection(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "pre-commit")
	host := "#!/bin/sh\r\nrun_something || exit 1\r\n"
	if err := os.WriteFile(dest, []byte(host), 0o755); err != nil {
		t.Fatal(err)
	}
	chainPre(t, dest)
	body := read(t, dest)
	// The appended section's marker lines must use CRLF, matching the host.
	if !strings.Contains(body, "# --- BEGIN "+tag+" ---\r\n") {
		t.Errorf("section BEGIN did not use CRLF:\n%q", body)
	}
	if !strings.Contains(body, "# --- END "+tag+" ---\r\n") {
		t.Errorf("section END did not use CRLF:\n%q", body)
	}
	// No bare LF-only marker line leaked in.
	if strings.Contains(body, "# --- BEGIN "+tag+" ---\n") && !strings.Contains(body, "# --- BEGIN "+tag+" ---\r\n") {
		t.Error("section used LF in a CRLF host")
	}
}

func TestStripSectionRefusesForeignFingerprint(t *testing.T) {
	// A real (non-heredoc) BEGIN whose body does not carry our ident is not an
	// ethos-written section — Chain must refuse rather than delete it.
	dest := filepath.Join(t.TempDir(), "pre-commit")
	host := "#!/bin/sh\n" +
		"# --- BEGIN " + tag + " ---\n" +
		"someone hand-wrote this and it is not ours\n" +
		"# --- END " + tag + " ---\n"
	if err := os.WriteFile(dest, []byte(host), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Chain(dest, src, tag, ident); err == nil {
		t.Fatal("expected a fingerprint refusal, got nil")
	} else if !strings.Contains(err.Error(), "fingerprint") {
		t.Errorf("error = %q, want a fingerprint refusal", err)
	}
	if got := read(t, dest); got != host {
		t.Error("host changed despite the refusal")
	}
}

func TestChainArithmeticShiftHostIsClean(t *testing.T) {
	// A host using $((1<<2)) must not be misread as opening a heredoc: Chain
	// appends once and is idempotent, and Unchain restores the host exactly.
	dest := filepath.Join(t.TempDir(), "pre-commit")
	host := "#!/bin/sh\nx=$((1<<2))\necho \"$x\"\nrun_lint || exit 1\n"
	if err := os.WriteFile(dest, []byte(host), 0o755); err != nil {
		t.Fatal(err)
	}
	chainPre(t, dest)
	chainPre(t, dest) // idempotent
	body := read(t, dest)
	if countBegin(body) != 1 {
		t.Errorf("BEGIN markers = %d, want 1", countBegin(body))
	}
	if !strings.Contains(body, "x=$((1<<2))") {
		t.Error("arithmetic host line lost")
	}
	res, err := Unchain(dest, tag, ident)
	if err != nil {
		t.Fatalf("Unchain: %v", err)
	}
	if res.Action != "reduced" {
		t.Errorf("action = %q, want reduced", res.Action)
	}
	if got := read(t, dest); got != host {
		t.Errorf("Unchain did not restore the host:\nwant=%q\ngot =%q", host, got)
	}
}

func TestChainMultiLineArithmeticHost(t *testing.T) {
	// Multi-line arithmetic ($((1 +\n2 << 3))) must not be misread as opening a
	// heredoc on its second line: Chain appends once and is idempotent, and
	// Unchain restores the host byte-for-byte.
	dest := filepath.Join(t.TempDir(), "pre-commit")
	host := "#!/bin/sh\nx=$((1 +\n2 << 3))\necho \"$x\"\nrun_lint || exit 1\n"
	if err := os.WriteFile(dest, []byte(host), 0o755); err != nil {
		t.Fatal(err)
	}
	if res := chainPre(t, dest); res.Action != "chained" {
		t.Errorf("action = %q, want chained", res.Action)
	}
	if res := chainPre(t, dest); res.Action != "refreshed" {
		t.Errorf("re-chain action = %q, want refreshed", res.Action)
	}
	if n := countBegin(read(t, dest)); n != 1 {
		t.Errorf("BEGIN markers = %d, want 1", n)
	}
	if _, err := Unchain(dest, tag, ident); err != nil {
		t.Fatalf("Unchain: %v", err)
	}
	if got := read(t, dest); got != host {
		t.Errorf("Unchain did not restore the host:\nwant=%q\ngot =%q", host, got)
	}
}

func TestChainMultiLineArithmeticAboveSectionNoDuplicate(t *testing.T) {
	// The worst case: multi-line arithmetic above a real section, where the
	// phantom delimiter a per-line scan would open masks the real section and
	// produces a DUPLICATE. With span state carried across lines there is no
	// phantom: exactly one section.
	dest := filepath.Join(t.TempDir(), "pre-commit")
	host := "#!/bin/sh\nx=$((1 +\n2 << 3))\n" + string(sectionBytes(tag, src))
	if err := os.WriteFile(dest, []byte(host), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := Chain(dest, src, tag, ident)
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	if res.Action != "refreshed" {
		t.Errorf("action = %q, want refreshed", res.Action)
	}
	if n := countBegin(read(t, dest)); n != 1 {
		t.Errorf("BEGIN markers = %d, want exactly 1 (no phantom-heredoc duplicate)", n)
	}
}

func TestChainBareArithmeticCommandHost(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "pre-commit")
	host := "#!/bin/sh\n(( flag = 1 << 2 ))\nrun_lint || exit 1\n"
	if err := os.WriteFile(dest, []byte(host), 0o755); err != nil {
		t.Fatal(err)
	}
	chainPre(t, dest)
	body := read(t, dest)
	if countBegin(body) != 1 {
		t.Errorf("BEGIN markers = %d, want 1", countBegin(body))
	}
	if !strings.Contains(body, "(( flag = 1 << 2 ))") {
		t.Error("bare arithmetic host line lost")
	}
}

func TestChainRefusesTruncatedDuplicateAfterCompleteSection(t *testing.T) {
	// A complete fingerprinted section, then a truncated duplicate BEGIN (no
	// END after it) with host content below. File-global pairing passed the
	// gate and stripSection dropped from the second BEGIN to EOF (silent host
	// deletion). Per-BEGIN pairing must refuse, byte-unchanged (B1).
	dest := filepath.Join(t.TempDir(), "pre-commit")
	host := "#!/bin/sh\n" +
		string(sectionBytes(tag, src)) +
		"# --- BEGIN " + tag + " ---\n" +
		"# " + ident + "\n" +
		"precious host content below the truncated BEGIN\n"
	if err := os.WriteFile(dest, []byte(host), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Chain(dest, src, tag, ident); err == nil {
		t.Fatal("expected a refusal on a truncated duplicate BEGIN")
	} else if !strings.Contains(err.Error(), "no matching END") {
		t.Errorf("error = %q, want an unpaired-BEGIN refusal", err)
	}
	if got := read(t, dest); got != host {
		t.Error("host content changed despite the refusal")
	}
}

func TestChainRefusesUnterminatedHeredoc(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "pre-commit")
	host := "#!/bin/sh\nrun_lint\ncat <<EOF\nnever closed\n"
	if err := os.WriteFile(dest, []byte(host), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Chain(dest, src, tag, ident); err == nil {
		t.Fatal("expected a refusal on an unterminated heredoc")
	} else if !strings.Contains(err.Error(), "unterminated here-document") {
		t.Errorf("error = %q, want an unterminated-heredoc refusal", err)
	}
	if got := read(t, dest); got != host {
		t.Error("host changed despite the refusal")
	}
	// Unchain must refuse too, not lie "noop".
	if _, err := Unchain(dest, tag, ident); err == nil {
		t.Fatal("expected Unchain to refuse on an unterminated heredoc")
	}
	if got := read(t, dest); got != host {
		t.Error("host changed despite the Unchain refusal")
	}
}

func TestHooksDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	t.Run("normal repo under .git, no warning", func(t *testing.T) {
		dir := t.TempDir()
		gitInit(t, dir)
		hd, warns := HooksDir(dir)
		if !strings.Contains(hd, filepath.Join(".git", "hooks")) {
			t.Errorf("hooks dir = %q, want under .git/hooks", hd)
		}
		if len(warns) != 0 {
			t.Errorf("unexpected warnings: %v", warns)
		}
	})

	t.Run("core.hooksPath inside work tree warns tracked", func(t *testing.T) {
		dir := t.TempDir()
		gitInit(t, dir)
		gitConfig(t, dir, "core.hooksPath", ".husky")
		hd, warns := HooksDir(dir)
		if !strings.Contains(hd, ".husky") {
			t.Errorf("hooks dir = %q, want .husky", hd)
		}
		if !hasWarning(warns, "inside the work tree") {
			t.Errorf("warnings = %v, want 'inside the work tree'", warns)
		}
	})

	t.Run("core.hooksPath outside repo warns shared", func(t *testing.T) {
		dir := t.TempDir()
		shared := t.TempDir()
		gitInit(t, dir)
		gitConfig(t, dir, "core.hooksPath", shared)
		_, warns := HooksDir(dir)
		if !hasWarning(warns, "outside the repo") {
			t.Errorf("warnings = %v, want 'outside the repo'", warns)
		}
		if hasWarning(warns, "inside the work tree") {
			t.Errorf("misclassified as inside the work tree: %v", warns)
		}
	})
}

func TestManualHooksDirWorktree(t *testing.T) {
	// A worktree's .git file points at the common gitdir via a gitdir: line
	// and a commondir file; manualHooksDir must land on the common hooks.
	root := t.TempDir()
	commonGit := filepath.Join(root, "maindir", ".git")
	if err := os.MkdirAll(filepath.Join(commonGit, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	wtGitDir := filepath.Join(commonGit, "worktrees", "wt")
	if err := os.MkdirAll(wtGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtGitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+wtGitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := manualHooksDir(wt)
	want := filepath.Join(commonGit, "hooks")
	if evalPath(got) != evalPath(want) {
		t.Errorf("worktree hooks dir = %q, want %q", got, want)
	}
}

func hasWarning(warns []string, sub string) bool {
	for _, w := range warns {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	run(t, dir, "init", "-q")
}

func gitConfig(t *testing.T, dir, key, val string) {
	t.Helper()
	run(t, dir, "config", key, val)
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, errb.String())
	}
}

