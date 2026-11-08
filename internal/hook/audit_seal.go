package hook

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
)

// SealOptions configures a local archive and optional full publication run.
type SealOptions struct {
	DryRun  bool
	Verbose bool
	// Out receives per-stream dry-run/verbose reporting. Nil discards it.
	Out io.Writer
	// StartDate returns an authoritative session start date (YYYY-MM-DD).
	// An existing archive directory wins; otherwise an empty result falls back
	// to the first live line's date, then now.
	StartDate func(sessionID string) string
}

// SealResult separates local archive completion from publication completion.
type SealResult struct {
	SessionsSealed int
	LinesSealed    int
	ChunksStaged   int
	// Deferred is retained for caller compatibility. Local archival is never
	// deferred merely because a record tree is gitlink-mounted or ignored.
	Deferred bool
}

// SealRepo archives all known session and mission streams under the supplied
// canonical store root. Blame archival never accesses the Git index. Full mode
// additionally publishes eligible copies; failure retains the local archive.
// Linked-checkout callers must use SealRepoWithPolicy with their roots snapshot.
func SealRepo(repoRoot string, now time.Time, opts SealOptions) (SealResult, error) {
	p, err := auditPolicy(repoRoot)
	if err != nil {
		return SealResult{}, err
	}
	return SealRepoWithPolicy(p, now, opts)
}

func reportf(opts SealOptions, format string, args ...any) {
	if opts.Out != nil && (opts.Verbose || opts.DryRun) {
		fmt.Fprintf(opts.Out, format, args...)
	}
}

// SealGitError is the root-resolution caller's pre-archive failure diagnostic.
// unchanged may be true only before any archive/publication work was applied.
func SealGitError(repoRoot, phase string, err error, unchanged bool) error {
	var e error
	if errors.Is(err, gitenv.ErrTimeout) {
		e = fmt.Errorf("the seal could not run because git did not answer in %s while %s (%w)", repoRoot, phase, err)
	} else {
		e = fmt.Errorf("%s: %w", phase, err)
	}
	if unchanged {
		return fmt.Errorf("%w; nothing was sealed or staged, the commit is blocked and the tree is unchanged", e)
	}
	return e
}
