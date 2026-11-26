//go:build darwin

package hook

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"golang.org/x/sys/unix"
)

type darwinHandoffDeferred struct{ cause error }

func (e *darwinHandoffDeferred) Error() string { return "Darwin handoff sandbox: " + e.cause.Error() }
func (e *darwinHandoffDeferred) Unwrap() error { return e.cause }

var darwinHandoffSupported = sync.OnceValue(probeDarwinHandoff)

func handoffSandboxSupported() bool { return darwinHandoffSupported() }

func probeDarwinHandoff() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", layout.SiblingTempPattern("ethos-sandbox-probe"))
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	checkout, scratch := filepath.Join(dir, "checkout"), filepath.Join(dir, "scratch")
	if os.Mkdir(checkout, 0o700) != nil || os.Mkdir(scratch, 0o700) != nil {
		return false
	}
	if _, err := handoffSandboxIdentity(checkout); err != nil {
		return false
	}
	if !probeDarwinHandoffPins(ctx, filepath.Join(scratch, "pin")) {
		return false
	}
	input := filepath.Join(checkout, "input")
	alias := filepath.Join(dir, "alias")
	if os.WriteFile(input, []byte("snapshot"), 0o600) != nil || os.Link(input, alias) != nil {
		return false
	}
	proof, err := digestGateInputs(ctx, checkout, "", []string{"input"})
	if err != nil {
		return false
	}
	check := editCheck{cwd: checkout, argv: []string{"/bin/sh", "-c",
		`test "$(/bin/cat input)" = snapshot && ! /bin/cat "$1" && ! /bin/cat "$2/checkout/input" && ! (echo changed > input)`,
		"sh", alias, dir}}
	result, err := runDarwinHandoffSnapshot(ctx, scratch, checkout, proof, []string{"input"}, check)
	return err == nil && result == ""
}

func handoffSandboxIdentity(root string) (string, error) {
	var stat unix.Stat_t
	if err := unix.Stat(root, &stat); err != nil {
		return "", err
	}
	boot, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return "", err
	}
	if boot == "" {
		return "", errors.New("empty boot session UUID")
	}
	return fmt.Sprintf("%d:%d:%s", stat.Dev, stat.Ino, boot), nil
}

func probeDarwinHandoffPins(ctx context.Context, path string) bool {
	pin, _, err := pinHandoffStripe(ctx, path)
	if err != nil {
		return false
	}
	defer pin.Close()
	other, _, err := pinHandoffStripe(ctx, path)
	if err != nil {
		return false
	}
	pruned := false
	prune := func() error { pruned = true; return nil }
	busy, err := withUnpinnedStripe(pin, prune)
	if closeErr := other.Close(); err != nil || closeErr != nil || !busy || pruned {
		return false
	}
	busy, err = withUnpinnedStripe(pin, prune)
	return err == nil && !busy && pruned
}

func cloneDarwinGateInputs(ctx context.Context, checkout, snapshot string, inputs []string, preserveMode bool) error {
	root, err := os.OpenRoot(checkout)
	if err != nil {
		return err
	}
	defer root.Close()
	inputs = slices.Clone(inputs)
	slices.Sort(inputs)
	for _, input := range slices.Compact(inputs) {
		if err := ctx.Err(); err != nil {
			return err
		}
		src, err := openGateInput(root, input)
		if err != nil {
			return err
		}
		err = cloneDarwinGateInput(src, filepath.Join(snapshot, input), preserveMode)
		_ = src.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func cloneDarwinGateInput(src *os.File, dst string, preserveMode bool) error {
	info, err := src.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular gate input: %q", src.Name())
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := unix.Fclonefileat(int(src.Fd()), unix.AT_FDCWD, dst, unix.CLONE_NOOWNERCOPY); err != nil {
		return err
	}
	mode := info.Mode().Perm()
	if !preserveMode {
		mode = mode&0o100 | 0o400
	}
	return os.Chmod(dst, mode)
}

func darwinPathWithin(root, path string) bool {
	canonical := func(p string) string {
		p = strings.ToLower(filepath.Clean(p))
		const data = "/system/volumes/data"
		if p == data {
			return "/"
		}
		if strings.HasPrefix(p, data+"/") {
			return strings.TrimPrefix(p, data)
		}
		return p
	}
	rel, err := filepath.Rel(canonical(root), canonical(path))
	return err == nil && filepath.IsLocal(rel)
}

type darwinHandoffPlan struct {
	// The shared gate selects and verifies the platform artifact before preparing it here.
	prepareRuntime func(context.Context, string) error
	env            map[string]string
	toolPaths      []string
}

func darwinHandoffInvocation(check editCheck, original, checkout string, roots handoffRoots, inputs []string) ([]string, string, error) {
	snapshot := roots.checkout
	rebase := func(path string) (string, bool) {
		for _, root := range []string{gateRoot, original, checkout} {
			rel, err := filepath.Rel(root, path)
			if err == nil && filepath.IsLocal(rel) {
				return filepath.Join(snapshot, rel), true
			}
		}
		return path, false
	}
	cwd := snapshot
	if check.cwd != "" {
		var ok bool
		cwd, ok = rebase(check.cwd)
		if !ok {
			return nil, "", errors.New("gate cwd is outside checkout")
		}
	}
	var argv []string
	replace := roots.replacer()
	for _, arg := range check.argv {
		switch arg {
		case "{files}":
			for _, input := range inputs {
				argv = append(argv, filepath.Join(snapshot, input))
			}
		default:
			operand, rebased := rebase(arg)
			if !rebased && filepath.IsAbs(arg) {
				physical, err := filepath.EvalSymlinks(arg)
				if darwinPathWithin(checkout, arg) || (err == nil && darwinPathWithin(checkout, physical)) {
					return nil, "", errors.New("a literal argument refers to the live checkout")
				}
			}
			argv = append(argv, replace.Replace(operand))
		}
	}
	if len(argv) == 0 || argv[0] == "" {
		return nil, "", errors.New("empty gate command")
	}
	return argv, cwd, nil
}

func darwinTrustedToolchain(path string) string {
	if !filepath.IsAbs(path) {
		return ""
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	for p := physical; ; p = filepath.Dir(p) {
		var stat unix.Stat_t
		if err := unix.Stat(p, &stat); err != nil || stat.Uid != 0 || stat.Mode&0o022 != 0 {
			return ""
		}
		if filepath.Dir(p) == p {
			return physical
		}
	}
}

func darwinHandoffEnvironment(ctx context.Context, run string, roots handoffRoots, overrides map[string]string) ([]string, []string) {
	var read []string
	path := []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}
	var env []string
	if output, err := darwinHandoffOutput(ctx, "/usr/bin/xcode-select", "/", []string{"PATH=/usr/bin:/bin"}, "-p"); err == nil {
		if dir := darwinTrustedToolchain(strings.TrimSpace(string(output))); dir != "" {
			read = append(read, dir)
			path = append([]string{filepath.Join(dir, "usr/bin")}, path...)
		}
	}
	goBin, _ := exec.LookPath("go")
	for _, candidate := range []string{goBin, "/usr/local/go/bin/go"} {
		binary := darwinTrustedToolchain(candidate)
		if binary == "" {
			continue
		}
		probeEnv := []string{"PATH=/usr/bin:/bin", "GOENV=off", "GOTOOLCHAIN=local"}
		if output, err := darwinHandoffOutput(ctx, binary, "/", probeEnv, "env", "GOROOT"); err == nil {
			dir := darwinTrustedToolchain(strings.TrimSpace(string(output)))
			if dir == "" {
				continue
			}
			read = append(read, dir)
			path = append([]string{filepath.Join(dir, "bin")}, path...)
			env = append(env, "GOROOT="+dir)
			break
		}
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		switch key {
		case "ETHOS_HANDOFF_ROOT", "ETHOS_HANDOFF_RUNTIME", "ETHOS_HANDOFF_SCRATCH", "TMPDIR":
			continue
		}
		keys = append(keys, key)
	}
	env = append(env, "PATH="+strings.Join(path, ":"), "HOME="+filepath.Join(run, "home"),
		"TMPDIR="+roots.scratch, "GOCACHE="+filepath.Join(run, "gocache"),
		"GOMODCACHE="+filepath.Join(run, "gomodcache"), "GOTOOLCHAIN=local", "GOPROXY=off",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	slices.Sort(keys)
	replace := roots.replacer()
	for _, key := range keys {
		env = append(env, key+"="+replace.Replace(overrides[key]))
	}
	env = append(env, "ETHOS_HANDOFF_ROOT="+roots.checkout, "ETHOS_HANDOFF_RUNTIME="+roots.runtime,
		"ETHOS_HANDOFF_SCRATCH="+roots.scratch)
	return env, read
}

func writeDarwinHandoffProfile(run, checkout string, roots handoffRoots, toolchain, checkoutScratch []string, system handoffSystemPlan, loopback bool) (string, error) {
	read := []string{roots.checkout, roots.runtime, "/bin", "/sbin", "/usr/bin", "/usr/lib", "/usr/libexec", "/usr/share",
		"/System/Library", "/System/Volumes/Preboot/Cryptexes/OS",
		"/dev", "/private/var/db/timezone"}
	read = append(read, toolchain...)
	var exact []string
	for _, input := range system.Mounts {
		if input.Directory {
			read = append(read, input.Source, input.Target)
		} else {
			exact = append(exact, input.Source, input.Target)
		}
	}
	write := []string{filepath.Join(run, "home"), roots.scratch, filepath.Join(run, "gocache"),
		filepath.Join(run, "gomodcache")}
	write = append(write, checkoutScratch...)
	read = append(read, write...)
	ancestors := map[string]bool{}
	for _, path := range slices.Concat(read, exact) {
		if darwinPathWithin(path, checkout) || darwinPathWithin(checkout, path) {
			return "", errors.New("checkout overlaps an allowed sandbox path")
		}
		for p := filepath.Dir(path); !ancestors[p]; p = filepath.Dir(p) {
			ancestors[p] = true
		}
	}
	var parents []string
	for p := range ancestors {
		parents = append(parents, p)
	}
	sort.Strings(parents)
	filters := func(op string, paths []string) string {
		var out strings.Builder
		for _, path := range paths {
			fmt.Fprintf(&out, " (%s %s)", op, strconv.Quote(path))
		}
		return out.String()
	}
	profile := "(version 1)\n(deny default)\n(allow process-fork sysctl-read)\n" +
		"(allow file-read-data (literal \"/\"))\n(allow file-read* (literal \"/private/var/select/sh\"))\n" +
		"(allow file-read-metadata" + filters("literal", parents) + ")\n" +
		"(allow file-read*" + filters("subpath", read) + filters("literal", exact) + ")\n" +
		"(allow file-write*" + filters("subpath", write) + " (literal \"/dev/null\"))\n" +
		"(allow network-bind network-inbound (local unix-socket (subpath " + strconv.Quote(roots.scratch) + ")))\n" +
		"(allow process-exec file-map-executable" + filters("subpath", read) + filters("literal", exact) + ")\n" +
		"(deny file-read* file-write*" + filters("subpath", []string{checkout}) + ")\n"
	if system.DeveloperDir != "" {
		profile += "(deny file-read* (literal " + strconv.Quote(darwinDeveloperSelector) + "))\n"
	}
	if loopback {
		profile += "(allow network-bind network-inbound (local ip \"localhost:*\"))\n" +
			"(allow network-outbound (remote ip \"localhost:*\"))\n"
	}
	target := filepath.Join(run, "profile.sb")
	return target, os.WriteFile(target, []byte(profile), 0o600)
}

func executeHandoffSnapshot(ctx context.Context, scratch, checkout, proof string, inputs []string, check editCheck, prepared ...handoffWorktreePlan) (string, error) {
	if !handoffSandboxSupported() {
		return "", &darwinHandoffDeferred{cause: errors.New("isolation self-test failed")}
	}
	return runDarwinHandoffSnapshot(ctx, scratch, checkout, proof, inputs, check, prepared...)
}

func runDarwinHandoffSnapshot(ctx context.Context, _, checkout, proof string, inputs []string, check editCheck, prepared ...handoffWorktreePlan) (string, error) {
	return runDarwinHandoffPlan(ctx, checkout, proof, inputs, check, darwinHandoffPlan{}, prepared...)
}

func runDarwinHandoffPlan(ctx context.Context, checkout, proof string, inputs []string, check editCheck, plan darwinHandoffPlan, prepared ...handoffWorktreePlan) (_ string, err error) {
	timeout := check.timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() {
		if err != nil {
			err = &darwinHandoffDeferred{cause: err}
		}
	}()
	original, err := filepath.Abs(checkout)
	if err != nil {
		return "", err
	}
	checkout, err = filepath.EvalSymlinks(original)
	if err != nil {
		return "", err
	}
	scratch, err := darwinHandoffScratch(checkout)
	if err != nil {
		return "", err
	}
	run := filepath.Join(scratch, "eh-"+rand.Text()[:8])
	if err := os.Mkdir(run, 0o700); err != nil {
		return "", err
	}
	defer os.RemoveAll(run)
	roots := handoffRoots{checkout: filepath.Join(run, "c"), runtime: filepath.Join(run, "r"), scratch: filepath.Join(run, "s"), temp: filepath.Join(run, "s")}
	snapshot := roots.checkout
	for _, name := range []string{"c", "r", "s", "s/home", "home", "gocache", "gomodcache"} {
		if err := os.Mkdir(filepath.Join(run, name), 0o700); err != nil {
			return "", err
		}
	}
	preserveMode := check.policy != nil && check.policy.WorktreeSnapshot != nil
	if err := cloneDarwinGateInputs(ctx, checkout, snapshot, inputs, preserveMode); err != nil {
		return "", err
	}
	got, err := digestGateInputsWithMode(ctx, snapshot, "", inputs, preserveMode)
	if err != nil {
		return "", err
	}
	if proof != got {
		return "", errors.New("could not establish snapshot isolation: snapshot proof mismatch")
	}
	if plan.prepareRuntime != nil {
		if err := plan.prepareRuntime(ctx, roots.runtime); err != nil {
			return "", err
		}
	}
	var env, toolchain, checkoutScratch []string
	if check.policy != nil && check.policy.WorktreeSnapshot != nil {
		env, checkoutScratch, err = configureDarwinWorktree(roots, checkout, check.policy.WorktreeSnapshot)
		if err != nil {
			return "", err
		}
	} else {
		env, toolchain = darwinHandoffEnvironment(ctx, run, roots, plan.env)
	}
	argv, cwd, err := darwinHandoffInvocation(check, original, checkout, roots, inputs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return "", fmt.Errorf("check cwd missing from snapshot or is not a directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("check cwd missing from snapshot or is not a directory: %w", unix.ENOTDIR)
	}
	if !gatePlanInputsPresent(snapshot, check.argv) {
		return "", errors.New("could not establish snapshot isolation: undeclared snapshot operand")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var system handoffSystemPlan
	if len(prepared) != 0 {
		system = prepared[0].system
	}
	command := argv[0]
	if !strings.ContainsRune(command, '/') {
		command = darwinSystemTool(command, env)
	}
	if system.DeveloperDir != "" {
		if !trustedDarwinCLT(system.DeveloperDir) {
			return "", errHandoffXcodeUntrusted
		}
		env = slices.DeleteFunc(env, func(variable string) bool {
			return strings.HasPrefix(variable, "DEVELOPER_DIR=")
		})
		env = append(env, "DEVELOPER_DIR="+system.DeveloperDir)
	}
	if darwinXcodeTool(command) && !(darwinMakeShim(command) && system.DeveloperDir != "") && !hasDarwinDeveloperSelector(system) {
		return "", errHandoffXcodeUntrusted
	}
	loopback := check.policy != nil && check.policy.WorktreeSnapshot != nil && check.policy.WorktreeSnapshot.Network == "loopback"
	profile, err := writeDarwinHandoffProfile(run, checkout, roots, append(toolchain, plan.toolPaths...), checkoutScratch, system, loopback)
	if err != nil {
		return "", err
	}
	return runDarwinHandoffCheck(ctx, profile, checkout, cwd, env, argv, check, loopback)
}

func darwinHandoffScratch(checkout string) (string, error) {
	const systemTemp = "/private/tmp"
	var info unix.Stat_t
	if err := unix.Lstat(systemTemp, &info); err != nil {
		return "", err
	}
	if info.Uid != 0 || info.Mode&unix.S_IFMT != unix.S_IFDIR || info.Mode&unix.S_ISVTX == 0 {
		return "", errors.New("sandbox scratch parent is not a root-owned sticky directory")
	}
	if darwinPathWithin(checkout, systemTemp) {
		return "", errors.New("sandbox scratch is inside checkout")
	}
	return systemTemp, nil
}

func runDarwinHandoffCheck(ctx context.Context, profile, checkout, cwd string, env, argv []string, check editCheck, loopback bool) (string, error) {
	start := time.Now()
	read, write, err := os.Pipe()
	if err != nil {
		return "", err
	}
	defer read.Close()
	defer write.Close()
	args := []string{"-f", profile, "/bin/sh", "-c", `printf R >&3 || exit 125; exec 3>&-; exec "$@"`, "ethos-handoff"}
	var stdout, stderr editTail
	wait, err := startDarwinHandoffProcess(ctx, "/usr/bin/sandbox-exec", append(args, argv...), func(cmd *exec.Cmd) {
		cmd.Dir, cmd.Env = cwd, env
		cmd.ExtraFiles = []*os.File{write}
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
	})
	if err != nil {
		return "", fmt.Errorf("sandbox launch did not complete: %w", err)
	}
	_ = write.Close()
	var ready [1]byte
	stop := context.AfterFunc(ctx, func() { _ = read.Close() })
	_, readyErr := io.ReadFull(read, ready[:])
	stop()
	if readyErr == nil && ready[0] == 'R' {
		diagnostic.LogHookCheck(ctx, checkout, checkout, "handoff", check.name, "launched", time.Since(start))
	}
	err = wait()
	if ctx.Err() != nil {
		return "", errors.New("snapshot check timed out or was interrupted")
	}
	if readyErr != nil || ready[0] != 'R' {
		return "", fmt.Errorf("sandbox launch did not complete: %v: %s", err, stderr.data)
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() < 0 {
			return "", errors.New("snapshot check did not complete")
		}
		switch code := exit.ExitCode(); code {
		case 128 + int(unix.SIGHUP), 128 + int(unix.SIGINT), 128 + int(unix.SIGKILL), 128 + int(unix.SIGTERM):
			return "", fmt.Errorf("check exited %d (likely killed or interrupted); not cached", code)
		default:
			if code >= 126 {
				return "", fmt.Errorf("sandbox launch did not complete: %v: %s", err, stderr.data)
			}
		}
	}
	if err == nil && (!check.failOnOutput || len(stdout.data) == 0) {
		if !loopback {
			diagnostic.LogHookCheck(ctx, checkout, checkout, "handoff", check.name, "completed", time.Since(start))
		}
		return "", nil
	}
	detail := "non-empty formatting output"
	if err != nil {
		detail = err.Error()
	}
	if !loopback {
		diagnostic.LogHookCheck(ctx, checkout, checkout, "handoff", check.name, "failed", time.Since(start))
	}
	return fmt.Sprintf("%s: %s\n%s", check.name, detail, bytes.Join([][]byte{stdout.data, stderr.data}, []byte("\n"))), nil
}

func RunHandoffSandbox() { runHandoffSandbox(nil) }

func runHandoffSandbox(func(string) error) {}
