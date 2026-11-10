package autorepair

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/session"
)

const stepSessions = "sessions"

func init() { Register(Step{Name: stepSessions, Scope: ScopeGlobal, Run: runSessions}) }

// SessionStart is the cheap pass the session-start hook runs on every start:
// step 17 alone, outside the layout stamp. It returns the items converted.
func SessionStart(ctx context.Context, env Env) (int, error) {
	out, err := runSessions(ctx, env)
	return out.Items, err
}

// logWriter turns each line a session store writes into one repair-log entry,
// so the step prints nothing.
type logWriter struct {
	log  Logger
	step string
}

func (w logWriter) Write(p []byte) (int, error) {
	w.log.Logf(w.step, "note", nil, errors.New(strings.TrimSpace(string(p))))
	return len(p), nil
}

// runSessions removes dead session rosters, dead current/<pid> files and
// retires purge tombstones. Purge is the non-force kind: a roster whose
// unsealed audit state cannot be proven clean stays. A roster without a
// checkout is left alone (DES-058: it ages out). Each tombstone is logged
// once and then retired, so the vacuum cross-check stops warning on it. Items
// counts rosters and files purged plus tombstones retired.
func runSessions(ctx context.Context, env Env) (Outcome, error) {
	var out Outcome
	if env.Global == "" {
		return out, nil
	}
	ss := session.NewStore(env.Global)
	ss.SetWarnings(logWriter{env.Log, stepSessions})

	pids, err := ss.PurgeCurrent()
	if err != nil {
		return out, fmt.Errorf("purging dead current-session files: %w", err)
	}
	for _, pid := range pids {
		out.Items++
		env.Log.Logf(stepSessions, "purge-current", []string{pid}, nil)
	}
	if ctx.Err() != nil {
		out.Deferred = true
		return out, nil
	}

	repoRoot := env.Roots.Checkout
	repoID, err := audit.RepoIdentity(repoRoot)
	if err != nil {
		// Without the identity no roster can be matched to this repository;
		// purge as if outside one, which leaves repository-bound rosters alone.
		env.Log.Logf(stepSessions, "repo-identity", nil, err)
		repoRoot, repoID = "", ""
	}
	rep, err := ss.PurgeTombstonedExcept(repoRoot, repoID, false, func(r *session.Roster) bool {
		return r.Checkout == ""
	})
	if err != nil {
		return out, fmt.Errorf("purging dead session rosters: %w", err)
	}
	for _, id := range rep.Purged {
		out.Items++
		env.Log.Logf(stepSessions, "purge-roster", []string{id}, nil)
	}
	for _, id := range rep.Refused {
		out.Residuals = append(out.Residuals, Residual{
			Step:   stepSessions,
			ID:     id,
			Reason: "the roster could not be read or proven free of unsealed audit lines; left in place",
		})
	}
	if ctx.Err() != nil {
		out.Deferred = true
		return out, nil
	}

	dir := filepath.Join(env.Global, "sessions")
	tombs, err := audit.ListTombstones(dir, nil)
	if err != nil {
		return out, fmt.Errorf("listing purge tombstones: %w", err)
	}
	for _, t := range tombs {
		retired, err := audit.AckTombstone(dir, t.Session)
		env.Log.Logf(stepSessions, "retire-tombstone", []string{retired}, err)
		if err != nil {
			return out, fmt.Errorf("retiring tombstone for %s: %w", t.Session, err)
		}
		out.Items++
	}
	return out, nil
}
