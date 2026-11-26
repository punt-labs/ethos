//go:build darwin

package hook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/macho"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/checkset"
	"golang.org/x/sys/unix"
)

const darwinDeveloperSelector = "/private/var/select/developer_dir"
const darwinCLT = "/Library/Developer/CommandLineTools"

func darwinXcodeTool(path string) bool {
	switch path {
	case "/usr/bin/xcode-select", "/usr/bin/xcrun", "/usr/bin/xcodebuild":
		return true
	}
	if filepath.Dir(path) != "/usr/bin" {
		return false
	}
	candidate, err := os.Stat(path)
	if err != nil {
		return false
	}
	shim, err := os.Stat("/usr/bin/clang")
	return err == nil && os.SameFile(candidate, shim)
}

func prepareHandoffSystem(ctx context.Context, checkout string, profile *checkset.WorktreeSnapshot, argv ...string) (handoffSystemPlan, error) {
	var plan handoffSystemPlan
	if profile == nil {
		return plan, nil
	}
	ctx, cancel := context.WithTimeout(ctx, gateInputTime)
	defer cancel()
	inputs := profile.System.Platforms[runtime.GOOS+"-"+runtime.GOARCH]
	for _, group := range []struct {
		paths []string
		dir   bool
	}{
		{slices.Concat(profile.System.Dirs, inputs.Dirs), true},
		{slices.Concat(profile.System.Files, inputs.Files), false},
	} {
		slices.Sort(group.paths)
		for _, path := range slices.Compact(group.paths) {
			if _, err := addDarwinSystemInput(ctx, &plan, checkout, path, "", group.dir); err != nil {
				return plan, err
			}
		}
	}
	roots := handoffRoots{checkout: gateRoot, runtime: "/runtime", scratch: "/scratch", temp: "/scratch"}
	env, err := roots.environment(profile, checkset.SnapshotPlatform{}, worktreeSandboxPATH)
	if err != nil {
		return plan, err
	}
	tools := slices.Concat(profile.System.Tools, inputs.Tools)
	if len(argv) > 0 && (argv[0] == "make" || argv[0] == "/usr/bin/make") {
		path := argv[0]
		if path == "make" {
			path = darwinSystemTool(path, env)
		}
		if darwinMakeShim(path) {
			if err := addDarwinCLT(ctx, &plan, checkout, darwinCLT); err != nil {
				return plan, err
			}
		}
	}
	slices.Sort(tools)
	for _, tool := range slices.Compact(tools) {
		path := darwinSystemTool(tool, env)
		if path == "" {
			return plan, fmt.Errorf("system tool %s is not available on the sandbox PATH", tool)
		}
		source, err := addDarwinSystemInput(ctx, &plan, checkout, path, tool, false)
		if err != nil {
			return plan, err
		}
		if err := addDarwinLibraryClosure(ctx, &plan, checkout, source, source, nil, make(map[string]bool)); err != nil {
			return plan, fmt.Errorf("cannot resolve dylibs for system tool %s: %w", tool, err)
		}
		if darwinMakeShim(path) {
			if err := addDarwinCLT(ctx, &plan, checkout, darwinCLT); err != nil {
				return plan, err
			}
		} else if path == "/usr/bin/git" && plan.DeveloperDir != "" && darwinXcodeTool(path) {
			git := filepath.Join(plan.DeveloperDir, "usr/bin/git")
			if darwinTrustedToolchain(git) != git || unix.Access(git, unix.X_OK) != nil {
				return plan, fmt.Errorf("%w: cannot use Command Line Tools git", errHandoffXcodeUntrusted)
			}
			if _, err := addDarwinSystemInput(ctx, &plan, checkout, git, "clt-git", false); err != nil {
				return plan, err
			}
		} else if darwinXcodeTool(path) && !hasDarwinDeveloperSelector(plan) {
			if err := addDarwinXcode(ctx, &plan, checkout); err != nil {
				return plan, fmt.Errorf("%w: %v", errHandoffXcodeUntrusted, err)
			}
		}
	}
	h := sha256.New()
	if err := json.NewEncoder(h).Encode([]any{"darwin-worktree-system-v1", plan.Mounts, plan.Inputs, plan.DeveloperDir}); err != nil {
		return plan, err
	}
	plan.Fingerprint = hex.EncodeToString(h.Sum(nil))
	return plan, nil
}

func darwinMakeShim(path string) bool {
	return path == "/usr/bin/make" && darwinXcodeTool(path)
}

func trustedDarwinCLT(dir string) bool {
	makePath := filepath.Join(dir, "usr/bin/make")
	return darwinTrustedToolchain(dir) == dir && darwinTrustedToolchain(makePath) == makePath &&
		unix.Access(makePath, unix.X_OK) == nil
}

func addDarwinCLT(ctx context.Context, plan *handoffSystemPlan, checkout, dir string) error {
	if plan.DeveloperDir == dir {
		return nil
	}
	if hasDarwinDeveloperSelector(*plan) || !safeDarwinSystemPath(dir, checkout) || !trustedDarwinCLT(dir) {
		return fmt.Errorf("%w: cannot use trusted Command Line Tools at %s", errHandoffXcodeUntrusted, dir)
	}
	for _, path := range []string{dir, filepath.Join(dir, "usr/bin")} {
		var stat unix.Stat_t
		if err := unix.Stat(path, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			return fmt.Errorf("%w: cannot fingerprint Command Line Tools directory %s", errHandoffXcodeUntrusted, path)
		}
		plan.Inputs = append(plan.Inputs, handoffSystemInput{
			Path: path, Tool: "command-line-tools", Directory: true,
			Digest: fmt.Sprintf("%d:%d:%d:%v:%v", stat.Dev, stat.Ino, stat.Mode, stat.Mtim, stat.Ctim),
		})
	}
	plan.Mounts = append(plan.Mounts, handoffSystemMount{Source: dir, Target: dir, Directory: true})
	if _, err := addDarwinSystemInput(ctx, plan, checkout, filepath.Join(dir, "usr/bin/make"), "clt-make", false); err != nil {
		return fmt.Errorf("%w: %v", errHandoffXcodeUntrusted, err)
	}
	plan.DeveloperDir = dir
	return nil
}

func darwinSystemTool(name string, env []string) string {
	for _, value := range env {
		if path, ok := strings.CutPrefix(value, "PATH="); ok {
			for _, dir := range filepath.SplitList(path) {
				candidate := filepath.Join(dir, name)
				if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && unix.Access(candidate, unix.X_OK) == nil {
					return candidate
				}
			}
		}
	}
	return ""
}

func addDarwinSystemInput(ctx context.Context, plan *handoffSystemPlan, checkout, path, tool string, directory bool) (string, error) {
	source, err := filepath.EvalSymlinks(path)
	if err != nil || !safeDarwinSystemPath(path, checkout) || !safeDarwinSystemPath(source, checkout) {
		return "", fmt.Errorf("cannot safely resolve system input %s", path)
	}
	digest, err := fingerprintDarwinSystemInput(ctx, source, directory)
	if err != nil {
		return "", fmt.Errorf("cannot fingerprint system input %s: %w", path, err)
	}
	plan.Inputs = append(plan.Inputs, handoffSystemInput{Path: path, Tool: tool, Digest: digest, Directory: directory})
	plan.Mounts = append(plan.Mounts, handoffSystemMount{Source: source, Target: path, Directory: directory})
	return source, nil
}

func safeDarwinSystemPath(path, checkout string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	for _, blocked := range []string{checkout, gateRoot, "/scratch", "/runtime", "/tmp", "/private/tmp", "/dev", "/private/dev"} {
		if darwinPathWithin(strings.ToLower(blocked), strings.ToLower(path)) ||
			darwinPathWithin(strings.ToLower(path), strings.ToLower(blocked)) {
			return false
		}
	}
	return true
}

func fingerprintDarwinSystemInput(ctx context.Context, path string, directory bool) (string, error) {
	h := sha256.New()
	if directory {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return "", errors.New("system input is not a directory")
		}
		enc := json.NewEncoder(h)
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
			var link string
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				link, err = os.Readlink(name)
				if err != nil {
					return err
				}
			case info.IsDir(), info.Mode().IsRegular():
			default:
				return errors.New("special file in system input directory")
			}
			return enc.Encode([]any{strings.TrimPrefix(name, path), info.Size(), info.ModTime().UnixNano(), info.Mode(), link})
		})
		if err != nil {
			return "", err
		}
	} else {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return "", err
		}
		f := os.NewFile(uintptr(fd), path)
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return "", errors.New("system input is not a regular file")
		}
		if _, err := io.Copy(&gateHashBudget{ctx: ctx, dst: h, remaining: math.MaxInt64}, f); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hasDarwinDeveloperSelector(plan handoffSystemPlan) bool {
	return slices.ContainsFunc(plan.Inputs, func(input handoffSystemInput) bool {
		return input.Path == darwinDeveloperSelector && input.Tool == "xcode-selector"
	})
}

func addDarwinXcode(ctx context.Context, plan *handoffSystemPlan, checkout string) error {
	if plan.DeveloperDir != "" {
		return errors.New("cannot mix Command Line Tools and Xcode selector")
	}
	if darwinTrustedToolchain(filepath.Dir(darwinDeveloperSelector)) != filepath.Dir(darwinDeveloperSelector) {
		return errors.New("untrusted parent directory for Xcode selector")
	}
	var selector unix.Stat_t
	if err := unix.Lstat(darwinDeveloperSelector, &selector); err != nil || selector.Uid != 0 || selector.Mode&unix.S_IFMT != unix.S_IFLNK {
		return errors.New("expected a root-owned Xcode selector symlink")
	}
	developer, err := os.Readlink(darwinDeveloperSelector)
	if err != nil || developer == "" || darwinTrustedToolchain(developer) != developer {
		return errors.New("cannot resolve trusted Xcode developer directory")
	}
	dir := developer
	if filepath.Base(dir) == "Developer" && filepath.Base(filepath.Dir(dir)) == "Contents" {
		dir = filepath.Dir(dir)
	} else if developer != "/Library/Developer/CommandLineTools" {
		return errors.New("unexpected Xcode developer directory")
	}
	if !safeDarwinSystemPath(dir, checkout) || !safeDarwinSystemPath(darwinDeveloperSelector, checkout) {
		return errors.New("unsafe system path overlaps Xcode")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	plan.Inputs = append(plan.Inputs, handoffSystemInput{
		Path: developer, Directory: true, Digest: fmt.Sprintf("%d:%v", info.ModTime().UnixNano(), info.Mode()),
	})
	plan.Mounts = append(plan.Mounts, handoffSystemMount{Source: dir, Target: dir, Directory: true})
	plan.Inputs = append(plan.Inputs, handoffSystemInput{
		Path: darwinDeveloperSelector, Tool: "xcode-selector", Digest: fmt.Sprintf("%s:%d:%d", developer, selector.Dev, selector.Ino),
	})
	plan.Mounts = append(plan.Mounts, handoffSystemMount{Source: darwinDeveloperSelector, Target: darwinDeveloperSelector})
	for _, path := range []string{filepath.Join(dir, "version.plist"), filepath.Join(developer, "usr/bin/xcodebuild")} {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if _, err := addDarwinSystemInput(ctx, plan, checkout, path, "", false); err != nil {
			return err
		}
	}
	return nil
}

func addDarwinLibraryClosure(ctx context.Context, plan *handoffSystemPlan, checkout, executable, source string, inherited []string, seen map[string]bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if seen[source] {
		return nil
	}
	seen[source] = true
	files, closeFiles, err := darwinMachOFiles(source)
	if err != nil {
		return err
	}
	defer closeFiles()
	for _, file := range files {
		var rpaths []string
		for _, load := range file.Loads {
			if rpath, ok := load.(*macho.Rpath); ok {
				rpaths = append(rpaths, expandDarwinDylibPath(rpath.Path, executable, source))
			}
		}
		rpaths = append(rpaths, inherited...)
		deps, err := file.ImportedLibraries()
		if err != nil {
			return err
		}
		for _, dep := range deps {
			if strings.HasPrefix(dep, "/usr/lib/") || strings.HasPrefix(dep, "/System/Library/") {
				continue
			}
			path := expandDarwinDylibPath(dep, executable, source)
			if rest, ok := strings.CutPrefix(path, "@rpath/"); ok {
				path = ""
				for _, base := range rpaths {
					candidate := filepath.Join(base, rest)
					if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
						path = candidate
						break
					}
				}
				if path == "" {
					return fmt.Errorf("cannot resolve %s", dep)
				}
			}
			target, err := addDarwinSystemInput(ctx, plan, checkout, path, "", false)
			if err != nil {
				return err
			}
			if err := addDarwinLibraryClosure(ctx, plan, checkout, executable, target, rpaths, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func expandDarwinDylibPath(path, executable, source string) string {
	for prefix, file := range map[string]string{"@loader_path/": source, "@executable_path/": executable} {
		if suffix, ok := strings.CutPrefix(path, prefix); ok {
			return filepath.Join(filepath.Dir(file), suffix)
		}
	}
	return path
}

func darwinMachOFiles(path string) ([]*macho.File, func(), error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	closeFile := func() { _ = f.Close() }
	fat, err := macho.NewFatFile(f)
	if err == nil {
		var files []*macho.File
		for _, arch := range fat.Arches {
			if (runtime.GOARCH == "arm64" && arch.Cpu == macho.CpuArm64) || (runtime.GOARCH == "amd64" && arch.Cpu == macho.CpuAmd64) {
				files = append(files, arch.File)
			}
		}
		if len(files) != 0 {
			return files, closeFile, nil
		}
		closeFile()
		return nil, nil, errors.New("system tool has no native Mach-O slice")
	}
	file, err := macho.NewFile(f)
	if err == nil {
		return []*macho.File{file}, closeFile, nil
	}
	var head [2]byte
	_, readErr := f.ReadAt(head[:], 0)
	closeFile()
	if readErr == nil && bytes.Equal(head[:], []byte("#!")) {
		return nil, func() {}, nil
	}
	return nil, nil, err
}
