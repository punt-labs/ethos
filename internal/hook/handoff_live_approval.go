package hook

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

type liveApprovalPolicy struct {
	Repo, Slug, Revision string
	checkout             string
	checks               []checkset.Check
}

type liveApprovalRecord struct {
	Repo, Slug, Revision, Grant string
}

func ResolveHandoffLivePolicy(checkout, slug string) (liveApprovalPolicy, error) {
	checks, _, err := configuredStageChecks(checkout, "", "handoff")
	if err != nil {
		return liveApprovalPolicy{}, err
	}
	for _, check := range checks {
		if check.HandoffPolicySlug == slug && check.WorktreeSnapshot != nil && check.WorktreeSnapshot.Isolation == checkset.IsolationLiveVerified {
			policy, err := newLiveApprovalPolicy(checkout, slug, check.HandoffPolicyRevision)
			policy.checks = checks
			return policy, err
		}
	}
	return liveApprovalPolicy{}, fmt.Errorf("selected checkset %q has no valid live-verified check", slug)
}

func ResolveHandoffLiveRevocation(checkout, slug string) (liveApprovalPolicy, error) {
	return newLiveApprovalPolicy(checkout, slug, "")
}

func newLiveApprovalPolicy(checkout, slug, revision string) (liveApprovalPolicy, error) {
	if err := attribute.ValidateSlug(slug); err != nil {
		return liveApprovalPolicy{}, err
	}
	_, common, err := resolve.HandGitDirs(checkout)
	if err != nil {
		return liveApprovalPolicy{}, err
	}
	repo, err := filepath.EvalSymlinks(common)
	if err != nil || !filepath.IsAbs(repo) {
		return liveApprovalPolicy{}, errors.New("cannot resolve the common Git directory")
	}
	physical, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		return liveApprovalPolicy{}, err
	}
	return liveApprovalPolicy{Repo: repo, Slug: slug, Revision: revision, checkout: physical}, nil
}

func (p liveApprovalPolicy) Description() (string, error) {
	data, err := json.MarshalIndent(p.checks, "", "  ")
	return editRedact(string(data)), err
}

func (p liveApprovalPolicy) pinPath(create bool) (string, error) {
	global, err := layout.MachineRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(global, "live-approvals")
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
	}
	physical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(p.checkout, physical); err != nil || filepath.IsLocal(rel) {
		return "", errors.New("live approval directory must be outside the checkout")
	}
	info, err := os.Lstat(physical)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("live approval directory is not private")
	}
	key := sha256.Sum256([]byte(p.Repo + "\x00" + p.Slug))
	return filepath.Join(physical, fmt.Sprintf("%x.json", key)), nil
}

func (p liveApprovalPolicy) approval() string {
	if p.Revision == "" {
		return ""
	}
	path, err := p.pinPath(false)
	if err != nil {
		return ""
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	var record liveApprovalRecord
	if err := json.NewDecoder(io.LimitReader(file, 16<<10)).Decode(&record); err != nil ||
		record.Repo != p.Repo || record.Slug != p.Slug || record.Revision != p.Revision {
		return ""
	}
	return record.Grant
}

func (p liveApprovalPolicy) Approve() error {
	if p.Revision == "" {
		return errors.New("no resolved live policy revision")
	}
	path, err := p.pinPath(true)
	if err != nil {
		return err
	}
	return updateLiveApproval(path, func() error {
		tmp, err := os.CreateTemp(filepath.Dir(path), layout.DotTempPattern("approve-live"))
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		if err := json.NewEncoder(tmp).Encode(liveApprovalRecord{p.Repo, p.Slug, p.Revision, rand.Text()}); err != nil {
			return err
		}
		if err := errors.Join(tmp.Sync(), tmp.Close()); err != nil {
			return err
		}
		return os.Rename(tmp.Name(), path)
	})
}

func (p liveApprovalPolicy) Revoke() error {
	path, err := p.pinPath(false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return updateLiveApproval(path, func() error {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	})
}

func updateLiveApproval(path string, change func() error) error {
	busy, err := audit.TryLock(path+".lock", change)
	if busy && err == nil {
		return errors.New("live approval is being updated; retry")
	}
	return err
}
