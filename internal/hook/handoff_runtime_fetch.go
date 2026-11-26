package hook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// FetchHandoffRuntimes installs the current platform's pinned bundles for the
// selected handoff policy. The handoff gate does not call it.
func FetchHandoffRuntimes(ctx context.Context, loc resolve.Located, agent string) ([]string, error) {
	if agent != "" {
		if err := attribute.ValidateSlug(agent); err != nil {
			return nil, err
		}
	}
	checks, _, err := stageChecksForLocation(loc, agent, "handoff")
	if err != nil {
		return nil, err
	}
	global, err := layout.MachineRoot()
	if err != nil {
		return nil, err
	}
	key := runtime.GOOS + "-" + runtime.GOARCH
	var bundles []handoffRuntime
	for _, check := range checks {
		if check.InvalidSnapshotSidecar {
			return nil, errors.New("invalid snapshot sidecar")
		}
		profile := check.WorktreeSnapshot
		if profile == nil || profile.Runtime == nil {
			continue
		}
		platform, ok := profile.Runtime.Platforms[key]
		if !ok {
			return nil, fmt.Errorf("runtime-missing: check %q has no pinned bundle for %s", check.Name, key)
		}
		bundles = append(bundles, newHandoffRuntime(global, platform, agent))
	}
	if len(bundles) == 0 {
		return nil, errors.New("selected handoff policy declares no runtime bundles")
	}
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 || req.URL.Scheme != "https" || req.URL.User != nil {
				return errors.New("runtime asset redirect refused")
			}
			return nil
		},
	}
	var installed []string
	seen := make(map[string]bool)
	for _, bundle := range bundles {
		if seen[bundle.archive] {
			continue
		}
		if err := bundle.fetch(ctx, client); err != nil {
			return nil, err
		}
		installed = append(installed, bundle.archive)
		seen[bundle.archive] = true
	}
	return installed, nil
}

func (b handoffRuntime) fetch(ctx context.Context, client *http.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := b.verify(ctx); err == nil {
		return nil
	}
	if b.platform.URL == "" {
		return fmt.Errorf("runtime-missing: no asset URL for %q; %s", b.platform.Bundle, b.remedy())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.platform.URL, nil)
	if err != nil {
		return errors.New("invalid published runtime asset URL")
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("could not fetch published runtime bundle %q", b.platform.Bundle)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("could not fetch published runtime bundle %q: HTTP %d", b.platform.Bundle, resp.StatusCode)
	}
	dir := filepath.Dir(b.archive)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	stage, err := os.CreateTemp(dir, layout.DotTempPattern("fetch-runtime"))
	if err != nil {
		return err
	}
	defer os.Remove(stage.Name())
	defer stage.Close()
	h := sha256.New()
	if _, err := io.Copy(&gateHashBudget{ctx: ctx, dst: io.MultiWriter(stage, h), remaining: handoffRuntimeArchiveBytes}, resp.Body); err != nil {
		return fmt.Errorf("could not read published runtime bundle %q: %w", b.platform.Bundle, err)
	}
	if hex.EncodeToString(h.Sum(nil)) != b.platform.SHA256 {
		return b.digestMismatch()
	}
	if err := errors.Join(stage.Sync(), stage.Close()); err != nil {
		return err
	}
	return os.Rename(stage.Name(), b.archive)
}
