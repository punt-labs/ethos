package gitenv

import (
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This observer is pinned to Go 1.26.5's Linux coverage lifecycle, not a
// directory exemption. Source: internal/coverage/cfile/{emit,testsupport}.go.
// Production attribution additionally relies on the reviewed synchronous
// ReadNeutralConfig -> read -> Run closure: Output waits for the command and
// its pipe readers; Run joins its signal watcher and cancels its timer before
// returning. The unset path finishes UnreadableUserConfig and closes its probes.
// There are no deferred product callbacks/finalizers; the child's test cleanups
// only restore environment. New asynchronous product work invalidates this
// boundary until joined. OS thread ownership alone cannot establish that fact.
type neutralCoverageFixture struct {
	exe, git, cwd, marker, evidence string
	coverageDir, profile            string
}

func newNeutralCoverageFixture(t *testing.T, git, base string) neutralCoverageFixture {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".tmp", "opencode"))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(root, 0o700))
	root, err = filepath.EvalSymlinks(root)
	require.NoError(t, err)
	evidence, err := os.MkdirTemp(root, "neutral-trace-")
	require.NoError(t, err)
	// Deliberately retained, including on failure. Never a test cleanup that
	// discards the raw trace, child stderr, or private coverage artifacts.
	t.Logf("neutral trace evidence (not child-counter aggregation): %s", evidence)
	exe, err := os.Executable()
	require.NoError(t, err)
	f := neutralCoverageFixture{exe: exe, git: git, cwd: base,
		marker: filepath.Join(evidence, "production-complete"), evidence: evidence}
	require.NoError(t, os.WriteFile(f.marker, []byte("complete\n"), 0o600))
	if testing.CoverMode() != "" {
		require.Equal(t, "go1.26.5", runtime.Version(), "coverage observer needs review for another runtime")
		f.coverageDir = filepath.Join(evidence, "coverage")
		f.profile = filepath.Join(evidence, "child-cover.out")
		require.NoError(t, os.Mkdir(f.coverageDir, 0o700))
		info, err := os.Lstat(f.coverageDir)
		require.NoError(t, err)
		require.True(t, info.IsDir() && info.Mode()&os.ModeSymlink == 0)
		entries, err := os.ReadDir(f.coverageDir)
		require.NoError(t, err)
		require.Empty(t, entries)
		// The child never receives the parent's profile flag or os.Args.
		if parent := flag.Lookup("test.coverprofile"); parent != nil && parent.Value.String() != "" {
			path, err := filepath.Abs(parent.Value.String())
			require.NoError(t, err)
			require.NotEqual(t, path, f.profile)
		}
	}
	return f
}

func (f neutralCoverageFixture) capture(t *testing.T, strace, record string) ([]byte, []byte, error) {
	t.Helper()
	// Snapshot, but never alter, a caller's existing coverage profile. Its final
	// production by the outer m.Run must also be checked by the invoking gate.
	var parentPath string
	var parentData []byte
	var parentMissing bool
	if parent := flag.Lookup("test.coverprofile"); parent != nil && parent.Value.String() != "" {
		parentPath = parent.Value.String()
		data, err := os.ReadFile(parentPath)
		parentMissing = os.IsNotExist(err)
		if !parentMissing {
			require.NoError(t, err)
			parentData = data
		}
	}
	args := []string{"-f", "-yy", "-s", "0", "-o", filepath.Join(f.evidence, "syscalls"),
		"-e", "trace=%file,clone,clone3,fork,vfork", f.exe, "-test.run=^TestNeutralConfigPhysicalTraceChild$"}
	if f.coverageDir != "" {
		args = append(args, "-test.gocoverdir="+f.coverageDir, "-test.coverprofile="+f.profile)
	}
	cmd := exec.Command(strace, args...)
	cmd.Dir = f.cwd
	cmd.Env = append(os.Environ(), "ETHOS_TEST_NEUTRAL_CONFIG_GIT="+f.git,
		"ETHOS_TEST_NEUTRAL_CONFIG_BASE="+f.cwd, "ETHOS_TEST_NEUTRAL_CONFIG_MARKER="+f.marker,
		"TMPDIR="+record, "TMP="+record, "TEMP="+record)
	out, runErr := cmd.CombinedOutput()
	require.NoError(t, os.WriteFile(filepath.Join(f.evidence, "child-output"), out, 0o600))
	calls, err := os.ReadFile(filepath.Join(f.evidence, "syscalls"))
	require.NoError(t, err)
	t.Logf("child output:\n%s\nwhole trace (coverage is NOT product scratch-free evidence):\n%s", out, calls)
	if parentPath != "" {
		data, err := os.ReadFile(parentPath)
		if parentMissing {
			require.True(t, os.IsNotExist(err), "child must not create the parent's profile")
		} else {
			require.NoError(t, err)
			require.Equal(t, parentData, data, "child must not overwrite the parent's profile")
		}
	}
	return calls, out, runErr
}

// neutralPhaseRecord retains both ordering positions, even if a call began
// before another PID's marker and completed afterward. Notices retain PID too.
type neutralPhaseRecord struct {
	pid, body  string
	start, end int
}

func neutralPhaseRecords(trace string) ([]neutralPhaseRecord, error) {
	// First parse the WHOLE trace with the unchanged framing validator. No
	// malformed tail, unknown record, or unfinished call can be hidden by phases.
	if _, err := neutralConfigTraceCalls(trace); err != nil {
		return nil, err
	}
	pending := make(map[string]neutralPhaseRecord)
	var records []neutralPhaseRecord
	for i, line := range strings.Split(strings.TrimSuffix(trace, "\n"), "\n") {
		parts := neutralTracePID.FindStringSubmatch(line)
		r := neutralPhaseRecord{pid: parts[1], body: parts[2], start: i, end: i}
		if resumed := neutralTraceResume.FindStringSubmatch(r.body); resumed != nil {
			p := pending[r.pid]
			r.start, r.body = p.start, p.body+resumed[2]
			delete(pending, r.pid)
		} else if strings.HasSuffix(r.body, " <unfinished ...>") {
			r.body = strings.TrimSuffix(r.body, " <unfinished ...>")
			pending[r.pid] = r
			continue
		}
		records = append(records, r)
	}
	return records, nil
}

var (
	neutralPhaseExit        = regexp.MustCompile(`^\+\+\+ exited with ([0-9]+) \+\+\+$`)
	neutralPhaseExec        = regexp.MustCompile(`^(` + neutralTracePath + `), \[\.\.\.\], 0x[0-9a-f]+ /\* [0-9]+ vars \*/$`)
	neutralPhaseClone       = regexp.MustCompile(`^child_stack=(?:NULL|0x[0-9a-f]+), flags=([A-Z0-9_|]+)(.*)$`)
	neutralPhaseCloneField  = regexp.MustCompile(`^, (parent_tid|tls|child_tid)=(NULL|0x[0-9a-f]+|\[[0-9]+(?:<pid:[0-9]+>)?\])`)
	neutralPhasePIDFD       = regexp.MustCompile(`^\[[0-9]+<pid:([1-9][0-9]*)>\]$`)
	neutralPhaseClone3      = regexp.MustCompile(`^\{flags=([A-Z0-9_|]+)((?:, [a-z_]+=(?:0x[0-9a-f]+|[0-9]+|\[[0-9]+(?:<pid:[0-9]+>)?\]))*)\}(?: => \{parent_tid=\[([1-9][0-9]*)\]\})?, (64|80|88)$`)
	neutralPhaseClone3Field = regexp.MustCompile(`^, ([a-z_]+)=([^,]+)`)
)

type neutralPhaseProcess struct {
	parent, group      string
	born, joined, exit int
	status             string
	thread, probe      bool
}

// Parse only the process-creation records explicitly requested above. Refuse
// untraced/namespace/parent-changing clone flags and unknown argument fields.
// Threads may be created by the Go runtime during coverage; independent product
// processes must all have exited BEFORE the marker, including the pidfd probe.
func neutralPhaseCreation(r neutralPhaseRecord) (child string, thread, probe bool, err error) {
	p := neutralTraceCall.FindStringSubmatch(r.body)
	var flags string
	var clone3ExitSignal string
	switch p[1] {
	case "fork", "vfork":
		if p[2] != "" {
			return "", false, false, fmt.Errorf("unsupported process arguments: %s", r.body)
		}
	case "clone":
		m := neutralPhaseClone.FindStringSubmatch(p[2])
		if m == nil {
			return "", false, false, fmt.Errorf("unsupported clone shape: %s", r.body)
		}
		flags = m[1]
		seen := make(map[string]bool)
		for tail := m[2]; tail != ""; {
			field := neutralPhaseCloneField.FindStringSubmatch(tail)
			if field == nil || seen[field[1]] {
				return "", false, false, fmt.Errorf("unsupported clone field: %s", r.body)
			}
			seen[field[1]] = true
			if fd := neutralPhasePIDFD.FindStringSubmatch(field[2]); fd != nil {
				if field[1] != "parent_tid" || !strings.Contains("|"+flags+"|", "|CLONE_PIDFD|") || !strings.HasSuffix(r.body, "= "+fd[1]) {
					return "", false, false, fmt.Errorf("inconsistent clone pidfd: %s", r.body)
				}
			}
			tail = strings.TrimPrefix(tail, field[0])
		}
	case "clone3":
		m := neutralPhaseClone3.FindStringSubmatch(p[2])
		if m == nil {
			return "", false, false, fmt.Errorf("unsupported clone3 shape: %s", r.body)
		}
		flags = m[1]
		if m[3] != "" && (!strings.Contains("|"+flags+"|", "|CLONE_PARENT_SETTID|") || !strings.HasSuffix(r.body, "= "+m[3])) {
			return "", false, false, fmt.Errorf("inconsistent clone3 returned parent_tid: %s", r.body)
		}
		seen := make(map[string]bool)
		for tail := m[2]; tail != ""; {
			field := neutralPhaseClone3Field.FindStringSubmatch(tail)
			if field == nil || seen[field[1]] {
				return "", false, false, fmt.Errorf("unsupported clone3 field: %s", r.body)
			}
			switch field[1] {
			case "child_tid", "parent_tid", "exit_signal", "stack", "stack_size", "tls", "pidfd":
			default:
				return "", false, false, fmt.Errorf("unknown clone3 field: %s", r.body)
			}
			seen[field[1]] = true
			if field[1] == "exit_signal" {
				clone3ExitSignal = field[2]
			}
			tail = strings.TrimPrefix(tail, field[0])
		}
	default:
		return "", false, false, fmt.Errorf("not a process creation: %s", r.body)
	}
	if flags != "" {
		set := make(map[string]bool)
		for _, f := range strings.Split(flags, "|") {
			if set[f] {
				return "", false, false, fmt.Errorf("duplicate clone flag: %s", r.body)
			}
			set[f] = true
		}
		thread = set["CLONE_THREAD"]
		if p[1] == "clone3" && (!thread || clone3ExitSignal != "0") {
			return "", false, false, fmt.Errorf("unsupported clone3 ownership/signal: %s", r.body)
		}
		if thread {
			for _, f := range []string{"CLONE_VM", "CLONE_FS", "CLONE_FILES", "CLONE_SIGHAND", "CLONE_THREAD", "CLONE_SYSVSEM"} {
				if !set[f] {
					return "", false, false, fmt.Errorf("incomplete thread ownership flags: %s", r.body)
				}
				delete(set, f)
			}
			for _, f := range []string{"CLONE_SETTLS", "CLONE_PARENT_SETTID", "CLONE_CHILD_CLEARTID"} {
				delete(set, f)
			}
		} else {
			if !set["CLONE_VM"] || !set["CLONE_VFORK"] {
				return "", false, false, fmt.Errorf("unsupported process flags: %s", r.body)
			}
			probe = set["CLONE_PIDFD"] && !set["SIGCHLD"]
			if !set["SIGCHLD"] && !probe {
				return "", false, false, fmt.Errorf("missing process exit signal: %s", r.body)
			}
			for _, f := range []string{"CLONE_VM", "CLONE_VFORK", "CLONE_PIDFD", "SIGCHLD"} {
				delete(set, f)
			}
		}
		if len(set) != 0 {
			return "", false, false, fmt.Errorf("unsupported clone flags: %s", r.body)
		}
	}
	result := r.body[strings.LastIndex(r.body, "= ")+2:]
	if strings.HasPrefix(result, "-1 ") {
		if p[1] == "clone3" && result == "-1 ENOSYS (Function not implemented)" {
			return "", thread, probe, nil // explicitly observed fallback, no child
		}
		return "", false, false, fmt.Errorf("failed process creation: %s", r.body)
	}
	if n, e := strconv.Atoi(result); e != nil || n <= 0 {
		return "", false, false, fmt.Errorf("invalid child PID: %s", r.body)
	}
	return result, thread, probe, nil
}

func neutralPhaseIsCreation(body string) bool {
	return strings.HasPrefix(body, "clone(") || strings.HasPrefix(body, "clone3(") ||
		strings.HasPrefix(body, "fork(") || strings.HasPrefix(body, "vfork(")
}

func validateNeutralConfigPhases(trace string, f neutralCoverageFixture) error {
	records, err := neutralPhaseRecords(trace)
	if err != nil {
		return err
	}
	root := records[0].pid
	processes := map[string]*neutralPhaseProcess{root: {group: root, born: -1, joined: -1, exit: -1}}
	marker := -1
	var markerRecord neutralPhaseRecord
	var files []neutralPhaseRecord
	// Parse process events and locate the one exact read-only sentinel; don't
	// classify a suffix or discard any framing errors before this pass completes.
	for _, r := range records {
		if neutralPhaseIsCreation(r.body) {
			child, thread, probe, err := neutralPhaseCreation(r)
			if err != nil {
				return err
			}
			if child != "" {
				if processes[child] != nil {
					return fmt.Errorf("duplicate/reused process PID %s", child)
				}
				processes[child] = &neutralPhaseProcess{parent: r.pid, born: r.start, joined: r.end, exit: -1, thread: thread, probe: probe}
			}
			continue
		}
		if neutralTraceNotice.MatchString(r.body) {
			continue
		}
		files = append(files, r)
		if strings.Contains(r.body, strconv.Quote(f.marker)) {
			want := `newfstatat\(AT_FDCWD<` + regexp.QuoteMeta(f.cwd) + `>, ` + regexp.QuoteMeta(strconv.Quote(f.marker)) + `, \{st_mode=S_IFREG\|0600, st_size=9, \.\.\.\}, AT_SYMLINK_NOFOLLOW\)\s+= 0`
			if marker != -1 || !neutralPhaseMatch(want, r.body) {
				return fmt.Errorf("duplicate or invalid production marker: %s", r.body)
			}
			marker, markerRecord = r.end, r
		}
	}
	if marker == -1 {
		return fmt.Errorf("missing production marker")
	}
	// Derive thread-group ownership from explicit creation, not a file path or
	// PID guess. Children can run before a parent's clone resume is observed.
	for range len(processes) {
		for pid, p := range processes {
			if p.group == "" && processes[p.parent] != nil && processes[p.parent].group != "" {
				p.group = pid
				if p.thread {
					p.group = processes[p.parent].group
				}
			}
		}
	}
	for _, r := range records {
		p := processes[r.pid]
		if p == nil || p.group == "" || r.start <= p.born || (p.exit != -1 && r.end >= p.exit) {
			return fmt.Errorf("unowned or out-of-lifetime record for PID %s: %s", r.pid, r.body)
		}
		if r.start < markerRecord.end && r.end >= markerRecord.start && r.start != markerRecord.start {
			return fmt.Errorf("outstanding observed call at production boundary: %s", r.body)
		}
		if strings.HasPrefix(r.body, "+++") {
			m := neutralPhaseExit.FindStringSubmatch(r.body)
			if m == nil {
				return fmt.Errorf("abnormal process exit: %s", r.body)
			}
			p.exit, p.status = r.end, m[1]
		}
	}
	if processes[markerRecord.pid].group != root {
		return fmt.Errorf("production marker was not emitted by the test process")
	}
	gitPIDs := make(map[string]bool)
	previousGitExit := -1
	for i, r := range files {
		parts := neutralTraceCall.FindStringSubmatch(r.body)
		if parts[1] != "execve" && parts[1] != "execveat" {
			continue
		}
		m := neutralPhaseExec.FindStringSubmatch(parts[2])
		if parts[1] != "execve" || m == nil || !strings.HasSuffix(r.body, "= 0") || r.end >= marker {
			return fmt.Errorf("unexpected or unsuccessful execution: %s", r.body)
		}
		if i == 0 && r.pid == root && m[1] == strconv.Quote(f.exe) {
			continue
		}
		p := processes[r.pid]
		if m[1] != strconv.Quote(f.git) || p.group == root || p.thread || p.probe || gitPIDs[r.pid] {
			return fmt.Errorf("unexpected execution producer: %s", r.body)
		}
		gitPIDs[r.pid] = true
		wantStatus := strconv.Itoa(len(gitPIDs) - 1) // value read: 0; unset read: 1
		if p.exit <= r.end || p.exit >= markerRecord.start || p.status != wantStatus || r.start <= previousGitExit {
			return fmt.Errorf("Git PID %s did not complete the expected read before the marker", r.pid)
		}
		previousGitExit = p.exit
	}
	if len(files) == 0 || !strings.HasPrefix(files[0].body, "execve("+strconv.Quote(f.exe)+",") || len(gitPIDs) != 2 {
		return fmt.Errorf("expected the actual test executable and two Git executions")
	}
	probes := 0
	for pid, p := range processes {
		if p.exit == -1 {
			return fmt.Errorf("surviving/unobserved exit for PID %s", pid)
		}
		if p.group == root {
			if p.status != "0" {
				return fmt.Errorf("test process/thread PID %s failed with %s", pid, p.status)
			}
			continue
		}
		if p.exit >= markerRecord.start || p.joined >= markerRecord.start || processes[p.parent].group != root {
			return fmt.Errorf("surviving or unexpected product work at boundary: PID %s", pid)
		}
		if !gitPIDs[pid] {
			if !p.probe || p.status != "0" {
				return fmt.Errorf("unexpected non-Git process PID %s", pid)
			}
			probes++
			for _, r := range files {
				if r.pid == pid {
					return fmt.Errorf("pidfd probe performed unexpected file work: %s", r.body)
				}
			}
		}
	}
	if probes > 1 {
		return fmt.Errorf("unexpected repeated pidfd probes")
	}
	var prefix []string
	var suffix []neutralPhaseRecord
	for _, r := range files {
		if f.coverageDir == "" || r.end <= marker {
			prefix = append(prefix, r.pid+" "+r.body)
		} else {
			if processes[r.pid].group != root {
				return fmt.Errorf("non-test coverage producer PID %s", r.pid)
			}
			suffix = append(suffix, r)
		}
	}
	// Uncovered children retain the original WHOLE-PROCESS nonmutation rule.
	// Covered children apply it to the complete prefix, marker included.
	if err := validateNeutralConfigTrace(strings.Join(prefix, "\n")); err != nil {
		return err
	}
	if f.coverageDir == "" {
		return nil
	}
	return validateNeutralCoverageEmission(suffix, root, f)
}

func neutralPhaseMatch(pattern, text string) bool {
	return regexp.MustCompile("^(?:" + pattern + ")$").MatchString(text)
}

// Require the complete fresh-directory protocol, including read-back. There
// are no generic read or write allowances in this suffix, even inside outdir.
// Go's successful m.Run teardown parses the emitted binary files and emits the
// private text profile; the parent also checks their headers and profile below.
func validateNeutralCoverageEmission(calls []neutralPhaseRecord, root string, f neutralCoverageFixture) error {
	if len(calls) != 14 {
		return fmt.Errorf("incomplete/unexpected coverage protocol: got %d file calls, want 14", len(calls))
	}
	q := func(s string) string { return regexp.QuoteMeta(strconv.Quote(s)) }
	at := `AT_FDCWD<` + regexp.QuoteMeta(f.cwd) + `>`
	statDir := `newfstatat\(` + at + `, ` + q(f.coverageDir) + `, \{st_mode=S_IFDIR\|0700, st_size=[0-9]+, \.\.\.\}, 0\)\s+= 0`
	missing := func(path, flag string) string {
		return `newfstatat\(` + at + `, ` + q(path) + `, 0x[0-9a-f]+, ` + flag + `\)\s+= -1 ENOENT \(No such file or directory\)`
	}
	open := func(path, flags string) string {
		return `openat\(` + at + `, ` + q(path) + `, ` + flags + `\)\s+= [0-9]+<` + regexp.QuoteMeta(path) + `>`
	}
	create := func(path string) string { return open(path, `O_RDWR\|O_CREAT\|O_TRUNC\|O_CLOEXEC, 0666`) }
	rename := func(from, to string) string {
		return `renameat\(` + at + `, ` + q(from) + `, ` + at + `, ` + q(to) + `\)\s+= 0`
	}
	// Extract the hash and timestamps only from the exact expected operations.
	metaStat := regexp.MustCompile(`^newfstatat\(` + at + `, "` + regexp.QuoteMeta(f.coverageDir) + `/covmeta\.([0-9a-f]{32})", 0x[0-9a-f]+, 0\)\s+= -1 ENOENT \(No such file or directory\)$`)
	m := metaStat.FindStringSubmatch(calls[1].body)
	if m == nil {
		return fmt.Errorf("invalid coverage metadata probe: %s", calls[1].body)
	}
	hash := m[1]
	// These are observed Linux paths, even when parser unit tests run elsewhere.
	meta := path.Join(f.coverageDir, "covmeta."+hash)
	metaTemp := regexp.MustCompile(`^openat\(` + at + `, "(` + regexp.QuoteMeta(path.Join(f.coverageDir, "tmp.covmeta."+hash)) + `[1-9][0-9]*)", O_RDWR\|O_CREAT\|O_TRUNC\|O_CLOEXEC, 0666\)\s+= [0-9]+<[^>]+>$`)
	m = metaTemp.FindStringSubmatch(calls[2].body)
	if m == nil {
		return fmt.Errorf("invalid coverage metadata creation: %s", calls[2].body)
	}
	mtmp := m[1]
	counterTemp := regexp.MustCompile(`^openat\(` + at + `, "(` + regexp.QuoteMeta(path.Join(f.coverageDir, "tmp.covcounters."+hash+"."+root+".")) + `[1-9][0-9]*)", O_RDWR\|O_CREAT\|O_TRUNC\|O_CLOEXEC, 0666\)\s+= [0-9]+<[^>]+>$`)
	m = counterTemp.FindStringSubmatch(calls[6].body)
	if m == nil {
		return fmt.Errorf("invalid coverage counter creation: %s", calls[6].body)
	}
	ctmp := m[1]
	counter := path.Join(f.coverageDir, strings.TrimPrefix(path.Base(ctmp), "tmp."))
	want := []string{statDir, missing(meta, "0"), create(mtmp), missing(meta, "AT_SYMLINK_NOFOLLOW"), rename(mtmp, meta),
		statDir, create(ctmp), missing(counter, "AT_SYMLINK_NOFOLLOW"), rename(ctmp, counter),
		open(f.coverageDir, `O_RDONLY\|O_CLOEXEC\|O_DIRECTORY`), create(f.profile),
		open(meta, `O_RDONLY\|O_CLOEXEC`), open(counter, `O_RDONLY\|O_CLOEXEC`), missing(path.Join(f.coverageDir, "metafiles.txt"), "0")}
	for i, r := range calls {
		if !neutralPhaseMatch(want[i], r.body) {
			return fmt.Errorf("unexpected coverage protocol step %d PID %s: %s", i, r.pid, r.body)
		}
	}
	return nil
}

func (f neutralCoverageFixture) checkCoverageArtifacts(t *testing.T, out []byte) {
	t.Helper()
	require.Contains(t, string(out), "coverage:")
	profile, err := os.ReadFile(f.profile)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(profile), "mode: "+testing.CoverMode()+"\n"))
	require.Contains(t, string(profile), "/internal/gitenv/gitenv.go:")
	entries, err := os.ReadDir(f.coverageDir)
	require.NoError(t, err)
	require.Len(t, entries, 2, "only finalized metadata and counters, never unfinished temps")
	var meta, counter []byte
	var hash string
	for _, e := range entries {
		info, err := os.Lstat(filepath.Join(f.coverageDir, e.Name()))
		require.NoError(t, err)
		require.True(t, info.Mode().IsRegular())
		data, err := os.ReadFile(filepath.Join(f.coverageDir, e.Name()))
		require.NoError(t, err)
		switch {
		case strings.HasPrefix(e.Name(), "covmeta."):
			meta, hash = data, strings.TrimPrefix(e.Name(), "covmeta.")
		case strings.HasPrefix(e.Name(), "covcounters."):
			counter = data
		default:
			t.Fatalf("unexpected coverage artifact %s", e.Name())
		}
	}
	// Pinned coverage/defs.go headers. m.Run has also parsed the full payloads.
	require.GreaterOrEqual(t, len(meta), 56)
	require.GreaterOrEqual(t, len(counter), 32)
	assert.Equal(t, "\x00cvm", string(meta[:4]))
	assert.Equal(t, "\x00cwm", string(counter[:4]))
	assert.Equal(t, uint32(1), binary.LittleEndian.Uint32(meta[4:8]))
	assert.Equal(t, uint32(1), binary.LittleEndian.Uint32(counter[4:8]))
	assert.Equal(t, uint64(len(meta)), binary.LittleEndian.Uint64(meta[8:16]))
	assert.Equal(t, hash, hex.EncodeToString(meta[24:40]))
	assert.Equal(t, hash, hex.EncodeToString(counter[8:24]))
}

// Hand-written protocol examples, not patterns generated by the validator.
func neutralPhaseExample(covered bool) (neutralCoverageFixture, []string, []string, []string) {
	f := neutralCoverageFixture{exe: "/fixture/test", git: "/git", cwd: "/fixture", marker: "/evidence/production-complete"}
	prefix := []string{
		`100 execve("/fixture/test", [...], 0x1 /* 1 vars */) = 0`,
		`100 clone(child_stack=0x1, flags=CLONE_VM|CLONE_FS|CLONE_FILES|CLONE_SIGHAND|CLONE_THREAD|CLONE_SYSVSEM) = 101`,
		`101 clone(child_stack=NULL, flags=CLONE_VM|CLONE_VFORK|SIGCHLD) = 200`,
		`200 execve("/git", [...], 0x1 /* 1 vars */) = 0`,
		`200 openat(AT_FDCWD</fixture>, "/fixture/included", O_RDONLY) = 3</fixture/included>`,
		`200 +++ exited with 0 +++`,
		`101 clone(child_stack=NULL, flags=CLONE_VM|CLONE_VFORK|SIGCHLD) = 201`,
		`201 execve("/git", [...], 0x1 /* 1 vars */) = 0`,
		`201 +++ exited with 1 +++`,
	}
	marker := []string{`101 newfstatat(AT_FDCWD</fixture>, "/evidence/production-complete", {st_mode=S_IFREG|0600, st_size=9, ...}, AT_SYMLINK_NOFOLLOW) = 0`}
	if !covered {
		return f, prefix, marker, []string{`101 access("/fixture/included", R_OK) = 0`}
	}
	f.coverageDir, f.profile = "/evidence/coverage", "/evidence/child-cover.out"
	suffix := []string{
		`101 newfstatat(AT_FDCWD</fixture>, "/evidence/coverage", {st_mode=S_IFDIR|0700, st_size=4096, ...}, 0) = 0`,
		`101 newfstatat(AT_FDCWD</fixture>, "/evidence/coverage/covmeta.0123456789abcdef0123456789abcdef", 0x1, 0) = -1 ENOENT (No such file or directory)`,
		`101 openat(AT_FDCWD</fixture>, "/evidence/coverage/tmp.covmeta.0123456789abcdef0123456789abcdef123", O_RDWR|O_CREAT|O_TRUNC|O_CLOEXEC, 0666) = 5</evidence/coverage/tmp.covmeta.0123456789abcdef0123456789abcdef123>`,
		`101 newfstatat(AT_FDCWD</fixture>, "/evidence/coverage/covmeta.0123456789abcdef0123456789abcdef", 0x1, AT_SYMLINK_NOFOLLOW) = -1 ENOENT (No such file or directory)`,
		`101 renameat(AT_FDCWD</fixture>, "/evidence/coverage/tmp.covmeta.0123456789abcdef0123456789abcdef123", AT_FDCWD</fixture>, "/evidence/coverage/covmeta.0123456789abcdef0123456789abcdef") = 0`,
		`101 newfstatat(AT_FDCWD</fixture>, "/evidence/coverage", {st_mode=S_IFDIR|0700, st_size=4096, ...}, 0) = 0`,
		`101 openat(AT_FDCWD</fixture>, "/evidence/coverage/tmp.covcounters.0123456789abcdef0123456789abcdef.100.456", O_RDWR|O_CREAT|O_TRUNC|O_CLOEXEC, 0666) = 5</evidence/coverage/tmp.covcounters.0123456789abcdef0123456789abcdef.100.456>`,
		`101 newfstatat(AT_FDCWD</fixture>, "/evidence/coverage/covcounters.0123456789abcdef0123456789abcdef.100.456", 0x1, AT_SYMLINK_NOFOLLOW) = -1 ENOENT (No such file or directory)`,
		`101 renameat(AT_FDCWD</fixture>, "/evidence/coverage/tmp.covcounters.0123456789abcdef0123456789abcdef.100.456", AT_FDCWD</fixture>, "/evidence/coverage/covcounters.0123456789abcdef0123456789abcdef.100.456") = 0`,
		`101 openat(AT_FDCWD</fixture>, "/evidence/coverage", O_RDONLY|O_CLOEXEC|O_DIRECTORY) = 5</evidence/coverage>`,
		`101 openat(AT_FDCWD</fixture>, "/evidence/child-cover.out", O_RDWR|O_CREAT|O_TRUNC|O_CLOEXEC, 0666) = 5</evidence/child-cover.out>`,
		`101 openat(AT_FDCWD</fixture>, "/evidence/coverage/covmeta.0123456789abcdef0123456789abcdef", O_RDONLY|O_CLOEXEC) = 5</evidence/coverage/covmeta.0123456789abcdef0123456789abcdef>`,
		`101 openat(AT_FDCWD</fixture>, "/evidence/coverage/covcounters.0123456789abcdef0123456789abcdef.100.456", O_RDONLY|O_CLOEXEC) = 6</evidence/coverage/covcounters.0123456789abcdef0123456789abcdef.100.456>`,
		`101 newfstatat(AT_FDCWD</fixture>, "/evidence/coverage/metafiles.txt", 0x1, 0) = -1 ENOENT (No such file or directory)`,
	}
	return f, prefix, marker, suffix
}

func neutralPhaseExampleTrace(prefix, marker, suffix []string) string {
	var lines []string
	lines = append(lines, prefix...)
	lines = append(lines, marker...)
	lines = append(lines, suffix...)
	lines = append(lines, "101 +++ exited with 0 +++", "100 +++ exited with 0 +++")
	return strings.Join(lines, "\n") + "\n"
}

func TestNeutralConfigCoveragePhases(t *testing.T) {
	for _, covered := range []bool{false, true} {
		t.Run(fmt.Sprintf("covered=%v", covered), func(t *testing.T) {
			f, prefix, marker, suffix := neutralPhaseExample(covered)
			require.NoError(t, validateNeutralConfigPhases(neutralPhaseExampleTrace(prefix, marker, suffix), f))
			// The uncovered observer must still reject mutations AFTER the marker.
			bad := append([]string(nil), suffix...)
			bad = append(bad, `101 mkdirat(AT_FDCWD</fixture>, "/evidence/coverage", 0700) = -1 EACCES (Permission denied)`)
			require.Error(t, validateNeutralConfigPhases(neutralPhaseExampleTrace(prefix, marker, bad), f))
		})
	}
	f, prefix, marker, suffix := neutralPhaseExample(true)
	valid := neutralPhaseExampleTrace(prefix, marker, suffix)
	t.Run("coverage may migrate between owned runtime threads", func(t *testing.T) {
		require.NoError(t, validateNeutralConfigPhases(strings.ReplaceAll(valid, "101 openat(", "100 openat("), f))
	})
	t.Run("completed split coverage call retains positions", func(t *testing.T) {
		split := strings.Replace(valid, suffix[2], strings.Replace(suffix[2], ") = 5", " <unfinished ...>\n100 --- SIGURG {si_signo=SIGURG} ---\n101 <... openat resumed>) = 5", 1), 1)
		require.NoError(t, validateNeutralConfigPhases(split, f))
		records, err := neutralPhaseRecords(split)
		require.NoError(t, err)
		found := false
		for _, r := range records {
			if strings.Contains(r.body, "tmp.covmeta.") && strings.HasPrefix(r.body, "openat(") {
				assert.Equal(t, "101", r.pid)
				assert.Equal(t, r.start+2, r.end)
				found = true
			}
		}
		require.True(t, found)
	})
	for _, tc := range []struct{ name, trace string }{
		{"missing marker", neutralPhaseExampleTrace(prefix, nil, suffix)},
		{"duplicate marker", neutralPhaseExampleTrace(prefix, append(append([]string(nil), marker...), marker...), suffix)},
		{"failed marker", strings.Replace(valid, "st_size=9, ...}, AT_SYMLINK_NOFOLLOW) = 0", "st_size=9, ...}, AT_SYMLINK_NOFOLLOW) = -1 EACCES (Permission denied)", 1)},
		{"coverage-shaped prefix write", neutralPhaseExampleTrace(append(append([]string(nil), prefix...), suffix[2]), marker, suffix)},
		{"failed coverage-shaped prefix write", neutralPhaseExampleTrace(append(append([]string(nil), prefix...), strings.Replace(suffix[2], "= 5</evidence/coverage/tmp.covmeta.0123456789abcdef0123456789abcdef123>", "= -1 EACCES (Permission denied)", 1)), marker, suffix)},
		{"pre-marker failed mkdirat", neutralPhaseExampleTrace(append(append([]string(nil), prefix...), `101 mkdirat(AT_FDCWD</fixture>, "/record/tmp", 0700) = -1 EACCES (Permission denied)`), marker, suffix)},
		{"pending read at boundary", neutralPhaseExampleTrace(append(append([]string(nil), prefix...), `100 access("/fixture/included", R_OK <unfinished ...>`), marker, append([]string{`100 <... access resumed>) = 0`}, suffix...))},
		{"pending clone at boundary", strings.Replace(strings.Replace(valid, prefix[1], `100 clone(child_stack=0x1, flags=CLONE_VM|CLONE_FS|CLONE_FILES|CLONE_SIGHAND|CLONE_THREAD|CLONE_SYSVSEM <unfinished ...>`, 1), marker[0], marker[0]+"\n100 <... clone resumed>) = 101", 1)},
		{"missing Git exit", strings.Replace(valid, prefix[8]+"\n", "", 1)},
		{"late Git exit", strings.Replace(strings.Replace(valid, prefix[8]+"\n", "", 1), marker[0], marker[0]+"\n"+prefix[8], 1)},
		{"wrong Git exit", strings.Replace(valid, prefix[8], "201 +++ exited with 0 +++", 1)},
		{"unsuccessful Git exec", strings.Replace(valid, prefix[3], strings.Replace(prefix[3], "= 0", "= -1 ENOENT (No such file or directory)", 1), 1)},
		{"extra exec", neutralPhaseExampleTrace(prefix, marker, append([]string{`101 execve("/git", [...], 0x1 /* 1 vars */) = 0`}, suffix...))},
		{"Git coverage producer", strings.Replace(valid, suffix[2], strings.Replace(suffix[2], "101 ", "200 ", 1), 1)},
		{"unowned coverage producer", strings.Replace(valid, suffix[2], strings.Replace(suffix[2], "101 ", "999 ", 1), 1)},
		{"missing thread ownership", strings.Replace(valid, prefix[1]+"\n", "", 1)},
		{"surviving process", neutralPhaseExampleTrace(append(append([]string(nil), prefix...), `101 vfork() = 300`), marker, suffix)},
		{"unexpected completed process", neutralPhaseExampleTrace(append(append([]string(nil), prefix...), `101 vfork() = 300`, `300 +++ exited with 0 +++`), marker, suffix)},
		{"record after exit", valid + `101 access("/fixture/included", R_OK) = 0` + "\n"},
		{"PID reused", strings.Replace(valid, prefix[6], strings.Replace(prefix[6], "201", "200", 1), 1)},
		{"killed runtime thread", strings.Replace(valid, "101 +++ exited with 0 +++", "101 +++ killed by SIGTERM +++", 1)},
		{"unknown suffix syscall", strings.Replace(valid, suffix[0], `101 future_syscall("/evidence/coverage") = 0`, 1)},
		{"unknown suffix notice", valid + "100 unrecognized observer output\n"},
		{"malformed suffix", valid + "100 openat(\n"},
		{"archival unknown unfinished", valid + "100 ???( <unfinished ...>\n"},
		{"unfinished suffix EOF", valid + "100 openat(AT_FDCWD</fixture>, \"/evidence/coverage\", O_RDONLY <unfinished ...>\n"},
		{"incomplete protocol", neutralPhaseExampleTrace(prefix, marker, suffix[:len(suffix)-1])},
		{"unexplained suffix read", neutralPhaseExampleTrace(prefix, marker, append(append([]string(nil), suffix...), `101 access("/fixture/included", R_OK) = 0`))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNeutralConfigPhases(tc.trace, f)
			require.Error(t, err)
			if strings.HasPrefix(tc.name, "pending ") {
				require.Contains(t, err.Error(), "outstanding observed call at production boundary")
			}
		})
	}
	for _, tc := range []struct {
		name             string
		step             int
		old, replacement string
	}{
		{"wrong directory", 2, "/evidence/coverage/", "/record/"},
		{"wrong hash", 6, "0123456789abcdef0123456789abcdef", "1123456789abcdef0123456789abcdef"},
		{"counter thread PID not process PID", 6, ".100.", ".101."},
		{"wrong flags", 2, "O_RDWR|O_CREAT|O_TRUNC|O_CLOEXEC", "O_WRONLY|O_CREAT|O_TRUNC|O_CLOEXEC"},
		{"wrong mode", 2, "0666", "0600"},
		{"failed create", 2, "= 5</evidence/coverage/tmp.covmeta.0123456789abcdef0123456789abcdef123>", "= -1 EACCES (Permission denied)"},
		{"wrong returned path", 2, "5</evidence/coverage/", "5</record/"},
		{"wrong rename temp", 4, "abcdef123", "abcdef124"},
		{"wrong rename hash", 4, `, "/evidence/coverage/covmeta.012`, `, "/evidence/coverage/covmeta.112`},
		{"failed rename", 8, "= 0", "= -1 EACCES (Permission denied)"},
		{"parent profile targeted", 10, "child-cover.out", "parent-cover.out"},
		{"failed profile emission", 10, "= 5</evidence/child-cover.out>", "= -1 EISDIR (Is a directory)"},
		{"unexpected read flags", 11, "O_RDONLY|O_CLOEXEC", "O_RDWR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := append([]string(nil), suffix...)
			bad[tc.step] = strings.ReplaceAll(bad[tc.step], tc.old, tc.replacement)
			require.NotEqual(t, suffix[tc.step], bad[tc.step], "negative control must actually change its record")
			require.Error(t, validateNeutralConfigPhases(neutralPhaseExampleTrace(prefix, marker, bad), f))
		})
	}
}

func TestNeutralConfigCoverageProcessEvents(t *testing.T) {
	const threadFlags = "CLONE_VM|CLONE_FS|CLONE_FILES|CLONE_SIGHAND|CLONE_THREAD|CLONE_SYSVSEM"
	const pthreadFlags = threadFlags + "|CLONE_SETTLS|CLONE_PARENT_SETTID|CLONE_CHILD_CLEARTID"
	const fields = ", child_tid=0x1, parent_tid=0x1, exit_signal=0, stack=0x2, stack_size=0x1000, tls=0x3"
	for _, tc := range []struct {
		name, call, child  string
		thread, probe, bad bool
	}{
		{"Go runtime thread", "clone(child_stack=0x1, flags=" + threadFlags + ") = 101", "101", true, false, false},
		{"pthread clone3", "clone3({flags=" + pthreadFlags + fields + "} => {parent_tid=[101]}, 88) = 101", "101", true, false, false},
		{"pthread clone3 fallback", "clone3({flags=" + pthreadFlags + fields + "}, 88) = -1 ENOSYS (Function not implemented)", "", true, false, false},
		{"pidfd Git", "clone(child_stack=NULL, flags=CLONE_VM|CLONE_PIDFD|CLONE_VFORK|SIGCHLD, parent_tid=[5<pid:101>]) = 101", "101", false, false, false},
		{"pidfd probe", "clone(child_stack=NULL, flags=CLONE_VM|CLONE_PIDFD|CLONE_VFORK, parent_tid=[5<pid:101>]) = 101", "101", false, true, false},
		{"fork", "fork() = 101", "101", false, false, false},
		{"vfork", "vfork() = 101", "101", false, false, false},
		{"unknown clone field", "clone(child_stack=0x1, flags=" + threadFlags + ", future=0x1) = 101", "", false, false, true},
		{"unknown clone3 field", "clone3({flags=" + pthreadFlags + fields + ", future=0}, 88) = 101", "", false, false, true},
		{"unknown clone flag", "clone(child_stack=0x1, flags=" + threadFlags + "|CLONE_FUTURE) = 101", "", false, false, true},
		{"untraced clone", "clone(child_stack=0x1, flags=" + threadFlags + "|CLONE_UNTRACED) = 101", "", false, false, true},
		{"incomplete thread flags", "clone(child_stack=0x1, flags=CLONE_THREAD) = 101", "", false, false, true},
		{"clone3 nonzero thread signal", "clone3({flags=" + pthreadFlags + strings.Replace(fields, "exit_signal=0", "exit_signal=17", 1) + "}, 88) = 101", "", false, false, true},
		{"wrong clone3 returned PID", "clone3({flags=" + pthreadFlags + fields + "} => {parent_tid=[102]}, 88) = 101", "", false, false, true},
		{"wrong pidfd returned PID", "clone(child_stack=NULL, flags=CLONE_VM|CLONE_PIDFD|CLONE_VFORK|SIGCHLD, parent_tid=[5<pid:102>]) = 101", "", false, false, true},
		{"duplicate clone field", "clone(child_stack=0x1, flags=" + threadFlags + ", tls=0x1, tls=0x2) = 101", "", false, false, true},
		{"failed clone", "clone(child_stack=0x1, flags=" + threadFlags + ") = -1 EAGAIN (Resource temporarily unavailable)", "", false, false, true},
		{"zero child PID", "fork() = 0", "", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records, err := neutralPhaseRecords("100 " + tc.call)
			require.NoError(t, err)
			child, thread, probe, err := neutralPhaseCreation(records[0])
			if tc.bad {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.child, child)
			assert.Equal(t, tc.thread, thread)
			assert.Equal(t, tc.probe, probe)
		})
	}
}

func TestNeutralConfigCoverageEmissionFailure(t *testing.T) {
	if runtime.GOOS != "linux" || testing.CoverMode() == "" {
		return // The covered Linux invocation must execute this negative control.
	}
	git, base := neutralConfigFixture(t)
	strace, err := exec.LookPath("strace")
	require.NoError(t, err)
	record := filepath.Join(base, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(record, 0o700))
	f := newNeutralCoverageFixture(t, git, base)
	f.profile = f.evidence // os.Create of a directory fails even for root.
	calls, out, runErr := f.capture(t, strace, record)
	require.Error(t, runErr, "m.Run must report real coverage emission failure")
	require.Contains(t, string(out), "error generating coverage report")
	require.Contains(t, string(calls), strconv.Quote(f.marker), "production reached its explicit boundary")
	require.Error(t, validateNeutralConfigPhases(string(calls), f), "failure must never produce a successful verdict")
}
