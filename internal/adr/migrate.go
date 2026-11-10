package adr

import (
	"fmt"
	"os"
	"reflect"
)

// Migration describes one explicitly selected global ADR. Migration copies,
// never moves or deletes its source, preserving IDs and both timestamps.
// Notice is set by MigrateQuiet when the redactor rewrote the copy; it names
// what happened, never a value.
type Migration struct {
	ID     string `json:"id"`
	Action string `json:"action"` // would-copy, copied, unchanged
	Notice string `json:"-"`
}

// rewriteNotice is the one line the redactor says when it changed an ADR.
const rewriteNotice = "ADR was rewritten before storage (a path, credential or address removed)"

// Migrate checks selected legacy IDs and, with apply, copies their redacted
// records into the repository. No ownership is guessed from a global record.
// All IDs are preflighted before publication. A crash between publications is
// retried safely: identical redacted destinations are already complete, while
// divergent destinations refuse without overwriting either copy.
// A *DurabilityError identifies a copied record already installed; a retry with
// apply re-syncs identical copies before reporting them unchanged.
func (s *Store) Migrate(ids []string, apply bool) ([]Migration, error) {
	return s.migrate(ids, apply, false)
}

// MigrateQuiet is Migrate without output: a copy the redactor rewrote is
// reported through Migration.Notice instead of stderr, for a caller that
// logs. It applies the copy; there is no quiet dry run.
func (s *Store) MigrateQuiet(ids []string) ([]Migration, error) {
	return s.migrate(ids, true, true)
}

func (s *Store) migrate(ids []string, apply, quiet bool) ([]Migration, error) {
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
			at  int // index into results
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
			copies = append(copies, pending{a: candidate, say: say, at: len(results) - 1})
		}
		if apply {
			for _, copy := range copies {
				// writeQuiet installs the prepared record and returns whether
				// the redactor changed it; the notice is said here, once,
				// either on stderr or on the result.
				rewritten, err := s.writeQuiet(copy.a, true)
				if installed(err) && (rewritten || copy.say != nil) {
					if quiet {
						results[copy.at].Notice = rewriteNotice
					} else {
						announcement(func() { fmt.Fprintln(os.Stderr, "ethos: redact: "+rewriteNotice) }).Say()
					}
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
