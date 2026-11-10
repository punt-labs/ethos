package identity

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// VoiceMove records one identity whose legacy top-level voice field
// MigrateLegacyVoices removed. Values are never reported.
type VoiceMove struct {
	Handle string
	Path   string
	// Dropped names the legacy keys discarded because the extension already
	// held a value for them.
	Dropped []string
}

// VoiceResidual names an identity whose voice field cannot be converted and was
// left unchanged.
type VoiceResidual struct {
	Handle string
	Path   string
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
	var dirErr *repositoryPathError
	if link, err := symlinkBelow(s.root, s.identitiesDir()); err != nil {
		return nil, nil, false, fmt.Errorf("examining identities for voice migration: %w", err)
	} else if link != "" {
		return nil, []VoiceResidual{{Path: link, Reason: "identity directory is refused: symlink at " + link + "; left unchanged"}}, false, nil
	}
	entries, err := s.readDir(s.identitiesDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, false, nil
	}
	if errors.As(err, &dirErr) {
		return nil, []VoiceResidual{{Path: s.identitiesDir(), Reason: "identity directory is refused: " + dirErr.reason + " at " + dirErr.path + "; left unchanged"}}, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("listing identities for voice migration: %w", err)
	}
	var handles []string
	for _, e := range entries {
		h, ok := strings.CutSuffix(e.Name(), ".yaml")
		if !ok || e.IsDir() || h == "" {
			continue
		}
		if !s.config.repository && e.Type()&os.ModeSymlink != 0 {
			// The repository store refuses links in checkPath; the global one has no
			// such check, so a link is refused here rather than followed.
			residuals = append(residuals, VoiceResidual{Handle: h, Path: s.Path(h), Reason: "identity is refused: symlink; left unchanged"})
			continue
		}
		handles = append(handles, h)
	}
	sort.Strings(handles)
	for _, h := range handles {
		moved, dropped, reason, err := s.migrateLegacyVoice(h, ext)
		var rp *repositoryPathError
		switch {
		case errors.Is(err, ErrLockBusy):
			busy = true
		case errors.As(err, &rp):
			residuals = append(residuals, VoiceResidual{Handle: h, Path: s.Path(h), Reason: "identity is refused: " + rp.reason + " at " + rp.path + "; left unchanged"})
		case err != nil:
			return moves, residuals, busy, err
		case reason != "":
			residuals = append(residuals, VoiceResidual{Handle: h, Path: s.Path(h), Reason: reason})
		case moved:
			moves = append(moves, VoiceMove{Handle: h, Path: s.Path(h), Dropped: dropped})
		}
	}
	return moves, residuals, busy, nil
}

// migrateLegacyVoice converts one identity. A non-empty reason means the voice
// field cannot be converted and the file is unchanged.
func (s *Store) migrateLegacyVoice(handle string, ext *Store) (moved bool, dropped []string, reason string, err error) {
	has, err := s.hasLegacyVoice(handle)
	if err != nil || !has {
		return false, nil, "", err
	}
	if !s.config.repository {
		return s.migrateLegacyVoiceUnlocked(handle, ext)
	}
	err = s.withTryLock(handle, func() error {
		var e error
		moved, dropped, reason, e = s.migrateLegacyVoiceUnlocked(handle, ext)
		return e
	})
	return moved, dropped, reason, err
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

func (s *Store) migrateLegacyVoiceUnlocked(handle string, ext *Store) (moved bool, dropped []string, reason string, err error) {
	path := s.Path(handle)
	data, err := s.readFile(path)
	if err != nil {
		return false, nil, "", fmt.Errorf("reading identity %q: %w", handle, err)
	}
	return s.convertVoice(handle, path, data, ext)
}

// convertVoice is the one voice migration, used by Load, the layered store and
// the repair step. Every scalar key under voice: moves into the vox extension in
// ext, except a key ext already holds: a value set later wins. The extension is
// written before the identity is rewritten, so a failure leaves the field in
// place. A non-empty reason means the field cannot be converted and the file is
// unchanged. The caller holds the handle lock of s.
func (s *Store) convertVoice(handle, path string, data []byte, ext *Store) (moved bool, dropped []string, reason string, err error) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return false, nil, "", nil
	}
	v, ok := raw["voice"]
	if !ok {
		return false, nil, "", nil
	}
	vm, ok := v.(map[string]any)
	if !ok && v != nil {
		return false, nil, fmt.Sprintf("voice field has unexpected type %T; the identity file was left unchanged", v), nil
	}
	values := make(map[string]string, len(vm))
	for k, val := range vm {
		switch val.(type) {
		case string, int, int64, float64, bool:
			values[k] = fmt.Sprint(val)
		case nil:
		default:
			return false, nil, fmt.Sprintf("voice.%s has unexpected type %T; the identity file was left unchanged", k, val), nil
		}
		if err := validateExtWrite("vox", k, values[k]); err != nil {
			return false, nil, fmt.Sprintf("voice.%s cannot be an extension key: %v; the identity file was left unchanged", k, err), nil
		}
	}
	existing, err := ext.readExtFile(ext.extPath(handle, "vox"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, nil, "", fmt.Errorf("reading vox extension of %q: %w", handle, err)
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
			dropped = append(dropped, k)
			continue
		}
		if err := ext.extSetUnlocked(handle, "vox", k, values[k], extConfig{}); err != nil {
			return false, nil, "", fmt.Errorf("moving voice.%s of %q to the vox extension: %w", k, handle, err)
		}
	}
	out, err := withoutVoiceKey(data)
	if err != nil {
		return false, nil, "", fmt.Errorf("removing voice field of %q: %w", handle, err)
	}
	if err := s.writeFile(path, out, false); err != nil {
		return false, nil, "", fmt.Errorf("rewriting identity %q without its voice field: %w", handle, err)
	}
	return true, dropped, "", nil
}

// symlinkBelow returns the first symlink on the way from root (exclusive) down
// to path, or "" when there is none or the walk reaches a missing entry. A link
// is never followed: the caller would read or rewrite its target.
func symlinkBelow(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(rel) && rel != "." {
		return "", fmt.Errorf("%s is not under %s", path, root)
	}
	cur := root
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		if name == "." {
			continue
		}
		cur = filepath.Join(cur, name)
		info, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return cur, nil
		}
	}
	return "", nil
}

// withoutVoiceKey returns data minus the lines of its top-level voice key: the
// key line through the last line of its value. Every other byte, including the
// comment above voice: and the indentation of other sequences, is kept. A flow
// mapping has no such line range and is re-encoded.
func withoutVoiceKey(data []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("identity is not a YAML mapping")
	}
	m := doc.Content[0]
	i := 0
	for ; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == "voice" {
			break
		}
	}
	if i+1 >= len(m.Content) {
		return nil, fmt.Errorf("identity has no top-level voice key")
	}
	if m.Style&yaml.FlowStyle != 0 {
		m.Content = append(m.Content[:i], m.Content[i+2:]...)
		return yaml.Marshal(&doc)
	}
	lines := strings.SplitAfter(string(data), "\n")
	start := m.Content[i].Line - 1
	end := len(lines)
	if i+2 < len(m.Content) {
		end = m.Content[i+2].Line - 1
		// The next key's head comment is not part of voice.
		for end > start+1 && strings.HasPrefix(strings.TrimSpace(lines[end-1]), "#") {
			end--
		}
	}
	for end > start+1 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	out := strings.Join(lines[:start], "") + strings.Join(lines[end:], "")
	var check map[string]any
	if err := yaml.Unmarshal([]byte(out), &check); err != nil {
		return nil, fmt.Errorf("result does not parse: %w", err)
	}
	if _, ok := check["voice"]; ok {
		return nil, fmt.Errorf("voice key remains after removal")
	}
	return []byte(out), nil
}
