package hook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

const handoffRuntimeArchiveBytes = 4 << 30

type handoffRuntime struct {
	archive  string
	platform checkset.SnapshotPlatform
	agent    string
}

func prepareHandoffRuntime(ctx context.Context, snapshot *checkset.WorktreeSnapshot, agent string) (handoffRuntime, error) {
	if snapshot.Runtime == nil {
		return handoffRuntime{}, nil
	}
	key := runtime.GOOS + "-" + runtime.GOARCH
	platform, ok := snapshot.Runtime.Platforms[key]
	if !ok {
		return handoffRuntime{}, fmt.Errorf("runtime-missing: no pinned bundle for %s; configure a verified platform bundle, then run ethos handoff fetch-runtime", key)
	}
	root, err := layout.MachineRoot()
	if err != nil {
		return handoffRuntime{}, errors.New("runtime-missing: cannot locate Ethos's runtime store; configure the user home and run ethos handoff fetch-runtime")
	}
	bundle := newHandoffRuntime(root, platform, agent)
	if err := bundle.verify(ctx); err != nil {
		return handoffRuntime{}, err
	}
	return bundle, nil
}

func newHandoffRuntime(root string, platform checkset.SnapshotPlatform, agent string) handoffRuntime {
	return handoffRuntime{archive: filepath.Join(root, "runtimes", platform.SHA256+".tar.gz"), platform: platform, agent: agent}
}

func (b handoffRuntime) remedy() string {
	install := "run ethos handoff fetch-runtime"
	if b.agent != "" && attribute.ValidateSlug(b.agent) == nil {
		install += " --agent=" + b.agent
	}
	install += " to install the pinned archive"
	if b.platform.URL != "" {
		return install + " from " + b.platform.URL
	}
	return install + " after adding a published URL, or install it at ~/.punt-labs/ethos/runtimes/" + filepath.Base(b.archive)
}

func (b handoffRuntime) open() (*os.File, error) {
	dir, err := os.OpenRoot(filepath.Dir(b.archive))
	if err != nil {
		return nil, fmt.Errorf("runtime-missing: runtime store unavailable; %s", b.remedy())
	}
	defer dir.Close()
	file, err := openGateInput(dir, filepath.Base(b.archive))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("runtime-missing: %s archive unavailable; %s", b.platform.Bundle, b.remedy())
	}
	if err != nil {
		return nil, fmt.Errorf("runtime-digest-mismatch: cannot safely open the archive; %s", b.remedy())
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > handoffRuntimeArchiveBytes {
		_ = file.Close()
		return nil, fmt.Errorf("runtime-digest-mismatch: archive is not a regular file within the size budget; %s", b.remedy())
	}
	return file, nil
}

func (b handoffRuntime) verify(ctx context.Context) error {
	file, err := b.open()
	if err != nil {
		return err
	}
	defer file.Close()
	h := sha256.New()
	_, err = io.Copy(&gateHashBudget{ctx: ctx, dst: h, remaining: handoffRuntimeArchiveBytes}, file)
	if ctx.Err() != nil {
		return errors.New("runtime verification interrupted; retry the handoff")
	}
	if err != nil || hex.EncodeToString(h.Sum(nil)) != b.platform.SHA256 {
		return b.digestMismatch()
	}
	return nil
}

func (b handoffRuntime) digestMismatch() error {
	return fmt.Errorf("runtime-digest-mismatch: archive does not match the pinned SHA-256; %s", b.remedy())
}
