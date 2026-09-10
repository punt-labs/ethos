package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// sandboxTimeout bounds how long a sandboxed hook may run before doctor
// stops waiting for it. A real ethos-managed hook returns in well under a
// second; this is generous headroom for a slow disk without letting a
// pathological host hook chained alongside it (an infinite loop, a hung
// network call) block `ethos doctor` indefinitely. A var, not a const, so a
// test can shrink it to prove the bound actually fires rather than trusting
// the number in this comment.
var sandboxTimeout = 10 * time.Second

// errSandboxGitUnavailable is returned by hookInvocationObserved when git is
// not on PATH. The sandboxed hook body cannot be exercised without it — the
// installed hooks themselves gate on `git rev-parse --show-toplevel` (§2.7),
// so a git-less host could never run the real hook either; this is not a new
// dependency the sandbox introduces.
var errSandboxGitUnavailable = errors.New("git not found on PATH")

// hookInvocationObserved runs body — the exact bytes installed at a git hook
// path, host content and any chained ethos section together — inside a
// disposable, throwaway git repository, and reports whether it invoked the
// `ethos` binary with argv.
//
// This is DES-hook-verification (ethos-kcbv): it replaces a lexical scan of
// the hook's shell source with proof by execution. A lexical scanner over
// shell text needed four documented rounds of refinement across two
// branches to close successive false-negative corners (a bare substring
// match, an inline comment, a separator boundary, a heredoc body) and each
// round closed one shape while leaving the next lexical corner open by
// construction — a scanner can only special-case shapes someone already
// found. Executing the hook has no such corners: a heredoc body is never
// executed as a command because the shell that runs it never treats it as
// one; a comment is skipped because the shell skips it; `eval` and an
// aliased wrapper resolve correctly because the code actually runs. The one
// documented limitation this method trades in return is described below.
//
// The sandbox: a fresh temp directory, `git init`'d so the hook's own
// `git rev-parse --show-toplevel` gate succeeds; a `.punt-labs/ethos/enabled`
// marker so the hook's own enablement gate passes regardless of whether the
// REAL repo being checked is enabled (doctor wants to know "if this body
// runs, does it call ethos", independent of that repo's current enablement
// state — checkHookPresence composes that answer with the real marker
// separately); and a stub `ethos` executable placed first on PATH that
// records its argv to a log file and exits 0 rather than doing any real
// work. body is copied in verbatim and executed via its own file (respecting
// whatever shebang it carries, exactly how git itself runs a hook) — it is
// never wrapped in a `sh -c` invocation, so a non-shell hook is exercised
// (or fails to run at all) the same way git would run it.
//
// This executes untrusted hook content, including any foreign host section
// chained alongside the ethos section — that is unavoidable, since the
// question being answered ("does the installed file, as installed, invoke
// ethos") is a statement about the whole file. The sandbox bounds the
// blast radius (an isolated temp directory, an isolated HOME, a stubbed
// `ethos`, a timeout) but is not a full OS sandbox: no seccomp, no chroot,
// no network isolation. A malicious or badly broken host hook can still do
// anything its own process's OS permissions allow during the timeout
// window — write files the invoking user can write, make network calls,
// spin the CPU until the timeout fires. See DESIGN.md's ADR for this
// decision and the alternatives it rejects.
//
// argv is the ethos subcommand doctor is looking for, e.g.
// []string{"audit", "seal"}. needsMsgArg requests a scratch commit-message
// file be created and passed as $1, which the commit-msg hook requires
// before it will do anything.
func hookInvocationObserved(body []byte, argv []string, needsMsgArg bool) (bool, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return false, errSandboxGitUnavailable
	}

	dir, err := os.MkdirTemp("", "ethos-doctor-hook-*")
	if err != nil {
		return false, fmt.Errorf("creating sandbox: %w", err)
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithTimeout(context.Background(), sandboxTimeout)
	defer cancel()

	if out, err := exec.CommandContext(ctx, "git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		return false, fmt.Errorf("sandbox git init: %w: %s", err, out)
	}

	markerDir := filepath.Join(dir, ".punt-labs", "ethos")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		return false, fmt.Errorf("sandbox marker dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, "enabled"), nil, 0o644); err != nil {
		return false, fmt.Errorf("sandbox marker file: %w", err)
	}

	stubDir := filepath.Join(dir, "stubbin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		return false, fmt.Errorf("sandbox stub dir: %w", err)
	}
	logPath := filepath.Join(dir, "invocations.log")
	stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shQuote(logPath) + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(stubDir, "ethos"), []byte(stub), 0o755); err != nil {
		return false, fmt.Errorf("sandbox stub: %w", err)
	}

	hookPath := filepath.Join(dir, "hook-under-test")
	if err := os.WriteFile(hookPath, body, 0o755); err != nil {
		return false, fmt.Errorf("sandbox hook copy: %w", err)
	}

	var args []string
	if needsMsgArg {
		msgPath := filepath.Join(dir, "COMMIT_EDITMSG")
		if err := os.WriteFile(msgPath, []byte("ethos doctor sandbox probe\n"), 0o644); err != nil {
			return false, fmt.Errorf("sandbox message file: %w", err)
		}
		args = append(args, msgPath)
	}

	cmd := exec.CommandContext(ctx, hookPath, args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + dir,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=ethos-doctor", "GIT_AUTHOR_EMAIL=doctor@ethos.invalid",
		"GIT_COMMITTER_NAME=ethos-doctor", "GIT_COMMITTER_EMAIL=doctor@ethos.invalid",
	}
	// The hook's own exit status is not doctor's concern here — a hook that
	// fails for reasons unrelated to the ethos call (a chained foreign
	// section erroring, a missing unrelated tool) still tells us whether the
	// stub was reached before that failure. Only the stub log answers the
	// question this function exists to answer.
	_ = cmd.Run()

	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // the stub never ran — no invocation observed
		}
		return false, fmt.Errorf("reading sandbox log: %w", err)
	}
	want := strings.Join(argv, " ")
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == want {
			return true, nil
		}
	}
	return false, nil
}

// shQuote wraps s in single quotes for embedding in a generated /bin/sh
// script, escaping any single quote it contains. s is always doctor's own
// temp path, never user input, but this is cheap insurance against a
// TMPDIR whose name happens to contain one.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
