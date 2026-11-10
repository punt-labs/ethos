package autorepair

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

func slowGuardSteps(t *testing.T, delay time.Duration) {
	t.Helper()
	oldGuard, oldChunk := liveGuard, liveChunkLines
	liveGuard = func(_ layout.Policy, _ audit.Namespace) audit.LineGuard {
		return func(raw []byte) ([]byte, string, bool) {
			time.Sleep(delay)
			return []byte(strings.ReplaceAll(string(raw), "Secret", "Masked")), "masked", false
		}
	}
	liveChunkLines = 4
	t.Cleanup(func() { liveGuard, liveChunkLines = oldGuard, oldChunk })
}

// TestLiveTailsProgressAcrossRuns: three streams of 12 lines each cost 36
// slow guard calls, more than one budget allows. Every run finishes what it
// can and records it; the step reports Deferred until the third stream is
// clean, and no run starts a stream over.
func TestLiveTailsProgressAcrossRuns(t *testing.T) {
	env := repoEnv(t, t.TempDir(), t.TempDir())
	slowGuardSteps(t, 10*time.Millisecond)
	streams := []audit.Stream{
		{Namespace: audit.SessionNS, Session: "s1"},
		{Namespace: audit.SessionNS, Session: "s2"},
		{Namespace: audit.SessionNS, Session: "s3"},
	}
	for _, s := range streams {
		var b strings.Builder
		for i := 1; i <= 12; i++ {
			b.WriteString(`{"ts":"` + audit.FormatLineTS(int64(i)*100) + `","tool":"Secret"}` + "\n")
		}
		require.NoError(t, audit.ReplaceLocalFile(env.Policy, s.LivePath(env.Policy), []byte(b.String())))
	}

	runs := 0
	for ; runs < 20; runs++ {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		out, err := runLiveTails(ctx, env)
		cancel()
		require.NoError(t, err)
		if !out.Deferred {
			break
		}
	}
	assert.Less(t, runs, 20, "the step finished")
	assert.Greater(t, runs, 0, "one budget was not enough")
	for _, s := range streams {
		b, err := os.ReadFile(s.LivePath(env.Policy))
		require.NoError(t, err)
		assert.Equal(t, 12, strings.Count(string(b), "Masked"), s.Session)
		assert.NotContains(t, string(b), "Secret")
	}
}

// TestEnsureStampsAfterLiveTailsProgress: with a step budget too small for the
// streams, each Ensure makes progress and defers; the repository stamp appears
// on the run that finishes, and the run after it does nothing.
func TestEnsureStampsAfterLiveTailsProgress(t *testing.T) {
	r := newRig(t, true)
	slowGuardSteps(t, 10*time.Millisecond)
	r.opts.stepBudget = 150 * time.Millisecond
	r.opts.steps = []Step{{Name: stepLiveTails, Scope: ScopeRepository, Run: runLiveTails}}
	r.opts.order = []string{stepLiveTails}
	_, p := repositoryTarget(r.roots)
	for _, id := range []string{"s1", "s2", "s3"} {
		s := audit.Stream{Namespace: audit.SessionNS, Session: id}
		var b strings.Builder
		for i := 1; i <= 12; i++ {
			b.WriteString(`{"ts":"` + audit.FormatLineTS(int64(i)*100) + `","tool":"Secret"}` + "\n")
		}
		require.NoError(t, audit.ReplaceLocalFile(p, s.LivePath(p), []byte(b.String())))
	}
	runs := 0
	for ; runs < 20; runs++ {
		r.ensure()
		if _, err := r.repoStamp(); err == nil {
			break
		}
	}
	require.Less(t, runs, 20, "the stamp was written")
	assert.Greater(t, runs, 0, "the first run could not finish")
}
