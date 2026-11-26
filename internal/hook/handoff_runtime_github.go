package hook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

func runtimeGitHubHost(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil || u.Port() != "" && u.Port() != "443" {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "github.com", "api.github.com", "objects.githubusercontent.com":
		return true
	default:
		return false
	}
}

func runtimeGitHubRelease(raw string) (releaseURL, assetName string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || !runtimeGitHubHost(u) || !strings.EqualFold(u.Hostname(), "github.com") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if len(parts) != 6 || parts[2] != "releases" || parts[3] != "download" ||
		parts[0] == "" || parts[1] == "" || parts[4] == "" || parts[5] == "" {
		return "", "", false
	}
	name, err := url.PathUnescape(parts[5])
	if err != nil {
		return "", "", false
	}
	return "https://api.github.com/repos/" + parts[0] + "/" + parts[1] + "/releases/tags/" + parts[4], name, true
}

func runtimeGitHubToken(ctx context.Context) string {
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if token := strings.TrimSpace(os.Getenv(key)); token != "" {
			return token
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", "github.com").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func runtimeGitHubAsset(ctx context.Context, client *http.Client, releaseURL, assetName, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL, nil)
	if err != nil {
		return "", errors.New("invalid GitHub runtime release URL")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("could not look up GitHub runtime release")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("could not look up GitHub runtime release: HTTP %d", resp.StatusCode)
	}
	const maxRelease = 4 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRelease+1))
	var release struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"assets"`
	}
	if err != nil || len(body) > maxRelease || json.Unmarshal(body, &release) != nil {
		return "", errors.New("invalid GitHub runtime release response")
	}
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			u, err := url.Parse(asset.URL)
			if err != nil || !runtimeGitHubHost(u) || !strings.EqualFold(u.Hostname(), "api.github.com") {
				return "", errors.New("invalid GitHub runtime asset API URL")
			}
			return asset.URL, nil
		}
	}
	return "", fmt.Errorf("GitHub runtime release has no asset %q", assetName)
}
