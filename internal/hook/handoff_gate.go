package hook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/redact"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

const gateRoot = "/checkout"
const gateBytes = 256 << 20
const gateInputTime = time.Second
const gateRecordAge = 24 * time.Hour
const gateRecordsPerStripe = 8
const gateRecordBytes = 512 << 10

var errHandoffScratchShadowsInput = errors.New("checkout scratch shadows snapshot content")

var handoffRunnerBuild = sync.OnceValues(func() (string, error) {
	f, err := os.Open("/proc/self/exe")
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
})

type gateResult struct {
	Key       string
	Proof     string
	Isolation string
	Failure   string
	Pending   bool `json:",omitempty"`
}

type handoffSystemPlan struct {
	Mounts       []handoffSystemMount
	Inputs       []handoffSystemInput
	Passwd       string
	Group        string
	DeveloperDir string
	Fingerprint  string
}

type handoffWorktreePlan struct {
	system     handoffSystemPlan
	gitDir     string
	runtimeDir string
	runtime    handoffRuntime
}

type handoffLiveGuard interface {
	close()
	finish(context.Context, []string) error
}

type handoffSystemMount struct {
	Source, Target string
	Directory      bool
}

type handoffSystemInput struct {
	Path, Tool, Digest string
	Directory          bool
}

var errHandoffXcodeUntrusted = errors.New("xcode developer dir untrusted in darwin snapshot")

func runHandoffGate(ctx context.Context, root, agent string, files []string, check checkset.Check, policy []checkset.Check) StageFeedback {
	manualRoot := root
	manualCheck := check
	if check.WorktreeSnapshot != nil {
		relative := make([]string, 0, len(files))
		for _, file := range files {
			if filepath.IsAbs(file) {
				path, err := filepath.Rel(root, file)
				if err != nil || !filepath.IsLocal(path) {
					continue
				}
				file = path
			}
			relative = append(relative, file)
		}
		files = relative
	}
	plan := stageInvocationsAt(gateRoot, files, check, true)
	if len(plan) == 0 {
		return StageFeedback{}
	}
	start := time.Now()
	mode := checkset.IsolationSnapshot
	if check.WorktreeSnapshot != nil && check.WorktreeSnapshot.Isolation != "" {
		mode = check.WorktreeSnapshot.Isolation
	}
	isolation := []string{mode}
	if runtime.GOOS == "darwin" && check.WorktreeSnapshot != nil && check.WorktreeSnapshot.Network == "loopback" {
		isolation = append(isolation, "residual (host localhost services reachable, shared port space)")
	}
	feedback := func(message string) StageFeedback {
		if check.OnFail == "advisory" {
			return StageFeedback{Advisory: message}
		}
		return StageFeedback{Blocking: message}
	}
	deferGate := func(reason string, noticeOnly ...bool) StageFeedback {
		diagnostic.LogHookCheck(ctx, root, root, "handoff", check.Name, "deferred", time.Since(start), mode)
		message := fmt.Sprintf("handoff checks deferred (%s): %s. Run the explicit project gate: %s", check.Name, reason, handoffManualCommand(manualRoot, files, manualCheck))
		if len(noticeOnly) > 0 && noticeOnly[0] {
			return StageFeedback{Advisory: message}
		}
		f := feedback(message)
		if f.Blocking != "" {
			f.Notice = message
		}
		return f
	}
	if check.InvalidSnapshotSidecar {
		return deferGate("invalid snapshot sidecar")
	}
	live := mode == checkset.IsolationLiveVerified
	var approval liveApprovalPolicy
	var grant string
	unapproved := "live-verified not approved (run: ethos handoff approve-live " + check.HandoffPolicySlug + ")"
	if live {
		if runtime.GOOS != "darwin" {
			return deferGate("live-verified unavailable in "+runtime.GOOS+" snapshot", true)
		}
		var err error
		approval, err = newLiveApprovalPolicy(root, check.HandoffPolicySlug, check.HandoffPolicyRevision)
		if err != nil {
			return deferGate(unapproved, true)
		}
		grant = approval.approval()
		if grant == "" {
			return deferGate(unapproved, true)
		}
	}
	var bundle handoffRuntime
	if check.WorktreeSnapshot != nil {
		if reason := unavailableSnapshotCapability(runtime.GOOS, check.WorktreeSnapshot.Requires); reason != "" && !live {
			return deferGate(reason)
		}
		var err error
		bundle, err = prepareHandoffRuntime(ctx, check.WorktreeSnapshot, agent)
		if err != nil {
			return deferGate(err.Error())
		}
		if field := unsupportedWorktreeField(check.WorktreeSnapshot); field != "" {
			return deferGate("worktree profile field " + field + " not supported by this runner yet")
		}
		if !live && !handoffSandboxSupported() {
			return deferGate("snapshot isolation unavailable on this host")
		}
		inputs, err := worktreeSnapshotInputs(ctx, root)
		if err != nil {
			return deferGate("cannot list the worktree snapshot inputs")
		}
		check.Snapshot = &checkset.Snapshot{Profile: check.WorktreeSnapshot.Profile, Inputs: inputs, Epoch: check.WorktreeSnapshot.Epoch}
	}
	if check.Snapshot == nil {
		return deferGate("no declared snapshot profile")
	}
	if !live && !handoffSandboxSupported() {
		return deferGate("snapshot isolation unavailable on this host")
	}
	runnerBuild, buildErr := handoffRunnerBuild()
	if buildErr != nil {
		return deferGate("cannot establish the handoff runner build identity")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return deferGate("cannot resolve physical checkout")
	}
	rawArgs, _ := check.Argv()
	if check.WorktreeSnapshot != nil && check.WorktreeSnapshot.Prehydrate != nil {
		rawArgs = slices.Concat(rawArgs, check.WorktreeSnapshot.Prehydrate.Run)
	}
	for _, arg := range rawArgs {
		if strings.Contains(arg, root) {
			return deferGate("a literal argument refers to the live checkout")
		}
	}
	identity, err := handoffSandboxIdentity(root)
	if err != nil {
		return deferGate("cannot establish the physical checkout identity")
	}
	store, err := resolve.StoreOf(root)
	if err != nil || store == "" {
		return deferGate("cannot resolve the canonical store")
	}
	loc, err := (resolve.Located{Checkout: root, Store: store}).Layout()
	if err != nil {
		return deferGate("cannot resolve the handoff cache")
	}
	cache := filepath.Join(loc.LocalRoot(), "handoff-v1")
	if err := layout.EnsureLocalDir(loc, cache); err != nil {
		return deferGate("cannot open the handoff cache")
	}

	var guard handoffLiveGuard
	if live {
		var err error
		guard, err = armHandoffLive(ctx, root, check.Snapshot.Inputs)
		if err != nil {
			return deferGate(err.Error(), true)
		}
		defer guard.close()
	}
	inputCtx, cancel := context.WithTimeout(ctx, gateInputTime)
	preserveMode := check.WorktreeSnapshot != nil
	var scratchFiles []string
	if preserveMode {
		scratchFiles = check.WorktreeSnapshot.Scratch.Files
	}
	state, proof, err := handoffGateInputs(inputCtx, root, check.Snapshot.Inputs, preserveMode, scratchFiles...)
	cancel()
	if errors.Is(err, errHandoffScratchShadowsInput) {
		return deferGate(err.Error())
	}
	if err != nil {
		return deferGate("cannot verify declared snapshot inputs within the key budget")
	}
	data, err := selectHandoffData(ctx, loc, check.WorktreeSnapshot, agent)
	if err != nil {
		return deferGate(err.Error())
	}
	system, err := prepareHandoffSystem(ctx, root, check.WorktreeSnapshot, rawArgs...)
	if err != nil {
		return deferGate(err.Error(), errors.Is(err, errHandoffXcodeUntrusted))
	}
	gitView, err := captureHandoffGit(ctx, root, check.WorktreeSnapshot)
	if err != nil {
		return deferGate(err.Error())
	}
	h := sha256.New()
	enc := json.NewEncoder(h)
	keyInputs := []string{"handoff-v1", runnerBuild, root, identity, state, proof, mode}
	if live {
		keyInputs = append(keyInputs, approval.Revision, grant)
	}
	if err := enc.Encode(keyInputs); err != nil {
		return deferGate("cannot encode the handoff key")
	}
	if err := enc.Encode(policy); err != nil {
		return deferGate("cannot encode the handoff policy")
	}
	if err := enc.Encode(check); err != nil {
		return deferGate("cannot encode the selected check")
	}
	for _, item := range data {
		if err := enc.Encode(item.digest); err != nil {
			return deferGate("cannot encode the handoff data")
		}
	}
	if system.Fingerprint != "" {
		if err := enc.Encode(system.Fingerprint); err != nil {
			return deferGate("cannot encode the handoff system inputs")
		}
	}
	if gitView.fingerprint != "" {
		if err := enc.Encode(gitView.fingerprint); err != nil {
			return deferGate("cannot encode the handoff Git view")
		}
	}
	for _, invocation := range plan {
		if err := enc.Encode([]any{invocation.argv, invocation.cwd}); err != nil {
			return deferGate("cannot encode the handoff plan")
		}
	}
	key := hex.EncodeToString(h.Sum(nil))
	verifyData := func() string {
		current, err := selectHandoffData(ctx, loc, check.WorktreeSnapshot, agent)
		if err != nil {
			return err.Error()
		}
		if !slices.Equal(data, current) {
			return "handoff data changed; retry the handoff"
		}
		return ""
	}
	verifyCurrent := func(when string) string {
		verify, cancel := context.WithTimeout(ctx, gateInputTime)
		defer cancel()
		if live {
			if approval.approval() != grant {
				return unapproved
			}
			inputs, err := worktreeSnapshotInputs(verify, root)
			if err != nil || !slices.Equal(inputs, check.Snapshot.Inputs) {
				return "checkout input set changed " + when
			}
		}
		currentState, currentProof, err := handoffGateInputs(verify, root, check.Snapshot.Inputs, preserveMode, scratchFiles...)
		if err != nil || currentState != state || currentProof != proof {
			return "checkout changed " + when
		}
		currentSystem, err := prepareHandoffSystem(ctx, root, check.WorktreeSnapshot, rawArgs...)
		if err != nil || currentSystem.Fingerprint != system.Fingerprint {
			return "system inputs changed " + when
		}
		currentGit, err := captureHandoffGit(ctx, root, check.WorktreeSnapshot)
		if err != nil || currentGit.fingerprint != gitView.fingerprint {
			return "Git view changed " + when
		}
		currentPolicy, _, err := configuredStageChecks(root, agent, "handoff")
		if err != nil || !reflect.DeepEqual(policy, currentPolicy) {
			return "handoff policy changed " + when
		}
		return verifyData()
	}
	finishLive := func() string {
		if !live {
			return ""
		}
		if reason := verifyCurrent("during the live-verified check"); reason != "" {
			return reason
		}
		if err := guard.finish(ctx, check.Snapshot.Inputs); err != nil {
			return err.Error()
		}
		if approval.approval() != grant {
			return unapproved
		}
		return ""
	}
	lockPath, slot := handoffGatePaths(cache, key)
	if err := layout.EnsureLocalDir(loc, filepath.Dir(slot)); err != nil {
		return deferGate("cannot open the handoff result directory")
	}
	pin, waited, err := pinHandoffStripe(ctx, lockPath+".pin")
	if err != nil {
		return deferGate("cannot register for the handoff lease")
	}
	defer pin.Close()
	var result StageFeedback
	for ctx.Err() == nil {
		busy, err := audit.TryLock(lockPath, func() error {
			// Re-select the input identity after waiting; a stale stop must not
			// report an owner's failure for a checkout that no longer matches.
			if waited || live {
				if reason := verifyCurrent("while waiting"); reason != "" {
					result = deferGate(reason, live)
					return nil
				}
			}
			room, err := retainHandoffResults(pin, slot, waited, time.Now())
			if err != nil {
				result = deferGate("cannot maintain the handoff cache")
				return nil
			}
			if !room {
				result = deferGate("handoff cache is full while other requests are waiting")
				return nil
			}
			// Holding the exclusive lease proves no prior owner still holds
			// it. An orphaned pending record is neither a pass nor a failure.
			if saved, ok := readGateResult(slot); ok && saved.Key == key && saved.Proof == proof && saved.Isolation == mode && !saved.Pending {
				if !waited {
					if reason := verifyData(); reason != "" {
						result = deferGate(reason)
						return nil
					}
				}
				if reason := finishLive(); reason != "" {
					result = deferGate(reason, true)
					return nil
				}
				diagnostic.LogHookCheck(ctx, root, root, "handoff", check.Name, "reused", time.Since(start), isolation...)
				result = feedback(saved.Failure)
				return nil
			}
			diagnostic.LogHookCheck(ctx, root, root, "handoff", check.Name, "miss", time.Since(start), isolation...)
			scratch := filepath.Join(cache, layout.TmpPath("snapshot-"+key[:3]))
			if err := removeSnapshotScratch(scratch); err != nil {
				result = deferGate("cannot clean up the previous snapshot: " + editRedact(err.Error()))
				return nil
			}
			if err := os.Mkdir(scratch, 0o700); err != nil {
				result = deferGate("cannot materialize the snapshot")
				return nil
			}
			cleanup := func() bool {
				if err := removeSnapshotScratch(scratch); err != nil {
					result = deferGate("cannot clean up the snapshot: " + editRedact(err.Error()))
					return false
				}
				return true
			}
			cleanupPending := true
			defer func() {
				if cleanupPending {
					cleanup()
				}
			}()
			prepared := handoffWorktreePlan{system: system, runtime: bundle}
			if bundle.archive != "" && runtime.GOOS != "darwin" {
				prepared.runtimeDir = filepath.Join(scratch, "runtime-snapshot")
				if err := bundle.extract(ctx, prepared.runtimeDir); err != nil {
					result = deferGate(err.Error())
					return nil
				}
			}
			if len(data) != 0 {
				if prepared.runtimeDir == "" {
					prepared.runtimeDir = filepath.Join(scratch, "runtime-snapshot")
					if err := os.Mkdir(prepared.runtimeDir, 0o700); err != nil {
						result = deferGate("cannot prepare the handoff data mount")
						return nil
					}
				}
				if err := materializeHandoffData(ctx, prepared.runtimeDir, data); err != nil {
					result = deferGate(err.Error())
					return nil
				}
			}
			if gitView.fingerprint != "" && !live {
				prepared.gitDir = filepath.Join(scratch, "git-view")
				if err := gitView.materialize(ctx, root, prepared.gitDir); err != nil {
					result = deferGate("cannot materialize the Git view within the snapshot budget")
					return nil
				}
			}
			if err := writeGateResult(loc, slot, gateResult{Key: key, Proof: proof, Isolation: mode, Pending: true}); err != nil {
				result = deferGate("cannot publish the handoff lease intent")
				return nil
			}
			terminal := false
			defer func() {
				if !terminal {
					_ = os.Remove(slot)
				}
			}()
			var failures []string
			for _, invocation := range plan {
				checkScratch := scratch
				if check.WorktreeSnapshot != nil {
					checkScratch, err = os.MkdirTemp(scratch, layout.ScratchDirPattern("check"))
					if err != nil {
						result = deferGate("cannot materialize the invocation snapshot")
						return nil
					}
				}
				var failure string
				if live {
					failure, err = executeHandoffLive(ctx, checkScratch, root, check.Snapshot.Inputs, invocation, prepared)
				} else {
					failure, err = executeHandoffSnapshot(ctx, checkScratch, root, proof, check.Snapshot.Inputs, invocation, prepared)
				}
				if err != nil {
					result = deferGate(err.Error(), live || errors.Is(err, errHandoffXcodeUntrusted))
					return nil
				}
				if failure != "" {
					failures = append(failures, failure)
				}
			}
			cleanupPending = false
			if !cleanup() {
				return nil
			}
			if ctx.Err() != nil {
				result = deferGate(mode+" check was interrupted", live)
				return nil
			}
			if reason := finishLive(); reason != "" {
				result = deferGate(reason, true)
				return nil
			}
			if !live {
				if reason := verifyCurrent("during the snapshot check"); reason != "" {
					result = deferGate(reason)
					return nil
				}
			}
			failure := editRedact(strings.Join(failures, "\n\n"))
			if err := writeGateResult(loc, slot, gateResult{Key: key, Proof: proof, Isolation: mode, Failure: failure}); err != nil {
				diagnostic.LogHookCheck(ctx, root, root, "handoff", check.Name, "cache_write_failed", time.Since(start))
			} else {
				terminal = true
			}
			outcome := "completed"
			if failure != "" {
				outcome = "failed"
			}
			diagnostic.LogHookCheck(ctx, root, root, "handoff", check.Name, outcome, time.Since(start), isolation...)
			result = feedback(failure)
			return nil
		})
		if err != nil {
			return deferGate("cannot acquire the handoff lease")
		}
		if !busy {
			return result
		}
		if !waited {
			diagnostic.LogHookCheck(ctx, root, root, "handoff", check.Name, "waiting", time.Since(start))
		}
		waited = true
		select {
		case <-ctx.Done():
		case <-time.After(25 * time.Millisecond):
		}
	}
	return deferGate("interrupted while waiting for the handoff lease")
}

func removeSnapshotScratch(scratch string) error {
	if err := filepath.WalkDir(scratch, func(path string, entry os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || !entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if mode := info.Mode(); mode&0o700 != 0o700 {
			return os.Chmod(path, mode|0o700)
		}
		return nil
	}); err != nil {
		return err
	}
	return os.RemoveAll(scratch)
}

func unsupportedWorktreeField(profile *checkset.WorktreeSnapshot) string {
	switch {
	case runtime.GOOS != "linux" && len(profile.Scratch.Seed) != 0:
		return "scratch.seed"
	case runtime.GOOS != "linux" && len(profile.Scratch.Files) != 0:
		return "scratch.files"
	case profile.Prehydrate != nil && runtime.GOOS != "linux":
		return "prehydrate"
	case profile.Git != nil && runtime.GOOS == "darwin":
		return "git"
	case runtime.GOOS != "linux" && len(profile.Data) != 0:
		return "data"
	default:
		return ""
	}
}

func handoffShellPath(s string) string {
	home, _ := os.UserHomeDir()
	if home != "" && filepath.IsAbs(s) {
		if rel, err := filepath.Rel(home, s); err == nil && filepath.IsLocal(rel) {
			return `"$HOME"/` + audit.ShellQuote(rel)
		}
	}
	return audit.ShellQuote(s)
}

func handoffManualCommand(root string, files []string, check checkset.Check) string {
	planRoot := root
	if check.Snapshot != nil {
		planRoot = gateRoot
	}
	snapshotWord := func(s string) string {
		if s == gateRoot {
			s = root
		} else if rel, ok := strings.CutPrefix(s, gateRoot+"/"); ok && filepath.IsLocal(rel) {
			s = filepath.Join(root, rel)
		}
		return handoffShellPath(s)
	}
	pathWord := handoffShellPath
	if check.Snapshot != nil {
		pathWord = snapshotWord
	}
	literalWord := func(arg string) string {
		if check.Snapshot != nil || !strings.Contains(arg, "{root}") {
			if check.Snapshot != nil || check.WorktreeSnapshot != nil {
				return snapshotWord(arg)
			}
			return handoffShellPath(arg)
		}
		parts := strings.Split(arg, "{root}")
		for i, part := range parts {
			parts[i] = audit.ShellQuote(part)
		}
		return strings.Join(parts, handoffShellPath(root))
	}
	var commands []string
	for _, invocation := range stageInvocationsRendered(planRoot, files, check, true, pathWord, literalWord) {
		if len(invocation.argv) == 0 {
			continue
		}
		commands = append(commands, "(cd "+pathWord(invocation.cwd)+" && "+strings.Join(invocation.argv, " ")+")")
	}
	return strings.Join(commands, " && ")
}

func handoffGatePaths(cache, key string) (lock, result string) {
	stripe := key[:3]
	return filepath.Join(cache, stripe+".json.lock"), filepath.Join(cache, stripe, key+".json")
}

func retainHandoffResults(pin *os.File, slot string, waited bool, now time.Time) (bool, error) {
	dir := filepath.Dir(slot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	var remove []string
	var candidates []os.FileInfo
	count, hasSlot := 0, false
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return false, err
		}
		count++
		current := entry.Name() == filepath.Base(slot)
		hasSlot = hasSlot || current
		if (!current || !waited) && now.Sub(info.ModTime()) >= gateRecordAge {
			remove = append(remove, entry.Name())
		} else if !current {
			candidates = append(candidates, info)
		}
	}
	reserve := 1
	if hasSlot && !slices.Contains(remove, filepath.Base(slot)) {
		reserve = 0
	}
	excess := count - len(remove) + reserve - gateRecordsPerStripe
	if excess > 0 {
		slices.SortStableFunc(candidates, func(a, b os.FileInfo) int { return a.ModTime().Compare(b.ModTime()) })
		for _, info := range candidates[:excess] {
			remove = append(remove, info.Name())
		}
	}
	if len(remove) == 0 {
		return true, nil
	}
	busy, err := withUnpinnedStripe(pin, func() error {
		for _, name := range remove {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				return err
			}
		}
		return nil
	})
	return !busy || hasSlot || count < gateRecordsPerStripe, err
}

func unavailableSnapshotCapability(goos string, required []string) string {
	for _, capability := range required {
		if goos == "darwin" || (goos == "linux" && capability == "mach_lookup") {
			return capability + " unavailable in " + goos + " snapshot"
		}
	}
	return ""
}

func handoffGateInputs(ctx context.Context, root string, inputs []string, preserveMode bool, scratchFiles ...string) (string, string, error) {
	if len(scratchFiles) > 0 {
		args := []string{"ls-files", "--cached", "-z", "--"}
		for _, file := range scratchFiles {
			args = append(args, ":(top,literal)"+file)
		}
		tracked, err := handoffGitOutput(ctx, root, args...)
		if err != nil {
			return "", "", err
		}
		if len(tracked) > 0 {
			return "", "", errHandoffScratchShadowsInput
		}
	}
	state, err := checkoutStateWithBudget(ctx, root)
	if err != nil {
		return "", "", err
	}
	proof, err := digestGateInputsWithMode(ctx, root, "", inputs, preserveMode)
	if err != nil {
		return "", "", err
	}
	if preserveMode {
		current, err := worktreeSnapshotInputs(ctx, root)
		if err != nil {
			return "", "", err
		}
		slices.Sort(current)
		want := slices.Clone(inputs)
		slices.Sort(want)
		if !slices.Equal(current, want) {
			return "", "", errors.New("snapshot input selection changed")
		}
	}
	return state, proof, nil
}

func digestGateInputs(ctx context.Context, root, dest string, inputs []string) (string, error) {
	return digestGateInputsWithMode(ctx, root, dest, inputs, false)
}

func digestGateInputsWithMode(ctx context.Context, root, dest string, inputs []string, preserveMode bool) (string, error) {
	src, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer src.Close()
	inputs = slices.Clone(inputs)
	slices.Sort(inputs)
	inputs = slices.Compact(inputs)
	h := sha256.New()
	budget := gateBytes
	for _, input := range inputs {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := copyGateInput(ctx, h, src, dest, input, budget, preserveMode)
		if err != nil {
			return "", err
		}
		budget -= n
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyGateInput(ctx context.Context, h io.Writer, src *os.Root, dest, input string, budget int, preserveMode bool) (int, error) {
	f, err := openGateInput(src, input)
	if err != nil {
		if preserveMode {
			if link, linkErr := src.Readlink(input); linkErr == nil {
				if len(link) > budget {
					return 0, errors.New("snapshot link exceeds the input budget")
				}
				_, _ = fmt.Fprintf(h, "%s\x00symlink\x00%d\x00%s", input, len(link), link)
				if dest != "" {
					path := filepath.Join(dest, input)
					if err := rejectHandoffSymlinkParents(dest, path); err != nil {
						return 0, err
					}
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						return 0, err
					}
					if err := os.Symlink(link, path); err != nil {
						return 0, err
					}
				}
				return len(link), nil
			}
		}
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(budget) {
		return 0, errors.New("snapshot input is not a regular file within budget")
	}
	mode := info.Mode().Perm()&0o100 | 0o400
	if preserveMode {
		mode = info.Mode().Perm()
	}
	_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\x00", input, mode, info.Size())
	output := &gateHashBudget{ctx: ctx, dst: h, remaining: int64(budget)}
	var target *os.File
	if dest != "" {
		path := filepath.Join(dest, input)
		if preserveMode {
			if err := rejectHandoffSymlinkParents(dest, path); err != nil {
				return 0, err
			}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return 0, err
		}
		target, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return 0, err
		}
		output.dst = io.MultiWriter(h, target)
	}
	n, err := io.Copy(output, io.LimitReader(f, info.Size()+1))
	if target != nil {
		if err == nil && preserveMode {
			err = target.Chmod(mode)
		}
		err = errors.Join(err, target.Close())
	}
	if err == nil && n != info.Size() {
		err = errors.New("snapshot input changed size")
	}
	return int(n), err
}

func rejectHandoffSymlinkParents(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return errors.New("snapshot path is outside its root")
	}
	for dir := filepath.Dir(path); dir != root; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("snapshot parent is not a directory")
		}
	}
	return nil
}

func readGateResult(path string) (gateResult, bool) {
	var result gateResult
	f, err := os.Open(path)
	if err != nil {
		return result, false
	}
	defer f.Close()
	err = json.NewDecoder(io.LimitReader(f, gateRecordBytes)).Decode(&result)
	return result, err == nil
}

func writeGateResult(p layout.Policy, path string, result gateResult) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if redact.LeakRule(result.Failure, home) != "" {
		return errors.New("handoff result failed the redaction guard")
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if len(data) >= gateRecordBytes {
		const suffix = "\n[handoff feedback truncated]"
		// JSON can expand each input byte up to six bytes.
		n := min(len(result.Failure), gateRecordBytes/6-len(suffix))
		for n > 0 && n < len(result.Failure) && !utf8.RuneStart(result.Failure[n]) {
			n--
		}
		result.Failure = result.Failure[:n] + suffix
		data, err = json.Marshal(result)
		if err != nil {
			return err
		}
		if len(data) >= gateRecordBytes {
			return errors.New("handoff result metadata exceeds the record limit")
		}
	}
	f, err := layout.CreateLocalTemp(p, "handoff-result", ".json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = f.Write(append(data, '\n'))
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// worktreeSnapshotInputs lists the files a worktree profile snapshots: tracked
// and untracked files that Git does not ignore and that exist on disk.
func worktreeSnapshotInputs(ctx context.Context, root string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, gateInputTime)
	defer cancel()
	out, err := handoffGitOutput(ctx, root, "ls-files", "-z", "--cached", "--others", "--exclude-standard",
		"--", ":(top)", ":(top,exclude).punt-labs/local", ":(top,exclude).punt-labs/ethos/sessions")
	if err != nil {
		return nil, err
	}
	var inputs []string
	seen := map[string]bool{}
	for _, name := range strings.Split(string(out), "\x00") {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil || info.IsDir() {
			continue
		}
		inputs = append(inputs, name)
	}
	if len(inputs) == 0 {
		return nil, errors.New("worktree has no snapshot inputs")
	}
	return inputs, nil
}
