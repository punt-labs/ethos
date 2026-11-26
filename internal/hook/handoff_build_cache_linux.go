package hook

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"golang.org/x/sys/unix"
)

func prepareHandoffBuildCache(ctx context.Context, store, digest string) (string, func() error, func(), error) {
	machine, err := layout.MachineRoot()
	if err != nil {
		return "", nil, nil, err
	}
	store, err = filepath.EvalSymlinks(store)
	if err != nil {
		return "", nil, nil, err
	}
	root := filepath.Join(filepath.Dir(machine), "local", "ethos", "handoff-cache", fmt.Sprintf("%x", sha256.Sum256([]byte(store))), digest)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", nil, nil, err
	}
	lease, _, err := pinHandoffLock(ctx, filepath.Join(root, "gocache.lock"), unix.LOCK_EX)
	if err != nil {
		return "", nil, nil, err
	}
	stage, err := os.MkdirTemp(root, ".gocache-")
	if err != nil {
		_ = lease.Close()
		return "", nil, nil, err
	}
	cleanup := func() {
		_ = os.RemoveAll(stage)
		_ = lease.Close()
	}
	persisted := filepath.Join(root, "gocache")
	private := filepath.Join(stage, "gocache")
	if _, err := os.Lstat(persisted); errors.Is(err, os.ErrNotExist) {
		err = os.Mkdir(private, 0o700)
		if err != nil {
			cleanup()
			return "", nil, nil, err
		}
	} else {
		if err == nil {
			err = validateHandoffBuildCache(ctx, persisted)
		}
		if err == nil {
			var dest *os.Root
			dest, err = os.OpenRoot(stage)
			if err == nil {
				remaining := int64(2 << 30)
				err = errors.Join(copyHandoffScratchSeed(root, "gocache", dest, "gocache", &remaining), dest.Close())
			}
		}
		if err != nil {
			cleanup()
			return "", nil, nil, err
		}
	}
	publish := func() error {
		if err := validateHandoffBuildCache(ctx, private); err != nil {
			return err
		}
		err := unix.Renameat2(unix.AT_FDCWD, private, unix.AT_FDCWD, persisted, unix.RENAME_EXCHANGE)
		if errors.Is(err, unix.ENOENT) {
			return os.Rename(private, persisted)
		}
		return err
	}
	return private, publish, cleanup, nil
}

func validateHandoffBuildCache(ctx context.Context, cache string) error {
	var entries int
	var total int64
	return filepath.WalkDir(cache, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("build cache contains a non-regular entry")
			}
			total += info.Size()
		}
		if entries > 250_000 || total > 2<<30 {
			return errors.New("build cache exceeds its size budget")
		}
		return nil
	})
}
