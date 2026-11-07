package claudemd

import "github.com/punt-labs/ethos/v4/internal/layout"

// Option configures host-file persistence. Without an option, existing global
// callers retain per-user locks and adjacent staging.
type Option func(*options)

type options struct {
	policy *layout.Policy
}

// WithRepository places locks and staging in the repository's shared local
// zone, even when the host is a symlink to a file outside the checkout. A
// cross-device rename is returned to the caller; there is no adjacent fallback.
// Changing from the old lock location requires quiescing older writers.
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
