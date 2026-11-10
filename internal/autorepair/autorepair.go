// Package autorepair converts every legacy on-disk layout ethos ever wrote
// into the current one (DES-084). It runs from every entry point through
// Ensure; a current layout stamp makes Ensure cost two stats, an absent or
// older stamp runs every Step in order under the repair lock.
//
// This file is the interface the step packages build against. Steps live in
// their own files in this package and register themselves in Steps; the
// engine (engine.go) owns the stamp, the lock, the budget and the log.
package autorepair

import (
	"context"
	"time"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// LayoutVersion is the layout the current binary writes. It names the
// DES-079 layout and is independent of the release number.
const LayoutVersion = 5

// Env is what a step needs: the located roots (Store, Checkout may be empty
// outside a repository), the repository policy (zero Policy outside a
// repository), the global root (~/.punt-labs/ethos), and a Logger. Steps
// never resolve roots or policy themselves.
type Env struct {
	Roots  resolve.Located
	Policy layout.Policy
	Global string
	Now    time.Time
	Log    Logger
}

// InRepository reports whether a repository store was located.
func (e Env) InRepository() bool { return e.Roots.Store != "" }

// Outcome is one step's result. Items counts conversions applied. Deferred
// is true when the step stopped early (busy lock, index lock, budget) and
// must run again next Ensure; the stamp is not written while any step
// defers. Residuals name permanent conditions the step cannot convert; they
// are recorded in the stamp and count as finished.
type Outcome struct {
	Items     int
	Deferred  bool
	Residuals []Residual
}

// Residual is a permanent condition a step leaves in place, recorded in the
// stamp so Ensure does not retry it and doctor can show it.
type Residual struct {
	Step   string `yaml:"step"`
	ID     string `yaml:"id"`
	Reason string `yaml:"reason"`
}

// Step is one conversion. Name is a stable slug used in the log and the
// stamp. Scope says whether the step touches the global store, the
// repository, or both; the engine skips repository steps outside a
// repository. Run must be idempotent by observed state and must respect
// ctx, which carries the step budget: a Run that outlives its deadline is
// abandoned (its result is discarded and the stamp withheld) while it keeps
// running in its goroutine, so it must not hold state the next Ensure needs.
type Step struct {
	Name  string
	Scope Scope
	Run   func(ctx context.Context, env Env) (Outcome, error)
}

// Scope is where a step reads and writes.
type Scope uint8

const (
	ScopeGlobal     Scope = iota + 1 // ~/.punt-labs/ethos only
	ScopeRepository                  // S and C only
	ScopeBoth                        // global and repository
)

// Logger appends one JSON line per action to the repair log. The engine
// supplies it; steps call it for every file they change, move or delete.
// Every string passes through the DES-080 prose redactor (credentials,
// addresses, home paths) before writing; still, never pass file contents.
type Logger interface {
	Logf(step, action string, paths []string, err error)
}

// Steps is the ordered list the engine runs. Each step file appends its Step
// in an init function; the order is fixed by StepOrder so init order does
// not matter.
var Steps []Step

// StepOrder is the DES-084 order. A registered step not named here fails the
// engine's order test. A name here with no registered step is a step that has
// not run: Ensure treats it as deferred and writes no stamp, so a binary
// built before every step unit landed never stamps a store current.
var StepOrder = []string{
	"global-missions",   // 1
	"global-audit",      // 2
	"round-decisions",   // 3
	"scrub-records",     // 4
	"record-tree-trace", // 5
	"live-tails",        // 6
	"corrupt-chunks",    // 7
	"overlays",          // 8
	"untrack-local",     // 9
	"gitignore",         // 10
	"config-file",       // 11
	"repo-adrs",         // 12
	"voice-field",       // 13
	"bead-alias",        // 14
	"leftovers",         // 15
	"hook-sections",     // 16
	"sessions",          // 17
}

// Register adds a step. Called from each step file's init.
func Register(s Step) { Steps = append(Steps, s) }

// Budgets, initial values from DES-084.
const (
	StepBudget   = 5 * time.Second
	EnsureBudget = 30 * time.Second
	LockWait     = 2 * time.Second
)
