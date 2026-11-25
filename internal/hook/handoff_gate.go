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
	"slices"
	"strings"
	"time"

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

type gateResult struct {
	Key     string
	Proof   string
	Failure string
	Pending bool `json:",omitempty"`
}

func runHandoffGate(ctx context.Context, root, agent string, files []string, check checkset.Check, policy []checkset.Check) StageFeedback {
	plan := stageInvocationsAt(gateRoot, files, check, true)
	if len(plan) == 0 {
		return StageFeedback{}
	}
	start := time.Now()
	feedback := func(message string) StageFeedback {
		if check.OnFail == "advisory" {
			return StageFeedback{Advisory: message}
		}
		return StageFeedback{Blocking: message}
	}
	deferGate := func(reason string) StageFeedback {
		diagnostic.LogHookCheck(ctx, root, root, "handoff", check.Name, "deferred", time.Since(start))
		argv, _ := check.Argv()
		message := fmt.Sprintf("handoff checks deferred (%s): %s. Run the explicit project gate: %s", check.Name, reason, strings.Join(argv, " "))
		f := feedback(message)
		if f.Blocking != "" {
			f.Notice = message
		}
		return f
	}
	if check.Snapshot == nil {
		return deferGate("no declared snapshot profile")
	}
	if !handoffSandboxSupported() {
		return deferGate("snapshot isolation unavailable on this host")
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return deferGate("cannot resolve physical checkout")
	}
	rawArgs, _ := check.Argv()
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

	inputCtx, cancel := context.WithTimeout(ctx, gateInputTime)
	state, proof, err := handoffGateInputs(inputCtx, root, check.Snapshot.Inputs)
	cancel()
	if err != nil {
		return deferGate("cannot verify declared snapshot inputs within the key budget")
	}
	h := sha256.New()
	enc := json.NewEncoder(h)
	if err := enc.Encode([]string{"handoff-v1", root, identity, state, proof}); err != nil {
		return deferGate("cannot encode the handoff key")
	}
	if err := enc.Encode(policy); err != nil {
		return deferGate("cannot encode the handoff policy")
	}
	if err := enc.Encode(check); err != nil {
		return deferGate("cannot encode the selected check")
	}
	for _, invocation := range plan {
		if err := enc.Encode([]any{invocation.argv, invocation.cwd}); err != nil {
			return deferGate("cannot encode the handoff plan")
		}
	}
	key := hex.EncodeToString(h.Sum(nil))
	verifyCurrent := func(when string) string {
		verify, cancel := context.WithTimeout(ctx, gateInputTime)
		defer cancel()
		currentState, currentProof, err := handoffGateInputs(verify, root, check.Snapshot.Inputs)
		if err != nil || currentState != state || currentProof != proof {
			return "checkout changed " + when
		}
		currentPolicy, _, err := configuredStageChecks(root, agent, "handoff")
		if err != nil || !reflect.DeepEqual(policy, currentPolicy) {
			return "handoff policy changed " + when
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
			if waited {
				if reason := verifyCurrent("while waiting"); reason != "" {
					result = deferGate(reason)
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
			if saved, ok := readGateResult(slot); ok && saved.Key == key && saved.Proof == proof && !saved.Pending {
				diagnostic.LogHookCheck(ctx, root, root, "handoff", check.Name, "reused", time.Since(start))
				result = feedback(saved.Failure)
				return nil
			}
			diagnostic.LogHookCheck(ctx, root, root, "handoff", check.Name, "miss", time.Since(start))
			scratch := filepath.Join(cache, layout.TmpPath("snapshot-"+key[:3]))
			if err := os.RemoveAll(scratch); err != nil {
				result = deferGate("cannot clean up the previous snapshot")
				return nil
			}
			if err := os.Mkdir(scratch, 0o700); err != nil {
				result = deferGate("cannot materialize the snapshot")
				return nil
			}
			defer func() { _ = os.RemoveAll(scratch) }()
			if err := writeGateResult(loc, slot, gateResult{Key: key, Proof: proof, Pending: true}); err != nil {
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
				failure, err := executeHandoffSnapshot(ctx, scratch, root, proof, check.Snapshot.Inputs, invocation)
				if err != nil {
					result = deferGate(err.Error())
					return nil
				}
				if failure != "" {
					failures = append(failures, failure)
				}
			}
			if ctx.Err() != nil {
				result = deferGate("snapshot check was interrupted")
				return nil
			}
			if reason := verifyCurrent("during the snapshot check"); reason != "" {
				result = deferGate(reason)
				return nil
			}
			failure := editRedact(strings.Join(failures, "\n\n"))
			if err := writeGateResult(loc, slot, gateResult{Key: key, Proof: proof, Failure: failure}); err != nil {
				diagnostic.LogHookCheck(ctx, root, root, "handoff", check.Name, "cache_write_failed", time.Since(start))
			} else {
				terminal = true
			}
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

func gatePlanInputsPresent(snapshot, cwd string, argv []string) bool {
	for _, arg := range append(slices.Clone(argv), cwd) {
		if arg != gateRoot && !strings.HasPrefix(arg, gateRoot+"/") {
			continue
		}
		rel := strings.TrimPrefix(arg, gateRoot)
		if _, err := os.Stat(filepath.Join(snapshot, rel)); err != nil {
			return false
		}
	}
	return true
}

func handoffGateInputs(ctx context.Context, root string, inputs []string) (string, string, error) {
	state, err := checkoutStateWithBudget(ctx, root)
	if err != nil {
		return "", "", err
	}
	proof, err := digestGateInputs(ctx, root, "", inputs)
	return state, proof, err
}

func digestGateInputs(ctx context.Context, root, dest string, inputs []string) (string, error) {
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
		n, err := copyGateInput(ctx, h, src, dest, input, budget)
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

func copyGateInput(ctx context.Context, h io.Writer, src *os.Root, dest, input string, budget int) (int, error) {
	f, err := openGateInput(src, input)
	if err != nil {
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
	_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\x00", input, mode, info.Size())
	output := &gateHashBudget{ctx: ctx, dst: h, remaining: int64(budget)}
	var target *os.File
	if dest != "" {
		path := filepath.Join(dest, input)
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
		err = errors.Join(err, target.Close())
	}
	if err == nil && n != info.Size() {
		err = errors.New("snapshot input changed size")
	}
	return int(n), err
}

func readGateResult(path string) (gateResult, bool) {
	var result gateResult
	f, err := os.Open(path)
	if err != nil {
		return result, false
	}
	defer f.Close()
	err = json.NewDecoder(io.LimitReader(f, 512<<10)).Decode(&result)
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
	f, err := layout.CreateLocalTemp(p, "handoff-result", ".json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	err = json.NewEncoder(f).Encode(result)
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
