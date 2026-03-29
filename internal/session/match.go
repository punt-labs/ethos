//go:build !windows

package session

import "fmt"

// MatchByPrefix finds a session ID from a prefix string. If the prefix
// exactly matches a session ID, that ID is returned. Otherwise, if the
// prefix matches exactly one session, that ID is returned; an error is
// returned when zero or multiple sessions match.
func (s *Store) MatchByPrefix(prefix string) (string, error) {
	ids, err := s.List()
	if err != nil {
		return "", fmt.Errorf("listing sessions: %w", err)
	}

	var matches []string
	for _, id := range ids {
		if id == prefix {
			return id, nil // exact match
		}
		if len(id) >= len(prefix) && id[:len(prefix)] == prefix {
			matches = append(matches, id)
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no session matching prefix %q", prefix)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous prefix %q: matches %d sessions", prefix, len(matches))
	}
}
