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

	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/punt-labs/ethos/v4/internal/redact"
	"golang.org/x/sys/unix"
)

const handoffSandboxArg = "--ethos-handoff-sandbox-v1"
const handoffSandboxReady = "ready\n"
const handoffMaterializePhase = "materialize snapshot inputs"
const handoffSandboxPrehydrating = "prehydrating\n"

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
	ScratchSeed     map[string]string
	System          handoffSystemPlan
	Loopback        bool
	GitDir          string
	RuntimeDir      string
	Prehydrate      []string
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
	if len(os.Args) < 7 || os.Args[1] != handoffSandboxArg || os.Getpid() != 1 {
		return
	}
	runtime.LockOSThread()
	status := os.NewFile(3, "handoff sandbox status")
	unix.CloseOnExec(3)
	manifestPipe := os.NewFile(4, "handoff sandbox manifest")
	manifest, err := io.ReadAll(manifestPipe)
	_ = manifestPipe.Close()
	if err != nil {
		_, _ = fmt.Fprintln(status, "read sandbox manifest")
		os.Exit(126)
	}
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
	err = enterHandoffSandbox(os.Args[2], os.Args[3], cwd, os.Args[5], manifest, os.Args[6:], status, reportCoverage)
	var setup *handoffSetupError
	if errors.As(err, &setup) {
		var errno unix.Errno
		if setup.phase == handoffMaterializePhase {
			_, _ = fmt.Fprintf(status, "%s: %.1024s\n", setup.phase, redact.Prose(setup.cause.Error()))
		} else if errors.As(setup.cause, &errno) {
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

func executeHandoffSnapshot(ctx context.Context, scratch, checkout, proof string, inputs []string, check editCheck, prepared ...handoffWorktreePlan) (string, error) {
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
	cwd := check.cwd
	if check.policy != nil && check.policy.WorktreeSnapshot != nil {
		profile := check.policy.WorktreeSnapshot
		roots := linuxHandoffRoots()
		env, err := worktreeSandboxEnvironment(profile)
		if err != nil {
			return "", err
		}
		var worktree handoffWorktreePlan
		if len(prepared) > 0 {
			worktree = prepared[0]
		} else {
			worktree.system, err = prepareHandoffSystem(ctx, checkout, profile)
			if err != nil {
				return "", err
			}
		}
		if profile.Git != nil && worktree.gitDir == "" {
			return "", errors.New("git view was not prepared")
		}
		if profile.Runtime != nil && worktree.runtimeDir == "" {
			return "", errors.New("runtime bundle was not prepared")
		}
		argv = roots.expandArgs(argv)
		for i := 1; i < len(argv); i++ {
			argv[i] = worktreeSandboxPath(checkout, argv[i])
		}
		cwd = worktreeSandboxPath(checkout, cwd)
		env = append(env, "ETHOS_HANDOFF_CHECKOUT_NAME="+filepath.Base(checkout))
		plan.Worktree = &handoffSandboxWorktree{
			Environment: env, CheckoutScratch: profile.Scratch.Checkout, ScratchSeed: profile.Scratch.Seed, System: worktree.system,
			Loopback: profile.Network == "loopback", GitDir: worktree.gitDir, RuntimeDir: worktree.runtimeDir,
		}
		if profile.Prehydrate != nil {
			plan.Worktree.Prehydrate = roots.expandArgs(profile.Prehydrate.Run)
			for i := 1; i < len(plan.Worktree.Prehydrate); i++ {
				plan.Worktree.Prehydrate[i] = worktreeSandboxPath(checkout, plan.Worktree.Prehydrate[i])
			}
		}
	}
	manifest, err := json.Marshal(plan)
	if err != nil {
		return "", errors.New("cannot encode the snapshot input manifest")
	}
	manifestReader, manifestWriter, err := os.Pipe()
	if err != nil {
		return "", errors.New("cannot create the snapshot runner manifest pipe")
	}
	defer manifestReader.Close()
	defer manifestWriter.Close()
	cmd := exec.CommandContext(ctx, self, append([]string{handoffSandboxArg, scratch, checkout, cwd, proof}, argv...)...)
	cmd.Env = slices.Clone(sandboxEnvironment)
	if dir := os.Getenv("GOCOVERDIR"); filepath.IsAbs(dir) {
		cmd.Env = append(cmd.Env, "GOCOVERDIR="+dir)
	}
	cmd.ExtraFiles = []*os.File{w, manifestReader}
	var prehydrateOutput editTail
	var prehydrateDrained chan struct{}
	var prehydrateWriter *os.File
	if plan.Worktree != nil && len(plan.Worktree.Prehydrate) != 0 {
		reader, writer, err := os.Pipe()
		if err != nil {
			return "", errors.New("cannot create the snapshot runner prehydrate output pipe")
		}
		defer reader.Close()
		defer writer.Close()
		cmd.ExtraFiles = append(cmd.ExtraFiles, writer)
		prehydrateWriter = writer
		prehydrateDrained = make(chan struct{})
		go func() {
			defer close(prehydrateDrained)
			_, _ = io.Copy(&prehydrateOutput, reader)
		}()
	}
	cmd.WaitDelay = time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: unix.CLONE_NEWUSER | unix.CLONE_NEWNS | unix.CLONE_NEWPID | unix.CLONE_NEWNET | unix.CLONE_NEWIPC,
		// The check keeps the invoking IDs: tools such as Homebrew refuse root,
		// and suites skip permission cases when euid is 0. Setup capabilities
		// survive the re-exec as ambient capabilities and are cleared before
		// the check starts.
		UidMappings: []syscall.SysProcIDMap{{ContainerID: os.Geteuid(), HostID: os.Geteuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getegid(), HostID: os.Getegid(), Size: 1}},
		AmbientCaps: []uintptr{unix.CAP_SYS_ADMIN, unix.CAP_SETPCAP},
		Pdeathsig:   unix.SIGKILL,
	}
	if plan.Worktree != nil && plan.Worktree.Loopback {
		cmd.SysProcAttr.AmbientCaps = append(cmd.SysProcAttr.AmbientCaps, unix.CAP_NET_ADMIN)
	}
	var stdout, stderr editTail
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return "", errors.New("snapshot isolation unavailable on this host")
	}
	_ = w.Close()
	_ = manifestReader.Close()
	_, manifestErr := manifestWriter.Write(manifest)
	_ = manifestWriter.Close()
	if prehydrateWriter != nil {
		_ = prehydrateWriter.Close()
	}
	setup, setupErr := io.ReadAll(io.LimitReader(r, 4096))
	status, prehydrating := strings.CutPrefix(string(setup), handoffSandboxPrehydrating)
	ready := setupErr == nil && status == handoffSandboxReady
	if ready {
		diagnostic.LogHookCheck(ctx, checkout, checkout, "handoff", check.name, "launched", time.Since(start))
	}
	err = cmd.Wait()
	if prehydrateDrained != nil {
		<-prehydrateDrained
	}
	if ctx.Err() != nil {
		if prehydrating && !ready {
			return "", errors.New("prehydrate-failed: timed out or interrupted")
		}
		return "", errors.New("snapshot check timed out or was interrupted")
	}
	if manifestErr != nil {
		return "", errors.New("cannot send the snapshot input manifest")
	}
	if !ready {
		if prehydrating && strings.HasPrefix(status, "prehydrate-failed") {
			const previewBytes = 2048
			preview := strings.TrimSpace(string(prehydrateOutput.data[max(0, len(prehydrateOutput.data)-previewBytes):]))
			if preview != "" {
				return "", fmt.Errorf("%s:\n%s", strings.TrimSpace(status), preview)
			}
			return "", errors.New(strings.TrimSpace(status))
		}
		detail := strings.TrimSpace(status)
		diagnostic.FromContext(ctx).Info("Handoff sandbox setup failed", "error_type", diagnostic.ErrorType(setupErr), "detail", detail)
		reason := "could not establish snapshot isolation or enter its working directory"
		if setupErr == nil && detail != "" {
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

func worktreeSandboxPath(checkout, value string) string {
	if filepath.IsAbs(value) {
		if rel, err := filepath.Rel(checkout, value); err == nil && filepath.IsLocal(rel) {
			return filepath.Join(gateRoot, rel)
		}
	}
	return value
}

func gatePlanInputsPresent(snapshot string, argv []string) bool {
	checkout, err := os.OpenRoot(snapshot)
	if err != nil {
		return false
	}
	defer checkout.Close()
	for _, arg := range argv {
		if arg != gateRoot && !strings.HasPrefix(arg, gateRoot+"/") {
			continue
		}
		rel := strings.TrimPrefix(arg, gateRoot+"/")
		if arg == gateRoot {
			rel = "."
		}
		if _, err := checkout.Lstat(rel); err != nil {
			return false
		}
	}
	return true
}

func resolveWorktreeExecutable(name string, env []string) (string, error) {
	if strings.ContainsRune(name, '/') {
		return name, nil
	}
	for _, dir := range handoffExecutableDirs(env) {
		candidate := filepath.Join(dir, name)
		if handoffExecutable(candidate) {
			return candidate, nil
		}
	}
	return "", unix.ENOENT
}

func handoffExecutableDirs(env []string) []string {
	for _, value := range env {
		if path, ok := strings.CutPrefix(value, "PATH="); ok {
			return filepath.SplitList(path)
		}
	}
	return nil
}

func handoffExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && unix.Access(path, unix.X_OK) == nil
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

func enterHandoffSandbox(root, live, cwd, proof string, manifest []byte, argv []string, status io.Writer, reportCoverage func() error) (err error) {
	phase := "validate sandbox plan"
	defer func() {
		if err != nil {
			err = &handoffSetupError{phase: phase, cause: err}
		}
	}()
	var plan handoffSandboxManifest
	if len(argv) == 0 || argv[0] == "" || !insideGatePath(gateRoot, cwd) ||
		json.Unmarshal(manifest, &plan) != nil || (plan.Worktree == nil && argv[0] != "/bin/sh") {
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
	phase = handoffMaterializePhase
	checkout := filepath.Join(root, gateRoot)
	if err := os.MkdirAll(checkout, 0o700); err != nil {
		return err
	}
	if err := unix.Mount("tmpfs", checkout, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0700,size=256m"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gateInputTime)
	digest, err := digestGateInputsWithMode(ctx, live, checkout, plan.Inputs, plan.Worktree != nil)
	cancel()
	if err != nil {
		return err
	}
	if digest != proof {
		return errors.New("snapshot inputs did not match the key")
	}
	phase = "check cwd missing from snapshot or is not a directory"
	checkoutRoot, err := os.OpenRoot(checkout)
	if err != nil {
		return err
	}
	relCwd := strings.TrimPrefix(cwd, gateRoot+"/")
	if cwd == gateRoot {
		relCwd = "."
	}
	info, err := checkoutRoot.Stat(relCwd)
	_ = checkoutRoot.Close()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return unix.ENOTDIR
	}
	env := sandboxEnvironment
	if plan.Worktree != nil {
		if plan.Worktree.RuntimeDir != "" {
			phase = "mount verified runtime bundle"
			if err := mountGateReadOnly(plan.Worktree.RuntimeDir, filepath.Join(root, linuxHandoffRoots().runtime)); err != nil {
				return err
			}
		}
		phase = "mount private worktree scratch"
		if err := mountHandoffWorktreeScratch(root, checkout, plan.Worktree.CheckoutScratch); err != nil {
			if errors.Is(err, errHandoffScratchShadowsInput) {
				phase = errHandoffScratchShadowsInput.Error()
			}
			return err
		}
		phase = "copy private scratch.seed"
		if err := seedHandoffScratch(root, plan.Worktree.ScratchSeed); err != nil {
			return err
		}
		if plan.Worktree.GitDir != "" {
			phase = "mount private Git view"
			gitDir := filepath.Join(checkout, ".git")
			if err := mountGateReadOnly(plan.Worktree.GitDir, gitDir); err != nil {
				return err
			}
			if err := unix.Mount("tmpfs", filepath.Join(gitDir, "worktrees"), "tmpfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "mode=0700,size=16m"); err != nil {
				return err
			}
		}
		env = plan.Worktree.Environment
	}
	phase = handoffMaterializePhase
	if !gatePlanInputsPresent(checkout, argv) {
		return errors.New("snapshot inputs did not match the invocation")
	}
	if err := unix.Mount("", checkout, "", unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, ""); err != nil {
		return err
	}
	phase = "mount read-only system runtime"
	if plan.Worktree != nil {
		if err := mountHandoffSystem(root, plan.Worktree.System); err != nil {
			return err
		}
		phase = "mount sandbox procfs and devices"
		if err := mountHandoffProcAndDev(root); err != nil {
			return err
		}
		if plan.Worktree.Loopback {
			phase = "write synthetic loopback resolver files"
			if err := writeHandoffLoopbackHosts(root); err != nil {
				return err
			}
			phase = "enable IPv4 and IPv6 loopback"
			if err := enableHandoffLoopback(); err != nil {
				return err
			}
		}
	} else {
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
	if plan.Worktree != nil {
		phase = "verify mounted system inputs"
		if err := verifyMountedHandoffSystem(plan.Worktree.System, env); err != nil {
			return err
		}
		if len(plan.Worktree.Prehydrate) != 0 {
			phase = "prehydrate-failed"
			if _, err := io.WriteString(status, handoffSandboxPrehydrating); err != nil {
				return err
			}
			if err := runHandoffPrehydrate(plan.Worktree.Prehydrate, env); err != nil {
				return err
			}
		}
	}
	phase = "start sandbox command"
	executable := argv[0]
	if plan.Worktree != nil {
		executable, err = resolveWorktreeExecutable(argv[0], env)
		if err != nil {
			return err
		}
	}
	if _, err := io.WriteString(status, handoffSandboxReady); err != nil {
		return err
	}
	return unix.Exec(executable, argv, env)
}

func runHandoffPrehydrate(argv, env []string) error {
	if os.Getpid() != 1 {
		return errors.New("prehydrate requires a private PID namespace")
	}
	path, err := resolveWorktreeExecutable(argv[0], env)
	if err != nil {
		return err
	}
	const prehydrateOutputFD = 5
	output := os.NewFile(prehydrateOutputFD, "handoff prehydrate output")
	unix.CloseOnExec(prehydrateOutputFD)
	defer output.Close()
	cmd := &exec.Cmd{Path: path, Args: argv, Env: env, Stdout: output, Stderr: output}
	return cmd.Run()
}

func mountGateReadOnly(source, target string) error {
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	return bindHandoffReadOnly(source, target)
}

func writeHandoffLoopbackHosts(root string) error {
	if err := os.WriteFile(filepath.Join(root, "etc/hosts"), []byte("127.0.0.1 localhost\n::1 localhost\n"), 0o444); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "etc/host.conf"), []byte("multi on\n"), 0o444)
}

func enableHandoffLoopback() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	req, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, req); err != nil {
		return err
	}
	req.SetUint16(req.Uint16() | unix.IFF_UP)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, req); err != nil {
		return err
	}
	if err := unix.Bind(fd, &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		return err
	}
	fd6, err := unix.Socket(unix.AF_INET6, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd6)
	return unix.Bind(fd6, &unix.SockaddrInet6{Addr: [16]byte{15: 1}})
}

func bindHandoffReadOnly(source, target string) error {
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		return err
	}
	return unix.Mount("", target, "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, "")
}

var handoffDevices = []string{"/dev/null", "/dev/zero", "/dev/random", "/dev/urandom"}

// mountHandoffProcAndDev gives the check a procfs for its own PID namespace
// and private device mounts.
func mountHandoffProcAndDev(root string) error {
	proc := filepath.Join(root, "proc")
	if err := os.MkdirAll(proc, 0o555); err != nil {
		return err
	}
	if err := unix.Mount("proc", proc, "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return err
	}
	for _, dev := range handoffDevices {
		target := filepath.Join(root, dev)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if err := unix.Mount(dev, target, "", unix.MS_BIND, ""); err != nil {
			return err
		}
		if err := unix.Mount("", target, "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_NOSUID|unix.MS_NOEXEC, ""); err != nil {
			return err
		}
	}
	if err := os.Symlink("/proc/self/fd", filepath.Join(root, "dev/fd")); err != nil {
		return err
	}
	shm := filepath.Join(root, "dev/shm")
	if err := os.Mkdir(shm, 0o755); err != nil {
		return err
	}
	if err := unix.Mount("tmpfs", shm, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "size=16m,mode=1777"); err != nil {
		return err
	}
	pts := filepath.Join(root, "dev/pts")
	if err := os.Mkdir(pts, 0o755); err != nil {
		return err
	}
	if err := unix.Mount("devpts", pts, "devpts", unix.MS_NOSUID|unix.MS_NOEXEC, "newinstance,ptmxmode=0666,mode=0620"); err != nil {
		return err
	}
	ptmx := filepath.Join(root, "dev/ptmx")
	f, err := os.OpenFile(ptmx, os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return unix.Mount(filepath.Join(pts, "ptmx"), ptmx, "", unix.MS_BIND, "")
}
