package hook

import (
	"bytes"
	"context"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

func startDarwinHandoffProcess(ctx context.Context, executable string, args []string, configure func(*exec.Cmd)) (func() error, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	configure(cmd)
	cmd.WaitDelay = time.Second
	editProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() error {
		defer func() { _ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL) }()
		return cmd.Wait()
	}, nil
}

func darwinHandoffOutput(ctx context.Context, executable, dir string, env []string, args ...string) ([]byte, error) {
	var output bytes.Buffer
	wait, err := startDarwinHandoffProcess(ctx, executable, args, func(cmd *exec.Cmd) {
		cmd.Dir, cmd.Env, cmd.Stdout = dir, env, &output
	})
	if err != nil {
		return nil, err
	}
	if err := wait(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
