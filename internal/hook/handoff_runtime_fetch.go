package hook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// FetchHandoffRuntimes installs the current platform's pinned bundles for the
// selected handoff policy. The handoff gate does not call it.
func FetchHandoffRuntimes(ctx context.Context, loc resolve.Located, agent string) ([]string, error) {
	bundles, err := handoffRuntimeBundles(loc, agent)
	if err != nil {
		return nil, err
	}
	client := handoffRuntimeHTTPClient()
	var installed []string
	for _, bundle := range bundles {
		if err := bundle.fetch(ctx, client); err != nil {
			return nil, err
		}
		installed = append(installed, bundle.archive)
	}
	return installed, nil
}

// InstallHandoffRuntimeFile verifies a local archive against the selected pin without downloading.
func InstallHandoffRuntimeFile(ctx context.Context, loc resolve.Located, agent, path string) ([]string, error) {
	bundles, err := handoffRuntimeBundles(loc, agent)
	if err != nil {
		return nil, err
	}
	if len(bundles) != 1 {
		return nil, errors.New("--file requires a handoff policy with a single distinct runtime digest")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("--file requires a regular runtime archive")
	}
	input, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer input.Close()
	bundle := bundles[0]
	if err := bundle.install(ctx, input, "local"); err != nil {
		return nil, err
	}
	return []string{bundle.archive}, nil
}

func handoffRuntimeBundles(loc resolve.Located, agent string) ([]handoffRuntime, error) {
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
	seen := make(map[string]int)
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
		bundle := newHandoffRuntime(global, platform, agent)
		if i, ok := seen[bundle.archive]; !ok {
			seen[bundle.archive] = len(bundles)
			bundles = append(bundles, bundle)
		} else if bundles[i].platform.URL == "" && bundle.platform.URL != "" {
			bundles[i] = bundle
		}
	}
	if len(bundles) == 0 {
		return nil, errors.New("selected handoff policy declares no runtime bundles")
	}
	return bundles, nil
}

func handoffRuntimeHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: checkHandoffRuntimeAddress}
	transport := &http.Transport{
		DialContext: dialer.DialContext, ForceAttemptHTTP2: true,
		TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: 90 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 || req.URL.Scheme != "https" || req.URL.User != nil {
				return errors.New("runtime asset redirect refused")
			}
			if !runtimeGitHubHost(req.URL) {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}
}

var handoffCarrierNAT = netip.MustParsePrefix("100.64.0.0/10")

func checkHandoffRuntimeAddress(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || handoffCarrierNAT.Contains(ip) {
		return errors.New("runtime asset address is not public")
	}
	return nil
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
	assetURL, token := b.platform.URL, ""
	if releaseURL, assetName, ok := runtimeGitHubRelease(assetURL); ok {
		token = runtimeGitHubToken(ctx)
		if token != "" {
			var err error
			assetURL, err = runtimeGitHubAsset(ctx, client, releaseURL, assetName, token)
			if err != nil {
				return err
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return errors.New("invalid published runtime asset URL")
	}
	if token != "" && runtimeGitHubHost(req.URL) {
		req.Header.Set("Accept", "application/octet-stream")
		req.Header.Set("Authorization", "Bearer "+token)
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
	return b.install(ctx, resp.Body, "published")
}

func (b handoffRuntime) install(ctx context.Context, input io.Reader, source string) error {
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
	if _, err := io.Copy(&gateHashBudget{ctx: ctx, dst: io.MultiWriter(stage, h), remaining: handoffRuntimeArchiveBytes}, input); err != nil {
		return fmt.Errorf("could not read %s runtime bundle %q: %w", source, b.platform.Bundle, err)
	}
	if actual := hex.EncodeToString(h.Sum(nil)); actual != b.platform.SHA256 {
		return fmt.Errorf("%w (expected SHA-256 %s, got %s)", b.digestMismatch(), b.platform.SHA256, actual)
	}
	if err := errors.Join(stage.Sync(), stage.Close()); err != nil {
		return err
	}
	return os.Rename(stage.Name(), b.archive)
}
