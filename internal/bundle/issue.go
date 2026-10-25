package bundle

import (
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// ConfigIssue is why the repository's bundle or resolution policy cannot be
// honoured, as a *resolve.ConfigError, or nil when it can (or there is no
// store to be configured). storeRoot is the repository that holds the store,
// ethosRoot its .punt-labs/ethos, globalRoot ~/.punt-labs/ethos. A missing
// active_bundle, an unreadable bundle, an unknown `resolution`, or repo-only
// with nothing to be authoritative about are each an issue. One notice path
// serves every consumer: session-start puts it in systemMessage, serve in its
// note, doctor in a row, the MCP whoami in its error.
func ConfigIssue(storeRoot, ethosRoot, globalRoot string) error {
	if storeRoot == "" {
		return nil
	}
	bundleRoot, active, err := ResolveRoot(storeRoot, globalRoot)
	if err != nil {
		return issue(err)
	}
	mode, err := resolve.ResolveResolution(storeRoot)
	if err != nil {
		return issue(err)
	}
	if mode == resolve.ResolutionRepoOnly {
		if err := VerifyRepoOnly(ethosRoot, bundleRoot, active); err != nil {
			return issue(err)
		}
	}
	return nil
}

func issue(err error) error {
	return &resolve.ConfigError{
		Err:    err,
		What:   "the repository's bundle or policy configuration is unusable",
		Effect: "so it was ignored: the repository layer is used without it.",
		Answer: "this answer ignores it: the repository layer is used without it",
		Policy: true,
	}
}
