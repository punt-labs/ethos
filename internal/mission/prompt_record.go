package mission

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// A consent is installed before its immutable prompt, under the prompt lock.
// Interruption can leave a consent without a payload, never a payload whose
// eligibility is inferred from a later request's mode. Repeating the exact
// recording completes that preparation; publication alone refuses missing data.
type promptConsent struct {
	Version    int    `yaml:"version"`
	Mission    string `yaml:"mission"`
	Delegation string `yaml:"delegation"`
	SHA256     string `yaml:"sha256"`
	Publish    bool   `yaml:"publish"`
}

func promptDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func promptConsentPath(p layout.Policy, id, delegation string) string {
	return filepath.Join(filepath.Dir(delegationPromptPath(p, id, delegation)), "prompt-consent.yaml")
}

// Prompt locks follow caller-held mission/delegation locks. Retry takes only
// this lock and never acquires authority or stream locks inside it.
func withPromptLock(p layout.Policy, id, delegation string, fn func() error) error {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id || delegation == "" || delegation == "." || delegation == ".." || filepath.Base(delegation) != delegation {
		return fmt.Errorf("invalid prompt identity")
	}
	path, err := p.LockPath("delegation-prompt", id+"/"+delegation)
	if err != nil {
		return err
	}
	if err := layout.EnsureLocalDir(p, filepath.Dir(path)); err != nil {
		return err
	}
	release, err := acquireDelegationLockPath(path)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// Do not follow redirected ancestors or open FIFOs/devices as prompt evidence.
func readPromptArtifact(path string) ([]byte, error) {
	for name := path; ; name = filepath.Dir(name) {
		if err := rejectSymlink(name); err != nil {
			return nil, err
		}
		if filepath.Dir(name) == name {
			break
		}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("nonregular prompt artifact: %s", filepath.Base(path))
	}
	return os.ReadFile(path)
}

func readPromptConsent(p layout.Policy, id, delegation string) (*promptConsent, error) {
	data, err := readPromptArtifact(promptConsentPath(p, id, delegation))
	if err != nil || data == nil {
		return nil, err // Missing/legacy consent is private, not current-mode consent.
	}
	var consent promptConsent
	if err := strictYAML(data, &consent); err != nil {
		return nil, fmt.Errorf("invalid prompt consent: %w", err)
	}
	digest, err := hex.DecodeString(consent.SHA256)
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != consent.SHA256 || consent.Version != 1 || consent.Mission != id || consent.Delegation != delegation {
		return nil, fmt.Errorf("invalid prompt consent identity/digest/version")
	}
	return &consent, nil
}

// Equal copies are idempotent, never replacements. All callers hold the prompt
// lock, including across linked checkouts; staging always belongs to S.
func installPromptArtifact(p layout.Policy, path string, body []byte) (retErr error) {
	old, err := readPromptArtifact(path)
	if err != nil {
		return err
	}
	if old != nil {
		if !bytes.Equal(old, body) {
			return fmt.Errorf("conflicting prompt artifact: %s; retained copies unchanged", filepath.Base(path))
		}
		return syncOwnedAncestors(p, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := layout.CreateLocalTemp(p, "prompt", ".data")
	if err != nil {
		return err
	}
	defer func() {
		if err := f.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			retErr = errors.Join(retErr, err)
		}
		if err := os.Remove(f.Name()); err != nil {
			retErr = errors.Join(retErr, err)
		}
	}()
	if n, err := f.Write(body); err != nil {
		return err
	} else if n != len(body) {
		return io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// No-clobber installation also protects against an editor that does not
	// take our lock. EXDEV refuses; never stage beside the published copy.
	if err := os.Link(f.Name(), path); err != nil {
		return fmt.Errorf("installing immutable prompt artifact: %w", err)
	}
	return syncOwnedAncestors(p, path)
}

func retainDelegationPrompt(p layout.Policy, id, delegation string, body []byte) error {
	return withPromptLock(p, id, delegation, func() error {
		path := delegationPromptPath(p, id, delegation)
		if err := layout.EnsureLocalDir(p, filepath.Dir(path)); err != nil {
			return err
		}
		consent, err := readPromptConsent(p, id, delegation)
		if err != nil {
			return err
		}
		old, err := readPromptArtifact(path)
		if err != nil {
			return err
		}
		if old != nil && !bytes.Equal(old, body) {
			return fmt.Errorf("conflicting retained delegation prompt")
		}
		if consent != nil && consent.SHA256 != promptDigest(body) {
			return fmt.Errorf("delegation prompt conflicts with captured consent")
		}
		storeDir := filepath.Join(p.RecordRoot(), "missions", id, "delegations", delegation)
		legacy, err := readPromptArtifact(filepath.Join(storeDir, "prompt.md"))
		if err != nil {
			return err
		}
		if legacy != nil && !bytes.Equal(legacy, body) {
			return fmt.Errorf("conflicting published delegation prompt")
		}
		record, err := readPromptArtifact(filepath.Join(storeDir, "record.yaml"))
		if err != nil {
			return err
		}
		if consent == nil && old == nil && legacy == nil && record == nil {
			// Existing authority or retained/legacy bytes cannot gain consent
			// by calling the new writer under full. This is not a backfill.
			consent = &promptConsent{Version: 1, Mission: id, Delegation: delegation, SHA256: promptDigest(body), Publish: p.RecordMode() == layout.RecordFull}
			data, err := yaml.Marshal(consent)
			if err != nil {
				return err
			}
			data, err = guardWrite(promptConsentPath(p, id, delegation), data)
			if err != nil {
				return err
			}
			if err := installPromptArtifact(p, promptConsentPath(p, id, delegation), data); err != nil {
				return err
			}
		}
		if consent != nil {
			if err := syncOwnedAncestors(p, promptConsentPath(p, id, delegation)); err != nil {
				return err
			}
		}
		if err := installPromptArtifact(p, path, body); err != nil {
			return err
		}
		published := filepath.Join(p.CheckoutRecordRoot(), "missions", id, "delegations", delegation, "prompt.md")
		copy, err := readPromptArtifact(published)
		if err != nil {
			return fmt.Errorf("prompt publication pending; local payload retained: %w", err)
		}
		if copy != nil && !bytes.Equal(copy, body) {
			return fmt.Errorf("conflicting published delegation prompt; local payload retained")
		}
		return publishPromptLocked(p, id, delegation, consent)
	})
}

func publishPromptLocked(p layout.Policy, id, delegation string, consent *promptConsent) error {
	if consent == nil || !consent.Publish {
		return nil
	}
	body, err := readPromptArtifact(delegationPromptPath(p, id, delegation))
	if err != nil {
		return err
	}
	if body == nil || !utf8.Valid(body) || promptDigest(body) != consent.SHA256 {
		return fmt.Errorf("prompt publication pending: retained payload missing or conflicts with captured consent")
	}
	for _, path := range []string{promptConsentPath(p, id, delegation), delegationPromptPath(p, id, delegation)} {
		if err := syncOwnedAncestors(p, path); err != nil {
			return fmt.Errorf("confirming retained prompt durability: %w", err)
		}
	}
	path := filepath.Join(p.CheckoutRecordRoot(), "missions", id, "delegations", delegation, "prompt.md")
	legacyPath := filepath.Join(p.RecordRoot(), "missions", id, "delegations", delegation, "prompt.md")
	if legacyPath != path {
		legacy, err := readPromptArtifact(legacyPath)
		if err != nil {
			return err
		}
		if legacy != nil && !bytes.Equal(legacy, body) {
			return fmt.Errorf("conflicting published delegation prompt; local payload retained")
		}
	}
	guarded, err := guardWrite(path, body)
	if err != nil {
		return err
	}
	if !bytes.Equal(guarded, body) {
		return fmt.Errorf("prompt publication guard changed captured payload")
	}
	if err := installPromptArtifact(p, path, guarded); err != nil {
		return fmt.Errorf("prompt publication pending; local payload and consent retained: %w", err)
	}
	return nil
}

// RetryPromptPublications completes only new prompts' captured full consent.
// It uses the caller's immutable S/C policy, not the current mode, and does not
// query or mutate Git's index. Missing consent never authorizes publication.
// Call from an existing seal/manual operation, outside any prompt lock.
func RetryPromptPublications(p layout.Policy) error {
	if p.RecordRoot() == "" {
		return fmt.Errorf("prompt publication requires a valid repository policy")
	}
	root := filepath.Join(p.LocalRoot(), "trace", "missions")
	missions, err := promptDirectories(root)
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range missions {
		delegations, err := promptDirectories(filepath.Join(root, id, "delegations"))
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, delegation := range delegations {
			err := withPromptLock(p, id, delegation, func() error {
				consent, err := readPromptConsent(p, id, delegation)
				if err != nil {
					return err
				}
				return publishPromptLocked(p, id, delegation, consent)
			})
			if err != nil {
				failures = append(failures, fmt.Errorf("mission %s delegation %s: %w", id, delegation, err))
			}
		}
	}
	return errors.Join(failures...)
}

func promptDirectories(path string) ([]string, error) {
	for name := path; ; name = filepath.Dir(name) {
		if err := rejectSymlink(name); err != nil {
			return nil, err
		}
		if filepath.Dir(name) == name {
			break
		}
	}
	entries, err := os.ReadDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink in retained prompt directories")
		}
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}
