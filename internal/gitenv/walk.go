package gitenv

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SetDeviceOf replaces the device lookup Walk uses, for a test, and returns the
// one it replaced.
func SetDeviceOf(f func(string) (uint64, bool)) func(string) (uint64, bool) {
	old := deviceOf
	deviceOf = f
	return old
}

// deviceOf is the device lookup Walk uses; SetDeviceOf replaces it in a test.
var deviceOf = statDevice

// Walk returns the directories git examines for a repository from start,
// nearest first, as git's setup_git_directory does: from the physical working
// directory (getcwd resolves symlinks) upward, stopping before the longest
// GIT_CEILING_DIRECTORIES entry that is a proper ancestor of it, so neither
// that entry nor anything above it is examined, and stopping before a parent
// on a different filesystem from the working directory unless
// GIT_DISCOVERY_ACROSS_FILESYSTEM is a true boolean. A parent whose device
// cannot be read is treated as a boundary (git dies there instead).
func Walk(start string) []string { return WalkEnv(start, nil) }

// WalkEnv is Walk reading GIT_CEILING_DIRECTORIES and
// GIT_DISCOVERY_ACROSS_FILESYSTEM from env (nil is the process's), as the git
// that env is for would.
func WalkEnv(start string, env []string) []string {
	dir := RealDir(start)
	off := ceilingOffset(dir, gitCeilings(env))
	dev, devOK := deviceOf(dir)
	oneFS := devOK && !acrossFilesystems(env)
	dirs := []string{dir}
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return dirs
		}
		// git cuts the path at the separator that ends parent: index 0 for
		// the root, else the length of parent.
		cut := len(parent)
		if parent == string(filepath.Separator) {
			cut = 0
		}
		if cut <= off {
			return dirs
		}
		if oneFS {
			if d, ok := deviceOf(parent); !ok || d != dev {
				return dirs
			}
		}
		dirs = append(dirs, parent)
		dir = parent
	}
}

// acrossFilesystems reports whether GIT_DISCOVERY_ACROSS_FILESYSTEM lets the
// walk cross a mount point. Unset or not a boolean is false; git dies on a
// value that is not a boolean.
func acrossFilesystems(env []string) bool {
	v, set := lookupEnv(env, "GIT_DISCOVERY_ACROSS_FILESYSTEM")
	if !set {
		return false
	}
	b, ok := ParseBool(v)
	return ok && b
}

// ParseBool parses a git boolean value: true/yes/on and false/no/off,
// case-insensitively, the empty string (false), or an integer (non-zero is
// true). ok is false for anything else.
func ParseBool(s string) (b, ok bool) {
	switch strings.ToLower(s) {
	case "true", "yes", "on":
		return true, true
	case "false", "no", "off", "":
		return false, true
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return false, false
	}
	return n != 0, true
}

// gitCeilings returns the GIT_CEILING_DIRECTORIES entries as git uses them
// (canonicalize_ceiling_entry): a relative entry is ignored; an entry before
// any empty entry is resolved to its real path, and ignored when that fails;
// an entry after an empty entry is kept exactly as written.
func gitCeilings(env []string) []string {
	var out []string
	asWritten := false
	for _, c := range strings.Split(envValue(env, "GIT_CEILING_DIRECTORIES"), string(os.PathListSeparator)) {
		switch {
		case c == "":
			asWritten = true
		case !filepath.IsAbs(c):
		case asWritten:
			out = append(out, c)
		default:
			if r, err := filepath.EvalSymlinks(c); err == nil {
				out = append(out, r)
			}
		}
	}
	return out
}

// ceilingOffset is git's longest_ancestor_length: the length of the longest
// ceiling that is a proper ancestor of path (a trailing separator not
// counted), or -1 when none is. The comparison is on the strings, as git's.
func ceilingOffset(path string, ceils []string) int {
	sep := string(filepath.Separator)
	if path == sep {
		return -1
	}
	longest := -1
	for _, c := range ceils {
		n := len(c)
		if strings.HasSuffix(c, sep) {
			n--
		}
		if !strings.HasPrefix(path, c[:n]) || len(path) <= n+1 || path[n:n+1] != sep {
			continue
		}
		longest = max(longest, n)
	}
	return longest
}

// RealDir returns p with symlinks resolved, or p cleaned when it cannot be.
func RealDir(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// lookupEnv is os.LookupEnv over env, or over the process's when env is nil.
// The last entry wins, as in an exec'd environment.
func lookupEnv(env []string, name string) (string, bool) {
	if env == nil {
		return os.LookupEnv(name)
	}
	v, found := "", false
	for _, kv := range env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == name {
			v, found = val, true
		}
	}
	return v, found
}

func envValue(env []string, name string) string {
	v, _ := lookupEnv(env, name)
	return v
}
