//go:build linux || darwin

package hooks

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This bounded observer checks namespace creation/open-for-write destinations,
// not every write. The fixture requests -f -yy -s 0 and the explicit filter
// %file,fsync,fchdir,clone,clone3,fork,vfork,exit_group, without timestamps/syscall numbers.
// archivalDestinations enumerates supported mutations and recognized calls
// outside that destination check; any other syscall name in the trace refuses.
// This says nothing about untraced calls, descriptor writes, deletion safety,
// file metadata/content changes, or mount/symlink races.
//
// Each line has a numeric tid. Paths use strace C escapes; numeric dirfds need
// absolute -yy annotations. Relative paths use annotated AT_FDCWD or traced cwd
// (inherited on fork, shared with CLONE_FS). Split/resumed calls join by tid and
// are ordered by their starting line. Split chdir/fchdir refuses. Truncation,
// unknown dirfds, missing destination operands and unsupported open flags refuse,
// including killed calls whose required path/flags never appeared. Fully printed
// killed mutations still contribute attempted destinations, not proof of success.
// Read-only open exemption requires known symbolic flags, exactly one access
// mode, and a resolvable pathname. openat2 supports decoded v0 open_how (24 bytes)
// with flags, optional octal mode, and resolve; unknown fields/bits refuse.
// RESOLVE_IN_ROOT refuses even in combinations: its root-relative absolute/..
// semantics are not implemented here. Open pathname/dirfd operands cannot be
// empty; a present mode is checked even when its text is empty.
type archivalCall struct {
	tid, order int
	name, args string
	result     string
	split      bool
}

var archivalCallPattern = regexp.MustCompile(`^([a-z0-9_]+)\((.*)\)\s+=\s+(.*)$`)

func parseArchivalCalls(trace string) ([]archivalCall, error) {
	pending := map[int]archivalCall{}
	var calls []archivalCall
	finish := func(c archivalCall, text string) error {
		m := archivalCallPattern.FindStringSubmatch(text)
		if m == nil || strings.TrimSpace(m[3]) == "" {
			return fmt.Errorf("unsupported syscall record %q", text)
		}
		c.name, c.args, c.result = m[1], m[2], m[3]
		calls = append(calls, c)
		return nil
	}
	for n, line := range strings.Split(trace, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		prefix, text, ok := strings.Cut(strings.TrimSpace(line), " ")
		tid, err := strconv.Atoi(prefix)
		if !ok || err != nil {
			return nil, fmt.Errorf("missing numeric tid on line %d: %q", n+1, line)
		}
		text = strings.TrimSpace(text)
		if strings.HasPrefix(text, "+++") || strings.HasPrefix(text, "---") {
			continue
		}
		if strings.HasPrefix(text, "<... ") {
			c, ok := pending[tid]
			if !ok {
				return nil, fmt.Errorf("resume without start for tid %d", tid)
			}
			marker := "<... " + c.name + " resumed>"
			if !strings.HasPrefix(text, marker) {
				return nil, fmt.Errorf("mismatched resume for tid %d: %q", tid, text)
			}
			delete(pending, tid)
			if err := finish(c, c.args+strings.TrimPrefix(text, marker)); err != nil {
				return nil, err
			}
			continue
		}
		c := archivalCall{tid: tid, order: n}
		if start, _, ok := strings.Cut(text, "<unfinished ...>"); ok {
			if _, exists := pending[tid]; exists {
				return nil, fmt.Errorf("two unfinished calls for tid %d", tid)
			}
			name, _, ok := strings.Cut(start, "(")
			if !ok {
				return nil, fmt.Errorf("malformed unfinished call %q", text)
			}
			c.name, c.args, c.split = name, start, true
			pending[tid] = c
			continue
		}
		if err := finish(c, text); err != nil {
			return nil, err
		}
	}
	for _, c := range pending {
		// A killed task may never resume. Still classify all printed destination
		// operands; if any required operand is missing, the later parser refuses.
		if err := finish(c, strings.TrimSuffix(strings.TrimSpace(c.args), ")")+") = ?"); err != nil {
			return nil, err
		}
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].order < calls[j].order })
	return calls, nil
}

func archivalArguments(text string) ([]string, error) {
	var args []string
	start, depth := 0, 0
	quoted, escaped, annotation := false, false, false
	for i := 0; i < len(text); i++ {
		b := text[i]
		if escaped {
			escaped = false
			continue
		}
		if (quoted || annotation) && b == '\\' {
			escaped = true
			continue
		}
		if annotation {
			annotation = b != '>'
			continue
		}
		if b == '"' {
			quoted = !quoted
			continue
		}
		if quoted {
			continue
		}
		switch b {
		case '<':
			annotation = true
		case '[', '{', '(':
			depth++
		case ']', '}', ')':
			depth--
		case ',':
			if depth == 0 {
				args = append(args, strings.TrimSpace(text[start:i]))
				start = i + 1
			}
		}
		if depth < 0 {
			return nil, fmt.Errorf("unbalanced syscall arguments %q", text)
		}
	}
	if quoted || escaped || annotation || depth != 0 {
		return nil, fmt.Errorf("incomplete syscall arguments %q", text)
	}
	return append(args, strings.TrimSpace(text[start:])), nil
}

func archivalFDPath(fd string) (string, error) {
	_, path, ok := strings.Cut(fd, "<")
	if !ok || !strings.HasSuffix(path, ">") {
		return "", fmt.Errorf("dirfd lacks -yy annotation: %q", fd)
	}
	path, err := strconv.Unquote(`"` + strings.TrimSuffix(path, ">") + `"`)
	if err != nil || !filepath.IsAbs(path) {
		return "", fmt.Errorf("unsupported dirfd path %q", fd)
	}
	return path, nil
}

// A nil dirfd means this syscall has no dirfd operand, not an empty operand.
func archivalPath(operand string, dirfd *string, cwd string) (string, error) {
	if dirfd != nil && *dirfd == "" {
		return "", fmt.Errorf("empty dirfd operand")
	}
	path, err := strconv.Unquote(operand)
	if err != nil || path == "" {
		return "", fmt.Errorf("unsupported/truncated pathname %q", operand)
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil // absolute operands ignore dirfd
	}
	base := cwd
	if dirfd != nil && *dirfd != "AT_FDCWD" {
		base, err = archivalFDPath(*dirfd)
		if err != nil {
			return "", err
		}
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("unknown cwd for %q", operand)
	}
	return filepath.Join(base, path), nil
}

type archivalDestination struct {
	path, source, syscall string
	directory             bool
}

type archivalOpenOptions struct {
	writes, needsMode bool
}

func archivalOpenFlags(text string) (archivalOpenOptions, error) {
	var options archivalOpenOptions
	seen := map[string]bool{}
	access := ""
	for _, token := range strings.Split(text, "|") {
		if seen[token] {
			return options, fmt.Errorf("duplicate open flags token %q", token)
		}
		seen[token] = true
		switch token {
		case "O_RDONLY", "O_WRONLY", "O_RDWR":
			if access != "" {
				return options, fmt.Errorf("conflicting open flags access modes: %q", text)
			}
			access = token
		case "O_APPEND", "O_ASYNC", "O_CLOEXEC", "O_CREAT", "O_DIRECT", "O_DIRECTORY", "O_DSYNC", "O_EXCL", "O_LARGEFILE", "O_NOATIME", "O_NOCTTY", "O_NOFOLLOW", "O_NONBLOCK", "O_PATH", "O_SYNC", "O_TMPFILE", "O_TRUNC":
		default:
			return options, fmt.Errorf("unsupported open flags token %q", token)
		}
	}
	if access == "" {
		return options, fmt.Errorf("open flags lack access mode: %q", text)
	}
	options.needsMode = seen["O_CREAT"] || seen["O_TMPFILE"]
	options.writes = access != "O_RDONLY" || options.needsMode || seen["O_TRUNC"]
	if seen["O_PATH"] && options.writes {
		return options, fmt.Errorf("conflicting O_PATH open flags: %q", text)
	}
	return options, nil
}

var archivalModePattern = regexp.MustCompile(`^(?:0|0[0-7]{1,4})$`)

func archivalOpenMode(text string) (uint64, error) {
	if !archivalModePattern.MatchString(text) {
		return 0, fmt.Errorf("unsupported open mode %q", text)
	}
	mode, err := strconv.ParseUint(text, 8, 12)
	if err != nil {
		return 0, fmt.Errorf("unsupported open mode %q: %w", text, err)
	}
	return mode, nil
}

func archivalOpenResolve(text string) error {
	if text == "0" {
		return nil
	}
	seen := map[string]bool{}
	for _, token := range strings.Split(text, "|") {
		if seen[token] {
			return fmt.Errorf("duplicate open_how resolve token %q", token)
		}
		seen[token] = true
		switch token {
		case "RESOLVE_NO_XDEV", "RESOLVE_NO_MAGICLINKS", "RESOLVE_NO_SYMLINKS", "RESOLVE_BENEATH", "RESOLVE_CACHED":
		default:
			return fmt.Errorf("unsupported open_how resolve token %q", token)
		}
	}
	return nil
}

func archivalOpenWrites(name string, args []string) (bool, error) {
	flagsAt := 1
	if name != "open" {
		flagsAt = 2
	}
	if len(args) <= flagsAt {
		return false, fmt.Errorf("missing open flags")
	}
	flags, mode := args[flagsAt], ""
	modePresent := false
	if name == "openat2" {
		if len(args) != 4 || args[3] != "24" || !strings.HasPrefix(flags, "{") || !strings.HasSuffix(flags, "}") {
			return false, fmt.Errorf("unsupported open_how operands")
		}
		fields, err := archivalArguments(flags[1 : len(flags)-1])
		if err != nil || len(fields) < 2 || len(fields) > 3 || !strings.HasPrefix(fields[0], "flags=") || !strings.HasPrefix(fields[len(fields)-1], "resolve=") {
			return false, fmt.Errorf("unsupported open_how fields %q", flags)
		}
		if len(fields) == 3 {
			if !strings.HasPrefix(fields[1], "mode=") {
				return false, fmt.Errorf("unsupported open_how field %q", fields[1])
			}
			mode = strings.TrimPrefix(fields[1], "mode=")
			modePresent = true
			if mode == "" {
				return false, fmt.Errorf("missing open mode")
			}
		}
		if err := archivalOpenResolve(strings.TrimPrefix(fields[len(fields)-1], "resolve=")); err != nil {
			return false, err
		}
		flags = strings.TrimPrefix(fields[0], "flags=")
	} else {
		if len(args) != flagsAt+1 && len(args) != flagsAt+2 {
			return false, fmt.Errorf("unsupported open operands")
		}
		if len(args) == flagsAt+2 {
			mode = args[flagsAt+1]
			modePresent = true
		}
	}
	options, err := archivalOpenFlags(flags)
	if err != nil {
		return false, err
	}
	if options.needsMode && !modePresent {
		return false, fmt.Errorf("missing open mode")
	}
	if modePresent {
		if !options.needsMode && name != "openat2" {
			return false, fmt.Errorf("unexpected open operands: mode without create")
		}
		value, err := archivalOpenMode(mode)
		if err != nil {
			return false, err
		}
		if !options.needsMode {
			if value != 0 {
				return false, fmt.Errorf("nonzero open mode without create")
			}
		}
	}
	return options.writes, nil
}

func archivalDestinations(c archivalCall, cwd string) ([]archivalDestination, error) {
	// These explicit names cover the installed-hook filter's observed calls and
	// common lookup variants. Recognition is not proof of non-creation for an
	// unknown primitive: extending the filter/call set requires a contract change.
	switch c.name {
	case "access", "faccessat", "faccessat2", "stat", "lstat", "newfstatat", "statx", "statfs", "readlink", "readlinkat", "getcwd":
		return nil, nil // lookup only; no namespace creation destination
	case "execve", "execveat", "clone", "clone3", "fork", "vfork", "chdir", "fchdir", "fsync", "exit_group":
		return nil, nil // execution/cwd/durability; cwd inheritance handled below
	case "unlink", "unlinkat", "rmdir":
		return nil, nil // removal explicitly outside the creation-placement claim
	case "open", "openat", "openat2", "creat", "mkdir", "mkdirat", "mknod", "mknodat", "symlink", "symlinkat", "link", "linkat", "rename", "renameat", "renameat2":
	default:
		return nil, fmt.Errorf("unsupported syscall %q in archival trace filter", c.name)
	}
	a, err := archivalArguments(c.args)
	if err != nil {
		return nil, err
	}
	pathArg, dirArg, sourceArg, sourceDir := 0, -1, -1, -1
	directory, readOnly := false, false
	switch c.name {
	case "open", "openat", "openat2":
		if c.name != "open" {
			pathArg, dirArg = 1, 0
		}
		writes, err := archivalOpenWrites(c.name, a)
		if err != nil {
			return nil, err
		}
		readOnly = !writes
	case "mkdir", "mkdirat":
		directory = true
		if c.name == "mkdirat" {
			pathArg, dirArg = 1, 0
		}
	case "mknodat":
		pathArg, dirArg = 1, 0
	case "symlink":
		pathArg = 1
	case "symlinkat":
		pathArg, dirArg = 2, 1
	case "link", "rename":
		pathArg, sourceArg = 1, 0
	case "linkat", "renameat", "renameat2":
		pathArg, dirArg, sourceArg, sourceDir = 3, 2, 1, 0
	}
	resolve := func(pos, fd int) (string, error) {
		if pos >= len(a) || fd >= len(a) {
			return "", fmt.Errorf("missing destination operand: %s(%s)", c.name, c.args)
		}
		var dir *string
		if fd >= 0 {
			dir = &a[fd]
		}
		return archivalPath(a[pos], dir, cwd)
	}
	dest, err := resolve(pathArg, dirArg)
	if err != nil {
		return nil, err
	}
	if readOnly {
		return nil, nil
	}
	effect := archivalDestination{path: dest, syscall: c.name, directory: directory}
	if sourceArg >= 0 {
		effect.source, err = resolve(sourceArg, sourceDir)
		if err != nil {
			return nil, err
		}
	}
	effects := []archivalDestination{effect}
	if c.name == "renameat2" {
		if len(a) != 5 || a[4] != "0" && a[4] != "RENAME_NOREPLACE" && a[4] != "RENAME_EXCHANGE" {
			return nil, fmt.Errorf("unsupported renameat2 flags: %s", c.args)
		}
		if a[4] == "RENAME_EXCHANGE" {
			effects = append(effects, archivalDestination{path: effect.source, source: dest, syscall: c.name})
		}
	}
	return effects, nil
}

type archivalTracePolicy struct {
	checkout, local, archive, published, index, objects string
	full                                                bool
}

func archivalWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

var archivalObjectPath = regexp.MustCompile(`^(?:[0-9a-f]{2}/(?:[0-9a-f]{38}|[0-9a-f]{62}|tmp_obj_[A-Za-z0-9]+)|tmp_obj_[A-Za-z0-9]+)$`)
var archivalObjectDirectory = regexp.MustCompile(`^[0-9a-f]{2}$`)

func (p archivalTracePolicy) allows(d archivalDestination) bool {
	if archivalWithin(p.local, d.path) {
		return true
	}
	if d.directory {
		// Only the exact publication's ancestors may be created outside local.
		// EEXIST probes of existing higher ancestors are also in the trace.
		objectDir, err := filepath.Rel(p.objects, d.path)
		gitDir := err == nil && (objectDir == "." || archivalObjectDirectory.MatchString(objectDir))
		return archivalWithin(d.path, p.local) || p.full && (archivalWithin(d.path, filepath.Dir(p.published)) || archivalWithin(d.path, filepath.Dir(p.index)) || gitDir)
	}
	if d.path == "/dev/null" && strings.HasPrefix(d.syscall, "open") {
		return true
	}
	if !p.full {
		return false
	}
	if d.path == p.published || d.path == p.index || d.path == p.index+".lock" {
		return true
	}
	rel, err := filepath.Rel(p.objects, d.path)
	return err == nil && archivalObjectPath.MatchString(filepath.ToSlash(rel))
}

type archivalTraceSummary struct {
	stageCreated, archiveInstall, indexLocked, publicationInstall bool
}

func checkArchivalTrace(trace string, p archivalTracePolicy) (archivalTraceSummary, error) {
	var summary archivalTraceSummary
	calls, err := parseArchivalCalls(trace)
	if err != nil {
		return summary, err
	}
	cwds := map[int]*string{}
	for _, c := range calls {
		if cwds[c.tid] == nil {
			if len(cwds) != 0 {
				return summary, fmt.Errorf("tid %d has no traced parent", c.tid)
			}
			cwd := p.checkout
			cwds[c.tid] = &cwd
		}
		cwd := cwds[c.tid]
		if c.name == "clone" || c.name == "clone3" || c.name == "fork" || c.name == "vfork" {
			child, err := strconv.Atoi(strings.Fields(c.result)[0])
			if err == nil && child > 0 {
				copyCWD := *cwd
				cwds[child] = &copyCWD
				if strings.Contains(c.args, "CLONE_FS") {
					cwds[child] = cwd
				}
			}
		}
		if c.name == "chdir" || c.name == "fchdir" {
			if c.split {
				return summary, fmt.Errorf("concurrent/split cwd change is outside trace contract")
			}
			if c.result == "0" {
				if c.name == "chdir" {
					*cwd, err = archivalPath(c.args, nil, *cwd)
				} else {
					*cwd, err = archivalFDPath(c.args)
				}
				if err != nil {
					return summary, err
				}
			}
		}
		effects, err := archivalDestinations(c, *cwd)
		if err != nil {
			return summary, fmt.Errorf("%s: %w", c.name, err)
		}
		for _, d := range effects {
			if !p.allows(d) {
				return summary, fmt.Errorf("archival namespace destination not permitted: %s -> %q (source %q)", c.name, d.path, d.source)
			}
			creating := strings.HasPrefix(c.name, "open") && strings.Contains(c.args, "O_CREAT")
			summary.stageCreated = summary.stageCreated || creating && archivalWithin(filepath.Join(p.local, "tmp"), d.path) && strings.HasPrefix(filepath.Base(d.path), "audit-") && strings.Contains(d.path, ".ethos-tmp")
			summary.indexLocked = summary.indexLocked || creating && d.path == p.index+".lock"
			if d.source != "" && d.path == p.archive {
				if !archivalWithin(filepath.Join(p.local, "tmp"), d.source) {
					return summary, fmt.Errorf("archive installed from non-local staging %q", d.source)
				}
				summary.archiveInstall = true
			}
			summary.publicationInstall = summary.publicationInstall || d.source != "" && d.path == p.published
		}
	}
	return summary, nil
}

func TestArchivalTraceDestinations(t *testing.T) {
	p := archivalTracePolicy{checkout: "/c", local: "/s/local", archive: "/s/local/trace/a.jsonl", published: "/c/records/a.jsonl", index: "/s/git/index", objects: "/s/git/objects", full: true}
	for _, tc := range []struct {
		name, trace, wantError string
	}{
		{"eligible final copy", `1 linkat(AT_FDCWD, "/s/local/tmp/stage", AT_FDCWD, "/c/records/a.jsonl", 0) = 0`, ""},
		{"removed intermediate", "1 linkat(AT_FDCWD, \"/s/local/tmp/stage\", AT_FDCWD, \"/c/records/a.jsonl.ethos-tmp\", 0) = 0\n1 rename(\"/c/records/a.jsonl.ethos-tmp\", \"/c/records/a.jsonl\") = 0", "destination not permitted"},
		{"relative cwd", "1 chdir(\"/c/records\") = 0\n1 link(\"/s/local/tmp/stage\", \"a.jsonl\") = 0", ""},
		{"fork inherits cwd before resume", "1 chdir(\"/c/records\") = 0\n1 vfork( <unfinished ...>\n2 rename(\"/s/local/tmp/stage\", \"a.jsonl\") = 0\n1 <... vfork resumed>) = 2", ""},
		{"shared cwd", "1 clone(child_stack=NULL, flags=CLONE_FS|SIGCHLD) = 2\n2 chdir(\"/c/records\") = 0\n1 rename(\"/s/local/tmp/stage\", \"a.jsonl\") = 0", ""},
		{"fchdir", "1 fchdir(3</c/records>) = 0\n1 rename(\"/s/local/tmp/stage\", \"a.jsonl\") = 0", ""},
		{"ambiguous shared cwd refuses", "1 chdir(\"/c/records\" <unfinished ...>\n1 <... chdir resumed>) = 0", "outside trace contract"},
		{"annotated dirfd", `1 renameat(3</s/local/tmp>, "stage", 4</c/records>, "a.jsonl") = 0`, ""},
		{"annotated cwd", `1 linkat(AT_FDCWD</s/local/tmp>, "stage", AT_FDCWD</c/records>, "a.jsonl", 0) = 0`, ""},
		{"split destination", "1 renameat2(3</s/local/tmp>, \"stage\", 4</c/records>, <unfinished ...>\n1 <... renameat2 resumed>\"a.jsonl\", RENAME_NOREPLACE) = 0", ""},
		{"escaped forbidden destination", `1 link("/s/local/tmp/stage", "/c/records/\141.jsonl\056ethos-tmp") = 0`, "destination not permitted"},
		{"escaped local bytes", `1 openat(3</s/local/tmp>, "odd,\t\n\377\"\\name", O_WRONLY|O_CREAT, 0600) = 4`, ""},
		{"create adjacent", `1 openat(AT_FDCWD, "/c/records/a.jsonl.ethos-tmp", O_CREAT|O_WRONLY, 0600) = 3`, "destination not permitted"},
		{"rename destination not source", `1 renameat(AT_FDCWD, "/s/local/tmp/stage", 4</c/records>, "bad") = 0`, "destination not permitted"},
		{"exchange source is also destination", `1 renameat2(AT_FDCWD, "/c/records/bad", AT_FDCWD, "/s/local/tmp/stage", RENAME_EXCHANGE) = 0`, "destination not permitted"},
		{"unknown dirfd", `1 linkat(3, "stage", 4, "a.jsonl", 0) = 0`, "annotation"},
		{"truncated operand", `1 rename("/s/local/tmp/stage", "/c/records/a"...) = 0`, "truncated"},
		{"unmatched resume", `1 <... linkat resumed>) = 0`, "resume without start"},
		{"missing syscall result", "1 clone(child_stack=NULL, flags=CLONE_FS) = ", "unsupported syscall record"},
		{"incomplete killed link", "1 linkat(AT_FDCWD, \"/s/local/tmp/stage\", <unfinished ...>\n1 +++ killed by SIGKILL +++", "missing destination"},
		{"complete killed link still checked", "1 link(\"/s/local/tmp/stage\", \"/c/records/bad\" <unfinished ...>\n1 +++ killed by SIGKILL +++", "destination not permitted"},
		{"Git object", `1 rename("/s/git/objects/aa/tmp_obj_123abc", "/s/git/objects/aa/01234567890123456789012345678901234567") = 0`, ""},
		{"Git object directory", `1 mkdir("/s/git/objects/aa", 0777) = 0`, ""},
		{"record directory staging forbidden", `1 mkdirat(AT_FDCWD, "/c/records/ethos-git-123.ethos-tmp", 0700) = 0`, "destination not permitted"},
		{"not arbitrary Git path", `1 creat("/s/git/objects/secret", 0600) = 3`, "destination not permitted"},
		{"actual index", `1 rename("/s/local/tmp/candidate", "/s/git/index") = 0`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := checkArchivalTrace(tc.trace, p)
			if tc.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantError)
			}
		})
	}
	for _, path := range []string{p.published, p.index, p.objects + "/aa/tmp_obj_1"} {
		p.full = false
		_, err := checkArchivalTrace(`1 link("/s/local/tmp/stage", `+strconv.Quote(path)+`) = 0`, p)
		assert.ErrorContains(t, err, "destination not permitted", "blame cannot publish or touch index/objects")
	}
}

func TestArchivalTraceContract(t *testing.T) {
	p := archivalTracePolicy{checkout: "/c", local: "/s/local", archive: "/s/local/trace/a.jsonl", published: "/c/records/a.jsonl", index: "/s/git/index", objects: "/s/git/objects", full: true}
	for _, tc := range []struct {
		name, trace, wantError string
	}{
		{"known process exit", "1 exit_group(0) = ?\n1 +++ exited with 0 +++", ""},
		{"undecodable teardown still refuses", "1 ???( <unfinished ...>\n1 +++ exited with 0 +++", "unsupported syscall record"},
		{"unknown namespace primitive", `1 new_namespace_primitive("/s/local/tmp/stage", "/c/records/bad") = 0`, "unsupported syscall"},
		{"unsupported real namespace primitive", `1 move_mount(AT_FDCWD, "/s/local/tmp/stage", AT_FDCWD, "/c/records/bad", 0) = 0`, "unsupported syscall"},
		{"unknown resumed primitive", "1 new_namespace_primitive(\"/c/records/bad\" <unfinished ...>\n1 <... new_namespace_primitive resumed>) = 0", "unsupported syscall"},
		{"garbage openat flags", `1 openat(AT_FDCWD, "/c/records/bad", GARBAGE) = 3`, "open flags"},
		{"garbage open flags", `1 open("/c/records/bad", GARBAGE) = 3`, "open flags"},
		{"unknown read flag", `1 openat(AT_FDCWD, "/c/records/bad", O_RDONLY|O_FUTURE) = 3`, "open flags"},
		{"numeric read flags", `1 openat(AT_FDCWD, "/c/records/bad", O_RDONLY|0x80000000) = 3`, "open flags"},
		{"numeric zero access", `1 openat(AT_FDCWD, "/c/records/bad", 0) = 3`, "open flags"},
		{"missing access mode", `1 openat(AT_FDCWD, "/c/records/bad", O_CLOEXEC) = 3`, "open flags"},
		{"conflicting access modes", `1 openat(AT_FDCWD, "/c/records/bad", O_RDONLY|O_WRONLY) = 3`, "open flags"},
		{"duplicate flags", `1 openat(AT_FDCWD, "/c/records/bad", O_RDONLY|O_RDONLY) = 3`, "open flags"},
		{"empty flag token", `1 openat(AT_FDCWD, "/c/records/bad", O_RDONLY|) = 3`, "open flags"},
		{"path access conflict", `1 openat(AT_FDCWD, "/c/records/bad", O_RDWR|O_PATH) = 3`, "open flags"},
		{"path truncation conflict", `1 openat(AT_FDCWD, "/c/records/bad", O_RDONLY|O_PATH|O_TRUNC) = 3`, "open flags"},
		{"missing flags", `1 openat(AT_FDCWD, "/c/records/bad") = 3`, "open flags"},
		{"missing create mode", `1 openat(AT_FDCWD, "/s/local/tmp/stage", O_WRONLY|O_CREAT) = 3`, "open mode"},
		{"garbage create mode", `1 openat(AT_FDCWD, "/s/local/tmp/stage", O_WRONLY|O_CREAT, GARBAGE) = 3`, "open mode"},
		{"extra read operand", `1 openat(AT_FDCWD, "/c/records/bad", O_RDONLY, 0600) = 3`, "open operands"},
		{"read only pathname truncation", `1 openat(AT_FDCWD, "/c/rec"..., O_RDONLY) = 3`, "truncated"},
		{"read only unknown relative dirfd", `1 openat(3, "bad", O_RDONLY) = 4`, "annotation"},
		{"read only absolute ignores dirfd", `1 openat(3, "/c/records/bad", O_RDONLY|O_CLOEXEC) = 4`, ""},
		{"read only directory flags", `1 openat(AT_FDCWD, "/c/records", O_RDONLY|O_NONBLOCK|O_CLOEXEC|O_DIRECTORY|O_NOFOLLOW) = 3`, ""},
		{"read only path descriptor", `1 openat(AT_FDCWD, "/c/records/bad", O_RDONLY|O_PATH|O_CLOEXEC) = 3`, ""},
		{"read access truncates", `1 openat(AT_FDCWD, "/c/records/bad", O_RDONLY|O_TRUNC) = 3`, "destination not permitted"},
		{"write access without create", `1 openat(AT_FDCWD, "/c/records/bad", O_RDWR|O_CLOEXEC) = 3`, "destination not permitted"},
		{"relative local write", `1 openat(3</s/local/tmp>, "stage", O_WRONLY|O_CREAT|O_EXCL|O_CLOEXEC, 0600) = 4`, ""},
		{"local anonymous file", `1 openat(AT_FDCWD, "/s/local/tmp", O_RDWR|O_TMPFILE|O_CLOEXEC, 0600) = 3`, ""},
		{"record anonymous file", `1 openat(AT_FDCWD, "/c/records", O_RDWR|O_TMPFILE, 0600) = 3`, "destination not permitted"},
		{"open read standard flags", `1 open("/c/records/bad", O_RDONLY|O_LARGEFILE|O_NOCTTY|O_NOATIME) = 3`, ""},
		{"openat2 garbage flags", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=GARBAGE, resolve=0}, 24) = 3`, "open flags"},
		{"openat2 missing access", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=O_CLOEXEC, resolve=0}, 24) = 3`, "open flags"},
		{"openat2 conflicting access", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=O_RDONLY|O_RDWR, resolve=0}, 24) = 3`, "open flags"},
		{"openat2 not decoded", `1 openat2(AT_FDCWD, "/c/records/bad", 0x1234, 24) = -1 EFAULT`, "open_how"},
		{"openat2 unknown field", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=O_RDONLY, future=0, resolve=0}, 24) = 3`, "open_how"},
		{"openat2 duplicate field", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=O_RDONLY, flags=O_RDWR, resolve=0}, 24) = 3`, "open_how"},
		{"openat2 missing resolve", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=O_RDONLY}, 24) = 3`, "open_how"},
		{"openat2 unknown resolve", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=O_RDONLY, resolve=RESOLVE_FUTURE}, 24) = 3`, "open_how resolve"},
		{"openat2 numeric resolve bits", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=O_RDONLY, resolve=0x80}, 24) = 3`, "open_how resolve"},
		{"openat2 duplicate resolve", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=O_RDONLY, resolve=RESOLVE_BENEATH|RESOLVE_BENEATH}, 24) = 3`, "open_how resolve"},
		{"openat2 empty resolve token", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=O_RDONLY, resolve=RESOLVE_BENEATH|}, 24) = 3`, "open_how resolve"},
		{"openat2 mode without create", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=O_RDONLY, mode=0600, resolve=0}, 24) = 3`, "open mode"},
		{"openat2 missing create mode", `1 openat2(AT_FDCWD, "/s/local/tmp/stage", {flags=O_CREAT|O_WRONLY, resolve=0}, 24) = 3`, "open mode"},
		{"openat2 invalid mode", `1 openat2(AT_FDCWD, "/s/local/tmp/stage", {flags=O_CREAT|O_WRONLY, mode=0899, resolve=0}, 24) = 3`, "open mode"},
		{"openat2 mode beyond permission bits", `1 openat2(AT_FDCWD, "/s/local/tmp/stage", {flags=O_CREAT|O_WRONLY, mode=010000, resolve=0}, 24) = 3`, "open mode"},
		{"openat2 unsupported size", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=O_RDONLY, resolve=0}, 32) = 3`, "open_how"},
		{"openat2 read only", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=O_RDONLY|O_CLOEXEC, resolve=0}, 24) = 3`, ""},
		{"openat2 read zero mode", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=O_RDONLY, mode=0, resolve=RESOLVE_NO_MAGICLINKS|RESOLVE_BENEATH}, 24) = 3`, ""},
		{"openat2 relative create", `1 openat2(3</s/local/tmp>, "stage", {flags=O_WRONLY|O_CREAT|O_EXCL, mode=0600, resolve=RESOLVE_NO_SYMLINKS}, 24) = 4`, ""},
		{"openat2 forbidden destination", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=O_WRONLY|O_CREAT, mode=0600, resolve=0}, 24) = 3`, "destination not permitted"},
		{"resumed valid openat2", "1 openat2(AT_FDCWD, \"/c/records/bad\", <unfinished ...>\n1 <... openat2 resumed>{flags=O_RDONLY, resolve=0}, 24) = 3", ""},
		{"resumed garbage openat2", "1 openat2(AT_FDCWD, \"/c/records/bad\", <unfinished ...>\n1 <... openat2 resumed>{flags=GARBAGE, resolve=0}, 24) = 3", "open flags"},
		{"killed incomplete openat2", "1 openat2(AT_FDCWD, \"/c/records/bad\", <unfinished ...>\n1 +++ killed by SIGKILL +++", "open_how"},
		{"killed incomplete open flags", "1 openat(AT_FDCWD, \"/c/records/bad\", <unfinished ...>\n1 +++ killed by SIGKILL +++", "open flags"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := checkArchivalTrace(tc.trace, p)
			if tc.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantError)
			}
		})
	}
}

func TestArchivalTraceOperandContract(t *testing.T) {
	p := archivalTracePolicy{checkout: "/c", local: "/s/local", archive: "/s/local/trace/a.jsonl", published: "/c/records/a.jsonl", index: "/s/git/index", objects: "/s/git/objects", full: true}
	for _, tc := range []struct {
		name, trace, wantError string
	}{
		// RESOLVE_IN_ROOT is deliberately unsupported, not resolved as ordinary
		// absolute/cwd-relative syntax. The return annotation shows the hidden
		// forbidden destination in the complete and resumed review falsifiers.
		{"in root complete forbidden creation", `1 openat2(3</c/records>, "/s/local/tmp/stage", {flags=O_WRONLY|O_CREAT, mode=0600, resolve=RESOLVE_IN_ROOT}, 24) = 4</c/records/s/local/tmp/stage>`, "unsupported open_how resolve token \"RESOLVE_IN_ROOT\""},
		{"in root resumed forbidden creation", "1 openat2(3</c/records>, \"/s/local/tmp/stage\", <unfinished ...>\n1 <... openat2 resumed>{flags=O_WRONLY|O_CREAT, mode=0600, resolve=RESOLVE_IN_ROOT}, 24) = 4</c/records/s/local/tmp/stage>", "unsupported open_how resolve token \"RESOLVE_IN_ROOT\""},
		{"in root combined first", `1 openat2(3</c/records>, "/s/local/tmp/stage", {flags=O_WRONLY|O_CREAT, mode=0600, resolve=RESOLVE_IN_ROOT|RESOLVE_NO_SYMLINKS}, 24) = 4`, "unsupported open_how resolve token \"RESOLVE_IN_ROOT\""},
		{"in root combined last", `1 openat2(3</c/records>, "/s/local/tmp/stage", {flags=O_WRONLY|O_CREAT, mode=0600, resolve=RESOLVE_NO_MAGICLINKS|RESOLVE_IN_ROOT}, 24) = 4`, "unsupported open_how resolve token \"RESOLVE_IN_ROOT\""},
		{"in root read only", `1 openat2(3</c/records>, "/s/local/tmp/stage", {flags=O_RDONLY, resolve=RESOLVE_IN_ROOT}, 24) = 4`, "unsupported open_how resolve token \"RESOLVE_IN_ROOT\""},
		{"in root relative dotdot", `1 openat2(3</s/local/tmp>, "../stage", {flags=O_WRONLY|O_CREAT, mode=0600, resolve=RESOLVE_IN_ROOT}, 24) = 4`, "unsupported open_how resolve token \"RESOLVE_IN_ROOT\""},
		{"open empty read mode", `1 open("/c/records/bad", O_RDONLY, ) = 3`, "open operands"},
		{"openat empty read mode", `1 openat(AT_FDCWD, "/c/records/bad", O_RDONLY, ) = 3`, "open operands"},
		{"openat resumed empty read mode", "1 openat(AT_FDCWD, \"/c/records/bad\", O_RDONLY, <unfinished ...>\n1 <... openat resumed>) = 3", "open operands"},
		{"open empty create mode", `1 open("/s/local/tmp/stage", O_WRONLY|O_CREAT, ) = 3`, "open mode"},
		{"openat empty create mode", `1 openat(AT_FDCWD, "/s/local/tmp/stage", O_WRONLY|O_CREAT, ) = 3`, "open mode"},
		{"openat empty anonymous mode", `1 openat(AT_FDCWD, "/s/local/tmp", O_RDWR|O_TMPFILE, ) = 3`, "open mode"},
		{"open empty pathname", `1 open(, O_RDONLY) = 3`, "pathname"},
		{"open quoted empty pathname", `1 open("", O_RDONLY) = 3`, "pathname"},
		{"open empty flags", `1 open("/c/records/bad", ) = 3`, "open flags"},
		{"open trailing operand after mode", `1 open("/s/local/tmp/stage", O_WRONLY|O_CREAT, 0600, ) = 3`, "open operands"},
		{"openat empty relative dirfd", `1 openat(, "bad", O_RDONLY) = 3`, "empty dirfd operand"},
		{"openat empty absolute dirfd", `1 openat(, "/c/records/bad", O_RDONLY) = 3`, "empty dirfd operand"},
		{"openat empty write dirfd", `1 openat(, "/s/local/tmp/stage", O_WRONLY|O_CREAT, 0600) = 3`, "empty dirfd operand"},
		{"openat empty pathname", `1 openat(AT_FDCWD, , O_RDONLY) = 3`, "pathname"},
		{"openat empty flags", `1 openat(AT_FDCWD, "/c/records/bad", ) = 3`, "open flags"},
		{"openat trailing operand after mode", `1 openat(AT_FDCWD, "/s/local/tmp/stage", O_WRONLY|O_CREAT, 0600, ) = 3`, "open operands"},
		{"openat2 empty relative dirfd", `1 openat2(, "bad", {flags=O_RDONLY, resolve=0}, 24) = 3`, "empty dirfd operand"},
		{"openat2 empty absolute dirfd", `1 openat2(, "/c/records/bad", {flags=O_RDONLY, resolve=0}, 24) = 3`, "empty dirfd operand"},
		{"openat2 empty write dirfd", `1 openat2(, "/s/local/tmp/stage", {flags=O_WRONLY|O_CREAT, mode=0600, resolve=0}, 24) = 3`, "empty dirfd operand"},
		{"openat2 empty pathname", `1 openat2(AT_FDCWD, , {flags=O_RDONLY, resolve=0}, 24) = 3`, "pathname"},
		{"openat2 empty how", `1 openat2(AT_FDCWD, "/c/records/bad", , 24) = 3`, "open_how"},
		{"openat2 empty size", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=O_RDONLY, resolve=0}, ) = 3`, "open_how"},
		{"openat2 empty read mode field", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=O_RDONLY, mode=, resolve=0}, 24) = 3`, "open mode"},
		{"openat2 empty create mode field", `1 openat2(AT_FDCWD, "/s/local/tmp/stage", {flags=O_WRONLY|O_CREAT, mode=, resolve=0}, 24) = 3`, "open mode"},
		{"openat2 empty flags field", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=, resolve=0}, 24) = 3`, "open flags"},
		{"openat2 empty resolve field", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=O_RDONLY, resolve=}, 24) = 3`, "open_how resolve"},
		{"openat2 trailing how field", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=O_RDONLY, resolve=0, }, 24) = 3`, "open_how"},
		{"openat2 trailing operand", `1 openat2(AT_FDCWD, "/c/records/bad", {flags=O_RDONLY, resolve=0}, 24, ) = 3`, "open_how"},
		{"open relative read no dirfd", `1 open("records/bad", O_RDONLY) = 3`, ""},
		{"openat absolute read unannotated dirfd", `1 openat(3, "/c/records/bad", O_RDONLY) = 4`, ""},
		{"openat relative read numeric dirfd", `1 openat(3</c/records>, "bad", O_RDONLY) = 4`, ""},
		{"openat relative read annotated cwd", `1 openat(AT_FDCWD</c/records>, "bad", O_RDONLY) = 3`, ""},
		{"openat relative read traced cwd", `1 openat(AT_FDCWD, "records/bad", O_RDONLY) = 3`, ""},
		{"openat2 absolute read unannotated dirfd", `1 openat2(3, "/c/records/bad", {flags=O_RDONLY, resolve=0}, 24) = 4`, ""},
		{"openat2 relative read numeric dirfd", `1 openat2(3</c/records>, "bad", {flags=O_RDONLY, mode=0, resolve=RESOLVE_NO_SYMLINKS}, 24) = 4`, ""},
		{"openat2 relative read annotated cwd", `1 openat2(AT_FDCWD</c/records>, "bad", {flags=O_RDONLY, resolve=RESOLVE_NO_MAGICLINKS}, 24) = 3`, ""},
		{"openat2 relative read traced cwd", `1 openat2(AT_FDCWD, "records/bad", {flags=O_RDONLY, resolve=0}, 24) = 3`, ""},
		{"open zero create mode", `1 open("/s/local/tmp/stage", O_WRONLY|O_CREAT, 0) = 3`, ""},
		{"openat zero create mode", `1 openat(AT_FDCWD, "/s/local/tmp/stage", O_WRONLY|O_CREAT, 0) = 3`, ""},
		{"openat2 zero create mode", `1 openat2(AT_FDCWD, "/s/local/tmp/stage", {flags=O_WRONLY|O_CREAT, mode=0, resolve=0}, 24) = 3`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := checkArchivalTrace(tc.trace, p)
			if tc.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantError)
			}
		})
	}
}
