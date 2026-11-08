package adr

import (
	"fmt"
	"reflect"
)

// Migration describes one explicitly selected global ADR. Migration copies,
// never moves or deletes its source, preserving IDs and both timestamps.
type Migration struct {
	ID     string `json:"id"`
	Action string `json:"action"` // would-copy, copied, unchanged
}

// Migrate checks selected legacy IDs and, with apply, copies their redacted
// records into the repository. No ownership is guessed from a global record.
// All IDs are preflighted before publication. A crash between publications is
// retried safely: identical redacted destinations are already complete, while
// divergent destinations refuse without overwriting either copy.
// A *DurabilityError identifies a copied record already installed; a retry with
// apply re-syncs identical copies before reporting them unchanged.
func (s *Store) Migrate(ids []string, apply bool) ([]Migration, error) {
	if !s.repository || s.legacy == nil {
		return nil, fmt.Errorf("ADR migration requires a repository and a legacy global store")
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("ADR migration requires explicitly selected IDs")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !idPattern.MatchString(id) {
			return nil, fmt.Errorf("invalid ADR id: must match DES-NNN")
		}
		if seen[id] {
			return nil, fmt.Errorf("ADR migration repeats ID %s", id)
		}
		seen[id] = true
	}
	var results []Migration
	err := s.withMutationLock(func() error {
		type pending struct {
			a   *ADR
			say announcement
		}
		var copies []pending
		for _, id := range ids {
			source, err := s.legacy.Load(id)
			if err != nil {
				return fmt.Errorf("loading migration source %s: %w", id, err)
			}
			candidate, say, err := s.prepare(source)
			if err != nil {
				return err
			}
			dest, err := s.loadOwn(id)
			if err == nil {
				if !reflect.DeepEqual(dest, candidate) {
					return fmt.Errorf("ADR %s has a divergent repository copy; refusing migration", id)
				}
				if apply {
					if err := s.confirmDurability(s.path(id)); err != nil {
						return err
					}
				}
				results = append(results, Migration{ID: id, Action: "unchanged"})
				continue
			}
			if !isAbsent(err) {
				return err
			}
			action := "would-copy"
			if apply {
				action = "copied"
			}
			results = append(results, Migration{ID: id, Action: action})
			copies = append(copies, pending{candidate, say})
		}
		if apply {
			for _, copy := range copies {
				err := s.write(copy.a, true)
				if installed(err) {
					copy.say.Say()
				}
				if err != nil {
					return fmt.Errorf("migrating ADR %s (retry is safe): %w", copy.a.ID, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}
