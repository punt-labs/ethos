package hook

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

func cachedHandoff(ctx context.Context, store, root string, checks []checkset.Check, invocations []editCheck, run func() StageFeedback) StageFeedback {
	state, err := checkoutState(root)
	if err != nil {
		return run()
	}
	type command struct {
		Cwd  string
		Argv []string
	}
	commands := make([]command, 0, len(invocations))
	for _, invocation := range invocations {
		commands = append(commands, command{invocation.cwd, invocation.argv})
	}
	key, err := json.Marshal(struct {
		State    string
		Checks   []checkset.Check
		Commands []command
	}{state, checks, commands})
	if err != nil {
		return run()
	}
	policy, err := layout.NewRepository(store, root, layout.RecordBlame)
	if err != nil {
		return run()
	}
	dir := filepath.Join(policy.LocalRoot(), "handoff-results", "v1")
	if err := layout.EnsureLocalDir(policy, dir); err != nil {
		return run()
	}
	path := filepath.Join(dir, fmt.Sprintf("%x.json", sha256.Sum256(key)))
	var feedback StageFeedback
	executed := false
	for {
		busy, err := audit.TryLock(path+".lock", func() error {
			current, stateErr := checkoutState(root)
			if stateErr != nil || current != state {
				feedback = run()
				executed = true
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr == nil && json.Unmarshal(data, &feedback) == nil {
				diagnostic.FromContext(ctx).Info("reused handoff result")
				return nil
			}
			feedback = run()
			executed = true
			if !errors.Is(readErr, os.ErrNotExist) || feedback.deferred || ctx.Err() != nil {
				return nil
			}
			if after, err := checkoutState(root); err != nil || after != state {
				return nil
			}
			data, err := json.Marshal(feedback)
			if err != nil {
				return err
			}
			return audit.WriteLocalAtomic(policy, path, data)
		})
		if err != nil {
			diagnostic.FromContext(ctx).Info("handoff result cache unavailable", "error_type", diagnostic.ErrorType(err))
			if !executed {
				return run()
			}
		}
		if !busy {
			return feedback
		}
		select {
		case <-ctx.Done():
			return StageFeedback{Blocking: "handoff checks deferred: another stop is running checks for this checkout state", deferred: true}
		case <-time.After(100 * time.Millisecond):
		}
	}
}
