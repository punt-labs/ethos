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
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/redact"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

// LayoutVersion is the layout the current binary writes. It names the
// DES-079 layout and is independent of the release number.
const LayoutVersion = 5

// Env is what a step needs: the located roots and the repository policy,
// both zero unless the repository pass is active (a repository was located
// cleanly and has used ethos), the global root (~/.punt-labs/ethos), and a
// Logger. Steps never resolve roots or policy themselves. A step reads the
// repository's roots only through StoreRoot and CheckoutRoot, never through
// Roots.Store or Roots.Checkout: those helpers check the located error and the
// active pass, which the located-use scanner requires of every reader.
type Env struct {
	Roots  resolve.Located
	Policy layout.Policy
	Global string
	Now    time.Time
	Log    Logger
}

// InRepository reports whether the repository pass is active: the engine
// located a repository that has used ethos and is serving it. A ScopeBoth
// step runs with this false outside a repository and in a repository ethos
// never touched, which it must then leave alone.
//
// The engine sets or zeroes Roots and Policy together, so this agrees with
// Roots.Store being set.
func (e Env) InRepository() bool { return e.Policy.LocalRoot() != "" }

// ErrNotInRepository is what StoreRoot and CheckoutRoot return while the
// repository pass is not active.
var ErrNotInRepository = errors.New("the repository pass is not active")

// StoreRoot is the repository's shared store root S. It returns an error
// unless the repository pass is active and the location carries none.
func (e Env) StoreRoot() (string, error) {
	if !e.InRepository() {
		return "", ErrNotInRepository
	}
	if err := e.Roots.Err(); err != nil {
		return "", fmt.Errorf("reading the store root: %w", err)
	}
	return e.Roots.Store, nil
}

// CheckoutRoot is the invoking checkout root C, with StoreRoot's conditions.
func (e Env) CheckoutRoot() (string, error) {
	if !e.InRepository() {
		return "", ErrNotInRepository
	}
	if err := e.Roots.Err(); err != nil {
		return "", fmt.Errorf("reading the checkout root: %w", err)
	}
	return e.Roots.Checkout, nil
}

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

// Class is the kind of a residual. Doctor warns only about ClassActionable.
type Class string

const (
	// ClassSettled is a final, correct state: there is nothing to do.
	ClassSettled Class = "settled"
	// ClassActionable is a condition a person can act on; its Action says how.
	// ethos doctor re-runs the owning step, so the residual clears once the
	// person has acted.
	ClassActionable Class = "actionable"
)

// fencedSuffix ends every reason round-decisions records for a fenced mission.
const fencedSuffix = ", mission fenced from lifecycle writes"

// writeFailPrefix starts the round-decisions reason for a write failure.
const writeFailPrefix = "mission authority cannot be written"

// reasonImportedKept is the global-missions reason for a mission imported
// with its global copy left in place.
// (defined in step_global_missions.go)

// settledReasons is the exact set of unclassed reasons, written by binaries
// before classes existed, that record a final state: step -> reasons. An
// old residual that is not listed is actionable. Read path only.
func settledReasons() map[string]map[string]bool {
	fenced := map[string]bool{}
	for _, k := range []mission.LegacyFence{mission.LegacyIncomplete, mission.LegacyRedactionRefused, 0} {
		fenced[k.String()+fencedSuffix] = true
	}
	return map[string]map[string]bool{
		"round-decisions":  fenced,
		stepGlobalMissions: {reasonImportedKept: true},
	}
}

// EffectiveClass is r.Class, or, for a residual from a stamp written before
// classes existed, settled when step and reason exactly match a historical
// final state and actionable otherwise.
func (r Residual) EffectiveClass() Class {
	if r.Class != "" {
		return r.Class
	}
	if settledReasons()[r.Step][r.Reason] {
		return ClassSettled
	}
	return ClassActionable
}

// needsReevaluation is true for an unclassed incomplete-history residual: older
// binaries wrote that reason for a damaged reflections file too, so it is not
// trusted as settled until round-decisions has run once more and classed it.
func (r Residual) needsReevaluation() bool {
	return r.Class == "" && r.Step == "round-decisions" && r.Reason == mission.LegacyIncomplete.String()+fencedSuffix
}

// EffectiveAction is what a person does about an actionable residual: its
// Action, or for one from a stamp written before actions existed, a pointer to
// the repair log. A settled residual has none.
func (r Residual) EffectiveAction() string {
	if r.EffectiveClass() == ClassSettled {
		return ""
	}
	if r.Action != "" {
		return r.Action
	}
	return "cause recorded in ~/.punt-labs/ethos/logs/repair.log for step " + r.Step + "; clears when the condition no longer holds"
}

// DisplayAction is EffectiveAction through the prose redactor, for a screen.
func (r Residual) DisplayAction() string { return redact.Prose(r.EffectiveAction()) }

// seeLogAction is the action for a condition whose cause only the repair log
// names.
const seeLogAction = "cause recorded in ~/.punt-labs/ethos/logs/ethos.log; clears when the condition no longer holds"

// settledResidual is a final, correct state: nothing for a person to do.
func settledResidual(step, id, reason string) Residual {
	return Residual{Class: ClassSettled, Step: step, ID: id, Reason: reason}
}

// actionableResidual is a condition a person can act on. The action says what
// to do and is never empty; doctor shows it beside the reason.
func actionableResidual(step, id, reason, action string) Residual {
	if action == "" {
		panic("autorepair: actionable residual without an action: " + step + " " + id)
	}
	return Residual{Class: ClassActionable, Step: step, ID: id, Reason: reason, Action: action}
}

// Residual is a permanent condition a step leaves in place, recorded in the
// stamp so Ensure does not retry it and doctor can show it.
type Residual struct {
	// Class says whether a person can act on the condition. A stamp written
	// before classes existed has none; EffectiveClass maps it.
	Class  Class  `yaml:"class,omitempty"`
	Step   string `yaml:"step"`
	ID     string `yaml:"id"`
	Reason string `yaml:"reason"`
	// Action is what a person does about an actionable residual.
	Action string `yaml:"action,omitempty"`
	// Scope names the store the condition is in. A ScopeBoth step sets it; the
	// zero value means the store the step was running for. It places the
	// residual and is not recorded. A residual is never written into a stamp
	// of a different scope: when the pass serves no stamp of this scope, it is
	// carried in Result.Residuals only, and the owning store's own pass records
	// it when it runs.
	Scope Scope `yaml:"-"`
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

// Logger records one diagnostic per repair action. The engine
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

var stepNameRE = regexp.MustCompile(`^[a-z0-9-]+$`)

// Register adds a step. Called from each step file's init. It panics on a name
// outside [a-z0-9-]: the name is joined into a lock file path.
func Register(s Step) {
	if !stepNameRE.MatchString(s.Name) {
		panic(fmt.Sprintf("autorepair: invalid step name %q", s.Name))
	}
	Steps = append(Steps, s)
}

// Budgets, initial values from DES-084.
const (
	StepBudget   = 5 * time.Second
	EnsureBudget = 30 * time.Second
	LockWait     = 2 * time.Second
)
