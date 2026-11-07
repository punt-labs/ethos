package mission

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// LoadDelegationPrompt unions retained detail and supported published/legacy
// copies. Absence is distinct from a conflict or an unreadable optional source.
func (s *Store) LoadDelegationPrompt(id, delegation string) ([]byte, error) {
	if s.policyErr != nil {
		return nil, s.policyErr
	}
	paths := []string{filepath.Join(DelegationDir(s.repoRoot, id, delegation), "prompt.md")}
	if s.twoTreeStorage {
		paths = append(paths, delegationPromptPath(s.policy, id, delegation), filepath.Join(s.policy.CheckoutRecordRoot(), "missions", filepath.Base(id), "delegations", filepath.Base(delegation), "prompt.md"))
	}
	var found []byte
	seen := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		b, err := readPromptArtifact(path)
		if err != nil {
			return nil, fmt.Errorf("reading delegation prompt: %w", err)
		}
		if b == nil {
			continue
		}
		if !utf8.Valid(b) {
			return nil, fmt.Errorf("delegation prompt is not valid UTF-8")
		}
		if found != nil && !bytes.Equal(found, b) {
			return nil, fmt.Errorf("unequal delegation prompt copies; detail unavailable until conflict is resolved")
		}
		found = append([]byte{}, b...)
	}
	if found == nil {
		return nil, os.ErrNotExist
	}
	if s.twoTreeStorage {
		consent, err := readPromptConsent(s.policy, filepath.Base(id), filepath.Base(delegation))
		if err != nil {
			return nil, err
		}
		if consent != nil && promptDigest(found) != consent.SHA256 {
			return nil, fmt.Errorf("delegation prompt conflicts with captured payload digest")
		}
	}
	return found, nil
}
