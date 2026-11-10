package identity

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// VoiceMove records one identity whose legacy top-level voice field
// MigrateLegacyVoices removed. Values are never reported.
type VoiceMove struct {
	Handle string
	Path   string
}

// VoiceResidual names an identity whose voice field cannot be converted and was
// left unchanged.
type VoiceResidual struct {
	Handle string
	Reason string
}

// MigrateLegacyVoices rewrites every identity in s that still carries a
// top-level voice: field (ethos before v2.0). Each scalar entry of the field
// moves to the vox extension in ext, except where ext already holds that key:
// a value set later wins. The extensions are written before the identity file
// is rewritten, so a failure leaves the legacy field in place. A repository
// store takes each handle's identity lock without waiting; a handle whose lock
// is held elsewhere sets busy. It returns the moves applied so far with the
// first error.
func (s *Store) MigrateLegacyVoices(ext *Store) (moves []VoiceMove, residuals []VoiceResidual, busy bool, err error) {
	if err := s.validatePolicy(); err != nil {
		return nil, nil, false, err
	}
	entries, err := s.readDir(s.identitiesDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("listing identities for voice migration: %w", err)
	}
	var handles []string
	for _, e := range entries {
		if h, ok := strings.CutSuffix(e.Name(), ".yaml"); ok && !e.IsDir() && h != "" {
			handles = append(handles, h)
		}
	}
	sort.Strings(handles)
	for _, h := range handles {
		moved, reason, err := s.migrateLegacyVoice(h, ext)
		switch {
		case errors.Is(err, ErrLockBusy):
			busy = true
		case err != nil:
			return moves, residuals, busy, err
		case reason != "":
			residuals = append(residuals, VoiceResidual{Handle: h, Reason: reason})
		case moved:
			moves = append(moves, VoiceMove{Handle: h, Path: s.Path(h)})
		}
	}
	return moves, residuals, busy, nil
}

// migrateLegacyVoice converts one identity. A non-empty reason means the voice
// field cannot be converted and the file is unchanged.
func (s *Store) migrateLegacyVoice(handle string, ext *Store) (moved bool, reason string, err error) {
	has, err := s.hasLegacyVoice(handle)
	if err != nil || !has {
		return false, "", err
	}
	if !s.config.repository {
		return s.migrateLegacyVoiceUnlocked(handle, ext)
	}
	err = s.withTryLock(handle, func() error {
		var e error
		moved, reason, e = s.migrateLegacyVoiceUnlocked(handle, ext)
		return e
	})
	return moved, reason, err
}

func (s *Store) hasLegacyVoice(handle string) (bool, error) {
	data, err := s.readFile(s.Path(handle))
	if err != nil {
		return false, fmt.Errorf("reading identity %q: %w", handle, err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		// An unparseable identity is reported by the readers that need it.
		return false, nil
	}
	_, ok := raw["voice"]
	return ok, nil
}

func (s *Store) migrateLegacyVoiceUnlocked(handle string, ext *Store) (moved bool, reason string, err error) {
	path := s.Path(handle)
	data, err := s.readFile(path)
	if err != nil {
		return false, "", fmt.Errorf("reading identity %q: %w", handle, err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return false, "", nil
	}
	v, ok := raw["voice"]
	if !ok {
		return false, "", nil
	}
	vm, ok := v.(map[string]any)
	if !ok && v != nil {
		return false, fmt.Sprintf("voice field has unexpected type %T; the identity file was left unchanged", v), nil
	}
	values := make(map[string]string, len(vm))
	for k, val := range vm {
		switch val.(type) {
		case string, int, int64, float64, bool:
			values[k] = fmt.Sprint(val)
		case nil:
		default:
			return false, fmt.Sprintf("voice.%s has unexpected type %T; the identity file was left unchanged", k, val), nil
		}
		if err := validateExtWrite("vox", k, values[k]); err != nil {
			return false, fmt.Sprintf("voice.%s cannot be an extension key: %v; the identity file was left unchanged", k, err), nil
		}
	}
	existing, err := ext.readExtFile(ext.extPath(handle, "vox"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, "", fmt.Errorf("reading vox extension of %q: %w", handle, err)
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if values[k] == "" {
			continue
		}
		if _, set := existing[k]; set {
			continue
		}
		if err := ext.extSetUnlocked(handle, "vox", k, values[k], extConfig{}); err != nil {
			return false, "", fmt.Errorf("moving voice.%s of %q to the vox extension: %w", k, handle, err)
		}
	}
	delete(raw, "voice")
	if err := s.rewriteRaw(path, raw); err != nil {
		return false, "", fmt.Errorf("rewriting identity %q without its voice field: %w", handle, err)
	}
	return true, "", nil
}
