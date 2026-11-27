package hook

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

func openHandoffRunnerExecutable() (*os.File, error) {
	path, err := os.Executable()
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if err := checkDarwinRunnerExecutable(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func checkDarwinRunnerExecutable(file *os.File) error {
	symbol, err := purego.Dlsym(purego.RTLD_DEFAULT, "proc_pidinfo")
	if err != nil {
		return err
	}
	// sys/proc_info.h: proc_regionwithpathinfo through the backing vnode's device and inode.
	var region struct {
		_      [96]byte
		device uint32
		_      [4]byte
		inode  uint64
		_      [1160]byte
	}
	var pidInfo func(pid, flavor int32, address uint64, region unsafe.Pointer, size int32) int32
	purego.RegisterFunc(&pidInfo, symbol)
	const procPIDRegionPathInfo = 8
	pc := reflect.ValueOf(checkDarwinRunnerExecutable).Pointer()
	size := int32(unsafe.Sizeof(region))
	if pidInfo(int32(os.Getpid()), procPIDRegionPathInfo, uint64(pc), unsafe.Pointer(&region), size) != size {
		return errors.New("cannot identify mapped Darwin runner executable")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return err
	}
	if uint32(stat.Dev) != region.device || stat.Ino != region.inode {
		return errors.New("runner executable no longer matches the running Darwin image")
	}
	return nil
}

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
