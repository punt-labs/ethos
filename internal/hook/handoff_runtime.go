package hook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"

	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

func validateHandoffRuntime(ctx context.Context, snapshot *checkset.WorktreeSnapshot) error {
	key := runtime.GOOS + "-" + runtime.GOARCH
	platform, ok := snapshot.Runtime.Platforms[key]
	if !ok {
		return fmt.Errorf("runtime-missing: no pinned bundle for %s; configure a verified platform bundle", key)
	}
	root, err := layout.MachineRoot()
	if err != nil {
		return errors.New("runtime-missing: cannot locate Ethos's runtime store; configure the user home and install the pinned runtime")
	}
	archive := platform.SHA256 + ".tar.gz"
	location := "~/.punt-labs/ethos/runtimes/" + archive
	install := "install the pinned runtime archive at " + location
	if platform.URL != "" {
		install += " from " + platform.URL
	}
	dir, err := os.OpenRoot(filepath.Join(root, "runtimes"))
	if err != nil {
		return fmt.Errorf("runtime-missing: runtime store unavailable; %s", install)
	}
	defer dir.Close()
	file, err := openGateInput(dir, archive)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("runtime-missing: %s archive unavailable; %s", platform.Bundle, install)
	}
	if err != nil {
		return fmt.Errorf("runtime-digest-mismatch: cannot safely open the archive; %s", install)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("runtime-digest-mismatch: archive is not a regular file; %s", install)
	}
	h := sha256.New()
	n, err := io.Copy(&gateHashBudget{ctx: ctx, dst: h, remaining: math.MaxInt64}, file)
	if ctx.Err() != nil {
		return errors.New("runtime verification interrupted; retry the handoff")
	}
	if err != nil || n != info.Size() || hex.EncodeToString(h.Sum(nil)) != platform.SHA256 {
		return fmt.Errorf("runtime-digest-mismatch: archive does not match the pinned SHA-256; %s", install)
	}
	return nil
}
