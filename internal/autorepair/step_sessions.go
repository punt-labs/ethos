package autorepair

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/session"
)

const stepSessions = "sessions"

// SealSession seals every stream of one session under the repository policy
// and returns the lines sealed. internal/hook sets it at init: hook imports
// this package, so the step cannot call the seal directly. startDate names a
// new sealed directory by session start; "" lets the seal choose.
var SealSession func(p layout.Policy, sessionID, startDate string, now time.Time) (int, error)

func init() { Register(Step{Name: stepSessions, Scope: ScopeBoth, Run: runSessions}) }

// SessionStart is the cheap pass the session-start hook runs on every start,
// outside the layout stamp: step 17 (dead session state), then step 7
// (sealed chunks damaged since the stamp, which no later Ensure revisits).
// It returns the items converted. A step 7 error is logged by the step and
// never fails the start.
func SessionStart(ctx context.Context, env Env) (int, error) {
	out, err := runSessions(ctx, env)
	if err != nil {
		// Session start runs this pass outside the engine, which is what
		// normally logs a step error; the hook discards the error, so it
		// is recorded here or nowhere.
		if env.Log != nil {
			env.Log.Logf("sessions", "session-start", nil, err)
		}
		return out.Items, err
	}
	if env.InRepository() {
		chunks, cerr := runCorruptChunks(ctx, env)
		if cerr != nil {
			env.Log.Logf(stepCorruptChunks, "session-start", nil, cerr)
		}
		out.Items += chunks.Items
	}
	return out.Items, nil
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

	repoRoot, err := env.CheckoutRoot()
	if err != nil && !errors.Is(err, ErrNotInRepository) {
		// A located failure is not "outside a repository", but the purge is
		// still safe: with no root, repository-bound rosters are left alone.
		env.Log.Logf(stepSessions, "locate-checkout", nil, err)
	}
	storeRoot, err := env.StoreRoot()
	if err != nil && !errors.Is(err, ErrNotInRepository) {
		env.Log.Logf(stepSessions, "locate-store", nil, err)
	}
	repoID, err := audit.RepoIdentity(repoRoot)
	if err != nil {
		// Without the identity no roster can be matched to this repository;
		// purge as if outside one, which leaves repository-bound rosters alone.
		env.Log.Logf(stepSessions, "repo-identity", nil, err)
		repoRoot, repoID = "", ""
	}
	keep := func(r *session.Roster) bool { return r.Checkout == "" }
	rep, err := ss.PurgeTombstonedExcept(storeRoot, repoRoot, repoID, false, keep)
	if err != nil {
		return out, fmt.Errorf("purging dead session rosters: %w", err)
	}
	// A refused dead session may hold lines nothing has sealed yet. Seal them
	// from the writer's own checkout, then purge once more: the guard that
	// refused still decides, now over the sealed record.
	if sealed := sealRefused(ss, env, repoRoot, rep.Refused); sealed > 0 {
		again, err := ss.PurgeTombstonedExcept(storeRoot, repoRoot, repoID, false, keep)
		if err != nil {
			return out, fmt.Errorf("purging dead session rosters after sealing: %w", err)
		}
		rep.Purged = append(rep.Purged, again.Purged...)
		rep.Refused = again.Refused
	}
	for _, id := range rep.Purged {
		out.Items++
		env.Log.Logf(stepSessions, "purge-roster", []string{id}, nil)
	}
	for _, id := range rep.Refused {
		// Rosters live in the global store, so the residual belongs to its stamp
		// whichever store a recheck happens to serve.
		rs := actionableResidual(stepSessions, id,
			"the roster could not be read or proven free of unsealed audit lines; left in place",
			"clears when the roster of session "+id+" is readable and its unsealed audit lines are sealed")
		rs.Scope = ScopeGlobal
		out.Residuals = append(out.Residuals, rs)
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

// sealRefused seals each refused session whose writer checkout exists and
// returns how many lines it sealed. A roster that cannot be read, has no
// recorded checkout, or whose checkout is gone is left to the guard's verdict.
// Failures are logged and the session stays refused.
func sealRefused(ss *session.Store, env Env, repoRoot string, refused []string) int {
	if SealSession == nil {
		return 0
	}
	total := 0
	for _, id := range refused {
		r, err := ss.Load(id)
		if err != nil || r.Checkout == "" {
			continue
		}
		if fi, err := os.Stat(r.Checkout); err != nil || !fi.IsDir() {
			continue
		}
		p, err := writerPolicy(env, repoRoot, r.Checkout)
		if err != nil {
			env.Log.Logf(stepSessions, "seal-session", []string{id}, err)
			continue
		}
		start := ""
		if len(r.Started) >= 10 {
			start = r.Started[:10]
		}
		n, err := SealSession(p, id, start, env.Now)
		env.Log.Logf(stepSessions, "seal-session", []string{id}, err)
		total += n
	}
	return total
}

// writerPolicy is the repository policy of the checkout that wrote a session:
// the pass's own when that checkout is the one being served, else one built
// from the writer's store. A foreign store's record mode is not read here, so
// it archives locally only, which is what blame mode does.
func writerPolicy(env Env, repoRoot, writer string) (layout.Policy, error) {
	if repoRoot != "" && env.InRepository() && sameDir(writer, repoRoot) {
		return env.Policy, nil
	}
	store, err := audit.StoreRootOf(writer)
	if err != nil {
		return layout.Policy{}, fmt.Errorf("locating the store of the writer checkout: %w", err)
	}
	return layout.NewRepository(store, writer, layout.RecordBlame)
}

func sameDir(a, b string) bool {
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	return aerr == nil && berr == nil && os.SameFile(ai, bi)
}
