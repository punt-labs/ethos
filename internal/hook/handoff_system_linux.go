package hook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/punt-labs/ethos/v4/internal/checkset"
	"golang.org/x/sys/unix"
)

var worktreeSystemRuntime = []string{"/usr/include", "/usr/share", "/usr/libexec", "/etc/alternatives"}

const handoffSystemProofTime = 5 * time.Second
const handoffSystemReadBuffer = 32 * 1024
const handoffSystemProofBatchSize = 256
const handoffSystemProofWorkers = 4

var worktreeSystemRoots = []string{
	"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32",
	"/home/linuxbrew/.linuxbrew",
	"/etc/alternatives", "/etc/os-release", "/etc/ssl/certs", "/etc/ssl/openssl.cnf", "/etc/ca-certificates",
	"/etc/texmf", "/var/lib/texmf",
}

func worktreeSandboxEnvironment(profile *checkset.WorktreeSnapshot) ([]string, error) {
	var platform checkset.SnapshotPlatform
	if profile.Runtime != nil {
		platform = profile.Runtime.Platforms[runtime.GOOS+"-"+runtime.GOARCH]
	}
	return linuxHandoffRoots().environment(profile, platform, worktreeSandboxPATH)
}

func prepareHandoffSystem(ctx context.Context, checkout string, profile *checkset.WorktreeSnapshot, _ ...string) (handoffSystemPlan, error) {
	var plan handoffSystemPlan
	if profile == nil {
		return plan, nil
	}
	ctx, cancel := context.WithTimeout(ctx, handoffSystemProofTime)
	defer cancel()
	var err error
	plan.Passwd, plan.Group, err = handoffAccountFiles()
	if err != nil {
		return plan, err
	}
	for _, dir := range slices.Concat(sandboxRuntime, worktreeSystemRuntime) {
		source, err := filepath.EvalSymlinks(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || pathsOverlap(source, checkout) || pathsOverlap(dir, checkout) {
			return plan, errors.New("cannot safely resolve the worktree system runtime")
		}
		plan.Mounts = append(plan.Mounts, handoffSystemMount{Source: source, Target: dir, Directory: true})
	}
	platform := profile.System.Platforms[runtime.GOOS+"-"+runtime.GOARCH]
	files := slices.Concat(profile.System.Files, platform.Files)
	if _, err := os.Stat("/etc/os-release"); err == nil {
		files = append(files, "/etc/os-release")
	} else if !errors.Is(err, os.ErrNotExist) {
		return plan, errors.New("cannot inspect system input /etc/os-release")
	}
	for _, group := range []struct {
		paths []string
		dir   bool
	}{
		{slices.Concat(profile.System.Dirs, platform.Dirs), true},
		{files, false},
	} {
		slices.Sort(group.paths)
		for _, path := range slices.Compact(group.paths) {
			if !safeHandoffSystemPath(path, checkout) {
				return plan, fmt.Errorf("cannot safely resolve system input %s", path)
			}
			source, err := filepath.EvalSymlinks(path)
			if err != nil || !safeHandoffSystemSource(path, source, checkout, group.dir) {
				return plan, fmt.Errorf("cannot safely resolve system input %s", path)
			}
			input := handoffSystemInput{Path: path, Directory: group.dir}
			var memo *handoffSystemMemo
			if group.dir {
				memo = loadHandoffSystemMemo(source)
			}
			input.Digest, err = fingerprintHandoffSystemInputMemo(ctx, source, group.dir, memo)
			if err != nil {
				return plan, fmt.Errorf("cannot fingerprint system input %s", path)
			}
			if memo != nil {
				memo.save()
			}
			plan.Inputs = append(plan.Inputs, input)
			if !handoffSystemCovers(plan.Mounts, path) {
				if group.dir {
					plan.Mounts = slices.DeleteFunc(plan.Mounts, func(mount handoffSystemMount) bool {
						return insideGatePath(path, mount.Target)
					})
				}
				plan.Mounts = append(plan.Mounts, handoffSystemMount{Source: source, Target: path, Directory: group.dir})
			}
		}
	}
	env, err := worktreeSandboxEnvironment(profile)
	if err != nil {
		return plan, err
	}
	tools := slices.Concat(profile.System.Tools, platform.Tools)
	slices.Sort(tools)
	for _, tool := range slices.Compact(tools) {
		var input handoffSystemInput
		for _, dir := range handoffExecutableDirs(env) {
			path := filepath.Join(dir, tool)
			if !handoffSystemCovers(plan.Mounts, path) || !handoffExecutable(path) {
				continue
			}
			source, err := filepath.EvalSymlinks(path)
			if err != nil || !safeHandoffSystemPath(source, checkout) || !handoffSystemSourceCovers(plan.Mounts, source) {
				continue
			}
			digest, err := fingerprintHandoffSystemInput(ctx, source, false)
			if err != nil {
				return plan, fmt.Errorf("cannot hash system tool %s", tool)
			}
			input = handoffSystemInput{Path: path, Tool: tool, Digest: digest}
			break
		}
		if input.Path == "" {
			return plan, fmt.Errorf("system tool %s is not available on the mounted sandbox PATH", tool)
		}
		plan.Inputs = append(plan.Inputs, input)
	}
	h := sha256.New()
	if err := json.NewEncoder(h).Encode([]any{"worktree-system-v1", plan.Mounts, plan.Inputs, plan.Passwd, plan.Group}); err != nil {
		return plan, err
	}
	plan.Fingerprint = hex.EncodeToString(h.Sum(nil))
	return plan, nil
}

func handoffAccountFiles() (string, string, error) {
	uid, gid := strconv.Itoa(os.Geteuid()), strconv.Itoa(os.Getegid())
	u, err := user.LookupId(uid)
	if err != nil || u.Username == "" || strings.ContainsAny(u.Username, ":\n\r\x00") {
		return "", "", errors.New("cannot resolve the invoking user's name for the snapshot")
	}
	g, err := user.LookupGroupId(gid)
	if err != nil || g.Name == "" || strings.ContainsAny(g.Name, ":\n\r\x00") {
		return "", "", errors.New("cannot resolve the invoking group's name for the snapshot")
	}
	home := filepath.Join(linuxHandoffRoots().scratch, "home")
	return fmt.Sprintf("%s:x:%s:%s::%s:/bin/sh\n", u.Username, uid, gid, home),
		fmt.Sprintf("%s:x:%s:\n", g.Name, gid), nil
}

func pathsOverlap(a, b string) bool {
	return insideGatePath(a, b) || insideGatePath(b, a)
}

func safeHandoffSystemPath(path, checkout string) bool {
	for _, blocked := range []string{checkout, gateRoot, "/scratch", "/runtime", "/tmp", "/dev", "/proc", "/sys", "/.old-root", "/etc/passwd", "/etc/group"} {
		if pathsOverlap(path, blocked) {
			return false
		}
	}
	return slices.ContainsFunc(worktreeSystemRoots, func(root string) bool {
		return insideGatePath(root, path)
	})
}

func safeHandoffSystemSource(path, source, checkout string, directory bool) bool {
	if safeHandoffSystemPath(source, checkout) {
		return true
	}
	return !directory && path == "/etc/os-release" &&
		source != "/nix/store" && insideGatePath("/nix/store", source) &&
		!pathsOverlap(source, checkout)
}

func handoffSystemCovers(mounts []handoffSystemMount, path string) bool {
	for _, m := range mounts {
		if path == m.Target || m.Directory && insideGatePath(m.Target, path) {
			return true
		}
	}
	return false
}

func handoffSystemSourceCovers(mounts []handoffSystemMount, path string) bool {
	for _, m := range mounts {
		if path == m.Source || m.Directory && insideGatePath(m.Source, path) {
			return true
		}
	}
	return false
}

func fingerprintHandoffSystemInput(ctx context.Context, path string, directory bool) (string, error) {
	return fingerprintHandoffSystemInputMemo(ctx, path, directory, nil)
}

func fingerprintHandoffSystemInputMemo(ctx context.Context, path string, directory bool, memo *handoffSystemMemo) (string, error) {
	if !directory {
		return fingerprintHandoffSystemFile(ctx, path, 0, make([]byte, handoffSystemReadBuffer), nil)
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", errors.New("system input is not a directory")
	}
	h := sha256.New()
	enc := json.NewEncoder(h)
	batch := make([]handoffSystemProofEntry, 0, handoffSystemProofBatchSize)
	buffers := make([][handoffSystemReadBuffer]byte, min(runtime.GOMAXPROCS(0), handoffSystemProofWorkers))
	err = filepath.WalkDir(path, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := handoffSystemProofEntry{path: name, info: info}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			item.link, err = os.Readlink(name)
			if err != nil {
				return err
			}
		case info.Mode().IsRegular(), info.IsDir():
		default:
			return errors.New("special file in system input directory")
		}
		batch = append(batch, item)
		if len(batch) == cap(batch) {
			err := writeHandoffSystemProofBatch(ctx, enc, path, batch, buffers, memo)
			batch = batch[:0]
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if err := writeHandoffSystemProofBatch(ctx, enc, path, batch, buffers, memo); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type handoffSystemProofEntry struct {
	path   string
	info   fs.FileInfo
	link   string
	digest string
	err    error
}

func writeHandoffSystemProofBatch(ctx context.Context, enc *json.Encoder, root string, batch []handoffSystemProofEntry, buffers [][handoffSystemReadBuffer]byte, memo *handoffSystemMemo) error {
	var workers sync.WaitGroup
	var next atomic.Int32
	for i := range min(len(buffers), len(batch)) {
		workers.Go(func() {
			for index := int(next.Add(1)) - 1; index < len(batch); index = int(next.Add(1)) - 1 {
				if ctx.Err() != nil {
					return
				}
				entry := &batch[index]
				if entry.info.Mode().IsRegular() {
					entry.digest, entry.err = fingerprintHandoffSystemFile(ctx, entry.path, unix.O_NOFOLLOW, buffers[i][:], memo)
				}
			}
		})
	}
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, entry := range batch {
		if entry.err != nil {
			return entry.err
		}
		if err := enc.Encode([]any{strings.TrimPrefix(entry.path, root), entry.info.Size(), entry.info.ModTime().UnixNano(), entry.info.Mode(), entry.link, entry.digest}); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func fingerprintHandoffSystemFile(ctx context.Context, path string, flags int, buffer []byte, memo *handoffSystemMemo) (string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|flags, 0)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("system input is not a regular file")
	}
	var key handoffMemoKey
	var memoPath string
	if memo != nil {
		key = handoffMemoIdentity(info)
		memoPath = handoffMemoPathKey(path)
		if digest, ok := memo.get(memoPath, key); ok {
			return digest, nil
		}
	}
	h := sha256.New()
	if _, err := io.CopyBuffer(&gateHashBudget{ctx: ctx, dst: h, remaining: math.MaxInt64}, struct{ io.Reader }{f}, buffer); err != nil {
		return "", err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if memo != nil {
		final, err := f.Stat()
		if err != nil || handoffMemoIdentity(final) != key {
			return "", errors.New("system input changed while hashing")
		}
		memo.put(memoPath, handoffMemoRecord{Key: key, Digest: digest})
	}
	return digest, nil
}

func mountHandoffSystem(root string, plan handoffSystemPlan) error {
	etc := filepath.Join(root, "etc")
	if err := os.Mkdir(etc, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(etc, "passwd"), []byte(plan.Passwd), 0o444); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(etc, "group"), []byte(plan.Group), 0o444); err != nil {
		return err
	}
	host, err := os.OpenRoot("/")
	if err != nil {
		return err
	}
	defer host.Close()
	for _, mount := range plan.Mounts {
		target := filepath.Join(root, mount.Target)
		if mount.Directory {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
		if err := bindHandoffSystemInput(host, target, mount); err != nil {
			return err
		}
	}
	return nil
}

func bindHandoffSystemInput(host *os.Root, target string, mount handoffSystemMount) error {
	source, err := openGateInput(host, strings.TrimPrefix(mount.Source, "/"))
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || (mount.Directory && !info.IsDir()) || (!mount.Directory && !info.Mode().IsRegular()) {
		return errors.New("system mount source changed type")
	}
	return bindHandoffReadOnly(fmt.Sprintf("/proc/self/fd/%d", source.Fd()), target)
}

func verifyMountedHandoffSystem(plan handoffSystemPlan, env []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), handoffSystemProofTime)
	defer cancel()
	for _, input := range plan.Inputs {
		if input.Tool != "" {
			path, err := resolveWorktreeExecutable(input.Tool, env)
			if err != nil || path != input.Path {
				return errors.New("sandbox system tool did not match the key")
			}
		}
		digest, err := fingerprintHandoffSystemInput(ctx, input.Path, input.Directory)
		if err != nil || digest != input.Digest {
			return errors.New("sandbox system input did not match the key")
		}
	}
	return nil
}
