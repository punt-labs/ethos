// Package gitenv runs the git commands whose output or messages ethos parses:
// in a fixed environment, and bounded by one timeout.
package gitenv

import (
	"context"
	"errors"
	"fmt"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Timeout bounds every git command ethos runs. git blocks forever on some
// layouts (it reads a FIFO commondir); a command that has not answered by
// then is an error, never a hang.
const Timeout = 10 * time.Second

// deadline is the bound Output applies: Timeout, unless a test shortened it.
var deadline atomic.Int64

func init() { deadline.Store(int64(Timeout)) }

// SetTimeout sets the bound Output applies and returns the previous one, for
// a test that must not wait Timeout for a git that hangs. Use it through
// testhelpers.GitTimeout, which restores the previous bound.
func SetTimeout(d time.Duration) time.Duration {
	return time.Duration(deadline.Swap(int64(d)))
}

// ErrTimeout is wrapped by the error Output, OutputEnv and Run return when
// git did not answer within its bound.
var ErrTimeout = errors.New("git did not answer")

// ErrInterrupted is wrapped by the error Run returns for a Network git stopped
// because ethos got SIGINT or SIGTERM.
var ErrInterrupted = errors.New("git interrupted")

// Output runs git with args — as `git -C dir args...` when dir is not "" —
// in the Parsed environment, and returns its stdout. A git that exits non-zero
// returns its *exec.ExitError (stderr captured). A git still running after
// Timeout (or the bound SetTimeout set) is killed and reported as ErrTimeout
// naming git, the arguments and the directory.
func Output(dir, git string, args ...string) ([]byte, error) {
	full := args
	if dir != "" {
		if err := DanglingDotGit(dir); err != nil {
			return nil, err
		}
		full = append([]string{"-C", dir}, args...)
	}
	out, err := Run(Spec{Git: git, Args: full, Env: Parsed()})
	if errors.Is(err, ErrTimeout) {
		return nil, timeoutError(git, args, dir, time.Duration(deadline.Load()))
	}
	return out, err
}

// OutputEnv is Output for a caller that needs its own environment: git runs
// in dir (cmd.Dir; the working directory when "") with env, or the inherited
// environment when env is nil. It is bounded and reports a timeout as Output
// does.
func OutputEnv(dir, git string, env []string, args ...string) ([]byte, error) {
	return Run(Spec{Git: git, Dir: dir, Env: env, Args: args})
}

// Spec describes one bounded git command for Run.
type Spec struct {
	Git    string        // the git binary
	Dir    string        // cmd.Dir; "" is the working directory
	Env    []string      // nil inherits the process environment
	Args   []string      // arguments after git
	Stdin  io.Reader     // nil for none
	Stdout io.Writer     // nil: captured and returned
	Stderr io.Writer     // nil: captured on the *exec.ExitError
	Limit  time.Duration // 0: the shared deadline (Timeout, or SetTimeout's)

	// Network marks git that talks to a remote or may ask the user something
	// (clone, submodule add). It cannot prompt: GIT_TERMINAL_PROMPT=0, no
	// askpass program, credential.interactive=never, and ssh in BatchMode unless
	// the user chose an ssh (GIT_SSH_COMMAND, GIT_SSH or core.sshCommand), whose
	// choice is kept. A credential prompt fails at once instead of waiting on a
	// terminal nobody is watching. It runs in its own process group like every
	// git, so a timeout reaches its descendants (ssh, helpers), and ethos
	// forwards SIGINT and SIGTERM to that group while it runs, so a Ctrl-C does
	// not leave a clone running.
	Network bool
}

// networkArgs are prepended to a Network git's arguments.
var networkArgs = []string{"-c", "core.askPass=", "-c", "credential.interactive=never"}

// networkEnv returns what Network adds to git's environment for the git run by
// s, given the environment it will have: no prompting of any kind, and an ssh in
// BatchMode only when the user has not chosen one.
func networkEnv(s Spec, env []string) []string {
	add := []string{"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=", "SSH_ASKPASS_REQUIRE=never"}
	if !sshChosen(s, env) {
		add = append(add, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	return add
}

// sshChosen reports whether GIT_SSH_COMMAND, GIT_SSH or core.sshCommand is set
// for the git run by s. A config that cannot be read counts as chosen: the
// user's ssh is never overridden on a guess.
func sshChosen(s Spec, env []string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_SSH_COMMAND=") || strings.HasPrefix(kv, "GIT_SSH=") {
			return true
		}
	}
	probe := Spec{Git: s.Git, Dir: s.Dir, Env: env, Args: []string{"config", "core.sshCommand"}}
	if s.Dir == "" {
		dir, nenv, cleanup, err := Neutral()
		if err != nil {
			return true
		}
		defer cleanup()
		probe.Dir, probe.Env = dir, nenv
	}
	_, absent, err := Read(probe, AbsentUnset)
	return err != nil || !absent
}

// groupGrace is how long the rest of a TERMed process group has to finish
// before it is killed.
const groupGrace = time.Second

// Run executes s under its bound. It returns stdout when s.Stdout is nil. A
// git that exits non-zero returns its *exec.ExitError; one still running at
// the bound is killed and reported as ErrTimeout naming git, the arguments,
// the directory and the bound.
func Run(s Spec) ([]byte, error) {
	if s.Dir != "" {
		if err := DanglingDotGit(s.Dir); err != nil {
			return nil, err
		}
	}
	limit := s.Limit
	if limit == 0 {
		limit = time.Duration(deadline.Load())
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	// A Network git is stopped, with its group, when ethos is interrupted:
	// ethos catches SIGINT and SIGTERM while it runs, cancels the command, and
	// returns ErrInterrupted. It runs in its own process group, which the
	// terminal's Ctrl-C does not reach, so nothing else would.
	var interrupted atomic.Bool
	if s.Network {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		done := make(chan struct{})
		defer func() { signal.Stop(sig); close(done) }()
		go func() {
			select {
			case <-sig:
				interrupted.Store(true)
				cancel()
			case <-done:
			}
		}()
	}
	cmd := exec.CommandContext(ctx, s.Git, s.Args...)
	cmd.Dir = s.Dir
	cmd.Env = stripTrace(s.Env)
	args := s.Args
	if s.Network {
		base := cmd.Env
		if base == nil {
			base = os.Environ()
		}
		cmd.Env = append(append([]string(nil), base...), networkEnv(s, base)...)
		args = append(append([]string(nil), networkArgs...), s.Args...)
	}
	cmd.Args = append([]string{s.Git}, args...)
	cmd.Stdin = s.Stdin
	// On the deadline git and its process group get SIGTERM, so git can remove
	// its index.lock; after the WaitDelay grace Go kills git, and killGroup
	// what is left of the group (a filter that ignored SIGTERM). A child
	// holding stdout does not stall the kill either. On Windows there are no
	// groups and no SIGTERM: the kill is immediate, and only git dies.
	group := true
	inOwnGroup(cmd)
	cmd.Cancel = func() error { return terminate(cmd, group) }
	cmd.WaitDelay = time.Second
	var out []byte
	var err error
	if s.Stdout == nil {
		cmd.Stderr = s.Stderr
		out, err = cmd.Output()
	} else {
		cmd.Stdout, cmd.Stderr = s.Stdout, s.Stderr
		err = cmd.Run()
	}
	if ctx.Err() == context.DeadlineExceeded {
		if group {
			endGroup(cmd, groupGrace)
		}
		return nil, timeoutError(s.Git, s.Args, s.Dir, limit)
	}
	if interrupted.Load() {
		endGroup(cmd, groupGrace)
		return nil, fmt.Errorf("%w: %s %s", ErrInterrupted, s.Git, strings.Join(s.Args, " "))
	}
	return out, err
}

// timeoutError names the command and where it ran; an empty dir is reported
// as the working directory.
func timeoutError(git string, args []string, dir string, limit time.Duration) error {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	return fmt.Errorf("%w: %s %s in %s did not answer within %v; refusing rather than guessing",
		ErrTimeout, git, strings.Join(args, " "), dir, limit)
}

// LocalEnvVars are the repository-local variables Parsed removes: the ones
// `git rev-parse --local-env-vars` lists (a test compares them), plus
// GIT_NAMESPACE. Left set, they make git answer for the repository they name
// instead of discovering one from the directory it is given. A pre-commit
// hook runs with GIT_DIR and GIT_INDEX_FILE exported; from a subdirectory,
// git would then call that subdirectory the top level.
var LocalEnvVars = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_CONFIG",
	"GIT_CONFIG_PARAMETERS",
	"GIT_CONFIG_COUNT",
	"GIT_OBJECT_DIRECTORY",
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_IMPLICIT_WORK_TREE",
	"GIT_GRAFT_FILE",
	"GIT_INDEX_FILE",
	"GIT_NO_REPLACE_OBJECTS",
	"GIT_REPLACE_REF_BASE",
	"GIT_PREFIX",
	"GIT_SHALLOW_FILE",
	"GIT_COMMON_DIR",
	"GIT_NAMESPACE",
}

// dropped is LocalEnvVars plus the locale, which Parsed sets itself.
var dropped = func() map[string]bool {
	m := map[string]bool{"LC_ALL": true, "LANGUAGE": true}
	for _, v := range LocalEnvVars {
		m[v] = true
	}
	return m
}()

// isTrace reports whether name makes git write diagnostics to stderr:
// GIT_TRACE, GIT_TRACE_*, GIT_TRACE2, GIT_TRACE2_* and GIT_CURL_VERBOSE. A
// read classifies git's stderr, so trace noise would turn "key unset" (exit 1,
// empty stderr) into a failure.
func isTrace(name string) bool {
	return name == "GIT_CURL_VERBOSE" || strings.HasPrefix(name, "GIT_TRACE")
}

// NotARepoMessage is what git prints when it walked up and found no
// repository at all. It is NOT in the message for a .git git found and could
// not use (a gitfile naming a missing directory, a worktree whose main
// checkout is gone): that one says "not a git repository: <path>" and is a
// failure, not "outside a repository". One matcher, used by Read, the
// resolver and enable.
const NotARepoMessage = "not a git repository (or any"

// IsNotARepo reports whether git's stderr says it found no repository.
func IsNotARepo(stderr string) bool {
	return strings.Contains(stderr, NotARepoMessage)
}

// Parsed returns the current environment with git's messages in the C locale
// (LC_ALL=C, LANGUAGE empty) and none of LocalEnvVars. ethos matches git's
// English messages ("not a git repository", "dubious ownership"); a
// translated message would match nothing.
func Parsed() []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !dropped[name] && !isTrace(name) {
			env = append(env, kv)
		}
	}
	return append(env, "LC_ALL=C", "LANGUAGE=")
}

// Inherited returns the current environment unchanged except that git's
// messages are in the C locale. A caller that needs its own GIT_DIR or
// GIT_INDEX_FILE (the seal, inside a pre-commit hook) and matches git's
// English messages uses it.
func Inherited() []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name != "LC_ALL" && name != "LANGUAGE" {
			env = append(env, kv)
		}
	}
	return append(stripTrace(env), "LC_ALL=C", "LANGUAGE=")
}

// stripTrace returns env without anything that makes git write trace output to
// stderr: the GIT_TRACE* variables and any trace2.* setting passed through
// GIT_CONFIG_PARAMETERS or GIT_CONFIG_COUNT/KEY_n/VALUE_n. A nil env is the
// process environment. Parsed and Inherited are already stripped; Run applies
// it to every command, so a Spec with no Env gets the same treatment.
func stripTrace(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env))
	var count = -1
	keys := map[int]string{}
	for _, kv := range env {
		name, val, _ := strings.Cut(kv, "=")
		switch {
		case isTrace(name):
			continue
		case name == "GIT_CONFIG_PARAMETERS":
			if v := stripTraceParameters(val); v != "" {
				out = append(out, name+"="+v)
			}
			continue
		case name == "GIT_CONFIG_COUNT":
			if n, err := strconv.Atoi(val); err == nil && n >= 0 {
				count = n
				continue
			}
		case strings.HasPrefix(name, "GIT_CONFIG_KEY_"):
			if i, err := strconv.Atoi(strings.TrimPrefix(name, "GIT_CONFIG_KEY_")); err == nil {
				keys[i] = val
				continue
			}
		case strings.HasPrefix(name, "GIT_CONFIG_VALUE_"):
			if _, err := strconv.Atoi(strings.TrimPrefix(name, "GIT_CONFIG_VALUE_")); err == nil {
				continue
			}
		}
		out = append(out, kv)
	}
	if count < 0 {
		return out
	}
	// Renumber the pairs that survive; the values are read again from env.
	values := map[int]string{}
	for _, kv := range env {
		name, val, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_CONFIG_VALUE_") {
			if i, err := strconv.Atoi(strings.TrimPrefix(name, "GIT_CONFIG_VALUE_")); err == nil {
				values[i] = val
			}
		}
	}
	n := 0
	for i := 0; i < count; i++ {
		k, ok := keys[i]
		if !ok || isTrace2Key(k) {
			continue
		}
		out = append(out, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", n, k), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", n, values[i]))
		n++
	}
	if n > 0 {
		out = append(out, "GIT_CONFIG_COUNT="+strconv.Itoa(n))
	}
	return out
}

func isTrace2Key(key string) bool {
	return strings.HasPrefix(strings.ToLower(key), "trace2.")
}

// stripTraceParameters removes the trace2.* entries from a GIT_CONFIG_PARAMETERS
// value: space-separated entries, each 'key=value' or 'key'='value' in git's
// shell quoting ('\” is a literal quote).
func stripTraceParameters(v string) string {
	var kept []string
	var raw, plain strings.Builder
	inQuote := false
	flush := func() {
		if raw.Len() == 0 {
			return
		}
		key, _, _ := strings.Cut(plain.String(), "=")
		if !isTrace2Key(key) {
			kept = append(kept, raw.String())
		}
		raw.Reset()
		plain.Reset()
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case !inQuote && c == ' ':
			flush()
			continue
		case !inQuote && c == '\\' && i+1 < len(v):
			i++
			raw.WriteByte(c)
			raw.WriteByte(v[i])
			plain.WriteByte(v[i])
			continue
		case c == '\'':
			inQuote = !inQuote
			raw.WriteByte(c)
			continue
		}
		raw.WriteByte(c)
		plain.WriteByte(c)
	}
	flush()
	return strings.Join(kept, " ")
}

// Absent says which failed exits of a read mean "there is nothing there".
// Every other failure is an error: a git failure must never read as an absent
// value (an empty repo, a missing user, "not a gitlink").
type Absent int

const (
	// AbsentUnset: exit 1 — `git config` for an unset key. Whatever git wrote to
	// stderr is not part of the rule: git config has no other exit 1.
	AbsentUnset Absent = 1 << iota
	// AbsentNoRemote: exit 2 — `git remote get-url` for a remote that does not exist.
	AbsentNoRemote
	// AbsentNotRepo: git's "not a git repository" message.
	AbsentNotRepo
)

// Read runs s and classifies the result. A success returns stdout. A failure
// that matches a rule in absent returns (nil, true, nil). Any other failure —
// a timeout, git not installed, another exit status, an unexpected message —
// returns an error carrying git's stderr, wrapping the *exec.ExitError (or
// ErrTimeout, or exec.ErrNotFound) for errors.Is and errors.As.
func Read(s Spec, absent Absent) (out []byte, isAbsent bool, err error) {
	if err := DanglingDotGit(s.Dir); err != nil { // Run checks only a non-empty Dir
		return nil, false, err
	}
	out, err = Run(s)
	if err == nil {
		return out, false, nil
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return nil, false, fmt.Errorf("git %s: %w", strings.Join(s.Args, " "), err)
	}
	stderr := strings.TrimSpace(string(ee.Stderr))
	switch {
	case absent&AbsentUnset != 0 && ee.ExitCode() == 1:
		return nil, true, nil
	case absent&AbsentNoRemote != 0 && ee.ExitCode() == 2:
		return nil, true, nil
	case absent&AbsentNotRepo != 0 && IsNotARepo(stderr):
		return nil, true, nil
	}
	return nil, false, fmt.Errorf("git %s: %w: %s", strings.Join(s.Args, " "), err, stderr)
}

// DanglingDotGit reports a .git that is a symlink to nothing (or a loop) in dir
// or in any directory git would examine above it: Walk's directories, nearest
// first, up to the GIT_CEILING_DIRECTORIES or filesystem boundary, as the hand
// rules walk. git skips such a link and answers for whatever repository
// encloses it, or says "not a git repository"; neither is the truth, so a read
// there is refused. The walk ends at the first .git that is a gitfile or a
// directory holding HEAD: that is the repository git uses. An empty dir is the
// working directory. It is nil when there is none; anything else is git's to
// judge.
func DanglingDotGit(dir string) error {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil // git reports it
		}
		dir = wd
	}
	for _, d := range Walk(dir) {
		dot := filepath.Join(d, ".git")
		li, err := os.Lstat(dot)
		if err != nil {
			continue
		}
		if li.Mode()&os.ModeSymlink != 0 {
			if _, err := os.Stat(dot); errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ELOOP) {
				return fmt.Errorf("%s is a symlink to nothing: git would skip it and answer for an enclosing repository, or none; remove or repair it", dot)
			}
		}
		if info, err := os.Stat(dot); err == nil {
			if info.Mode().IsRegular() {
				return nil // a gitfile: the repository
			}
			if info.IsDir() {
				if h, err := os.Stat(filepath.Join(dot, "HEAD")); err == nil && h.Mode().IsRegular() {
					return nil
				}
			}
		}
	}
	return nil
}

// userConfigEnv is Parsed without dropping GIT_CONFIG_PARAMETERS and
// GIT_CONFIG_COUNT: those are the user's own config (`git -c`, the environment),
// not a repository's, and a read of the user's config must see them.
func userConfigEnv() []string {
	keep := map[string]bool{"GIT_CONFIG_PARAMETERS": true, "GIT_CONFIG_COUNT": true}
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if (!dropped[name] || keep[name]) && name != "LC_ALL" && name != "LANGUAGE" {
			env = append(env, kv)
		}
	}
	return append(stripTrace(env), "LC_ALL=C", "LANGUAGE=")
}

// Neutral makes a fresh empty directory outside any repository and the Parsed
// environment for running git there: GIT_CEILING_DIRECTORIES names its parent,
// so git never walks up into a repository that encloses the temp directory. A
// git run there reads the user's whole config (system, global, [include],
// GIT_CONFIG_COUNT and -c) and no repository's, and a broken .git in the
// process's working directory cannot fail it. cleanup removes the directory.
func Neutral() (dir string, env []string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", layout.ScratchDirPattern("ethos-git"))
	if err != nil {
		return "", nil, nil, fmt.Errorf("making a neutral directory for git: %w", err)
	}
	// Resolve symlinks (macOS /var -> /private/var) so the ceiling matches
	// the physical path git walks.
	if r, rerr := filepath.EvalSymlinks(dir); rerr == nil {
		dir = r
	}
	env = append(userConfigEnv(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
	return dir, env, func() { _ = os.RemoveAll(dir) }, nil
}
