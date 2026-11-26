package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/coverage"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"golang.org/x/sys/unix"
)

const handoffSandboxArg = "--ethos-handoff-sandbox-v1"
const handoffSandboxReady = "ready\n"

var sandboxRuntime = []string{"/bin", "/lib", "/lib64", "/usr/bin", "/usr/lib", "/usr/lib64"}

const sandboxPATH = "/usr/bin:/bin"
const worktreeSandboxPATH = "/usr/local/bin:/usr/bin:/bin"

var sandboxEnvironment = []string{"PATH=" + sandboxPATH, "LC_ALL=C", "TZ=UTC", "HOME=/nonexistent"}

func linuxHandoffRoots() handoffRoots {
	return handoffRoots{checkout: gateRoot, runtime: "/runtime", scratch: "/scratch", temp: "/tmp"}
}

type handoffSandboxManifest struct {
	Inputs   []string
	Worktree *handoffSandboxWorktree
}

type handoffSandboxWorktree struct {
	Environment     []string
	CheckoutScratch []string
}

// RunHandoffSandbox dispatches the private namespace child before CLI setup.
func RunHandoffSandbox() {
	runHandoffSandbox(func(dir string) error {
		if err := coverage.WriteMetaDir(dir); err != nil {
			return err
		}
		return coverage.WriteCountersDir(dir)
	})
}

func runHandoffSandbox(writeCoverage func(string) error) {
	if len(os.Args) < 8 || os.Args[1] != handoffSandboxArg || os.Getpid() != 1 {
		return
	}
	runtime.LockOSThread()
	status := os.NewFile(3, "handoff sandbox status")
	unix.CloseOnExec(3)
	cwd := os.Args[4]
	reportCoverage := func() error { return nil }
	if dir := os.Getenv("GOCOVERDIR"); filepath.IsAbs(dir) {
		coverDir, err := os.Open(dir)
		if err != nil {
			_, _ = fmt.Fprintln(status, "open sandbox coverage directory")
			os.Exit(126)
		}
		reportCoverage = func() error { return writeSandboxCoverage(coverDir, cwd, writeCoverage) }
	}
	err := enterHandoffSandbox(os.Args[2], os.Args[3], cwd, os.Args[5], os.Args[6], os.Args[7:], status, reportCoverage)
	var setup *handoffSetupError
	if errors.As(err, &setup) {
		var errno unix.Errno
		if errors.As(setup.cause, &errno) {
			_, _ = fmt.Fprintf(status, "%s: %s\n", setup.phase, errno)
		} else {
			_, _ = fmt.Fprintln(status, setup.phase)
		}
	}
	os.Exit(126)
}

func writeSandboxCoverage(dir *os.File, cwd string, write func(string) error) error {
	if err := unix.Fchdir(int(dir.Fd())); err != nil {
		return errors.Join(err, dir.Close())
	}
	return errors.Join(dir.Close(), write("."), unix.Chdir(cwd))
}

func handoffSandboxSupported() bool { return true }

func handoffSandboxIdentity(root string) (string, error) {
	var physical unix.Stat_t
	if err := unix.Stat(root, &physical); err != nil {
		return "", err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%d:%d", strings.TrimSpace(string(boot)), physical.Dev, physical.Ino), nil
}

func executeHandoffSnapshot(ctx context.Context, scratch, checkout, proof string, inputs []string, check editCheck) (string, error) {
	for _, root := range sandboxRuntime {
		physical, err := filepath.EvalSymlinks(root)
		if err == nil && (insideGatePath(physical, checkout) || insideGatePath(checkout, physical)) {
			return "", errors.New("checkout overlaps the sandbox system runtime")
		}
	}
	self, err := os.Executable()
	if err != nil {
		return "", errors.New("cannot locate the snapshot runner")
	}
	r, w, err := os.Pipe()
	if err != nil {
		return "", errors.New("cannot create the snapshot runner status pipe")
	}
	defer r.Close()
	defer w.Close()

	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, check.timeout)
	defer cancel()
	plan := handoffSandboxManifest{Inputs: inputs}
	argv := check.argv
	if check.policy != nil && check.policy.WorktreeSnapshot != nil {
		profile := check.policy.WorktreeSnapshot
		roots := linuxHandoffRoots()
		var platform checkset.SnapshotPlatform
		if profile.Runtime != nil {
			platform = profile.Runtime.Platforms[runtime.GOOS+"-"+runtime.GOARCH]
		}
		env, err := roots.environment(profile, platform, worktreeSandboxPATH)
		if err != nil {
			return "", err
		}
		argv = roots.expandArgs(argv)
		env = append(env, "ETHOS_HANDOFF_CHECKOUT_NAME="+filepath.Base(checkout))
		plan.Worktree = &handoffSandboxWorktree{Environment: env, CheckoutScratch: profile.Scratch.Checkout}
	}
	manifest, err := json.Marshal(plan)
	if err != nil {
		return "", errors.New("cannot encode the snapshot input manifest")
	}
	cmd := exec.CommandContext(ctx, self, append([]string{handoffSandboxArg, scratch, checkout, check.cwd, proof, string(manifest)}, argv...)...)
	cmd.Env = slices.Clone(sandboxEnvironment)
	if dir := os.Getenv("GOCOVERDIR"); filepath.IsAbs(dir) {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+dir)
	}
	cmd.ExtraFiles = []*os.File{w}
	cmd.WaitDelay = time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  unix.CLONE_NEWUSER | unix.CLONE_NEWNS | unix.CLONE_NEWPID | unix.CLONE_NEWNET | unix.CLONE_NEWIPC,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Geteuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getegid(), Size: 1}},
		Pdeathsig:   unix.SIGKILL,
	}
	var stdout, stderr editTail
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return "", errors.New("snapshot isolation unavailable on this host")
	}
	_ = w.Close()
	setup, setupErr := io.ReadAll(io.LimitReader(r, 4096))
	ready := setupErr == nil && string(setup) == handoffSandboxReady
	if ready {
		diagnostic.LogHookCheck(ctx, checkout, checkout, "handoff", check.name, "launched", time.Since(start))
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return "", errors.New("snapshot check timed out or was interrupted")
	}
	if !ready {
		diagnostic.FromContext(ctx).Info("Handoff sandbox setup failed", "error_type", diagnostic.ErrorType(setupErr))
		reason := "could not establish snapshot isolation or enter its working directory"
		if detail := strings.TrimSpace(string(setup)); setupErr == nil && detail != "" {
			reason += " (" + detail + ")"
		}
		return "", errors.New(reason)
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() < 0 {
			return "", errors.New("snapshot check did not complete")
		}
		switch code := exit.ExitCode(); code {
		case 128 + int(unix.SIGHUP), 128 + int(unix.SIGINT), 128 + int(unix.SIGKILL), 128 + int(unix.SIGTERM):
			return "", fmt.Errorf("check exited %d (likely killed or interrupted); not cached", code)
		}
	}
	if err == nil && (!check.failOnOutput || len(stdout.data) == 0) {
		diagnostic.LogHookCheck(ctx, checkout, checkout, "handoff", check.name, "completed", time.Since(start))
		return "", nil
	}
	detail := "non-empty formatting output"
	if err != nil {
		detail = err.Error()
	}
	diagnostic.LogHookCheck(ctx, checkout, checkout, "handoff", check.name, "failed", time.Since(start))
	lines := strings.Split(strings.TrimRight(string(bytes.Join([][]byte{stdout.data, stderr.data}, []byte("\n"))), "\n"), "\n")
	return fmt.Sprintf("%s: %s\ncommand: %s\n%s", check.name, detail, strings.Join(check.argv, " "), strings.Join(lines[max(0, len(lines)-40):], "\n")), nil
}

func insideGatePath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && filepath.IsLocal(rel)
}

type handoffSetupError struct {
	phase string
	cause error
}

func (e *handoffSetupError) Error() string { return e.phase + ": " + e.cause.Error() }
func (e *handoffSetupError) Unwrap() error { return e.cause }

func enterHandoffSandbox(root, live, cwd, proof, manifest string, argv []string, status io.Writer, reportCoverage func() error) (err error) {
	phase := "validate sandbox plan"
	defer func() {
		if err != nil {
			err = &handoffSetupError{phase: phase, cause: err}
		}
	}()
	if len(argv) == 0 || argv[0] != "/bin/sh" || !insideGatePath(gateRoot, cwd) {
		return errors.New("invalid sandbox plan")
	}
	phase = "make mount propagation private"
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return err
	}
	phase = "bind sandbox root"
	if err := unix.Mount(root, root, "", unix.MS_BIND, ""); err != nil {
		return err
	}
	phase = "materialize snapshot inputs"
	checkout := filepath.Join(root, gateRoot)
	if err := os.MkdirAll(checkout, 0o700); err != nil {
		return err
	}
	if err := unix.Mount("tmpfs", checkout, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0700,size=256m"); err != nil {
		return err
	}
	var plan handoffSandboxManifest
	if err := json.Unmarshal([]byte(manifest), &plan); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gateInputTime)
	digest, err := digestGateInputs(ctx, live, checkout, plan.Inputs)
	cancel()
	if err != nil || digest != proof {
		return errors.New("snapshot inputs did not match the key or invocation")
	}
	phase = "check cwd missing from snapshot or is not a directory"
	info, err := os.Stat(filepath.Join(checkout, strings.TrimPrefix(cwd, gateRoot)))
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return unix.ENOTDIR
	}
	env := sandboxEnvironment
	if plan.Worktree != nil {
		phase = "mount private worktree scratch"
		if err := mountHandoffWorktreeScratch(root, checkout, plan.Worktree.CheckoutScratch); err != nil {
			if errors.Is(err, errHandoffScratchShadowsInput) {
				phase = errHandoffScratchShadowsInput.Error()
			}
			return err
		}
		env = plan.Worktree.Environment
	}
	phase = "materialize snapshot inputs"
	if !gatePlanInputsPresent(checkout, argv) {
		return errors.New("snapshot inputs did not match the key or invocation")
	}
	if err := unix.Mount("", checkout, "", unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, ""); err != nil {
		return err
	}
	phase = "mount read-only system runtime"
	for _, runtimeDir := range sandboxRuntime {
		if _, err := os.Stat(runtimeDir); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := mountGateReadOnly(runtimeDir, filepath.Join(root, runtimeDir)); err != nil {
			return err
		}
	}
	oldRoot := filepath.Join(root, ".old-root")
	phase = "pivot to sandbox root"
	if err := os.Mkdir(oldRoot, 0o700); err != nil {
		return err
	}
	if err := unix.PivotRoot(root, oldRoot); err != nil {
		return err
	}
	phase = "enter check cwd in snapshot"
	if err := unix.Chdir(cwd); err != nil {
		return err
	}
	phase = "detach live filesystem"
	if err := unix.Unmount("/.old-root", unix.MNT_DETACH); err != nil {
		return err
	}
	if err := os.Remove("/.old-root"); err != nil {
		return err
	}
	if err := unix.Mount("", "/", "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, ""); err != nil {
		return err
	}
	phase = "drop sandbox privileges"
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	// Root must not regain capabilities on exec inside the user namespace.
	const noRootAndLocked = 3
	if err := unix.Prctl(unix.PR_SET_SECUREBITS, noRootAndLocked, 0, 0, 0); err != nil {
		return err
	}
	caps := [2]unix.CapUserData{}
	if err := unix.Capset(&unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}, &caps[0]); err != nil {
		return err
	}
	phase = "collect sandbox coverage"
	if err := reportCoverage(); err != nil {
		return err
	}
	phase = "start sandbox command"
	if _, err := io.WriteString(status, handoffSandboxReady); err != nil {
		return err
	}
	return unix.Exec(argv[0], argv, env)
}

func mountGateReadOnly(source, target string) error {
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		return err
	}
	return unix.Mount("", target, "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, "")
}
