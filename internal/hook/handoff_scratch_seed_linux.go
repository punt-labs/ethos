package hook

import (
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func seedHandoffScratch(root string, entries map[string]string) error {
	if len(entries) == 0 {
		return nil
	}
	roots := linuxHandoffRoots()
	dest, err := os.OpenRoot(filepath.Join(root, roots.scratch))
	if err != nil {
		return err
	}
	defer dest.Close()
	remaining := int64(16 << 30)
	destinations := slices.Sorted(maps.Keys(entries))
	var copied []string
	for _, name := range destinations {
		target := strings.TrimPrefix(name, "{scratch}/")
		if !validHandoffSeedPath(target, false) {
			return errors.New("invalid scratch seed destination")
		}
		for _, other := range copied {
			if insideGatePath(other, target) || insideGatePath(target, other) {
				return errors.New("overlapping scratch seed destinations")
			}
		}
		copied = append(copied, target)
		base, source, ok := strings.Cut(entries[name], "/")
		fromRuntime := base == "{runtime}"
		if !ok || !validHandoffSeedPath(source, fromRuntime) {
			return errors.New("invalid scratch seed source")
		}
		var from string
		switch base {
		case "{root}":
			from = roots.checkout
		case "{runtime}":
			from = roots.runtime
		default:
			return errors.New("invalid scratch seed source root")
		}
		if err := copyHandoffScratchSeed(filepath.Join(root, from), source, dest, target, &remaining, fromRuntime); err != nil {
			return err
		}
	}
	return nil
}

func validHandoffSeedPath(name string, allowGit bool) bool {
	return name != "." && filepath.IsLocal(name) && filepath.Clean(name) == name &&
		!strings.ContainsAny(name, "\\\x00{}") && (allowGit || !slices.Contains(strings.Split(name, "/"), ".git"))
}

func copyHandoffScratchSeed(from, source string, dest *os.Root, target string, remaining *int64, allowGit bool) error {
	src, err := os.OpenRoot(from)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := dest.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	return fs.WalkDir(src.FS(), source, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, name)
		if err != nil || !validHandoffSeedPath(name, allowGit) {
			return errors.New("invalid path in scratch seed tree")
		}
		output := filepath.Join(target, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return dest.Mkdir(output, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			return copyHandoffScratchSeedFile(src, name, dest, output, info.Mode().Perm()|0o600, remaining)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := src.Readlink(name)
			if err != nil {
				return err
			}
			if rel == "." || filepath.IsAbs(link) || !insideGatePath(source, filepath.Join(filepath.Dir(name), link)) {
				return errors.New("scratch seed symlink escapes the copied tree")
			}
			return dest.Symlink(link, output)
		default:
			return errors.New("scratch seed contains a special file")
		}
	})
}

func copyHandoffScratchSeedFile(src *os.Root, name string, dest *os.Root, target string, mode os.FileMode, remaining *int64) error {
	in, err := src.Open(name)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := dest.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(in, *remaining+1))
	*remaining -= n
	if *remaining < 0 {
		err = errors.New("scratch seed exceeds the copy budget")
	}
	return errors.Join(err, out.Close())
}
