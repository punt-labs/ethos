package seed

import "github.com/punt-labs/ethos/v4/internal/layout"

// Option configures one seed operation.
type Option func(*seeder)

// WithRepositoryAgents places staging for the explicit agentsRoot category in
// the repository's shared local zone. Global source, skills and seed-manifest
// placement are unchanged. No-clobber installs still use link, replacements use
// rename; cross-device errors never cause adjacent staging fallback.
func WithRepositoryAgents(p layout.Policy) Option {
	return func(s *seeder) { s.agentPolicy = &p }
}

func (s *seeder) policies(scope string) []layout.Policy {
	if scope != scopeAgents || s.agentPolicy == nil {
		return nil
	}
	return []layout.Policy{*s.agentPolicy}
}
