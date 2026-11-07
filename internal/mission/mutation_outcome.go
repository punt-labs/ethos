package mission

import (
	"fmt"
	"os"
)

// MutationOutcome carries advisory diagnostics for one mission mutation.
// Warnings do not revoke accepted authority or make an installed write fail.
// Required-event and rollback failures remain errors, not warnings.
// Warnings may accompany an error when reconciliation reported optional work
// for a previously accepted operation before the current mutation was refused.
// They never report an unaccepted forward write as a successful installation.
type MutationOutcome struct {
	Warnings []string `json:"warnings,omitempty"`
}

func (out *MutationOutcome) warn(message string) {
	if out == nil {
		fmt.Fprintln(os.Stderr, "ethos: "+message)
		return
	}
	out.Warnings = append(out.Warnings, message)
}

func (out *MutationOutcome) include(other MutationOutcome) {
	for _, warning := range other.Warnings {
		out.warn(warning)
	}
}

func (out MutationOutcome) sayWarnings() {
	for _, warning := range out.Warnings {
		fmt.Fprintln(os.Stderr, "ethos: "+warning)
	}
}

// Update retains the stderr diagnostics of the original API. Call
// UpdateWithOutcome to render diagnostics in a machine-readable response instead.
func (s *Store) Update(c *Contract) error {
	out, err := s.UpdateWithOutcome(c)
	out.sayWarnings()
	return err
}

// UpdateWithOutcome is Update with operation-scoped diagnostics and no warning
// output to stderr. It preserves Update's validation and rollback contract.
func (s *Store) UpdateWithOutcome(c *Contract) (MutationOutcome, error) {
	var out MutationOutcome
	err := s.update(c, &out)
	return out, err
}

// Close is CloseWithOutcome with compatibility diagnostics on stderr.
func (s *Store) Close(id, status string) (*Result, error) {
	r, out, err := s.CloseWithOutcome(id, status)
	out.sayWarnings()
	return r, err
}

// CloseWithOutcome returns the satisfying result and operation-scoped warnings.
// The result is non-nil exactly when the error is nil, as with Close.
func (s *Store) CloseWithOutcome(id, status string) (*Result, MutationOutcome, error) {
	var out MutationOutcome
	r, err := s.close(id, status, &out)
	return r, out, err
}

// Abandon is AbandonWithOutcome with compatibility diagnostics on stderr.
func (s *Store) Abandon(id, reason string) (*Contract, error) {
	c, out, err := s.AbandonWithOutcome(id, reason)
	out.sayWarnings()
	return c, err
}

// AbandonWithOutcome is Abandon with operation-scoped diagnostics, without
// changing its delegation and result gates.
func (s *Store) AbandonWithOutcome(id, reason string) (*Contract, MutationOutcome, error) {
	var out MutationOutcome
	c, err := s.abandon(id, reason, &out)
	return c, out, err
}

// AdvanceRound is AdvanceRoundWithOutcome with compatibility stderr warnings.
func (s *Store) AdvanceRound(id, actor string) (int, error) {
	round, out, err := s.AdvanceRoundWithOutcome(id, actor)
	out.sayWarnings()
	return round, err
}

// AdvanceRoundWithOutcome is AdvanceRound with operation-scoped diagnostics.
// Optional reflection trace failures do not alter permission to advance.
func (s *Store) AdvanceRoundWithOutcome(id, actor string) (int, MutationOutcome, error) {
	var out MutationOutcome
	round, err := s.advanceRound(id, actor, &out)
	return round, out, err
}

// AppendResult is AppendResultWithOutcome with compatibility stderr warnings.
func (s *Store) AppendResult(id string, r *Result) error {
	out, err := s.AppendResultWithOutcome(id, r)
	out.sayWarnings()
	return err
}

// AppendResultWithOutcome is AppendResult with operation-scoped diagnostics.
// Required-event failures still roll back the result and return an error.
func (s *Store) AppendResultWithOutcome(id string, r *Result) (MutationOutcome, error) {
	var out MutationOutcome
	err := s.appendResult(id, r, &out)
	return out, err
}

// Correct preserves the legacy stderr diagnostics, once per warning.
func (s *Store) Correct(id string, c Correction) error {
	out, err := s.CorrectWithOutcome(id, c)
	out.sayWarnings()
	return err
}

// CorrectWithOutcome carries prior accepted reflection recovery warnings even
// when this correction is refused. It prints no warnings to stderr and does not
// change correction acceptance, required-event errors or lifecycle guards.
func (s *Store) CorrectWithOutcome(id string, c Correction) (MutationOutcome, error) {
	var out MutationOutcome
	err := s.correct(id, c, &out)
	return out, err
}
