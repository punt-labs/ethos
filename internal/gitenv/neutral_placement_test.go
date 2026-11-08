package gitenv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func neutralConfigFixture(t *testing.T) (string, string) {
	t.Helper()
	git, err := exec.LookPath("git")
	require.NoError(t, err, "actual Git is required")
	base := t.TempDir()
	for name, content := range map[string]string{
		"system":        "[user]\n name = system\n signingkey = system-only\n",
		"included":      "[user]\n name = included\n email = include-only@example.test\n",
		"global":        "[user]\n name = global\n[include]\n path = included\n[includeIf \"gitdir:**\"]\n path = must-not-read\n",
		"must-not-read": "invalid config\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(base, name), []byte(content), 0o600))
	}
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(base, "system"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(base, "global"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CEILING_DIRECTORIES", "")
	return git, base
}

func TestNeutralConfigPrecedence(t *testing.T) {
	git, _ := neutralConfigFixture(t)
	for _, tc := range []struct{ key, want string }{
		{"user.name", "included"},
		{"user.email", "include-only@example.test"},
		{"user.signingkey", "system-only"},
	} {
		out, absent, err := ReadNeutralConfig(git, AbsentUnset, tc.key)
		require.NoError(t, err)
		assert.False(t, absent)
		assert.Equal(t, tc.want, strings.TrimSpace(string(out)))
	}
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.name")
	t.Setenv("GIT_CONFIG_VALUE_0", "command")
	out, absent, err := ReadNeutralConfig(git, AbsentUnset, "user.name")
	require.NoError(t, err)
	assert.False(t, absent)
	assert.Equal(t, "command\n", string(out))
	t.Setenv("GIT_CONFIG_PARAMETERS", "'user.name=parameters'")
	out, _, err = ReadNeutralConfig(git, AbsentUnset, "user.name")
	require.NoError(t, err)
	assert.Equal(t, "parameters\n", string(out))
}

func TestNeutralConfigWithoutTemporary(t *testing.T) {
	git, base := neutralConfigFixture(t)
	for _, marker := range []string{"repository", "broken gitfile", "dangling symlink"} {
		t.Run(marker, func(t *testing.T) {
			cwd := filepath.Join(base, strings.ReplaceAll(marker, " ", "-"))
			require.NoError(t, os.Mkdir(cwd, 0o700))
			switch marker {
			case "repository":
				cmd := exec.Command(git, "init", "-q", cwd)
				cmd.Env = append(Parsed(), "GIT_CONFIG_GLOBAL="+os.DevNull)
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", out)
				require.NoError(t, os.WriteFile(filepath.Join(cwd, ".git", "config"), []byte("[user]\n name = repository\n"), 0o600))
			case "broken gitfile":
				require.NoError(t, os.WriteFile(filepath.Join(cwd, ".git"), []byte("gitdir: missing\n"), 0o600))
			case "dangling symlink":
				require.NoError(t, os.Symlink(filepath.Join(cwd, "missing"), filepath.Join(cwd, ".git")))
			}
			t.Chdir(cwd)
			record := filepath.Join(cwd, ".punt-labs", "ethos")
			require.NoError(t, os.MkdirAll(record, 0o700))
			// A file, not a writable temp directory: success establishes that
			// config reads need no staging or adjacent fallback at all.
			obstruction := filepath.Join(record, "not-a-directory")
			require.NoError(t, os.WriteFile(obstruction, []byte("preserve\n"), 0o600))
			t.Setenv("TMPDIR", obstruction)
			t.Setenv("TMP", obstruction)
			t.Setenv("TEMP", obstruction)
			t.Setenv("GIT_DIR", filepath.Join(cwd, "missing"))
			out, absent, err := ReadNeutralConfig(git, AbsentUnset, "user.name")
			require.NoError(t, err)
			assert.False(t, absent)
			assert.Equal(t, "included\n", string(out))
			entries, err := os.ReadDir(record)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			assert.Equal(t, "not-a-directory", entries[0].Name())
		})
	}
}

func TestNeutralConfigErrors(t *testing.T) {
	git, base := neutralConfigFixture(t)
	t.Run("unset", func(t *testing.T) {
		out, absent, err := ReadNeutralConfig(git, AbsentUnset, "user.missing")
		require.NoError(t, err)
		assert.True(t, absent)
		assert.Empty(t, out)
	})
	t.Run("invalid global", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(base, "must-not-read"))
		_, absent, err := ReadNeutralConfig(git, AbsentUnset, "user.missing")
		require.Error(t, err)
		assert.False(t, absent)
		var exit *exec.ExitError
		assert.ErrorAs(t, err, &exit)
	})
	t.Run("invalid command count", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_COUNT", "not-a-count")
		_, absent, err := ReadNeutralConfig(git, AbsentUnset, "user.name")
		require.Error(t, err)
		assert.False(t, absent)
	})
	t.Run("file scope excludes user config", func(t *testing.T) {
		out, absent, err := ReadNeutralConfig(git, 0, "--file", filepath.Join(base, "system"), "user.name")
		require.NoError(t, err)
		assert.False(t, absent)
		assert.Equal(t, "system\n", string(out))
	})
}

func TestNeutralConfigConcurrentReads(t *testing.T) {
	git, _ := neutralConfigFixture(t)
	// No process-wide environment mutation while readers run. All invoke the
	// actual Git independently; there is no new lock or mutable context.
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			out, absent, err := ReadNeutralConfig(git, AbsentUnset, "user.name")
			assert.NoError(t, err)
			assert.False(t, absent)
			assert.Equal(t, "included\n", string(out))
		})
	}
	workers.Wait()
}

func TestNeutralConfigPhysicalTrace(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("physical syscall observation requires Linux strace")
	}
	git, base := neutralConfigFixture(t)
	strace, err := exec.LookPath("strace")
	require.NoError(t, err, "physical placement evidence requires strace")
	require.NoError(t, os.Symlink(filepath.Join(base, "missing"), filepath.Join(base, ".git")))
	record := filepath.Join(base, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(record, 0o700))
	fixture := newNeutralCoverageFixture(t, git, base)
	calls, out, runErr := fixture.capture(t, strace, record)
	require.NoError(t, runErr, "%s", out)
	assert.NoError(t, validateNeutralConfigPhases(string(calls), fixture))
	assert.Contains(t, string(calls), filepath.Join(base, "included"), "Git actually read the included config")
	if fixture.coverageDir != "" {
		fixture.checkCoverageArtifacts(t, out)
	}
}

const (
	neutralTracePath  = `"(?:[^"\\]|\\.)*"`
	neutralTraceDirFD = `(?:AT_FDCWD|-?[0-9]+)(?:<[^>]*>)?`
	neutralTraceFlags = `(O_[A-Z0-9_]+(?:\|O_[A-Z0-9_]+)*)`
)

var (
	neutralTracePID     = regexp.MustCompile(`^([1-9][0-9]*)[ \t]+(.+)$`)
	neutralTraceStart   = regexp.MustCompile(`^([a-z][a-z0-9_]*)\(`)
	neutralTraceResume  = regexp.MustCompile(`^<\.\.\. ([a-z][a-z0-9_]*) resumed>(.*)$`)
	neutralTraceCall    = regexp.MustCompile(`^([a-z][a-z0-9_]*)\((.*)\)[ \t]+= (?:[0-9]+(?:<.+>)?|-1 [A-Z][A-Z0-9_]+ \([^()]*\))$`)
	neutralTraceNotice  = regexp.MustCompile(`^(?:\+\+\+ exited with [0-9]+ \+\+\+|\+\+\+ killed by SIG[A-Z0-9]+(?: \(core dumped\))? \+\+\+|--- SIG[A-Z0-9]+ \{.*\} ---)$`)
	neutralTraceOpen    = regexp.MustCompile(`^` + neutralTracePath + `, ` + neutralTraceFlags + `(?:, 0[0-7]+)?$`)
	neutralTraceOpenat  = regexp.MustCompile(`^` + neutralTraceDirFD + `, ` + neutralTracePath + `, ` + neutralTraceFlags + `(?:, 0[0-7]+)?$`)
	neutralTraceOpenat2 = regexp.MustCompile(`^` + neutralTraceDirFD + `, ` + neutralTracePath + `, \{flags=` + neutralTraceFlags + `(?:, mode=0[0-7]+)?(?:, resolve=[A-Z0-9_|]+)?\}, [0-9]+$`)
	neutralTraceNull    = regexp.MustCompile(`^openat\(AT_FDCWD<[^>]*>, "/dev/null", O_RDWR\)[ \t]+= [0-9]+</dev/null<char 1:3>>$`)
)

// neutralConfigTraceCalls accepts the PID-prefixed framing produced by this
// fixture's strace -f -yy -s 0 -e trace=%file invocation. It joins each unfinished
// call only to the same PID's same-named resume, including splits within arguments.
// It checks record framing, not the semantics of every syscall argument. Unknown
// or incomplete framing refuses; signal/exit notices are the only non-call records.
func neutralConfigTraceCalls(trace string) ([]string, error) {
	type unfinishedCall struct{ name, prefix string }
	pending := make(map[string]unfinishedCall)
	var calls []string
	// Only a terminal newline is optional; an interior blank is not a record.
	for i, line := range strings.Split(strings.TrimSuffix(trace, "\n"), "\n") {
		parts := neutralTracePID.FindStringSubmatch(line)
		if parts == nil {
			return nil, fmt.Errorf("trace line %d: unknown record shape: %q", i+1, line)
		}
		pid, body := parts[1], parts[2]
		if neutralTraceNotice.MatchString(body) {
			if strings.HasPrefix(body, "+++") && pending[pid].name != "" {
				return nil, fmt.Errorf("trace line %d: PID %s exited with an incomplete syscall", i+1, pid)
			}
			continue
		}
		if resumed := neutralTraceResume.FindStringSubmatch(body); resumed != nil {
			start, ok := pending[pid]
			if !ok {
				return nil, fmt.Errorf("trace line %d: unmatched resume for PID %s", i+1, pid)
			}
			if start.name != resumed[1] {
				return nil, fmt.Errorf("trace line %d: PID %s resumed %s, pending %s", i+1, pid, resumed[1], start.name)
			}
			body = start.prefix + resumed[2]
			delete(pending, pid)
		} else {
			if pending[pid].name != "" {
				return nil, fmt.Errorf("trace line %d: PID %s has an incomplete syscall before another call", i+1, pid)
			}
			if strings.HasSuffix(body, " <unfinished ...>") {
				start := neutralTraceStart.FindStringSubmatch(body)
				if start == nil {
					return nil, fmt.Errorf("trace line %d: malformed unfinished syscall: %q", i+1, body)
				}
				pending[pid] = unfinishedCall{start[1], strings.TrimSuffix(body, " <unfinished ...>")}
				continue
			}
		}
		if strings.Contains(body, "<unfinished ...>") || strings.Contains(body, "resumed>") || !neutralTraceCall.MatchString(body) {
			return nil, fmt.Errorf("trace line %d: malformed completed syscall: %q", i+1, body)
		}
		calls = append(calls, body)
	}
	if len(pending) != 0 {
		return nil, fmt.Errorf("incomplete trace: %d unfinished syscall(s)", len(pending))
	}
	if len(calls) == 0 {
		return nil, fmt.Errorf("trace contains no completed syscalls")
	}
	return calls, nil
}

// validateNeutralConfigTrace is a bounded observer for this read-only config
// fixture, not a universal syscall nonmutation proof. Reassemble before any
// classification, and refuse unknown syscalls/shapes rather than losing evidence.
func validateNeutralConfigTrace(trace string) error {
	calls, err := neutralConfigTraceCalls(trace)
	if err != nil {
		return err
	}
	for _, call := range calls {
		parts := neutralTraceCall.FindStringSubmatch(call)
		var openShape *regexp.Regexp
		switch parts[1] {
		case "mkdir", "mkdirat", "creat", "unlink", "unlinkat", "rename", "renameat", "renameat2",
			"link", "linkat", "symlink", "symlinkat", "truncate", "chmod", "fchmodat", "fchmodat2",
			"chown", "lchown", "fchownat", "utime", "utimes", "futimesat", "utimensat", "mknod", "mknodat":
			// Attempts count, even when the return value reports failure.
			return fmt.Errorf("filesystem mutation attempt: %s", call)
		case "open":
			openShape = neutralTraceOpen
		case "openat":
			openShape = neutralTraceOpenat
		case "openat2":
			openShape = neutralTraceOpenat2
		case "execve", "execveat", "access", "faccessat", "faccessat2", "stat", "lstat", "newfstatat", "statx",
			"readlink", "readlinkat", "getcwd", "chdir":
			continue
		default:
			return fmt.Errorf("unsupported syscall in neutral config trace: %s", call)
		}
		// Git's setup opens the exact null character device read/write. No path
		// substring, failed open, other device, or create/truncate flag qualifies.
		if neutralTraceNull.MatchString(call) {
			continue
		}
		open := openShape.FindStringSubmatch(parts[2])
		if open == nil {
			return fmt.Errorf("unsupported open shape: %s", call)
		}
		flags := strings.Split(open[1], "|")
		for _, flag := range flags {
			switch flag {
			case "O_WRONLY", "O_RDWR", "O_CREAT", "O_TRUNC", "O_TMPFILE", "O_APPEND":
				return fmt.Errorf("write-capable open attempt: %s", call)
			case "O_RDONLY", "O_PATH", "O_CLOEXEC", "O_DIRECTORY", "O_NOFOLLOW", "O_NONBLOCK", "O_LARGEFILE":
			default:
				return fmt.Errorf("unsupported open flag %s: %s", flag, call)
			}
		}
		if flags[0] != "O_RDONLY" && flags[0] != "O_PATH" {
			return fmt.Errorf("unsupported open access mode: %s", call)
		}
	}
	return nil
}

func TestNeutralConfigTraceValidation(t *testing.T) {
	for _, tc := range []struct {
		name, trace, wantError string
	}{
		{"permitted complete null", `123 openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR) = 3</dev/null<char 1:3>>`, ""},
		{"permitted split null", "123 openat(AT_FDCWD</fixture>, \"/dev/null\", O_RDWR <unfinished ...>\n123 <... openat resumed>) = 3</dev/null<char 1:3>>", ""},
		{"forbidden split write", "123 openat(AT_FDCWD</fixture>, \"/fixture/file\", O_WRONLY <unfinished ...>\n123 <... openat resumed>) = 3</fixture/file>", "write-capable open attempt"},
		{"forbidden split create", "123 openat(AT_FDCWD</fixture>, \"/fixture/file\", O_RDONLY|O_CREAT, 0600 <unfinished ...>\n123 <... openat resumed>) = 3</fixture/file>", "write-capable open attempt"},
		{"permitted split within argument", "123 openat(AT_FDCWD</fixture>, \"/dev/null\", O_ <unfinished ...>\n123 <... openat resumed>RDWR) = 3</dev/null<char 1:3>>", ""},
		{"permitted split result", "123 openat(AT_FDCWD</fixture>, \"/dev/null\", O_RDWR) <unfinished ...>\n123 <... openat resumed> = 3</dev/null<char 1:3>>", ""},
		{"permitted padded result", `123 openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR)    = 3</dev/null<char 1:3>>`, ""},
		{"permitted regular read", `123 openat(AT_FDCWD</fixture>, "/fixture/included", O_RDONLY|O_CLOEXEC) = 3</fixture/included>`, ""},
		{"permitted failed read", `123 openat(AT_FDCWD</fixture>, "/missing", O_RDONLY) = -1 ENOENT (No such file or directory)`, ""},
		{"permitted open read", `123 open("/fixture/included", O_RDONLY) = 3</fixture/included>`, ""},
		{"permitted openat2 read", `123 openat2(AT_FDCWD</fixture>, "/fixture/included", {flags=O_RDONLY|O_CLOEXEC, resolve=RESOLVE_BENEATH}, 24) = 3</fixture/included>`, ""},
		{"permitted escaped path", `123 openat(AT_FDCWD</fixture>, "/fixture/a\"b", O_RDONLY) = 3</fixture/a\"b>`, ""},
		{"permitted flag text in read path", `123 openat(AT_FDCWD</fixture>, "/fixture/O_RDWR", O_RDONLY) = 3</fixture/O_RDWR>`, ""},
		{"forbidden regular rdwr", `123 openat(AT_FDCWD</fixture>, "/fixture/file", O_RDWR) = 3</fixture/file>`, "write-capable open attempt"},
		{"forbidden split regular rdwr", "123 openat(AT_FDCWD</fixture>, \"/fixture/file\", O_ <unfinished ...>\n123 <... openat resumed>RDWR) = 3</fixture/file>", "write-capable open attempt"},
		{"forbidden split truncate", "123 openat(AT_FDCWD</fixture>, \"/fixture/file\", O_RDONLY|O_ <unfinished ...>\n123 <... openat resumed>TRUNC) = 3</fixture/file>", "write-capable open attempt"},
		{"forbidden split open write", "123 open(\"/fixture/file\", O_ <unfinished ...>\n123 <... open resumed>WRONLY) = 3</fixture/file>", "write-capable open attempt"},
		{"forbidden split openat2 create", "123 openat2(AT_FDCWD</fixture>, \"/fixture/file\", {flags=O_RDONLY|O_ <unfinished ...>\n123 <... openat2 resumed>CREAT, mode=0600}, 24) = 3</fixture/file>", "write-capable open attempt"},
		{"forbidden failed write", "123 openat(AT_FDCWD</fixture>, \"/fixture/file\", O_WRONLY <unfinished ...>\n123 <... openat resumed>) = -1 EACCES (Permission denied)", "write-capable open attempt"},
		{"forbidden failed create", "123 openat(AT_FDCWD</fixture>, \"/fixture/file\", O_RDONLY|O_ <unfinished ...>\n123 <... openat resumed>CREAT, 0600) = -1 EACCES (Permission denied)", "write-capable open attempt"},
		{"forbidden null create", `123 openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR|O_CREAT, 0600) = 3</dev/null<char 1:3>>`, "write-capable open attempt"},
		{"forbidden null truncate", "123 openat(AT_FDCWD</fixture>, \"/dev/null\", O_RDWR|O_ <unfinished ...>\n123 <... openat resumed>TRUNC) = 3</dev/null<char 1:3>>", "write-capable open attempt"},
		{"forbidden null tmpfile", `123 openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR|O_TMPFILE, 0600) = 3</dev/null<char 1:3>>`, "write-capable open attempt"},
		{"forbidden null append", `123 openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR|O_APPEND) = 3</dev/null<char 1:3>>`, "write-capable open attempt"},
		{"forbidden null failed", `123 openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR) = -1 EACCES (Permission denied)`, "write-capable open attempt"},
		{"forbidden split null failed", "123 openat(AT_FDCWD</fixture>, \"/dev/null\", O_RDWR <unfinished ...>\n123 <... openat resumed>) = -1 EACCES (Permission denied)", "write-capable open attempt"},
		{"forbidden null regular file", `123 openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR) = 3</dev/null>`, "write-capable open attempt"},
		{"forbidden null wrong character device", "123 openat(AT_FDCWD</fixture>, \"/dev/null\", O_RDWR <unfinished ...>\n123 <... openat resumed>) = 3</dev/null<char 1:5>>", "write-capable open attempt"},
		{"forbidden null block device", `123 openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR) = 3</dev/null<block 1:3>>`, "write-capable open attempt"},
		{"forbidden null missing device evidence", `123 openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR) = 3`, "write-capable open attempt"},
		{"forbidden null substring", `123 openat(AT_FDCWD</fixture>, "/fixture/dev/null", O_RDWR) = 3</fixture/dev/null<char 1:3>>`, "write-capable open attempt"},
		{"forbidden null different returned path", `123 openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR) = 3</fixture/file<char 1:3>>`, "write-capable open attempt"},
		{"forbidden null different syscall", `123 open("/dev/null", O_RDWR) = 3</dev/null<char 1:3>>`, "write-capable open attempt"},
		{"forbidden null extra flag", `123 openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR|O_CLOEXEC) = 3</dev/null<char 1:3>>`, "write-capable open attempt"},
		{"unknown open flag", `123 openat(AT_FDCWD</fixture>, "/fixture/file", O_RDONLY|O_UNKNOWN) = 3</fixture/file>`, "unsupported open flag"},
		{"unknown open mode", `123 openat(AT_FDCWD</fixture>, "/fixture/file", O_CLOEXEC) = 3</fixture/file>`, "unsupported open access mode"},
		{"unknown open shape", `123 openat(unknown) = 3`, "unsupported open shape"},
		{"unknown openat2 shape", `123 openat2(AT_FDCWD</fixture>, "/fixture/file", 0x1, 24) = 3`, "unsupported open shape"},
		{"unknown syscall", `123 future_syscall("/fixture/file") = 0`, "unsupported syscall"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNeutralConfigTrace(tc.trace)
			if tc.wantError == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantError)
			}
		})
	}
}

func TestNeutralConfigTraceReassembly(t *testing.T) {
	// Same syscall names overlap across PIDs; another name and a signal overlap
	// too. Verify the joined bodies, not just the validator's accept/reject result.
	trace := `123 openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR <unfinished ...>
456 openat(AT_FDCWD</fixture>, "/fixture/included", O_RDONLY <unfinished ...>
789 newfstatat(AT_FDCWD</fixture>, "/fixture/global",  <unfinished ...>
123 --- SIGURG {si_signo=SIGURG, si_code=SI_TKILL, si_pid=123, si_uid=1000} ---
456 <... openat resumed>) = 4</fixture/included>
789 <... newfstatat resumed>{st_mode=S_IFREG|0600, st_size=42}, 0) = 0
123 <... openat resumed>) = 3</dev/null<char 1:3>>
789 +++ exited with 0 +++
123 access("/fixture/missing", F_OK) = -1 ENOENT (No such file or directory)
123 +++ exited with 1 +++
456 +++ killed by SIGTERM +++
`
	calls, err := neutralConfigTraceCalls(trace)
	require.NoError(t, err)
	assert.Equal(t, []string{
		`openat(AT_FDCWD</fixture>, "/fixture/included", O_RDONLY) = 4</fixture/included>`,
		`newfstatat(AT_FDCWD</fixture>, "/fixture/global", {st_mode=S_IFREG|0600, st_size=42}, 0) = 0`,
		`openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR) = 3</dev/null<char 1:3>>`,
		`access("/fixture/missing", F_OK) = -1 ENOENT (No such file or directory)`,
	}, calls)
	assert.NoError(t, validateNeutralConfigTrace(trace))

	// A permitted null open cannot hide another PID's failed write attempt.
	trace = `123 openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR <unfinished ...>
456 openat(AT_FDCWD</fixture>, "/fixture/file", O_WRONLY <unfinished ...>
123 <... openat resumed>) = 3</dev/null<char 1:3>>
456 <... openat resumed>) = -1 EACCES (Permission denied)`
	err = validateNeutralConfigTrace(trace)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write-capable open attempt")
	assert.Contains(t, err.Error(), `"/fixture/file"`)
}

func TestNeutralConfigTraceFraming(t *testing.T) {
	const start = `123 openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR <unfinished ...>`
	const resume = `123 <... openat resumed>) = 3</dev/null<char 1:3>>`
	const read = `456 access("/fixture/file", F_OK) = 0`
	for _, tc := range []struct{ name, trace, wantError string }{
		{"empty", "", "unknown record shape"},
		{"missing PID", `openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR) = 3</dev/null<char 1:3>>`, "unknown record shape"},
		{"bad PID", `thread openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR) = 3</dev/null<char 1:3>>`, "unknown record shape"},
		{"unexpected bracket PID format", `[pid 123] access("/fixture/file", F_OK) = 0`, "unknown record shape"},
		{"internal blank record", read + "\n\n" + read, "unknown record shape"},
		{"unknown record", "123 unrecognized observer output", "malformed completed syscall"},
		{"unmatched resume", resume, "unmatched resume"},
		{"wrong PID resume", start + "\n456 <... openat resumed>) = 3</dev/null<char 1:3>>", "unmatched resume"},
		{"misnamed resume", start + "\n123 <... open resumed>) = 3</dev/null<char 1:3>>", "resumed open, pending openat"},
		{"unfinished at EOF", read + "\n" + start, "incomplete trace"},
		{"duplicate unfinished", start + "\n" + start, "incomplete syscall before another call"},
		{"same PID complete before resume", start + "\n123 access(\"/fixture/file\", F_OK) = 0\n" + resume, "incomplete syscall before another call"},
		{"exit before resume", start + "\n123 +++ exited with 0 +++", "exited with an incomplete syscall"},
		{"killed before resume", start + "\n123 +++ killed by SIGTERM +++", "exited with an incomplete syscall"},
		{"duplicate resume", start + "\n" + resume + "\n" + resume, "unmatched resume"},
		{"malformed unfinished start", "123 nonsense <unfinished ...>\n" + resume, "malformed unfinished syscall"},
		{"malformed unfinished marker", `123 openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR <unfinished ...`, "malformed completed syscall"},
		{"trailing unfinished junk", start + " junk", "malformed completed syscall"},
		{"malformed resume marker", start + "\n123 <... openat resumed ) = 3", "incomplete syscall before another call"},
		{"malformed resumed result", start + "\n123 <... openat resumed>) = unknown", "malformed completed syscall"},
		{"missing result", `123 openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR)`, "malformed completed syscall"},
		{"missing close paren", `123 openat(AT_FDCWD</fixture>, "/dev/null", O_RDWR = 3`, "malformed completed syscall"},
		{"trailing result junk", read + " unexplained", "malformed completed syscall"},
		{"truncated record after valid call", read + "\n123 openat(", "malformed completed syscall"},
		{"malformed signal notice", "123 --- SIGURG ---", "malformed completed syscall"},
		{"notices without calls", "123 +++ exited with 0 +++", "no completed syscalls"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNeutralConfigTrace(tc.trace)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantError)
		})
	}
}

func TestNeutralConfigTraceMutations(t *testing.T) {
	for _, tc := range []struct{ name, args string }{
		{"mkdir", `"/fixture/new", 0700`},
		{"mkdirat", `AT_FDCWD</fixture>, "new", 0700`},
		{"creat", `"/fixture/new", 0600`},
		{"unlink", `"/fixture/file"`},
		{"unlinkat", `AT_FDCWD</fixture>, "file", 0`},
		{"rename", `"/fixture/a", "/fixture/b"`},
		{"renameat", `AT_FDCWD</fixture>, "a", AT_FDCWD</fixture>, "b"`},
		{"renameat2", `AT_FDCWD</fixture>, "a", AT_FDCWD</fixture>, "b", RENAME_NOREPLACE`},
		{"link", `"/fixture/a", "/fixture/b"`},
		{"linkat", `AT_FDCWD</fixture>, "a", AT_FDCWD</fixture>, "b", 0`},
		{"symlink", `"a", "/fixture/b"`},
		{"symlinkat", `"a", AT_FDCWD</fixture>, "b"`},
		{"truncate", `"/fixture/file", 0`},
		{"chmod", `"/fixture/file", 0600`},
		{"fchmodat", `AT_FDCWD</fixture>, "file", 0600`},
		{"fchmodat2", `AT_FDCWD</fixture>, "file", 0600, 0`},
		{"chown", `"/fixture/file", 1000, 1000`},
		{"lchown", `"/fixture/file", 1000, 1000`},
		{"fchownat", `AT_FDCWD</fixture>, "file", 1000, 1000, 0`},
		{"utime", `"/fixture/file", NULL`},
		{"utimes", `"/fixture/file", NULL`},
		{"futimesat", `AT_FDCWD</fixture>, "file", NULL`},
		{"utimensat", `AT_FDCWD</fixture>, "file", NULL, 0`},
		{"mknod", `"/fixture/new", S_IFIFO|0600`},
		{"mknodat", `AT_FDCWD</fixture>, "new", S_IFIFO|0600`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, result := range []string{"0", "-1 EACCES (Permission denied)"} {
				t.Run(result, func(t *testing.T) {
					for _, trace := range []string{
						fmt.Sprintf("123 %s(%s) = %s", tc.name, tc.args, result),
						fmt.Sprintf("123 %s(%s <unfinished ...>\n456 access(\"/fixture/file\", F_OK) = 0\n123 <... %s resumed>) = %s", tc.name, tc.args, tc.name, result),
					} {
						err := validateNeutralConfigTrace(trace)
						require.Error(t, err)
						assert.Contains(t, err.Error(), "filesystem mutation attempt")
						assert.Contains(t, err.Error(), tc.name+"(")
					}
				})
			}
		})
	}
}

func TestNeutralConfigPhysicalTraceChild(t *testing.T) {
	git := os.Getenv("ETHOS_TEST_NEUTRAL_CONFIG_GIT")
	if git == "" {
		return // Only the traced subprocess executes this test's body.
	}
	// TestMain deliberately strips inherited Git configuration, even in this
	// subprocess. Restore only these fixture paths, not an ambient user config.
	base := os.Getenv("ETHOS_TEST_NEUTRAL_CONFIG_BASE")
	require.NotEmpty(t, base)
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		require.Equal(t, filepath.Join(base, ".punt-labs", "ethos"), os.Getenv(name))
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(base, "global"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(base, "system"))
	out, absent, err := ReadNeutralConfig(git, AbsentUnset, "user.name")
	require.NoError(t, err)
	require.False(t, absent)
	require.Equal(t, "included\n", string(out))
	out, absent, err = ReadNeutralConfig(git, AbsentUnset, "user.missing")
	require.NoError(t, err)
	require.True(t, absent)
	require.Empty(t, out)
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		require.Equal(t, filepath.Join(base, ".punt-labs", "ethos"), os.Getenv(name))
	}
	// Explicitly after both synchronous calls and fatal assertions, never a
	// defer: a failed production phase must not authorize coverage emission.
	marker := os.Getenv("ETHOS_TEST_NEUTRAL_CONFIG_MARKER")
	require.NotEmpty(t, marker)
	info, err := os.Lstat(marker)
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular())
}
