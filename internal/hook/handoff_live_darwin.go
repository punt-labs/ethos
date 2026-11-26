package hook

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/punt-labs/ethos/v4/internal/diagnostic"
)

func executeHandoffLive(ctx context.Context, _, root string, inputs []string, check editCheck, prepared handoffWorktreePlan) (string, error) {
	timeout := check.timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	base, err := darwinHandoffScratch(root)
	if err != nil {
		return "", err
	}
	run, err := os.MkdirTemp(base, "eh-live-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(run)
	roots := handoffRoots{checkout: root, scratch: run, temp: run, runtime: filepath.Join(run, "runtime")}
	if err := os.Mkdir(filepath.Join(run, "home"), 0o700); err != nil {
		return "", err
	}
	if prepared.runtime.archive != "" {
		if err := prepared.runtime.extract(ctx, roots.runtime); err != nil {
			return "", err
		}
	}
	env, _, err := configureDarwinWorktree(roots, root, check.policy.WorktreeSnapshot, prepared.runtime.platform)
	if err != nil {
		return "", err
	}
	argv, cwd, err := darwinHandoffInvocation(check, root, root, roots, inputs)
	if err != nil {
		return "", err
	}
	command := argv[0]
	if !strings.ContainsRune(command, '/') {
		command = darwinSystemTool(command, env)
		if command == "" {
			return "", fmt.Errorf("live-verified command %q is not on the declared PATH", argv[0])
		}
	}
	system := prepared.system
	if system.DeveloperDir != "" {
		env = slices.DeleteFunc(env, func(variable string) bool {
			return strings.HasPrefix(variable, "DEVELOPER_DIR=")
		})
		env = append(env, "DEVELOPER_DIR="+system.DeveloperDir)
	}
	if darwinXcodeTool(command) && system.DeveloperDir == "" {
		return "", errHandoffXcodeUntrusted
	}
	start := time.Now()
	var stdout, stderr editTail
	wait, err := startDarwinHandoffProcess(ctx, command, argv[1:], func(cmd *exec.Cmd) {
		cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = cwd, env, &stdout, &stderr
	})
	if err != nil {
		return "", fmt.Errorf("live-verified launch did not complete: %w", err)
	}
	diagnostic.LogHookCheck(ctx, root, cwd, "handoff", check.name, "launched", time.Since(start), checkset.IsolationLiveVerified)
	err = wait()
	if ctx.Err() != nil {
		return "", errors.New("live-verified check timed out or was interrupted")
	}
	return darwinHandoffCommandVerdict(err, check, stdout, stderr)
}
