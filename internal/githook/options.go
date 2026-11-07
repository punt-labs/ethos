package githook

import "github.com/punt-labs/ethos/v4/internal/layout"

// Option configures hook persistence; omitted options preserve existing callers.
type Option func(*options)

type options struct {
	policy *layout.Policy
}

// WithRepository stages hook replacements in the shared local zone, including
// in-tree core.hooksPath hooks and symlink targets. Cross-device renames fail
// without replacing the old hook or falling back to adjacent staging.
func WithRepository(p layout.Policy) Option {
	return func(o *options) { o.policy = &p }
}

func repositoryPolicies(opts []Option) []layout.Policy {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.policy == nil {
		return nil
	}
	return []layout.Policy{*o.policy}
}
