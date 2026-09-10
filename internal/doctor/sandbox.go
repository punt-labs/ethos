package doctor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/punt-labs/ethos/v4/internal/textscan"
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

// errSandboxUnsupportedPlatform is returned by hookInvocationObserved on a
// GOOS this sandbox cannot exercise: it shells out to git, and (per the
// ENOEXEC fallback below) to sh, neither of which this package can assume on
// Windows the way it can on a Unix host. checkHookPresence turns this into
// an honest "cannot verify by execution on this platform" rather than
// letting every enabled Windows install FAIL against a hook that may be
// perfectly healthy — see DES-077's residual section, M4.
var errSandboxUnsupportedPlatform = errors.New("hook execution verification is not supported on this platform")

// sandboxGOOS is runtime.GOOS, held in a var (not read inline) so a test can
// override it and exercise the package's Windows guards without needing an
// actual Windows build — the same pattern sandboxTimeout already uses for
// the same reason. Two guards read it: the sandbox's own unsupported-platform
// return below, and checkHookPresence's executable-bit check, which is
// meaningless on a platform whose FileMode carries no execute bits.
var sandboxGOOS = runtime.GOOS

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
// aliased wrapper resolve correctly because the code actually runs. The
// documented limitations this method trades in return are described below.
//
// The sandbox: a fresh temp directory, `git init`'d so the hook's own
// `git rev-parse --show-toplevel` gate succeeds; a `.punt-labs/ethos/enabled`
// marker so the hook's own enablement gate passes regardless of whether the
// REAL repo being checked is enabled (doctor wants to know "if this body
// runs, does it call ethos", independent of that repo's current enablement
// state — checkHookPresence composes that answer with the real marker
// separately); and a stub `ethos` executable placed first on PATH that
// records its argv to a log file and exits 0 rather than doing any real
// work. body is copied in verbatim and executed via its own file, respecting
// whatever shebang it carries — matching how git runs a hook directly. git
// spawns hooks via libc's execvp, not the bare execve syscall Go's
// os/exec uses; execvp (and execlp) carry a POSIX-mandated fallback:
// when the target has no recognized shebang, execve fails ENOEXEC, and
// execvp retries the file as an argument to `sh`. This is not git's own
// code — it is a property of the C library git links against, which
// happens to give a shebang-less hook a real, working execution path. This
// sandbox now matches that observable fallback exactly (H2/ethos-kcbv
// follow-up: `sh -c '"$0" "$@"' <path> <args>` on ENOEXEC) rather than
// reporting a shebang-less hook as unexecutable, which os/exec's bare
// execve otherwise makes it look like. This is not a uniform `sh -c` wrap —
// checkHookPresence still only attempts execution at all for a body
// textscan.IsShellHook already classifies as shell (including "no shebang",
// which git also treats as shell), so a hook with a real non-shell shebang
// (`#!/usr/bin/env python3`) is never routed through either path.
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
	if sandboxGOOS == "windows" {
		return false, errSandboxUnsupportedPlatform
	}
	if _, err := exec.LookPath("git"); err != nil {
		return false, errSandboxGitUnavailable
	}

	dir, err := os.MkdirTemp("", "ethos-doctor-hook-*")
	if err != nil {
		return false, fmt.Errorf("creating sandbox: %w", err)
	}
	defer removeSandbox(dir)

	ctx, cancel := context.WithTimeout(context.Background(), sandboxTimeout)
	defer cancel()

	// sandboxEnv is the ONLY environment every sandbox git invocation and
	// the hook execution itself see — assigning cmd.Env (never appending to
	// os.Environ()) means everything is absent by default and only what is
	// listed here comes back. This is load-bearing (S1): the CALLER's
	// process environment can carry git state — GIT_DIR, GIT_WORK_TREE,
	// GIT_OBJECT_DIRECTORY, GIT_CONFIG_COUNT/KEY_N/VALUE_N, and others not
	// named here — that would otherwise redirect `git init`, or the hook's
	// own `git rev-parse --show-toplevel`, at the REAL repository instead
	// of this disposable one. Proven: with an inherited GIT_DIR pointing at
	// a real repo, `git init` here returned exit 0 while creating no
	// repository at dir, and the hook's own rev-parse silently resolved to
	// the real repo's toplevel — the untrusted hook body then ran, and
	// wrote, INSIDE the real checkout, with hookInvocationObserved still
	// reporting a clean result. The self-test below closes this without
	// needing the enumeration to be exhaustive; GIT_CEILING_DIRECTORIES is
	// a second, independent backstop bounding git's own upward directory
	// search to dir's immediate parent, so even a sandbox whose `.git`
	// failed to materialize for a reason neither of those anticipates
	// cannot resolve to an ancestor repository — which the real repo being
	// checked usually is, since this org's TMPDIR convention places .tmp/
	// inside the repo itself.
	sandboxEnv := []string{
		"PATH=" + filepath.Join(dir, "stubbin") + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + dir,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CEILING_DIRECTORIES=" + filepath.Dir(dir),
		"GIT_AUTHOR_NAME=ethos-doctor", "GIT_AUTHOR_EMAIL=doctor@ethos.invalid",
		"GIT_COMMITTER_NAME=ethos-doctor", "GIT_COMMITTER_EMAIL=doctor@ethos.invalid",
	}

	// --template= defeats a caller's init.templateDir/GIT_TEMPLATE_DIR,
	// which could otherwise seed the "sandbox" with attacker- or
	// operator-controlled hooks of its own before ours ever runs.
	initCmd := exec.CommandContext(ctx, "git", "-C", dir, "init", "-q", "--template=")
	initCmd.Env = sandboxEnv
	if out, err := initCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("sandbox git init: %w: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return false, fmt.Errorf("sandbox git init did not create a repository: %w", err)
	}

	// Containment self-test (S1): `git init` exiting 0 and a `.git` entry
	// existing are both necessary but not sufficient — ask git directly
	// whether IT thinks dir is the repository root, rather than trusting
	// init's exit code. A caller environment that redirected git's state
	// out from under us produces exactly this shape: init reports success,
	// but git's own answer to "where is the repo root" is somewhere else
	// entirely. Refuse to execute anything if the sandbox cannot vouch for
	// itself — this is the property that makes the exact enumeration above
	// unnecessary to keep complete.
	top, topErr := gitInSandbox(ctx, dir, sandboxEnv, "rev-parse", "--show-toplevel")
	if topErr != nil || !textscan.SamePath(top, dir) {
		return false, fmt.Errorf("sandbox containment self-test failed (resolved toplevel %q, want %q): %v", top, dir, topErr)
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

	// invocationNonce is a fresh, unpredictable-ahead-of-time value the stub
	// writes as the first line of every record (qodo, PR #515): a hook
	// body written or reviewed before this run has no way to know it, so a
	// hook that merely writes plausible-looking text straight to
	// "invocations.log" — the qodo PoC was `printf 'audit seal\n' >
	// invocations.log` — no longer produces a record hookInvocationObserved
	// will accept. This does not defend against a hook that actively reads
	// its own sandbox at runtime (the stub script, containing this same
	// nonce, is right there on disk) — that residual is the same
	// no-full-OS-sandbox limitation DES-077 already documents for arbitrary
	// code execution generally, not a gap specific to this check.
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return false, fmt.Errorf("sandbox nonce: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes)

	// Each argument is written on its own line via a `for` loop over "$@",
	// not `"$*"` (M2/qodo, PR #515): $* joins argv with IFS's first
	// character, so a hook calling `ethos 'audit seal'` (one argument) and
	// one calling `ethos audit seal` (two arguments) previously serialized
	// to the identical string "audit seal" and were indistinguishable. A
	// line-per-argument record, terminated by invocationRecordSep, lets the
	// matcher below compare argv element-by-element instead of a lossy
	// joined string.
	stub := "#!/bin/sh\n{\nprintf '%s\\n' " + shQuote(nonce) + "\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\nprintf '" + invocationRecordSep + "'\n} >> " + shQuote(logPath) + "\nexit 0\n"
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
	cmd.Env = sandboxEnv
	// cmd.Stdout and cmd.Stderr are deliberately left nil, wiring them to
	// /dev/null rather than an os.Pipe. A hook that backgrounds a
	// long-running child inherits its parent's file descriptors by
	// default; with a pipe, that child holds the write end open long after
	// the parent exits, and Cmd.Wait blocks until EVERY holder of the pipe
	// closes it — the classic os/exec deadlock trap. Capturing hook output
	// here (for a better error message, say) needs cmd.WaitDelay set
	// alongside it, or this exact hang reappears.
	// The default cmd.Cancel (called on a context timeout) only signals
	// cmd's direct PID; giving cmd its own process group lets Cancel kill
	// the whole group instead (S4, partial — see the unconditional reap
	// below for the rest of the fix).
	setNewProcessGroup(cmd)
	cmd.Cancel = func() error { return reapProcessGroup(cmd) }
	cmd.WaitDelay = sandboxTimeout // bound Wait() even if Cancel's kill somehow doesn't land
	// The hook's own exit status is not doctor's concern when the stub log
	// below shows the call happened — a hook that fails for reasons
	// unrelated to the ethos call (a chained foreign section erroring, a
	// missing unrelated tool) still tells us whether the stub was reached
	// before that failure. runErr becomes the ONLY signal, though, when the
	// log is absent: classifyMissedInvocation below examines it to tell
	// "never got the chance to call ethos" apart from "ran fine and
	// genuinely never calls ethos" (H1).
	runErr := cmd.Run()
	// A hook may background a child (`cmd &`, nohup) that outlives the
	// hook's OWN quick, normal exit — cmd.Cancel above only fires on a
	// context timeout, which never happens in that case, since Run()
	// already returned once the direct child (the shell) exited. Reap the
	// whole process group unconditionally here too, regardless of how Run
	// returned, so no orphaned grandchild survives into the sandbox's
	// temp-dir cleanup below — a directory that is about to not exist (S4).
	_ = reapProcessGroup(cmd)

	// libc's execvp (which git uses to spawn hooks) falls back to the shell
	// when a direct execve fails with ENOEXEC — the kernel's answer for a
	// script with no (or an unrecognized) shebang line. Match that
	// observable behavior exactly (H2): a shebang-less hook must be
	// exercised the same way git actually runs it, not reported as
	// unexecutable because this sandbox tried a bare execve (what Go's
	// os/exec does) and stopped there.
	if errors.Is(runErr, syscall.ENOEXEC) && ctx.Err() == nil {
		shCmd := exec.CommandContext(ctx, "sh", append([]string{"-c", `"$0" "$@"`, hookPath}, args...)...)
		shCmd.Dir = dir
		shCmd.Env = sandboxEnv
		setNewProcessGroup(shCmd)
		shCmd.Cancel = func() error { return reapProcessGroup(shCmd) }
		shCmd.WaitDelay = sandboxTimeout
		runErr = shCmd.Run()
		_ = reapProcessGroup(shCmd)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, classifyMissedInvocation(ctx, runErr)
		}
		return false, fmt.Errorf("reading sandbox log: %w", err)
	}
	return matchesInvocation(string(data), nonce, argv), nil
}

// invocationRecordSep terminates one logged invocation record in the stub's
// log file. \x1e (ASCII record separator) is vanishingly unlikely to appear
// in a real hook's arguments and cannot collide with a newline the way a
// bare line-per-record format would.
const invocationRecordSep = "\x1e"

// matchesInvocation reports whether data — the stub's raw log file — holds a
// record for nonce whose leading lines equal argv exactly, element by
// element. Each record is nonce followed by one line per logged argument
// (see hookInvocationObserved's stub script); a record for the wrong nonce,
// or one whose argv does not start with argv exactly, is not a match.
//
// A prefix match, not full-record equality: a real hook calling
// `ethos audit seal --quiet` logs argument lines "audit" "seal" "--quiet",
// which must still count as an observed "audit seal" invocation — the
// trailing "--quiet" line is simply not compared.
func matchesInvocation(data, nonce string, argv []string) bool {
	for _, record := range strings.Split(data, invocationRecordSep) {
		lines := strings.Split(record, "\n")
		if len(lines) == 0 || lines[0] != nonce {
			continue
		}
		got := lines[1:]
		if len(got) > 0 && got[len(got)-1] == "" {
			got = got[:len(got)-1] // trailing "" from the final arg line's "\n"
		}
		if len(got) < len(argv) {
			continue
		}
		match := true
		for i, w := range argv {
			if got[i] != w {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// classifyMissedInvocation explains why the ethos stub was never reached, so
// checkHookPresence can give an accurate FAIL message instead of a blanket
// "stale — run `ethos enable`", which is the wrong remedy for a missing
// interpreter or a hung host section (H1). Three conditions previously
// collapsed into an indistinguishable (false, nil): a hook that could never
// be executed at all (ENOEXEC/ENOENT — no usable shebang, or one naming a
// missing interpreter), one killed at the sandbox timeout (a chained section
// may be hanging), and one that exited before reaching the ethos call at all
// (a host section's own guard, not the ethos section, is what failed).
//
// runErr == nil — the hook ran to completion and simply never called
// ethos — is deliberately left as (nil, nil): that is the one case doctor's
// existing "stale"/"not chained" messaging already describes correctly, and
// converting it to an error here would just re-derive the same FAIL text
// through a different path.
func classifyMissedInvocation(ctx context.Context, runErr error) error {
	if runErr == nil {
		return nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("the hook did not finish within %s — a chained section may be hanging", sandboxTimeout)
	}
	if errors.Is(runErr, syscall.ENOEXEC) || errors.Is(runErr, syscall.ENOENT) {
		return fmt.Errorf("the hook could not be executed at all: %v", runErr)
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return &hookExitedEarlyError{code: exitErr.ExitCode()}
	}
	return fmt.Errorf("running the sandboxed hook: %w", runErr)
}

// hookExitedEarlyError distinguishes classifyMissedInvocation's "exited
// before reaching ethos" case from its siblings (P4): checkHookPresence
// needs to tell this one apart from a missing interpreter or a timeout, so
// it can downgrade to WARN when the installed marker section is provably
// byte-current — a host section's own guard failed, not the ethos section,
// and re-chaining identical content via `ethos enable` would reproduce the
// exact same failure. A bare error string would need callers to parse
// prose to make that distinction, which is fragile; a typed error lets
// errors.As do it precisely.
type hookExitedEarlyError struct {
	code int
}

func (e *hookExitedEarlyError) Error() string {
	return fmt.Sprintf("the hook exited %d before reaching the ethos call", e.code)
}

// gitInSandbox runs `git -C dir <args>` with env (never the caller's
// environment) and returns trimmed stdout. Used for the containment
// self-test (S1) — a targeted, read-only git query, not a general-purpose
// runner, which is why it always attaches Output()'s stderr on failure
// rather than exposing a broader interface.
func gitInSandbox(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("%w: %s", err, exitErr.Stderr)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// shQuote wraps s in single quotes for embedding in a generated /bin/sh
// script, escaping any single quote it contains. s is always doctor's own
// temp path, never user input, but this is cheap insurance against a
// TMPDIR whose name happens to contain one.
// removeSandbox deletes dir, retrying once with every entry's permissions
// forced open first. A bare os.RemoveAll silently gives up partway through
// a tree containing an unreadable/unwritable entry — a hook that leaves
// behind a 0o000 subdirectory (deliberately or not) leaks the sandbox's
// temp dir with no signal that it happened. Best-effort: if the retry also
// fails, the leak is a disk-usage nit, not a correctness problem — S1's
// containment guarantees already bound what could happen inside dir in the
// first place.
func removeSandbox(dir string) {
	if err := os.RemoveAll(dir); err == nil {
		return
	}
	// filepath.Walk passes a directory's own readdir failure to walkFn AS
	// walkErr for that same path (it attempts to list the directory before
	// invoking walkFn, not after) — so an unwritable directory is exactly
	// the case where walkErr is non-nil here, not nil. Chmod unconditionally
	// and keep walking best-effort; a path Walk cannot even Lstat has
	// nothing to chmod, but every other permission failure is fixable.
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, walkErr error) error {
		_ = os.Chmod(p, 0o700)
		return nil
	})
	_ = os.RemoveAll(dir)
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
