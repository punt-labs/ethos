//go:build !windows

package mission

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/team"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

// newContract returns a fresh, valid contract with the given mission ID
// for use in store tests.
func newContract(missionID string) *Contract {
	c := validContract()
	c.MissionID = missionID
	return &c
}

// disjointContract returns a fresh, valid contract whose write_set is
// unique to the given mission ID. Tests that create more than one
// mission in the same store must use this so the cross-mission
// write_set conflict check (Phase 3.2) does not collapse them.
//
// The write_set namespace `tests/<missionID>/` is deliberately fake —
// it never collides with real source paths and is trivially distinct
// per ID.
func disjointContract(missionID string) *Contract {
	c := validContract()
	c.MissionID = missionID
	c.WriteSet = []string{"tests/" + missionID + "/"}
	return &c
}

// submitRoundResult persists a minimal valid result for a mission's
// current round so the test can drive Store.Close through the
// Phase 3.6 gate. The helper is a one-liner for every pre-3.6 test
// whose only interest in the result artifact is "close is allowed".
// A round-aware variant (submitResultForRound) covers the rarer case
// of exercising the gate at a non-current round.
func submitRoundResult(t *testing.T, s *Store, c *Contract, verdict string) {
	t.Helper()
	r := &Result{
		Mission:    c.MissionID,
		Round:      c.CurrentRound,
		Author:     c.Worker,
		Verdict:    verdict,
		Confidence: 0.8,
		Evidence: []EvidenceCheck{
			{Name: "go test ./...", Status: EvidenceStatusPass},
		},
	}
	require.NoError(t, s.AppendResult(c.MissionID, r))
}

func TestStore_RoundTrip(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-001")

	require.NoError(t, s.Create(c))

	loaded, err := s.Load("m-2026-04-07-001")
	require.NoError(t, err)
	assert.Equal(t, c.MissionID, loaded.MissionID)
	assert.Equal(t, c.Status, loaded.Status)
	assert.Equal(t, c.CreatedAt, loaded.CreatedAt)
	assert.Equal(t, c.Leader, loaded.Leader)
	assert.Equal(t, c.Worker, loaded.Worker)
	assert.Equal(t, c.Evaluator.Handle, loaded.Evaluator.Handle)
	assert.Equal(t, c.Evaluator.PinnedAt, loaded.Evaluator.PinnedAt)
	assert.Equal(t, c.WriteSet, loaded.WriteSet)
	assert.Equal(t, c.Tools, loaded.Tools)
	assert.Equal(t, c.SuccessCriteria, loaded.SuccessCriteria)
	assert.Equal(t, c.Budget.Rounds, loaded.Budget.Rounds)
	assert.Equal(t, c.Budget.ReflectionAfterEach, loaded.Budget.ReflectionAfterEach)
	assert.Equal(t, c.Inputs.Bead, loaded.Inputs.Bead)
	assert.Equal(t, c.Inputs.Files, loaded.Inputs.Files)
}

func TestStore_CreateRejectsNil(t *testing.T) {
	s := testStore(t)
	require.Error(t, s.Create(nil))
}

func TestStore_CreateRejectsInvalid(t *testing.T) {
	s := testStore(t)
	c := newContract("not-a-mission-id")
	require.Error(t, s.Create(c))
}

// TestStore_CreateDoesNotMutateCallerOnValidationFailure asserts that
// a Create that fails Validate() leaves the caller's struct untouched.
// Create uses a shallow-copy pattern so UpdatedAt's default-fill never
// leaks back to the caller on a failure path.
//
// The test explicitly clears UpdatedAt to exercise the default-fill
// path: if Create were to mutate the caller's struct (by setting
// UpdatedAt = CreatedAt before the shallow copy), the post-failure
// assertion would catch it because the observed UpdatedAt would have
// been replaced with CreatedAt.
func TestStore_CreateDoesNotMutateCallerOnValidationFailure(t *testing.T) {
	s := testStore(t)
	// Start with a valid contract, clear UpdatedAt so the default-fill
	// path is exercised, then break it by emptying WriteSet so Validate
	// rejects it.
	c := newContract("m-2026-04-07-030")
	c.UpdatedAt = "" // force the default-fill code path
	c.WriteSet = nil // validation will reject this

	err := s.Create(c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write_set")
	assert.Equal(t, "", c.UpdatedAt, "Create must not default-fill UpdatedAt on failure")
}

// TestStore_CreateSuccessReflectsUpdatedAt asserts that a successful
// Create reflects the (possibly defaulted) UpdatedAt back to the
// caller. This is the one field Create is contracted to set, and the
// caller needs it to build a consistent in-memory view after the
// round-trip.
func TestStore_CreateSuccessReflectsUpdatedAt(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-031")
	c.UpdatedAt = "" // force the default-fill path
	require.NoError(t, s.Create(c))
	assert.Equal(t, c.CreatedAt, c.UpdatedAt, "Create must default UpdatedAt to CreatedAt and reflect it back")
}

func TestStore_CreateRejectsDuplicate(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-001")
	require.NoError(t, s.Create(c))

	dup := newContract("m-2026-04-07-001")
	err := s.Create(dup)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestStore_LoadNotFound(t *testing.T) {
	s := testStore(t)
	_, err := s.Load("m-2026-04-07-001")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_LoadEmptyID(t *testing.T) {
	s := testStore(t)
	_, err := s.Load("")
	require.Error(t, err)
}

func TestStore_LoadCorruptYAML(t *testing.T) {
	s := testStore(t)
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(s.missionsDir(), "m-2026-04-07-001.yaml"),
		[]byte("not: [valid"), 0o600,
	))
	_, err := s.Load("m-2026-04-07-001")
	require.Error(t, err)
}

func TestStore_LoadFailsValidation(t *testing.T) {
	s := testStore(t)
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	// Persist a contract that parses as YAML but fails Validate.
	bad := []byte(`mission_id: bogus
status: open
created_at: 2026-04-07T21:30:00Z
updated_at: 2026-04-07T21:30:00Z
leader: claude
worker: bwk
evaluator:
  handle: djb
  pinned_at: 2026-04-07T21:30:00Z
inputs: {}
write_set:
  - internal/mission/
success_criteria:
  - foo
budget:
  rounds: 3
`)
	require.NoError(t, os.WriteFile(
		filepath.Join(s.missionsDir(), "m-2026-04-07-001.yaml"),
		bad, 0o600,
	))
	_, err := s.Load("m-2026-04-07-001")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validation")
}

// TestStore_LoadRejectsUnknownField asserts that Store.Load uses
// KnownFields(true). An attacker with local write access cannot drop
// extra fields into the on-disk YAML and have them silently ignored —
// the trust boundary on disk is symmetric with the create paths.
func TestStore_LoadRejectsUnknownField(t *testing.T) {
	s := testStore(t)
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	bad := []byte(`mission_id: m-2026-04-07-001
status: open
created_at: 2026-04-07T21:30:00Z
updated_at: 2026-04-07T21:30:00Z
leader: claude
worker: bwk
evaluator:
  handle: djb
  pinned_at: 2026-04-07T21:30:00Z
inputs: {}
write_set:
  - internal/mission/
success_criteria:
  - make check passes
budget:
  rounds: 3
bogus: smuggled
`)
	require.NoError(t, os.WriteFile(
		filepath.Join(s.missionsDir(), "m-2026-04-07-001.yaml"),
		bad, 0o600,
	))
	_, err := s.Load("m-2026-04-07-001")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field bogus not found")
}

// TestStore_CloseFailsOnUnknownField asserts that Close's loadLocked
// path also rejects unknown fields. Round 2 added Validate symmetry;
// round 3 adds KnownFields symmetry.
func TestStore_CloseFailsOnUnknownField(t *testing.T) {
	s := testStore(t)
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	bad := []byte(`mission_id: m-2026-04-07-001
status: open
created_at: 2026-04-07T21:30:00Z
updated_at: 2026-04-07T21:30:00Z
leader: claude
worker: bwk
evaluator:
  handle: djb
  pinned_at: 2026-04-07T21:30:00Z
inputs: {}
write_set:
  - internal/mission/
success_criteria:
  - make check passes
budget:
  rounds: 3
bogus: smuggled
`)
	path := filepath.Join(s.missionsDir(), "m-2026-04-07-001.yaml")
	require.NoError(t, os.WriteFile(path, bad, 0o600))

	originalBytes, err := os.ReadFile(path)
	require.NoError(t, err)

	err = s.Close("m-2026-04-07-001", StatusClosed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field bogus not found")

	// File on disk must be untouched.
	afterBytes, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, originalBytes, afterBytes, "Close must not mutate a YAML rejected by KnownFields")
}

func TestStore_Update(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-002")
	require.NoError(t, s.Create(c))

	loaded, err := s.Load("m-2026-04-07-002")
	require.NoError(t, err)
	loaded.Context = "added context after creation"
	require.NoError(t, s.Update(loaded))

	reloaded, err := s.Load("m-2026-04-07-002")
	require.NoError(t, err)
	assert.Equal(t, "added context after creation", reloaded.Context)
	// UpdatedAt should have moved forward.
	assert.NotEqual(t, c.CreatedAt, reloaded.UpdatedAt)
}

func TestStore_UpdateMissing(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-099")
	err := s.Update(c)
	require.Error(t, err)
}

// TestStore_UpdateDoesNotMutateCallerOnFailure asserts that a failed
// Update leaves the caller's Contract struct in its pre-call state.
// This is the contract for every method that takes a pointer argument:
// failure must not mutate.
func TestStore_UpdateDoesNotMutateCallerOnFailure(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-001")
	require.NoError(t, s.Create(c))

	loaded, err := s.Load("m-2026-04-07-001")
	require.NoError(t, err)

	originalUpdatedAt := loaded.UpdatedAt
	originalWriteSet := append([]string(nil), loaded.WriteSet...)

	// Force a validation failure by clearing the write_set — Update's
	// shallow-copy pattern must reject this without touching the caller.
	loaded.WriteSet = nil
	err = s.Update(loaded)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid contract")

	// Caller's struct is unchanged — UpdatedAt not bumped, WriteSet is
	// still whatever the caller set it to before the failed call.
	assert.Equal(t, originalUpdatedAt, loaded.UpdatedAt, "UpdatedAt must not change on failed Update")
	assert.Nil(t, loaded.WriteSet, "Update must not touch the caller's WriteSet")
	_ = originalWriteSet // kept for readability of the intent above
}

// TestStore_UpdateSuccessReflectsUpdatedAt asserts that a successful
// Update does reflect the new UpdatedAt back to the caller — this is
// the one field Update is contracted to mutate on success.
func TestStore_UpdateSuccessReflectsUpdatedAt(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-001")
	require.NoError(t, s.Create(c))

	loaded, err := s.Load("m-2026-04-07-001")
	require.NoError(t, err)
	originalUpdatedAt := loaded.UpdatedAt

	// Real mutation: add context. The validContract's CreatedAt is
	// hardcoded in the past, so time.Now() inside Update will always
	// produce a different RFC3339 string — no sleep needed.
	loaded.Context = "post-create mutation"
	require.NoError(t, s.Update(loaded))

	assert.NotEqual(t, originalUpdatedAt, loaded.UpdatedAt, "UpdatedAt must advance on success")
}

func TestStore_Close(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-003")
	require.NoError(t, s.Create(c))
	submitRoundResult(t, s, c, VerdictPass)

	require.NoError(t, s.Close("m-2026-04-07-003", StatusClosed))

	loaded, err := s.Load("m-2026-04-07-003")
	require.NoError(t, err)
	assert.Equal(t, StatusClosed, loaded.Status)
	assert.NotEmpty(t, loaded.ClosedAt)
}

func TestStore_CloseRejectsOpenStatus(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-004")
	require.NoError(t, s.Create(c))
	err := s.Close("m-2026-04-07-004", StatusOpen)
	require.Error(t, err)
}

func TestStore_CloseRejectsUnknownStatus(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-005")
	require.NoError(t, s.Create(c))
	err := s.Close("m-2026-04-07-005", "abandoned")
	require.Error(t, err)
}

// TestStore_CloseRejectsAlreadyTerminal asserts that re-closing a
// mission that's already in a terminal state fails. Re-closing would
// silently overwrite the original closed_at timestamp and append a
// duplicate "close" event to the JSONL log, violating the
// one-transition-per-event invariant.
func TestStore_CloseRejectsAlreadyTerminal(t *testing.T) {
	cases := []string{StatusClosed, StatusFailed, StatusEscalated}
	s := testStore(t)
	for i, firstStatus := range cases {
		id := fmt.Sprintf("m-2026-04-07-%03d", 40+i)
		t.Run(firstStatus, func(t *testing.T) {
			c := newContract(id)
			require.NoError(t, s.Create(c))
			submitRoundResult(t, s, c, VerdictPass)
			require.NoError(t, s.Close(id, firstStatus))

			// Second close must fail regardless of the target status.
			for _, secondStatus := range cases {
				err := s.Close(id, secondStatus)
				require.Error(t, err, "re-closing %s with %s must fail", firstStatus, secondStatus)
				assert.Contains(t, err.Error(), "already in terminal state")
			}
		})
	}
}

// TestDecodeContractStrict_RejectsMultipleDocuments asserts that a
// YAML stream containing two complete documents is rejected. Multi-
// document input could be used to smuggle content past the trust
// boundary by appending a second document to a legitimate contract.
func TestDecodeContractStrict_RejectsMultipleDocuments(t *testing.T) {
	// Build a valid contract YAML, then append a second document.
	c := validContract()
	first, err := yaml.Marshal(&c)
	require.NoError(t, err)
	second := []byte("---\nmission_id: m-2026-04-07-999\n")
	combined := append(first, second...)

	_, err = DecodeContractStrict(combined, "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple YAML documents")
}

// TestDecodeContractStrict_RejectsTrailingContent asserts that a
// valid contract followed by trailing scalar content is rejected.
// Trailing content is a subtler form of the multi-document smuggling
// attack.
func TestDecodeContractStrict_RejectsTrailingContent(t *testing.T) {
	c := validContract()
	first, err := yaml.Marshal(&c)
	require.NoError(t, err)
	combined := append(first, []byte("---\nextra_scalar\n")...)

	_, err = DecodeContractStrict(combined, "test")
	require.Error(t, err)
	// Either "multiple YAML documents" or "trailing content" — both
	// are valid ways for the decoder to describe the extra stream.
	msg := err.Error()
	assert.True(t,
		strings.Contains(msg, "multiple YAML documents") || strings.Contains(msg, "trailing content"),
		"expected multi-document or trailing-content error, got: %s", msg)
}

// TestDecodeContractStrict_AcceptsSingleDocument asserts that a
// well-formed single-document contract decodes successfully — the
// strict decoder must not be overly aggressive.
func TestDecodeContractStrict_AcceptsSingleDocument(t *testing.T) {
	c := validContract()
	data, err := yaml.Marshal(&c)
	require.NoError(t, err)
	parsed, err := DecodeContractStrict(data, "test")
	require.NoError(t, err)
	assert.Equal(t, c.MissionID, parsed.MissionID)
}

// TestStore_UpdateRollsBackOnEventAppendFailure asserts that Update
// restores the original on-disk contract bytes when appending the
// update event fails, matching the method's atomic-from-caller's-view
// contract. The failure is induced by replacing the mission's log
// file with a directory, which makes os.OpenFile(O_APPEND) fail.
func TestStore_UpdateRollsBackOnEventAppendFailure(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-020")
	require.NoError(t, s.Create(c))

	// Snapshot the on-disk contract and the caller's struct before
	// the sabotaged update.
	contractPath := s.contractPath(c.MissionID)
	originalBytes, err := os.ReadFile(contractPath)
	require.NoError(t, err)
	originalUpdatedAt := c.UpdatedAt

	// Sabotage: remove the log file and replace it with a directory
	// so appendEventLocked's OpenFile call fails.
	logPath := s.logPath(c.MissionID)
	require.NoError(t, os.Remove(logPath))
	require.NoError(t, os.Mkdir(logPath, 0o700))

	// Attempt a real update — add context.
	c.Context = "this update must roll back"
	err = s.Update(c)
	require.Error(t, err, "Update must fail when the event log is unreachable")
	assert.Contains(t, err.Error(), "event append failed")
	assert.Contains(t, err.Error(), "contract rolled back")

	// Verify the on-disk bytes match the original exactly.
	restoredBytes, err := os.ReadFile(contractPath)
	require.NoError(t, err)
	assert.Equal(t, string(originalBytes), string(restoredBytes), "contract bytes must be identical after rollback")

	// Caller's struct must not have been mutated.
	assert.Equal(t, originalUpdatedAt, c.UpdatedAt, "caller UpdatedAt must not advance on failed Update")
}

// TestStore_CloseRollsBackOnEventAppendFailure asserts the same
// atomic-rollback behavior for Close: if the event-log append fails,
// the on-disk contract bytes are restored to the pre-close state.
func TestStore_CloseRollsBackOnEventAppendFailure(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-021")
	require.NoError(t, s.Create(c))
	// Phase 3.6: the close gate requires a result; submit one before
	// sabotaging the log so the sabotage exercises the rollback path,
	// not the gate refusal.
	submitRoundResult(t, s, c, VerdictPass)

	contractPath := s.contractPath(c.MissionID)
	originalBytes, err := os.ReadFile(contractPath)
	require.NoError(t, err)

	// Sabotage the log path the same way as the Update test.
	logPath := s.logPath(c.MissionID)
	require.NoError(t, os.Remove(logPath))
	require.NoError(t, os.Mkdir(logPath, 0o700))

	err = s.Close(c.MissionID, StatusClosed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "event append failed")
	assert.Contains(t, err.Error(), "contract rolled back")

	restoredBytes, err := os.ReadFile(contractPath)
	require.NoError(t, err)
	assert.Equal(t, string(originalBytes), string(restoredBytes), "closed contract bytes must match pre-close state")
}

// TestStore_CreateRollsBackOnEventAppendFailure asserts that Create
// removes the contract file when the event append fails so a retry
// doesn't hit "already exists."
func TestStore_CreateRollsBackOnEventAppendFailure(t *testing.T) {
	s := testStore(t)
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))

	// Pre-create a directory at the log path so appendEventLocked's
	// OpenFile call fails when Create tries to log the create event.
	missionID := "m-2026-04-07-022"
	logPath := filepath.Join(s.missionsDir(), missionID+".jsonl")
	require.NoError(t, os.Mkdir(logPath, 0o700))

	c := newContract(missionID)
	err := s.Create(c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "event append failed")
	assert.Contains(t, err.Error(), "contract rolled back")

	// The contract file must not exist after rollback.
	_, statErr := os.Stat(s.contractPath(missionID))
	assert.True(t, os.IsNotExist(statErr), "contract file must be removed on rollback")
}

func TestStore_CloseAcceptsFailedAndEscalated(t *testing.T) {
	cases := map[string]string{
		StatusFailed:    "m-2026-04-07-006",
		StatusEscalated: "m-2026-04-07-007",
	}
	s := testStore(t)
	for status, id := range cases {
		t.Run(status, func(t *testing.T) {
			c := disjointContract(id)
			require.NoError(t, s.Create(c))
			submitRoundResult(t, s, c, VerdictPass)
			require.NoError(t, s.Close(id, status))
			loaded, err := s.Load(id)
			require.NoError(t, err)
			assert.Equal(t, status, loaded.Status)
		})
	}
}

// TestStore_CloseFailsOnCorruptContract asserts that Close refuses to
// mutate a contract that fails validation on load. A hand-edited or
// corrupt on-disk YAML must be rejected before Close touches it,
// otherwise the pre-existing corruption could slip through Close's
// post-mutation Validate (the mutation might happen to fix the field
// under inspection).
func TestStore_CloseFailsOnCorruptContract(t *testing.T) {
	s := testStore(t)
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))

	missionID := "m-2026-04-07-999"
	// Parseable YAML but invalid mission_id (fails rule 1).
	corrupt := []byte(`mission_id: bogus-id
status: open
created_at: 2026-04-07T21:30:00Z
updated_at: 2026-04-07T21:30:00Z
leader: claude
worker: bwk
evaluator:
  handle: djb
  pinned_at: 2026-04-07T21:30:00Z
inputs: {}
write_set:
  - internal/mission/
success_criteria:
  - make check passes
budget:
  rounds: 3
`)
	path := s.contractPath(missionID)
	require.NoError(t, os.WriteFile(path, corrupt, 0o600))

	originalBytes, err := os.ReadFile(path)
	require.NoError(t, err)

	err = s.Close(missionID, StatusClosed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed validation on load")

	// File on disk must be untouched.
	afterBytes, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, originalBytes, afterBytes, "corrupt contract must not be mutated by a failed Close")
}

func TestStore_List(t *testing.T) {
	s := testStore(t)

	// Empty list.
	ids, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, ids)

	require.NoError(t, s.Create(disjointContract("m-2026-04-07-001")))
	require.NoError(t, s.Create(disjointContract("m-2026-04-07-002")))

	ids, err = s.List()
	require.NoError(t, err)
	assert.Len(t, ids, 2)
	assert.Contains(t, ids, "m-2026-04-07-001")
	assert.Contains(t, ids, "m-2026-04-07-002")
}

func TestStore_ListSkipsCounter(t *testing.T) {
	s := testStore(t)
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))

	// Drop a counter file directly to simulate the ID generator state.
	require.NoError(t, os.WriteFile(
		filepath.Join(s.missionsDir(), ".counter-2026-04-07"),
		[]byte("3"), 0o600,
	))

	require.NoError(t, s.Create(newContract("m-2026-04-07-001")))

	ids, err := s.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"m-2026-04-07-001"}, ids)
}

func TestStore_ListNoDirectory(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "nonexistent"))
	ids, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestStore_MatchByPrefix(t *testing.T) {
	s := testStore(t)
	require.NoError(t, s.Create(disjointContract("m-2026-04-07-001")))
	require.NoError(t, s.Create(disjointContract("m-2026-04-07-002")))
	require.NoError(t, s.Create(disjointContract("m-2026-04-08-001")))

	tests := []struct {
		name    string
		prefix  string
		want    string
		wantErr string
	}{
		{
			name:   "exact match",
			prefix: "m-2026-04-07-001",
			want:   "m-2026-04-07-001",
		},
		{
			name:   "unique prefix",
			prefix: "m-2026-04-08",
			want:   "m-2026-04-08-001",
		},
		{
			name:    "ambiguous prefix",
			prefix:  "m-2026-04-07",
			wantErr: "ambiguous prefix",
		},
		{
			name:    "no match",
			prefix:  "m-2025",
			wantErr: "no mission matching prefix",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.MatchByPrefix(tt.prefix)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestStore_FilePermissions(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-001")
	require.NoError(t, s.Create(c))

	info, err := os.Stat(s.contractPath("m-2026-04-07-001"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestStore_ConcurrentCreate(t *testing.T) {
	// Each goroutine creates a distinct mission ID with a disjoint
	// write_set. The test asserts the store does not corrupt state
	// under concurrent flock acquisition. Phase 3.2's create-lock
	// serializes the conflict scan so each goroutine sees a stable
	// view of the registry.
	s := testStore(t)
	const n = 20

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("m-2026-04-07-%03d", i+1)
			c := disjointContract(id)
			if err := s.Create(c); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	ids, err := s.List()
	require.NoError(t, err)
	assert.Len(t, ids, n)
}

func TestStore_ConcurrentUpdateSameMission(t *testing.T) {
	// Two goroutines mutate the same mission. The flock must serialize
	// them; both writes must succeed and the final state must be
	// well-formed YAML that loads cleanly.
	s := testStore(t)
	c := newContract("m-2026-04-07-001")
	require.NoError(t, s.Create(c))

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			loaded, err := s.Load("m-2026-04-07-001")
			if err != nil {
				errs <- err
				return
			}
			loaded.Context = fmt.Sprintf("writer-%03d", i)
			if err := s.Update(loaded); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	loaded, err := s.Load("m-2026-04-07-001")
	require.NoError(t, err)
	assert.NotEmpty(t, loaded.Context)
}

// withWriteSet returns a fresh valid contract with the given mission
// ID and write_set. Used by the cross-mission conflict integration
// tests below.
func withWriteSet(missionID string, writeSet ...string) *Contract {
	c := validContract()
	c.MissionID = missionID
	c.WriteSet = writeSet
	return &c
}

// TestStore_CreateRejectsCrossMissionConflict asserts the Phase 3.2
// admission control: a Create whose write_set overlaps an existing
// open mission's write_set must fail, and the failure must leave no
// trace on disk for the rejected mission.
func TestStore_CreateRejectsCrossMissionConflict(t *testing.T) {
	s := testStore(t)

	a := withWriteSet("m-2026-04-08-001", "internal/foo/")
	require.NoError(t, s.Create(a))

	// Snapshot mission A on disk so we can prove a rejected create
	// does not perturb existing missions.
	aPath := s.contractPath(a.MissionID)
	aBytesBefore, err := os.ReadFile(aPath)
	require.NoError(t, err)
	aLogPath := s.logPath(a.MissionID)
	aLogBefore, err := os.ReadFile(aLogPath)
	require.NoError(t, err)

	b := withWriteSet("m-2026-04-08-002", "internal/foo/bar.go")
	err = s.Create(b)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write_set conflict")
	assert.Contains(t, err.Error(), a.MissionID)
	assert.Contains(t, err.Error(), "worker: bwk")
	assert.Contains(t, err.Error(), "internal/foo/bar.go")

	// No on-disk traces for the rejected mission B.
	_, statErr := os.Stat(s.contractPath(b.MissionID))
	assert.True(t, os.IsNotExist(statErr), "B's contract file must not exist")
	_, statErr = os.Stat(s.logPath(b.MissionID))
	assert.True(t, os.IsNotExist(statErr), "B's log file must not exist")

	// Mission A's contract and log are byte-identical to before the
	// rejected create.
	aBytesAfter, err := os.ReadFile(aPath)
	require.NoError(t, err)
	assert.Equal(t, aBytesBefore, aBytesAfter, "mission A's contract must be unchanged")
	aLogAfter, err := os.ReadFile(aLogPath)
	require.NoError(t, err)
	assert.Equal(t, aLogBefore, aLogAfter, "mission A's log must be unchanged")
}

// TestStore_CreateAllowsConflictAfterClose asserts that closed,
// failed, and escalated missions are out of the conflict registry —
// only StatusOpen blocks a new create.
func TestStore_CreateAllowsConflictAfterClose(t *testing.T) {
	cases := []string{StatusClosed, StatusFailed, StatusEscalated}
	for _, terminal := range cases {
		t.Run(terminal, func(t *testing.T) {
			s := testStore(t)

			a := withWriteSet("m-2026-04-08-001", "internal/foo/")
			require.NoError(t, s.Create(a))
			submitRoundResult(t, s, a, VerdictPass)
			require.NoError(t, s.Close(a.MissionID, terminal))

			// Overlapping create must succeed: A is no longer active.
			b := withWriteSet("m-2026-04-08-002", "internal/foo/bar.go")
			require.NoError(t, s.Create(b))

			loaded, err := s.Load(b.MissionID)
			require.NoError(t, err)
			assert.Equal(t, StatusOpen, loaded.Status)
		})
	}
}

// TestStore_CreateAllowsDisjointWriteSets asserts that two open
// missions with non-overlapping write_sets coexist without error.
// This is the happy path: the conflict check must not be a blanket
// "one mission at a time" gate.
func TestStore_CreateAllowsDisjointWriteSets(t *testing.T) {
	s := testStore(t)

	a := withWriteSet("m-2026-04-08-001", "internal/foo/")
	require.NoError(t, s.Create(a))

	b := withWriteSet("m-2026-04-08-002", "cmd/ethos/")
	require.NoError(t, s.Create(b))

	ids, err := s.List()
	require.NoError(t, err)
	assert.Len(t, ids, 2)
}

// TestStore_CreateMultiConflictReportsAllBlockers asserts that a new
// mission overlapping two existing open missions surfaces both
// blockers in the error message — one line per blocker.
func TestStore_CreateMultiConflictReportsAllBlockers(t *testing.T) {
	s := testStore(t)

	a := withWriteSet("m-2026-04-08-001", "internal/foo/")
	require.NoError(t, s.Create(a))

	b := withWriteSet("m-2026-04-08-002", "cmd/ethos/")
	require.NoError(t, s.Create(b))

	c := withWriteSet("m-2026-04-08-003",
		"internal/foo/bar.go",
		"cmd/ethos/serve.go",
	)
	err := s.Create(c)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, a.MissionID)
	assert.Contains(t, msg, b.MissionID)
	assert.Contains(t, msg, "internal/foo/bar.go")
	assert.Contains(t, msg, "cmd/ethos/serve.go")

	// Multi-conflict errors are one line per blocker.
	lines := strings.Split(msg, "\n")
	assert.Len(t, lines, 2, "expected one line per blocking mission")
}

// TestStore_CreateConflictLeavesNoArtifacts asserts the rollback
// criterion: a conflict-rejected create must leave no partial state
// (no <id>.yaml, no <id>.jsonl, no .tmp leftovers) for the rejected
// mission. Empty lock files are acceptable — they are stable, named
// after the mission ID, and used to serialize concurrent attempts.
func TestStore_CreateConflictLeavesNoArtifacts(t *testing.T) {
	s := testStore(t)

	a := withWriteSet("m-2026-04-08-001", "internal/foo/")
	require.NoError(t, s.Create(a))

	b := withWriteSet("m-2026-04-08-002", "internal/foo/bar.go")
	err := s.Create(b)
	require.Error(t, err)

	// No contract or log file for the rejected mission.
	_, statErr := os.Stat(s.contractPath(b.MissionID))
	assert.True(t, os.IsNotExist(statErr), "rejected mission must have no contract file")
	_, statErr = os.Stat(s.logPath(b.MissionID))
	assert.True(t, os.IsNotExist(statErr), "rejected mission must have no log file")

	// No .tmp leftovers anywhere in the missions directory.
	entries, err := os.ReadDir(s.missionsDir())
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasSuffix(e.Name(), ".tmp"),
			"unexpected .tmp leftover after rejected create: %s", e.Name())
	}
}

// TestStore_CreateConcurrentConflictSerialization asserts the
// directory-level create lock: 10 goroutines try to claim the same
// write_set with disjoint mission IDs; exactly one succeeds and the
// other 9 see a conflict error. Without the create lock all 10 would
// race past their conflict scan and corrupt each other.
func TestStore_CreateConcurrentConflictSerialization(t *testing.T) {
	s := testStore(t)
	const n = 10

	var wg sync.WaitGroup
	errs := make(chan error, n)
	successes := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("m-2026-04-08-%03d", i+1)
			// Same write_set on every goroutine — only one can win.
			c := withWriteSet(id, "internal/contended/")
			if err := s.Create(c); err != nil {
				errs <- err
				return
			}
			successes <- id
		}()
	}
	wg.Wait()
	close(errs)
	close(successes)

	var winners []string
	for id := range successes {
		winners = append(winners, id)
	}
	var failures []error
	for err := range errs {
		failures = append(failures, err)
	}
	assert.Len(t, winners, 1, "exactly one concurrent Create must succeed")
	assert.Len(t, failures, n-1, "all other concurrent Creates must fail")
	for _, err := range failures {
		assert.Contains(t, err.Error(), "write_set conflict",
			"failure must be the conflict error, not a lock error")
	}
}

// fakeHashSources returns a HashSources that resolves the given handle
// to a stub identity with deterministic content. Tests use this to
// exercise ApplyServerFields without depending on the live identity,
// role, or team stores.
//
// The stub content is fixed (`<handle>-personality`, etc.) so two
// successive ApplyServerFields calls against the same fake produce
// the same hash — exactly the determinism invariant the tests assert.
func fakeHashSources(handles ...string) HashSources {
	ids := make(map[string]*EvaluatorIdentity, len(handles))
	for _, h := range handles {
		ids[h] = &EvaluatorIdentity{
			Handle:              h,
			PersonalityContent:  h + "-personality",
			WritingStyleContent: h + "-writing-style",
			Talents:             []string{"engineering"},
			TalentContents:      []string{h + "-engineering-content"},
		}
	}
	return HashSources{
		Identities: &mapIdentityLoader{m: ids},
		Roles:      &mapRoleLister{m: nil},
	}
}

// mapIdentityLoader is a tiny in-memory IdentityLoader for store tests.
type mapIdentityLoader struct {
	m map[string]*EvaluatorIdentity
}

func (l *mapIdentityLoader) LoadEvaluator(handle string) (*EvaluatorIdentity, error) {
	id, ok := l.m[handle]
	if !ok {
		return nil, fmt.Errorf("identity %q not found", handle)
	}
	return id, nil
}

// mapRoleLister is a tiny in-memory RoleLister for store tests.
type mapRoleLister struct {
	m map[string][]EvaluatorRole
}

func (l *mapRoleLister) ListRoles(handle string) ([]EvaluatorRole, error) {
	return l.m[handle], nil
}

// TestApplyServerFields_PopulatesEvaluatorHash asserts that the
// fix-it-now invariant from DES-033 holds: every contract created via
// ApplyServerFields carries a non-empty Evaluator.Hash equal to the
// deterministic recomputation of the seeded content.
func TestApplyServerFields_PopulatesEvaluatorHash(t *testing.T) {
	s := testStore(t)
	c := validContract()
	c.MissionID = ""
	c.CreatedAt = ""
	c.UpdatedAt = ""
	c.Evaluator.PinnedAt = ""
	c.Evaluator.Hash = ""

	sources := fakeHashSources("djb")
	require.NoError(t, s.ApplyServerFields(&c, time.Now(), sources))

	assert.NotEmpty(t, c.Evaluator.Hash, "ApplyServerFields must populate Evaluator.Hash")
	assert.Len(t, c.Evaluator.Hash, 64, "Evaluator.Hash must be a 64-char hex sha256")

	// Recompute against the same sources and assert exact equality.
	expected, err := ComputeEvaluatorHash("djb", sources)
	require.NoError(t, err)
	assert.Equal(t, expected, c.Evaluator.Hash)
}

// TestApplyServerFields_DeterministicAcrossCalls asserts the
// determinism invariant: two ApplyServerFields calls against
// byte-identical sources yield byte-identical hashes. The mission_id
// differs (counter advances), so the test compares only the hash field.
func TestApplyServerFields_DeterministicAcrossCalls(t *testing.T) {
	s := testStore(t)
	sources := fakeHashSources("djb")

	a := validContract()
	require.NoError(t, s.ApplyServerFields(&a, time.Now(), sources))
	b := validContract()
	require.NoError(t, s.ApplyServerFields(&b, time.Now(), sources))

	assert.Equal(t, a.Evaluator.Hash, b.Evaluator.Hash,
		"two ApplyServerFields calls against identical sources must produce identical hashes")
}

// TestApplyServerFields_RejectsUnresolvableEvaluator asserts that an
// evaluator handle the loader cannot resolve fails ApplyServerFields
// with a wrapped error AND leaves no on-disk artifacts (the mission
// is not created).
func TestApplyServerFields_RejectsUnresolvableEvaluator(t *testing.T) {
	s := testStore(t)
	c := validContract()
	c.Evaluator.Handle = "ghost"

	sources := fakeHashSources("djb") // ghost is not in the source map
	err := s.ApplyServerFields(&c, time.Now(), sources)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ghost")

	// No mission file was created.
	ids, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, ids)
}

// errNotExistLoader is a tiny IdentityLoader that always returns a
// wrapped os.ErrNotExist. It exercises ApplyServerFields' error
// collapse path — the one that turns the 6-level wrapped chain from
// hash.go into a single-line ErrEvaluatorNotFound with the recovery
// hint.
type errNotExistLoader struct{}

func (errNotExistLoader) LoadEvaluator(handle string) (*EvaluatorIdentity, error) {
	return nil, fmt.Errorf("reading identity %q: %w", handle, os.ErrNotExist)
}

// TestApplyServerFields_RejectsUnresolvableEvaluator_NotExistCollapse
// asserts the error collapse that store.go performs when the hash
// loader returns an os.ErrNotExist-wrapped chain. The operator-facing
// error must match the ErrEvaluatorNotFound sentinel AND carry the
// `ethos identity list` recovery hint — without the collapse the
// operator sees six levels of wrap and no actionable guidance.
func TestApplyServerFields_RejectsUnresolvableEvaluator_NotExistCollapse(t *testing.T) {
	s := testStore(t)
	c := validContract()
	c.Evaluator.Handle = "ghost"

	sources := HashSources{
		Identities: errNotExistLoader{},
		Roles:      &mapRoleLister{m: nil},
	}
	err := s.ApplyServerFields(&c, time.Now(), sources)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrEvaluatorNotFound),
		"errors.Is(err, ErrEvaluatorNotFound) must be true for the collapse path")
	assert.Contains(t, err.Error(), "ghost")
	assert.Contains(t, err.Error(), "ethos identity list",
		"operator-facing error must carry the recovery hint")

	// No mission file was created.
	ids, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, ids)
}

// TestApplyServerFields_RejectsNilSources asserts that a misconfigured
// caller (nil identity loader or nil role lister) fails fast with an
// explicit error rather than silently producing an empty hash.
func TestApplyServerFields_RejectsNilSources(t *testing.T) {
	s := testStore(t)
	c := validContract()
	err := s.ApplyServerFields(&c, time.Now(), HashSources{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Identities loader is nil")
}

// TestApplyServerFields_RejectsEmptyEvaluatorHandle asserts that an
// empty handle is caught at the boundary so the hash function never
// receives a zero-value handle. The contract Validate() also rejects
// it, but ApplyServerFields' early check produces a clearer
// diagnostic for the operator.
func TestApplyServerFields_RejectsEmptyEvaluatorHandle(t *testing.T) {
	s := testStore(t)
	c := validContract()
	c.Evaluator.Handle = ""
	sources := fakeHashSources("djb")
	err := s.ApplyServerFields(&c, time.Now(), sources)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "evaluator.handle is required")
}

// TestApplyServerFields_LiveStoresRoundTrip is the success-criterion-1
// integration test from DES-033: seed a minimal identity through the
// real identity, attribute, and role stores; create a mission naming
// the seeded handle; assert the recorded hash is non-empty and equal
// to a deterministic recomputation against the same content.
//
// This is the end-to-end shape the live CLI/MCP path takes — fake
// loaders verify the algorithm, this test verifies the wiring.
func TestApplyServerFields_LiveStoresRoundTrip(t *testing.T) {
	dir := t.TempDir()
	is := identity.NewStore(dir)

	// Seed personality, writing-style, and talent files.
	require.NoError(t, attribute.NewStore(dir, attribute.Personalities).Save(&attribute.Attribute{
		Slug:    "bernstein",
		Content: "# Bernstein\n\nMethodical security review.\n",
	}))
	require.NoError(t, attribute.NewStore(dir, attribute.WritingStyles).Save(&attribute.Attribute{
		Slug:    "bernstein-prose",
		Content: "# Bernstein Prose\n\nShort declarative sentences.\n",
	}))
	require.NoError(t, attribute.NewStore(dir, attribute.Talents).Save(&attribute.Attribute{
		Slug:    "security",
		Content: "# Security\n\nThreat modeling.\n",
	}))
	require.NoError(t, is.Save(&identity.Identity{
		Name:         "Dan B",
		Handle:       "djb",
		Kind:         "agent",
		Personality:  "bernstein",
		WritingStyle: "bernstein-prose",
		Talents:      []string{"security"},
	}))

	ms := NewStore(dir)
	sources, err := NewLiveHashSources(is, role.NewLayeredStore("", dir), team.NewLayeredStore("", dir))
	require.NoError(t, err)

	c := validContract()
	c.MissionID = ""
	c.CreatedAt = ""
	c.UpdatedAt = ""
	c.Evaluator.PinnedAt = ""
	c.Evaluator.Hash = ""

	require.NoError(t, ms.ApplyServerFields(&c, time.Now(), sources))
	require.NoError(t, ms.Create(&c))

	// Hash is non-empty and matches a fresh recomputation against
	// the same live sources.
	require.NotEmpty(t, c.Evaluator.Hash)
	require.Len(t, c.Evaluator.Hash, 64)
	expected, err := ComputeEvaluatorHash("djb", sources)
	require.NoError(t, err)
	assert.Equal(t, expected, c.Evaluator.Hash)

	// And the hash survives the YAML round trip.
	loaded, err := ms.Load(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, c.Evaluator.Hash, loaded.Evaluator.Hash)
}

// TestApplyServerFields_LiveStoresEachSourceMatters reproduces the
// edit-detection table from hash_test.go but against real on-disk
// stores: each .md file is rewritten between two ApplyServerFields
// calls and the resulting hashes are asserted to differ. This is the
// integration-level proof of success criterion 3.
func TestApplyServerFields_LiveStoresEachSourceMatters(t *testing.T) {
	type fixture struct {
		personality  string
		writingStyle string
		talent       string
	}
	baseline := fixture{
		personality:  "# Bernstein\n\nbaseline\n",
		writingStyle: "# Prose\n\nbaseline\n",
		talent:       "# Security\n\nbaseline\n",
	}
	cases := []struct {
		name   string
		mutate func(*fixture)
	}{
		{
			name:   "personality edit",
			mutate: func(f *fixture) { f.personality = "# Bernstein\n\nedited\n" },
		},
		{
			name:   "writing style edit",
			mutate: func(f *fixture) { f.writingStyle = "# Prose\n\nedited\n" },
		},
		{
			name:   "talent edit",
			mutate: func(f *fixture) { f.talent = "# Security\n\nedited\n" },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			is := identity.NewStore(dir)
			ps := attribute.NewStore(dir, attribute.Personalities)
			ws := attribute.NewStore(dir, attribute.WritingStyles)
			ts := attribute.NewStore(dir, attribute.Talents)

			// Seed baseline.
			require.NoError(t, ps.Save(&attribute.Attribute{
				Slug: "bernstein", Content: baseline.personality,
			}))
			require.NoError(t, ws.Save(&attribute.Attribute{
				Slug: "bernstein-prose", Content: baseline.writingStyle,
			}))
			require.NoError(t, ts.Save(&attribute.Attribute{
				Slug: "security", Content: baseline.talent,
			}))
			require.NoError(t, is.Save(&identity.Identity{
				Name:         "Dan B",
				Handle:       "djb",
				Kind:         "agent",
				Personality:  "bernstein",
				WritingStyle: "bernstein-prose",
				Talents:      []string{"security"},
			}))

			sources, err := NewLiveHashSources(is, role.NewLayeredStore("", dir), team.NewLayeredStore("", dir))
			require.NoError(t, err)
			baselineHash, err := ComputeEvaluatorHash("djb", sources)
			require.NoError(t, err)

			// Apply the mutation by overwriting the .md file on disk.
			f := baseline
			tc.mutate(&f)
			require.NoError(t, os.WriteFile(
				filepath.Join(dir, "personalities", "bernstein.md"),
				[]byte(f.personality), 0o600,
			))
			require.NoError(t, os.WriteFile(
				filepath.Join(dir, "writing-styles", "bernstein-prose.md"),
				[]byte(f.writingStyle), 0o600,
			))
			require.NoError(t, os.WriteFile(
				filepath.Join(dir, "talents", "security.md"),
				[]byte(f.talent), 0o600,
			))

			driftedHash, err := ComputeEvaluatorHash("djb", sources)
			require.NoError(t, err)
			assert.NotEqual(t, baselineHash, driftedHash,
				"%s: live-store hash must change after .md edit", tc.name)
		})
	}
}

// TestApplyServerFields_LiveStoresRolesCovered is the round 4 Bugbot
// regression test. It seeds an evaluator whose only distinguishing
// content is a team-based role assignment, then proves two things:
//
//  1. Editing the role .yaml file between two ApplyServerFields calls
//     changes the pinned hash. Without the team walk, the role edit
//     would be invisible to the hash and the "role content is part
//     of the evaluator" invariant would silently regress.
//
//  2. Two NewLiveHashSources constructions rooted at the same directory
//     yield byte-identical hashes. This is the parity invariant the
//     MCP and CLI create paths rely on: every caller that wires the
//     full set of stores must produce the same hash for the same
//     on-disk content.
//
// The earlier LiveStoresEachSourceMatters test covers personality,
// writing style, and talent content but does NOT seed a role — a
// silently-dropped role section would have slipped past it. This
// test closes that gap.
func TestApplyServerFields_LiveStoresRolesCovered(t *testing.T) {
	dir := t.TempDir()
	is := identity.NewStore(dir)
	rs := role.NewLayeredStore("", dir)
	ts := team.NewLayeredStore("", dir)

	// Minimal identity — no personality, no writing style, no talents.
	// Everything that can influence the hash comes from the role.
	require.NoError(t, is.Save(&identity.Identity{
		Name:   "Dan B",
		Handle: "djb",
		Kind:   "agent",
	}))

	// Seed the role and a team that binds djb to it. Without this the
	// liveRoleLister would return an empty list and the role section
	// would be absent from the hash regardless of what's in the role
	// file on disk.
	require.NoError(t, rs.Save(&role.Role{
		Name:             "verifier",
		Responsibilities: []string{"baseline responsibility"},
	}))
	identityExists := func(h string) bool { return h == "djb" }
	roleExists := func(n string) bool { return rs.Exists(n) }
	require.NoError(t, ts.Save(&team.Team{
		Name: "frozen-verifier",
		Members: []team.Member{
			{Identity: "djb", Role: "verifier"},
		},
	}, identityExists, roleExists))

	// Parity: two fresh NewLiveHashSources rooted at the same dir must
	// produce byte-identical hashes. This is the Bugbot finding in
	// test form — MCP and CLI wire fresh sources on every call, and
	// any hidden state (map iteration order, silent nil fallbacks)
	// would surface here.
	sourcesA, err := NewLiveHashSources(is, role.NewLayeredStore("", dir), team.NewLayeredStore("", dir))
	require.NoError(t, err)
	sourcesB, err := NewLiveHashSources(is, role.NewLayeredStore("", dir), team.NewLayeredStore("", dir))
	require.NoError(t, err)

	hashA, err := ComputeEvaluatorHash("djb", sourcesA)
	require.NoError(t, err)
	hashB, err := ComputeEvaluatorHash("djb", sourcesB)
	require.NoError(t, err)
	assert.Equal(t, hashA, hashB,
		"two fresh NewLiveHashSources rooted at the same directory must produce identical hashes")

	// Role edit detection: rewrite the role .yaml directly on disk and
	// assert the hash changes. A silent-nil role lister would return
	// an empty list regardless of the edit and the hash would be
	// stable — which is exactly the bug this test exists to catch.
	//
	// The role store's Save refuses to overwrite, so the edit is
	// delete-plus-save. The team binding is unaffected — the team
	// file still names "verifier" and the new role file still has
	// the same name, so referential integrity holds.
	require.NoError(t, rs.Delete("verifier"))
	require.NoError(t, rs.Save(&role.Role{
		Name:             "verifier",
		Responsibilities: []string{"edited responsibility"},
	}))

	sourcesC, err := NewLiveHashSources(is, role.NewLayeredStore("", dir), team.NewLayeredStore("", dir))
	require.NoError(t, err)
	hashC, err := ComputeEvaluatorHash("djb", sourcesC)
	require.NoError(t, err)
	assert.NotEqual(t, hashA, hashC,
		"editing the bound role's content must change the evaluator hash")
}

// TestApplyServerFields_HashRoundTripsThroughCreate asserts that the
// hash set by ApplyServerFields survives Store.Create unchanged and
// loads back exactly. This is the end-to-end happy path: launch a
// mission, reload it, and verify the pinned hash is the bytes the
// caller saw.
func TestApplyServerFields_HashRoundTripsThroughCreate(t *testing.T) {
	s := testStore(t)
	c := validContract()
	c.MissionID = ""
	c.CreatedAt = ""
	c.UpdatedAt = ""
	c.Evaluator.PinnedAt = ""

	sources := fakeHashSources("djb")
	require.NoError(t, s.ApplyServerFields(&c, time.Now(), sources))
	wantHash := c.Evaluator.Hash
	require.NoError(t, s.Create(&c))

	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, wantHash, loaded.Evaluator.Hash,
		"Evaluator.Hash must survive the YAML round trip byte-for-byte")
}

// --- 3.4: round-advance gate and reflection store ---

// reflectionFor returns a fresh, valid reflection for the given round
// with the given recommendation. Tests use this to keep the table
// rows compact and the assertions focused on the gate behavior, not
// on building the input.
func reflectionFor(round int, rec string) *Reflection {
	return &Reflection{
		Round:          round,
		Author:         "claude",
		Converging:     true,
		Signals:        []string{"all green"},
		Recommendation: rec,
		Reason:         "round " + fmt.Sprint(round) + " " + rec,
	}
}

// TestStore_FreshContractStartsAtRoundOne asserts the round-tracking
// default: a mission created via the store starts at round 1, and
// CurrentRound is reflected back to the caller. The default-fill is
// the upgrade path that lets pre-3.4 callers (and the existing
// validContract test fixture) keep working without explicitly
// setting CurrentRound.
func TestStore_FreshContractStartsAtRoundOne(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	c.CurrentRound = 0
	require.NoError(t, s.Create(c))
	assert.Equal(t, 1, c.CurrentRound, "Create must reflect CurrentRound back as 1")

	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, 1, loaded.CurrentRound, "stored mission must start at round 1")
}

// TestStore_LoadDefaultsCurrentRoundForPre34Files asserts that a
// hand-written contract YAML without a current_round line still
// loads in 3.4. Pre-3.4 missions on disk have no current_round
// field; the read path defaults the missing value to 1 so the
// upgrade is invisible to operators.
func TestStore_LoadDefaultsCurrentRoundForPre34Files(t *testing.T) {
	s := testStore(t)
	require.NoError(t, os.MkdirAll(s.missionsDir(), 0o700))
	body := []byte(`mission_id: m-2026-04-07-001
status: open
created_at: 2026-04-07T21:30:00Z
updated_at: 2026-04-07T21:30:00Z
leader: claude
worker: bwk
evaluator:
  handle: djb
  pinned_at: 2026-04-07T21:30:00Z
inputs: {}
write_set:
  - internal/mission/
success_criteria:
  - make check passes
budget:
  rounds: 3
`)
	require.NoError(t, os.WriteFile(
		filepath.Join(s.missionsDir(), "m-2026-04-07-001.yaml"),
		body, 0o600,
	))
	loaded, err := s.Load("m-2026-04-07-001")
	require.NoError(t, err)
	assert.Equal(t, 1, loaded.CurrentRound)
}

// TestStore_AppendReflection_RoundTrip asserts success criteria 1
// and 2: a well-formed reflection is stored and retrievable via
// LoadReflections after AppendReflection succeeds.
func TestStore_AppendReflection_RoundTrip(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	r := reflectionFor(1, RecommendationContinue)
	require.NoError(t, s.AppendReflection(c.MissionID, r))
	assert.NotEmpty(t, r.CreatedAt, "AppendReflection must reflect CreatedAt back")

	loaded, err := s.LoadReflections(c.MissionID)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, 1, loaded[0].Round)
	assert.Equal(t, RecommendationContinue, loaded[0].Recommendation)
	assert.Equal(t, "claude", loaded[0].Author)
}

// TestStore_AppendReflection_RejectsWrongRound asserts that the
// reflection store refuses a Round value that does not match the
// mission's CurrentRound. The misuse should be caught at submit
// time, not at advance time, so the operator's error message is
// close to the bug.
func TestStore_AppendReflection_RejectsWrongRound(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	// Mission is at round 1; submitting a round-2 reflection is wrong.
	r := reflectionFor(2, RecommendationContinue)
	err := s.AppendReflection(c.MissionID, r)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "round 2")
	assert.Contains(t, err.Error(), "current round 1")
}

// TestStore_AppendReflection_RejectsDuplicate asserts the
// append-only invariant: once round N's reflection is recorded, a
// second submission for round N is refused. This is success
// criterion 7.
func TestStore_AppendReflection_RejectsDuplicate(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(1, RecommendationContinue)))
	err := s.AppendReflection(c.MissionID, reflectionFor(1, RecommendationPivot))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	assert.Contains(t, err.Error(), "append-only")
}

// TestStore_AppendReflection_RejectsClosedMission asserts that
// reflections cannot be recorded on a terminal mission.
func TestStore_AppendReflection_RejectsClosedMission(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))
	submitRoundResult(t, s, c, VerdictPass)
	require.NoError(t, s.Close(c.MissionID, StatusClosed))

	err := s.AppendReflection(c.MissionID, reflectionFor(1, RecommendationContinue))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal state")
}

// TestStore_AppendReflection_RejectsMalformed asserts that
// AppendReflection runs Validate before any disk I/O. A malformed
// reflection (no signals) is refused and the on-disk file is left
// unchanged.
func TestStore_AppendReflection_RejectsMalformed(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	bad := reflectionFor(1, RecommendationContinue)
	bad.Signals = nil
	err := s.AppendReflection(c.MissionID, bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signals")

	// No reflections file should exist.
	_, statErr := os.Stat(s.reflectionsPath(c.MissionID))
	assert.True(t, os.IsNotExist(statErr), "rejected reflection must leave no file")
}

// TestStore_AdvanceRound_BlockedWithoutReflection asserts success
// criterion 3: round 1 → round 2 fails when round 1 has no
// reflection on disk.
func TestStore_AdvanceRound_BlockedWithoutReflection(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	_, err := s.AdvanceRound(c.MissionID, "claude")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no reflection for round 1")
	assert.Contains(t, err.Error(), c.MissionID)

	// Mission must still be at round 1 — failed advance leaves
	// CurrentRound untouched.
	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, 1, loaded.CurrentRound)
}

// TestStore_AdvanceRound_UnblocksAfterReflection asserts success
// criterion 4: submitting the round 1 reflection and retrying the
// advance succeeds, and CurrentRound becomes 2.
func TestStore_AdvanceRound_UnblocksAfterReflection(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(1, RecommendationContinue)))
	newRound, err := s.AdvanceRound(c.MissionID, "claude")
	require.NoError(t, err)
	assert.Equal(t, 2, newRound)

	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, 2, loaded.CurrentRound)
}

// TestStore_AdvanceRound_BudgetExhaustionRefused asserts success
// criterion 5: a mission whose CurrentRound has reached its budget
// cannot advance regardless of reflection state.
func TestStore_AdvanceRound_BudgetExhaustionRefused(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	c.Budget.Rounds = 3
	require.NoError(t, s.Create(c))

	// Round 1 → 2.
	require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(1, RecommendationContinue)))
	_, err := s.AdvanceRound(c.MissionID, "claude")
	require.NoError(t, err)

	// Round 2 → 3.
	require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(2, RecommendationContinue)))
	_, err = s.AdvanceRound(c.MissionID, "claude")
	require.NoError(t, err)

	// Round 3 → 4 must fail (budget exhausted).
	require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(3, RecommendationContinue)))
	_, err = s.AdvanceRound(c.MissionID, "claude")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exhausted its round budget")
	assert.Contains(t, err.Error(), "3/3")

	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, 3, loaded.CurrentRound)
}

// TestStore_AdvanceRound_StopRecommendationBlocks asserts success
// criterion 6 (stop variant): a stop reflection blocks the next
// advance and the leader's reason is surfaced verbatim.
func TestStore_AdvanceRound_StopRecommendationBlocks(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	r := reflectionFor(1, RecommendationStop)
	r.Reason = "the test fixture is irreparable; stop and re-scope"
	require.NoError(t, s.AppendReflection(c.MissionID, r))

	_, err := s.AdvanceRound(c.MissionID, "claude")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `recommends "stop"`)
	assert.Contains(t, err.Error(), "the test fixture is irreparable")
}

// TestStore_AdvanceRound_EscalateRecommendationBlocks asserts the
// escalate variant of success criterion 6.
func TestStore_AdvanceRound_EscalateRecommendationBlocks(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	r := reflectionFor(1, RecommendationEscalate)
	r.Reason = "needs human review"
	require.NoError(t, s.AppendReflection(c.MissionID, r))

	_, err := s.AdvanceRound(c.MissionID, "claude")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `recommends "escalate"`)
	assert.Contains(t, err.Error(), "needs human review")
}

// TestStore_AdvanceRound_PivotPermitted asserts that a pivot
// recommendation does NOT block the advance — pivot is the
// "different approach in the next round" signal, not "stop".
func TestStore_AdvanceRound_PivotPermitted(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(1, RecommendationPivot)))
	newRound, err := s.AdvanceRound(c.MissionID, "claude")
	require.NoError(t, err)
	assert.Equal(t, 2, newRound)
}

// TestStore_AdvanceRound_RefusesClosedMission asserts that the gate
// refuses to advance a mission that is no longer open.
func TestStore_AdvanceRound_RefusesClosedMission(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))
	submitRoundResult(t, s, c, VerdictPass)
	require.NoError(t, s.Close(c.MissionID, StatusClosed))

	_, err := s.AdvanceRound(c.MissionID, "claude")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal state")
}

// TestStore_AdvanceRound_LogsTransition asserts success criterion 8:
// a successful round advance writes a `round_advanced` event with
// from/to round numbers, and an unsuccessful one does not.
// Reflections write a `reflect` event regardless of advance.
func TestStore_AdvanceRound_LogsTransition(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(1, RecommendationContinue)))
	_, err := s.AdvanceRound(c.MissionID, "claude")
	require.NoError(t, err)

	events := readLog(t, s, c.MissionID)
	// create + reflect + round_advanced.
	require.Len(t, events, 3)
	assert.Equal(t, "create", events[0].Event)
	assert.Equal(t, "reflect", events[1].Event)
	assert.Equal(t, float64(1), events[1].Details["round"])
	assert.Equal(t, "continue", events[1].Details["recommendation"])
	assert.Equal(t, "round_advanced", events[2].Event)
	assert.Equal(t, float64(1), events[2].Details["from_round"])
	assert.Equal(t, float64(2), events[2].Details["to_round"])
}

// TestStore_LoadReflections_EmptyForFreshMission asserts that a
// brand-new mission has no reflections file on disk and
// LoadReflections returns nil with no error.
func TestStore_LoadReflections_EmptyForFreshMission(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	rs, err := s.LoadReflections(c.MissionID)
	require.NoError(t, err)
	assert.Nil(t, rs)
}

// TestStore_LoadReflections_RejectsUnknownField asserts that a
// hand-edited reflections file with a smuggled key is rejected on
// read, symmetric with the contract decode trust boundary.
func TestStore_LoadReflections_RejectsUnknownField(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	// Drop a hand-rolled reflections file with a bogus key.
	body := []byte(`reflections:
  - round: 1
    author: claude
    converging: true
    signals:
      - one
    recommendation: continue
    reason: ok
    bogus: smuggled
`)
	require.NoError(t, os.WriteFile(s.reflectionsPath(c.MissionID), body, 0o600))

	_, err := s.LoadReflections(c.MissionID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field bogus not found")
}

// TestStore_AdvanceRound_ConcurrentSerialization asserts that two
// concurrent advances on the same mission cannot both succeed: the
// per-mission flock serializes the bumps so the contract's
// CurrentRound never skips a round.
func TestStore_AdvanceRound_ConcurrentSerialization(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	c.Budget.Rounds = 5
	require.NoError(t, s.Create(c))
	require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(1, RecommendationContinue)))

	const n = 10
	var wg sync.WaitGroup
	results := make(chan int, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.AdvanceRound(c.MissionID, "claude")
			if err != nil {
				errs <- err
				return
			}
			results <- r
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	var winners []int
	for r := range results {
		winners = append(winners, r)
	}
	// Exactly one goroutine successfully bumps to round 2; the rest
	// see "no reflection for round 2" because no reflection has been
	// submitted for the new round yet. This is the round-monotone
	// invariant under concurrency.
	assert.Len(t, winners, 1, "exactly one concurrent advance must win")
	if len(winners) == 1 {
		assert.Equal(t, 2, winners[0])
	}

	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, 2, loaded.CurrentRound)
}

// TestStore_CreateRejectsDotSegmentBypass asserts that dot-segment
// path syntax (legitimate per the per-entry validator's
// TestValidate_AcceptsSingleDotSegment) cannot bypass the cross-
// mission write_set conflict check. This regression test exists
// because round 3's empty-segment filter missed the dot-segment
// variant; djb's frozen-evaluator review caught the gap.
func TestStore_CreateRejectsDotSegmentBypass(t *testing.T) {
	s := testStore(t)

	a := withWriteSet("m-2026-04-08-001", "internal/mission/store.go")
	require.NoError(t, s.Create(a))

	// Each variant must conflict with A.
	cases := []string{
		"./internal/mission/store.go",
		"internal/./mission/store.go",
		"internal/mission/./store.go",
		"./internal/./mission/store.go",
	}
	for i, ws := range cases {
		t.Run(ws, func(t *testing.T) {
			id := fmt.Sprintf("m-2026-04-08-%03d", i+2)
			b := withWriteSet(id, ws)
			err := s.Create(b)
			require.Error(t, err, "dot-segment variant must be rejected: %q", ws)
			assert.Contains(t, err.Error(), "write_set conflict")
			assert.Contains(t, err.Error(), a.MissionID)
		})
	}
}

// TestStore_ListSkipsReflectionsFile asserts the Phase 3.4 round-2
// regression fix: Store.List must not treat the sibling
// <id>.reflections.yaml file as a mission contract. The bug this test
// exists to prevent was catastrophic: after any mission had a
// reflection on disk, List returned "<id>" and "<id>.reflections",
// which (a) emitted spurious "field reflections not found" warnings
// on every list invocation, (b) made `mission show <prefix>` ambiguous
// because two IDs matched the same prefix, and (c) — the showstopper
// — caused every subsequent `mission create` to fail because
// checkWriteSetConflicts loads every open mission and treats a load
// failure as fatal, bringing the entire mission-create path down for
// anyone who uses the round-advance gate.
//
// This single test exercises all three failure modes from the mdm
// reproduction: list, show prefix match, and create after reflection.
func TestStore_ListSkipsReflectionsFile(t *testing.T) {
	s := testStore(t)
	c := validContract()
	c.MissionID = "m-2026-04-08-001"
	c.WriteSet = []string{"tests/list-skip-reflections/"}
	require.NoError(t, s.Create(&c))

	// Append a reflection so the sibling file exists on disk.
	r := &Reflection{
		Round:          1,
		Author:         "claude",
		Converging:     true,
		Signals:        []string{"round 1 complete"},
		Recommendation: RecommendationContinue,
	}
	require.NoError(t, s.AppendReflection(c.MissionID, r))

	// Verify both files actually exist — the test is meaningless if
	// the reflections file was not written.
	_, err := os.Stat(s.reflectionsPath(c.MissionID))
	require.NoError(t, err, "reflections file must exist for the test to be meaningful")

	// Failure mode 1: List must return exactly one mission ID, not
	// a phantom "<id>.reflections" entry.
	ids, err := s.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"m-2026-04-08-001"}, ids,
		"List must skip <id>.reflections.yaml")

	// Failure mode 2: MatchByPrefix must resolve unambiguously. A
	// reflections file masquerading as a contract would make the
	// daily-prefix match return "ambiguous prefix".
	id, err := s.MatchByPrefix("m-2026-04-08")
	require.NoError(t, err)
	assert.Equal(t, "m-2026-04-08-001", id)

	// Failure mode 3: Create must succeed for a new mission. This is
	// the one that takes ethos offline — checkWriteSetConflicts
	// loads every existing mission, and a load failure on the
	// phantom reflections file would bubble up as a fatal error for
	// every new create attempt.
	c2 := validContract()
	c2.MissionID = "m-2026-04-08-002"
	c2.WriteSet = []string{"tests/list-skip-reflections-2/"}
	assert.NoError(t, s.Create(&c2),
		"Create must not choke on the sibling reflections file")
}

// --- Phase 3.5: worker-verifier role distinction ---

// TestStore_CreateRejectsWorkerEqualsEvaluator asserts the weakest of
// Phase 3.5's role invariants: a contract that names the same handle
// for worker and evaluator is rejected with an actionable error. The
// check does not depend on a RoleLister — it is a structural refusal
// that fires before any store state is touched.
func TestStore_CreateRejectsWorkerEqualsEvaluator(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-210")
	c.Worker = "bwk"
	c.Evaluator.Handle = "bwk"

	err := s.Create(c)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "bwk")
	assert.Contains(t, msg, "worker")
	assert.Contains(t, msg, "evaluator")
	assert.Contains(t, msg, "verifier must not review its own work")

	// No file was written — the self-verification guard fires before
	// the create lock is taken.
	_, statErr := os.Stat(s.contractPath(c.MissionID))
	assert.True(t, os.IsNotExist(statErr),
		"rejected self-verification contract must leave no file on disk")
}

// TestStore_CreateRejectsWorkerEqualsEvaluatorWithWhitespace covers the
// corner case where the two fields are logically equal but differ by
// surrounding whitespace. The trim in checkSelfVerification must
// normalize both sides before comparison.
func TestStore_CreateRejectsWorkerEqualsEvaluatorWithWhitespace(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-211")
	c.Worker = "bwk"
	c.Evaluator.Handle = " bwk "
	// containsControlChar allows spaces; Validate trims for its own
	// non-empty check but does not canonicalize. The self-verification
	// guard does the canonical comparison itself.

	err := s.Create(c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot also be evaluator")
}

// TestStore_CreateAllowsDistinctHandles is the positive control: a
// contract with distinct worker and evaluator handles and no configured
// RoleLister is accepted. The self-verification guard does not
// over-reach.
func TestStore_CreateAllowsDistinctHandles(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-212")
	// worker=bwk, evaluator=djb is the fixture default.
	require.NoError(t, s.Create(c))
}

// TestStore_CreateRejectsSameTeamRoleBinding asserts Phase 3.5's
// stronger invariant: two distinct handles bound to the exact same
// team-scoped role (`engineering/go-specialist`) cannot verify each
// other's work, and Store.Create refuses with an error naming both
// handles, the shared binding, and the recovery path.
//
// The test wires a fake RoleLister via Store.WithRoleLister so it
// exercises the opt-in integration contract without standing up the
// full identity/role/team stores.
func TestStore_CreateRejectsSameTeamRoleBinding(t *testing.T) {
	s := testStore(t).WithRoleLister(&mapRoleLister{
		m: map[string][]EvaluatorRole{
			"bwk": {{Name: "engineering/go-specialist"}},
			"mdm": {{Name: "engineering/go-specialist"}},
		},
	})
	c := newContract("m-2026-04-07-220")
	c.Worker = "bwk"
	c.Evaluator.Handle = "mdm"

	err := s.Create(c)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "bwk")
	assert.Contains(t, msg, "mdm")
	assert.Contains(t, msg, "engineering/go-specialist")
	assert.Contains(t, msg, "recovery")

	_, statErr := os.Stat(s.contractPath(c.MissionID))
	assert.True(t, os.IsNotExist(statErr),
		"rejected role-overlap contract must leave no file on disk")
}

// TestStore_CreateRejectsSameRoleSlugOnDifferentTeams covers the
// canonicalization branch of the overlap rule. Two handles on
// different teams but bound to the same role slug (the teams differ
// but the role name after the slash matches) still share the same
// responsibilities and must be refused.
func TestStore_CreateRejectsSameRoleSlugOnDifferentTeams(t *testing.T) {
	s := testStore(t).WithRoleLister(&mapRoleLister{
		m: map[string][]EvaluatorRole{
			"bwk": {{Name: "engineering/go-specialist"}},
			"alt": {{Name: "security/go-specialist"}},
		},
	})
	c := newContract("m-2026-04-07-221")
	c.Worker = "bwk"
	c.Evaluator.Handle = "alt"

	err := s.Create(c)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "same role slug after canonicalization")
	assert.Contains(t, msg, "engineering/go-specialist")
	assert.Contains(t, msg, "security/go-specialist")
}

// TestStore_CreateAcceptsDistinctRoles is the canonical example from
// the mission contract: `bwk` (engineering/go-specialist) and `djb`
// (security/security-reviewer) share a team ideation but have
// distinct roles. Create must succeed.
func TestStore_CreateAcceptsDistinctRoles(t *testing.T) {
	s := testStore(t).WithRoleLister(&mapRoleLister{
		m: map[string][]EvaluatorRole{
			"bwk": {{Name: "engineering/go-specialist"}},
			"djb": {{Name: "engineering/security-reviewer"}},
		},
	})
	c := newContract("m-2026-04-07-222")
	c.Worker = "bwk"
	c.Evaluator.Handle = "djb"

	require.NoError(t, s.Create(c))
	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, "bwk", loaded.Worker)
	assert.Equal(t, "djb", loaded.Evaluator.Handle)
}

// TestStore_CreateAcceptsEvaluatorWithNoRoles covers the "evaluator on
// no teams" branch: an identity with no role bindings cannot overlap
// anything, so Create is accepted as long as the worker != evaluator
// guard passes.
func TestStore_CreateAcceptsEvaluatorWithNoRoles(t *testing.T) {
	s := testStore(t).WithRoleLister(&mapRoleLister{
		m: map[string][]EvaluatorRole{
			"bwk": {{Name: "engineering/go-specialist"}},
			"djb": nil, // no role bindings
		},
	})
	c := newContract("m-2026-04-07-223")
	c.Worker = "bwk"
	c.Evaluator.Handle = "djb"
	require.NoError(t, s.Create(c))
}

// TestStore_CreateWithoutRoleListerSkipsOverlapCheck asserts the
// backward-compatibility invariant: a bare Store (no RoleLister wired)
// runs only the worker!=evaluator guard and lets role-coincident
// handles through. This is the pre-3.5 fixture test pattern used by
// every existing store test, and this test is the regression guard
// that ensures the old path keeps compiling and running.
func TestStore_CreateWithoutRoleListerSkipsOverlapCheck(t *testing.T) {
	// Deliberately no WithRoleLister — even though bwk and mdm
	// would share a role binding if the lister were wired, the
	// bare store accepts the contract.
	s := testStore(t)
	c := newContract("m-2026-04-07-224")
	c.Worker = "bwk"
	c.Evaluator.Handle = "mdm"
	require.NoError(t, s.Create(c))
}

// TestStore_CreateReportsMultipleOverlappingBindings asserts that when
// worker and evaluator share more than one role (both team/role and
// a canonicalized slug on another team), the error names every
// offending binding — one line per overlap. Operators need the full
// picture to plan their recovery.
func TestStore_CreateReportsMultipleOverlappingBindings(t *testing.T) {
	s := testStore(t).WithRoleLister(&mapRoleLister{
		m: map[string][]EvaluatorRole{
			"bwk": {
				{Name: "engineering/go-specialist"},
				{Name: "infra/reviewer"},
			},
			"mdm": {
				{Name: "engineering/go-specialist"}, // exact binding match
				{Name: "security/reviewer"},         // canonical slug match
			},
		},
	})
	c := newContract("m-2026-04-07-225")
	c.Worker = "bwk"
	c.Evaluator.Handle = "mdm"

	err := s.Create(c)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "engineering/go-specialist")
	assert.Contains(t, msg, "infra/reviewer")
	assert.Contains(t, msg, "security/reviewer")
	assert.Contains(t, msg, "2 overlapping role assignment",
		"error must count the overlaps")
}

// TestStore_CreateRoleOverlapCheckIsCreateOnly asserts invariant 8 from
// the mission contract: the overlap check applies only at create
// time. A pre-3.5 mission on disk with a role-coincident pair keeps
// loading cleanly — the gate is not retroactive.
//
// We simulate "pre-3.5 on disk" by creating the contract via a bare
// store (no lister) and then reading it back from a lister-wired
// store. The read path must not re-run the overlap check.
func TestStore_CreateRoleOverlapCheckIsCreateOnly(t *testing.T) {
	root := t.TempDir()
	bare := NewStore(root)
	c := newContract("m-2026-04-07-226")
	c.Worker = "bwk"
	c.Evaluator.Handle = "mdm"
	require.NoError(t, bare.Create(c),
		"bare store must accept the pre-3.5 contract")

	wired := NewStore(root).WithRoleLister(&mapRoleLister{
		m: map[string][]EvaluatorRole{
			"bwk": {{Name: "engineering/go-specialist"}},
			"mdm": {{Name: "engineering/go-specialist"}},
		},
	})
	loaded, err := wired.Load(c.MissionID)
	require.NoError(t, err,
		"Load must not retroactively apply the overlap check")
	assert.Equal(t, "bwk", loaded.Worker)
	assert.Equal(t, "mdm", loaded.Evaluator.Handle)
}

// TestCanonicalRoleSlug exercises the role-slug canonicalization
// helper directly. The rule is "strip everything up to and including
// the last slash"; names with no slash pass through; empty and
// whitespace-only inputs yield "".
func TestCanonicalRoleSlug(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"engineering/go-specialist", "go-specialist"},
		{"security/go-specialist", "go-specialist"},
		{"bare-role", "bare-role"},
		{"engineering/subgroup/go-specialist", "go-specialist"},
		{"", ""},
		{"  ", ""},
		{"engineering/", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, canonicalRoleSlug(tc.in))
		})
	}
}

// --- Phase 3.6: result artifact and close gate ---

// resultFor returns a minimal valid result for the given mission's
// current round with the given verdict. Tests that only care about
// the gate's presence/absence behavior use this; the more detailed
// tests build results inline.
func resultFor(c *Contract, verdict string) *Result {
	return &Result{
		Mission:    c.MissionID,
		Round:      c.CurrentRound,
		Author:     c.Worker,
		Verdict:    verdict,
		Confidence: 0.8,
		Evidence: []EvidenceCheck{
			{Name: "make check", Status: EvidenceStatusPass},
		},
	}
}

// TestStore_AppendResult_RoundTrip asserts success criterion 1 and 6:
// a well-formed result is persisted and retrievable via LoadResult
// after AppendResult succeeds.
func TestStore_AppendResult_RoundTrip(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	r := resultFor(c, VerdictPass)
	require.NoError(t, s.AppendResult(c.MissionID, r))
	assert.NotEmpty(t, r.CreatedAt, "AppendResult must reflect CreatedAt back")

	loaded, err := s.LoadResult(c.MissionID, 1)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, 1, loaded.Round)
	assert.Equal(t, VerdictPass, loaded.Verdict)
	assert.Equal(t, c.Worker, loaded.Author)
}

// TestStore_AppendResult_RejectsWrongRound asserts the round number
// cross-check: submitting a result whose Round does not match
// CurrentRound is refused so the operator sees the bug at submit
// time, not at close time.
func TestStore_AppendResult_RejectsWrongRound(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	r := resultFor(c, VerdictPass)
	r.Round = 2
	err := s.AppendResult(c.MissionID, r)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "round 2")
	assert.Contains(t, err.Error(), "current round 1")
}

// TestStore_AppendResult_RejectsMismatchedMissionID asserts that the
// result's self-declared Mission field must match the target
// missionID. A file renamed between missions cannot slip past the
// gate by claiming the wrong parent.
func TestStore_AppendResult_RejectsMismatchedMissionID(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	r := resultFor(c, VerdictPass)
	r.Mission = "m-2026-04-08-999"
	err := s.AppendResult(c.MissionID, r)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match")
}

// TestStore_AppendResult_RejectsDuplicate asserts success criterion 3:
// a second submission for the same round is refused with an
// operator-readable error naming the mission and the round.
func TestStore_AppendResult_RejectsDuplicate(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	require.NoError(t, s.AppendResult(c.MissionID, resultFor(c, VerdictPass)))

	err := s.AppendResult(c.MissionID, resultFor(c, VerdictFail))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	assert.Contains(t, err.Error(), "append-only")
	assert.Contains(t, err.Error(), c.MissionID)
	assert.Contains(t, err.Error(), "round 1")
}

// TestStore_AppendResult_RejectsClosedMission asserts that results
// cannot be recorded on a terminal mission.
func TestStore_AppendResult_RejectsClosedMission(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))
	submitRoundResult(t, s, c, VerdictPass)
	require.NoError(t, s.Close(c.MissionID, StatusClosed))

	err := s.AppendResult(c.MissionID, resultFor(c, VerdictPass))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "terminal state")
}

// TestStore_AppendResult_RejectsMalformed asserts that AppendResult
// runs Validate before any disk I/O. A malformed result (empty
// evidence) is refused and the on-disk file is left unchanged.
func TestStore_AppendResult_RejectsMalformed(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	bad := resultFor(c, VerdictPass)
	bad.Evidence = nil
	err := s.AppendResult(c.MissionID, bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "evidence")

	_, statErr := os.Stat(s.resultsPath(c.MissionID))
	assert.True(t, os.IsNotExist(statErr), "rejected result must leave no file")
}

// TestStore_AppendResult_FilesChangedContainment covers success
// criterion 4: every equivalence class of files_changed containment
// failure is rejected — direct violation, traversal, control
// characters, absolute paths, empty segments, and root claims. This
// is the "fix the class, not the instance" test: the result
// validator must reject every variant the write_set admission rules
// already reject at contract create time.
//
// Round 2 of Phase 3.6 extends the table with the parent-prefix
// class. All four reviewers independently flagged the H1 bug in
// round 1: the symmetric pathsOverlap helper accepted a result
// claiming a parent directory of a write_set file entry. The new
// cases below exercise every variant — parent of a file entry,
// parent of a directory entry, top-level ancestor, and the
// mixed-bag case with multiple paths where some are valid and some
// are the exploit.
func TestStore_AppendResult_FilesChangedContainment(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	c.WriteSet = []string{"internal/mission/", "cmd/ethos/mission.go"}
	require.NoError(t, s.Create(c))

	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{
			name:    "direct violation",
			path:    "cmd/other/file.go",
			wantErr: "outside mission",
		},
		{
			name:    "sibling prefix",
			path:    "internal/missionary/foo.go",
			wantErr: "outside mission",
		},
		{
			name:    "traversal",
			path:    "../etc/passwd",
			wantErr: "path traversal",
		},
		{
			name:    "absolute",
			path:    "/etc/passwd",
			wantErr: "relative path",
		},
		{
			name:    "control character",
			path:    "internal/mission/\nbad.go",
			wantErr: "control character",
		},
		{
			name:    "null byte",
			path:    "internal/mission/\x00bypass",
			wantErr: "null byte",
		},
		{
			name:    "root claim",
			path:    ".",
			wantErr: "project root",
		},
		{
			name:    "dot slash root claim",
			path:    "./",
			wantErr: "project root",
		},
		// --- Round 2: the parent-prefix class (H1). ---
		{
			// Parent of a file entry: contract allows
			// cmd/ethos/mission.go, result claims cmd/ethos. Must
			// refuse — the file entry is `cmd/ethos/mission.go`,
			// not a directory, so cmd/ethos would quietly claim
			// authority over every other file under cmd/ethos/.
			name:    "parent-prefix of file entry",
			path:    "cmd/ethos",
			wantErr: "outside mission",
		},
		{
			// Top-level ancestor of a file entry: result claims
			// `cmd` against write_set `cmd/ethos/mission.go`.
			name:    "top-level ancestor of file entry",
			path:    "cmd",
			wantErr: "outside mission",
		},
		{
			// Top-level ancestor of a directory entry: result
			// claims `internal` against write_set `internal/mission/`.
			// Must refuse — the result would cover every other
			// package under internal/.
			name:    "top-level ancestor of directory entry",
			path:    "internal",
			wantErr: "outside mission",
		},
		{
			// Parent of a directory entry with trailing slash.
			// Write_set is `internal/mission/`; result claims
			// `internal/missi` which is a STRING prefix but not a
			// SEGMENT prefix — this case was already covered by
			// "sibling prefix" but lock the intent here.
			name:    "string-but-not-segment prefix",
			path:    "internal/missi",
			wantErr: "outside mission",
		},
		{
			// Result claims a dot-syntax root. The per-entry
			// validator rejects dot-root first — but the test
			// locks the behavior in case the order ever changes.
			name:    "dot-dot root claim",
			path:    "./.",
			wantErr: "project root",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := resultFor(c, VerdictPass)
			r.FilesChanged = []FileChange{{Path: tt.path, Added: 1, Removed: 0}}
			err := s.AppendResult(c.MissionID, r)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			// No results file should exist for the rejected input.
			_, statErr := os.Stat(s.resultsPath(c.MissionID))
			assert.True(t, os.IsNotExist(statErr),
				"rejected result must leave no file for case %q", tt.name)
		})
	}
}

// TestStore_AppendResult_FilesChangedContainment_MixedBag asserts
// the class fix applies to the multi-path case: a result with some
// valid files_changed and some parent-prefix invalid entries must
// be refused, and every invalid path must appear in the error
// message. The operator needs the full fix list in one pass.
//
// Round 2 of Phase 3.6 added this test alongside the parent-prefix
// cases above: fixing the single instance is not sufficient when
// the exploit can land on any of several files in a single YAML.
func TestStore_AppendResult_FilesChangedContainment_MixedBag(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	c.WriteSet = []string{"internal/mission/", "cmd/ethos/mission.go"}
	require.NoError(t, s.Create(c))

	r := resultFor(c, VerdictPass)
	r.FilesChanged = []FileChange{
		{Path: "internal/mission/result.go"}, // valid
		{Path: "cmd/ethos/mission.go"},       // valid (exact match to file entry)
		{Path: "cmd/ethos"},                  // INVALID (parent of file entry)
		{Path: "cmd"},                        // INVALID (top-level ancestor)
		{Path: "internal"},                   // INVALID (top-level ancestor of dir)
	}
	err := s.AppendResult(c.MissionID, r)
	require.Error(t, err)
	msg := err.Error()
	// Every invalid path must be named in the error.
	assert.Contains(t, msg, "cmd/ethos")
	assert.Contains(t, msg, "cmd,")
	assert.Contains(t, msg, "internal")
	// The valid paths must not appear.
	assert.NotContains(t, msg, "internal/mission/result.go")
	assert.NotContains(t, msg, "cmd/ethos/mission.go,")
	// Count: 3 out-of-bounds paths.
	assert.Contains(t, msg, "3 path(s)")
}

// TestStore_AppendResult_FilesChangedContainment_TrailingSlash
// asserts the round 2 fix preserved the trailing-slash equivalence
// the normalization layer already provides. A write_set entry with
// a trailing slash (`internal/mission/`) must still admit a result
// path without one (`internal/mission/result.go`), and vice versa.
func TestStore_AppendResult_FilesChangedContainment_TrailingSlash(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	c.WriteSet = []string{"internal/mission/"}
	require.NoError(t, s.Create(c))

	r := resultFor(c, VerdictPass)
	r.FilesChanged = []FileChange{
		{Path: "internal/mission/result.go"},
		{Path: "internal/mission/store.go"},
	}
	require.NoError(t, s.AppendResult(c.MissionID, r))
}

// TestStore_AppendResult_FilesChangedAcceptsValid asserts the positive
// branch of the containment check: paths under a write_set entry
// (exact, prefix, and dot-segment variants) are all admitted.
func TestStore_AppendResult_FilesChangedAcceptsValid(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	c.WriteSet = []string{"internal/mission/", "cmd/ethos/mission.go"}
	require.NoError(t, s.Create(c))

	valid := []string{
		"internal/mission/result.go",
		"internal/mission/store.go",
		"cmd/ethos/mission.go",
		"./internal/mission/result.go",
		"internal/./mission/store.go",
	}
	r := resultFor(c, VerdictPass)
	r.FilesChanged = nil
	for _, p := range valid {
		r.FilesChanged = append(r.FilesChanged, FileChange{Path: p, Added: 1})
	}
	require.NoError(t, s.AppendResult(c.MissionID, r))
}

// TestStore_AppendResult_FilesChangedReportsAllOutOfBounds asserts
// that a result with multiple out-of-bounds paths lists every one in
// the error message — operators need the full fix list in a single
// pass, not one retry per path.
func TestStore_AppendResult_FilesChangedReportsAllOutOfBounds(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	c.WriteSet = []string{"internal/mission/"}
	require.NoError(t, s.Create(c))

	r := resultFor(c, VerdictPass)
	r.FilesChanged = []FileChange{
		{Path: "cmd/foo.go"},
		{Path: "cmd/bar.go"},
		{Path: "internal/mission/ok.go"}, // this one is allowed
	}
	err := s.AppendResult(c.MissionID, r)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "cmd/foo.go")
	assert.Contains(t, msg, "cmd/bar.go")
	assert.NotContains(t, msg, "internal/mission/ok.go",
		"error must not list the path that is inside the write_set")
	assert.Contains(t, msg, "2 path(s)")
}

// TestStore_LoadResults_RejectsUnknownField asserts that a
// hand-edited results file with a smuggled key is rejected on read,
// symmetric with the contract and reflections decode trust boundaries.
func TestStore_LoadResults_RejectsUnknownField(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	body := []byte(`results:
  - mission: m-2026-04-08-001
    round: 1
    author: bwk
    verdict: pass
    confidence: 0.8
    evidence:
      - name: make check
        status: pass
    bogus: smuggled
`)
	require.NoError(t, os.WriteFile(s.resultsPath(c.MissionID), body, 0o600))

	_, err := s.LoadResults(c.MissionID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field bogus not found")
}

// TestStore_LoadResults_RejectsMismatchedMissionID asserts the
// on-disk trust symmetry Phase 3.6 round 5 added: a hand-edited
// results file whose entry carries a Mission ID different from the
// file's parent mission is rejected at read time, exactly as
// AppendResult rejects it at write time.
//
// Before the fix, an attacker with local write access to
// ~/.punt-labs/ethos/missions/<id>.results.yaml could drop a
// forged result into mission A's sibling file claiming mission B.
// decodeResultsFile ran r.Validate (which passes — the forged
// entry is internally consistent) but never checked
// r.Mission == missionID. The close gate then accepted the forged
// result as long as the round matched, silently authorizing a
// terminal transition on A based on work reported for B.
//
// The test seeds a results file under missionA containing a result
// whose mission field is missionB, then drives two surfaces:
//  1. LoadResults(missionA) must return an error naming both IDs.
//  2. Close(missionA, StatusClosed) must refuse, propagating the
//     load failure. Pre-fix, Close silently accepted because
//     checkResultGateLocked only matched on round number.
func TestStore_LoadResults_RejectsMismatchedMissionID(t *testing.T) {
	s := testStore(t)
	missionA := "m-2026-04-08-001"
	missionB := "m-2026-04-08-002"
	c := newContract(missionA)
	require.NoError(t, s.Create(c))

	// Hand-craft a results file under missionA whose single entry
	// declares itself as belonging to missionB. Round 1 matches the
	// freshly-created mission's current_round so the pre-fix gate
	// would have been satisfied by the forgery.
	body := []byte(`results:
  - mission: ` + missionB + `
    round: 1
    author: bwk
    verdict: pass
    confidence: 0.8
    evidence:
      - name: make check
        status: pass
`)
	require.NoError(t, os.WriteFile(s.resultsPath(missionA), body, 0o600))

	// Surface 1: LoadResults names both IDs in the mismatch error.
	_, err := s.LoadResults(missionA)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "mission")
	assert.Contains(t, msg, missionA, "error must name the target mission")
	assert.Contains(t, msg, missionB, "error must name the forged mission")

	// Surface 2: Close refuses, and the load failure is propagated
	// through checkResultGateLocked rather than silently satisfied
	// by the round match. Pre-fix this assertion fails — Close
	// accepts the forgery and flips missionA to closed.
	err = s.Close(missionA, StatusClosed)
	require.Error(t, err, "close must refuse when the results file is forged")
	assert.Contains(t, err.Error(), "mission")

	// And the mission must still be open — the gate refusal must
	// not leak any partial state through.
	loaded, loadErr := s.Load(missionA)
	require.NoError(t, loadErr)
	assert.Equal(t, StatusOpen, loaded.Status,
		"gate refusal must not flip the status on a forgery")
	assert.Empty(t, loaded.ClosedAt)
}

// TestStore_LoadResults_EmptyForFreshMission asserts that a brand-new
// mission has no results file on disk and LoadResults returns nil
// with no error.
func TestStore_LoadResults_EmptyForFreshMission(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	rs, err := s.LoadResults(c.MissionID)
	require.NoError(t, err)
	assert.Nil(t, rs)
}

// TestStore_LoadResult_MissingRoundReturnsNil asserts that
// LoadResult returns (nil, nil) for a round with no result on file.
// This is the shape the close gate relies on to decide refusal.
func TestStore_LoadResult_MissingRoundReturnsNil(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	r, err := s.LoadResult(c.MissionID, 1)
	require.NoError(t, err)
	assert.Nil(t, r)
}

// TestStore_CloseGate_RefusesWithoutResult covers success criterion 5:
// every terminal status transition is refused unless a result
// exists for the current round. The error names the mission, the
// round, and the recovery command.
func TestStore_CloseGate_RefusesWithoutResult(t *testing.T) {
	for _, status := range []string{StatusClosed, StatusFailed, StatusEscalated} {
		t.Run(status, func(t *testing.T) {
			s := testStore(t)
			c := disjointContract("m-2026-04-08-001")
			require.NoError(t, s.Create(c))

			err := s.Close(c.MissionID, status)
			require.Error(t, err)
			msg := err.Error()
			assert.Contains(t, msg, c.MissionID)
			assert.Contains(t, msg, "no result artifact for round 1")
			assert.Contains(t, msg, "ethos mission result")

			// No terminal transition should have happened.
			loaded, loadErr := s.Load(c.MissionID)
			require.NoError(t, loadErr)
			assert.Equal(t, StatusOpen, loaded.Status,
				"gate refusal must not flip the status")
			assert.Empty(t, loaded.ClosedAt)
		})
	}
}

// TestStore_CloseGate_AcceptsWithResult covers success criterion 6:
// a mission with a valid result for the current round closes
// cleanly. Every terminal status is exercised.
func TestStore_CloseGate_AcceptsWithResult(t *testing.T) {
	cases := map[string]string{
		StatusClosed:    VerdictPass,
		StatusFailed:    VerdictFail,
		StatusEscalated: VerdictEscalate,
	}
	for status, verdict := range cases {
		t.Run(status, func(t *testing.T) {
			s := testStore(t)
			c := disjointContract("m-2026-04-08-001")
			require.NoError(t, s.Create(c))
			submitRoundResult(t, s, c, verdict)

			require.NoError(t, s.Close(c.MissionID, status))

			loaded, err := s.Load(c.MissionID)
			require.NoError(t, err)
			assert.Equal(t, status, loaded.Status)
			assert.NotEmpty(t, loaded.ClosedAt)
		})
	}
}

// TestStore_CloseGate_RefusesWhenOnlyStaleRoundHasResult asserts that
// the gate checks the CURRENT round, not any round. If a mission has
// advanced from round 1 to round 2, the round-1 result no longer
// satisfies the gate — the worker must submit a round-2 result.
func TestStore_CloseGate_RefusesWhenOnlyStaleRoundHasResult(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	// Round 1: submit result and advance.
	submitRoundResult(t, s, c, VerdictPass)
	require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(1, RecommendationContinue)))
	newRound, err := s.AdvanceRound(c.MissionID, "claude")
	require.NoError(t, err)
	require.Equal(t, 2, newRound)

	// Round 2 has no result yet; close must refuse.
	err = s.Close(c.MissionID, StatusClosed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "round 2")
}

// TestStore_ListSkipsResultsFile is the Phase 3.6 regression test
// for the Phase 3.4 round-2 BLOCKER. The bug Phase 3.4 round 2 fixed:
// Store.List treated sibling YAML files as contracts, breaking
// List itself, MatchByPrefix (ambiguous matches), and Create's
// cross-mission conflict scan (fatal load failure). Phase 3.6 adds a
// new sibling file (.results.yaml) and must not reproduce that
// failure mode.
//
// The test creates a mission, submits a result, and drives all three
// failure surfaces: List must return the contract only, MatchByPrefix
// must resolve unambiguously, and a second Create must succeed.
func TestStore_ListSkipsResultsFile(t *testing.T) {
	s := testStore(t)
	c := validContract()
	c.MissionID = "m-2026-04-08-001"
	c.WriteSet = []string{"tests/list-skip-results/"}
	require.NoError(t, s.Create(&c))

	// Submit a result so the sibling file exists on disk.
	r := &Result{
		Mission:    c.MissionID,
		Round:      1,
		Author:     "bwk",
		Verdict:    VerdictPass,
		Confidence: 0.9,
		Evidence: []EvidenceCheck{
			{Name: "make check", Status: EvidenceStatusPass},
		},
	}
	require.NoError(t, s.AppendResult(c.MissionID, r))

	// Verify the sibling file actually exists — the test is
	// meaningless if AppendResult silently dropped the write.
	_, err := os.Stat(s.resultsPath(c.MissionID))
	require.NoError(t, err, "results file must exist for the test to be meaningful")

	// Failure mode 1: List must return exactly one mission ID.
	ids, err := s.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"m-2026-04-08-001"}, ids,
		"List must skip <id>.results.yaml")

	// Failure mode 2: MatchByPrefix must resolve unambiguously.
	id, err := s.MatchByPrefix("m-2026-04-08")
	require.NoError(t, err)
	assert.Equal(t, "m-2026-04-08-001", id)

	// Failure mode 3: Create must succeed for a new mission.
	c2 := validContract()
	c2.MissionID = "m-2026-04-08-002"
	c2.WriteSet = []string{"tests/list-skip-results-2/"}
	assert.NoError(t, s.Create(&c2),
		"Create must not choke on the sibling results file")
}

// TestStore_ListSkipsResultsFile_CoexistsWithReflections asserts both
// sibling files coexist with the contract: a mission with BOTH a
// reflections file and a results file still lists as one mission.
// This covers the interaction between Phase 3.4 and Phase 3.6 at the
// List boundary — a regression that only surfaces when both sibling
// files are present.
func TestStore_ListSkipsResultsFile_CoexistsWithReflections(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	submitRoundResult(t, s, c, VerdictPass)
	require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(1, RecommendationContinue)))

	ids, err := s.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"m-2026-04-08-001"}, ids,
		"List must return exactly one mission ID when both sibling files exist")
}

// TestStore_NonTerminalTransitionsUnchanged covers success criterion 9:
// the Phase 3.6 gate fires only on terminal close. Every other
// transition — AppendReflection, AdvanceRound, Update — is unchanged.
// A mission can go round 1 → round 2 → round 3 without ever
// submitting a result.
func TestStore_NonTerminalTransitionsUnchanged(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	c.Budget.Rounds = 3
	require.NoError(t, s.Create(c))

	// Round 1 → 2: reflection only, no result. The gate is
	// close-specific; advance must work.
	require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(1, RecommendationContinue)))
	newRound, err := s.AdvanceRound(c.MissionID, "claude")
	require.NoError(t, err)
	assert.Equal(t, 2, newRound)

	// Round 2 → 3: same.
	require.NoError(t, s.AppendReflection(c.MissionID, reflectionFor(2, RecommendationContinue)))
	newRound, err = s.AdvanceRound(c.MissionID, "claude")
	require.NoError(t, err)
	assert.Equal(t, 3, newRound)

	// Update: the context field must round-trip without a result.
	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err)
	loaded.Context = "mid-round context update"
	require.NoError(t, s.Update(loaded))

	// The close gate fires only now, at round 3.
	err = s.Close(c.MissionID, StatusClosed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "round 3")
}

// TestStore_Close_EventLogRecordsRoundAndVerdict asserts the M3 fix:
// the close event carries the round number and verdict of the
// satisfying result so an auditor reading the JSONL does not have
// to scan back across round_advanced events to reconstruct which
// result authorized the terminal transition. Round 2 of Phase 3.6
// added these fields to the close event's Details map.
func TestStore_Close_EventLogRecordsRoundAndVerdict(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	// Submit a round-1 result with a specific verdict so the
	// assertion can distinguish it from a default-filled value.
	r := resultFor(c, VerdictFail)
	require.NoError(t, s.AppendResult(c.MissionID, r))

	// Close with a terminal status that does not match the verdict
	// — the gate does not require equality, but the event log must
	// still record the result's OWN verdict, not the close status.
	require.NoError(t, s.Close(c.MissionID, StatusFailed))

	events := readLog(t, s, c.MissionID)
	require.NotEmpty(t, events)
	var closeEvent *Event
	for i := range events {
		if events[i].Event == "close" {
			closeEvent = &events[i]
			break
		}
	}
	require.NotNil(t, closeEvent, "close event must exist in log")
	assert.Equal(t, StatusFailed, closeEvent.Details["status"])
	// Round is JSON-decoded as float64, same as the result event.
	assert.Equal(t, float64(1), closeEvent.Details["round"],
		"close event must carry the satisfying result's round")
	assert.Equal(t, VerdictFail, closeEvent.Details["verdict"],
		"close event must carry the satisfying result's verdict, not the close status")
}

// TestStore_AppendResult_LogsEvent asserts that a successful
// AppendResult writes a `result` event with round and verdict
// details so the audit trail is complete.
func TestStore_AppendResult_LogsEvent(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	require.NoError(t, s.AppendResult(c.MissionID, resultFor(c, VerdictPass)))

	events := readLog(t, s, c.MissionID)
	require.Len(t, events, 2)
	assert.Equal(t, "create", events[0].Event)
	assert.Equal(t, "result", events[1].Event)
	assert.Equal(t, float64(1), events[1].Details["round"])
	assert.Equal(t, "pass", events[1].Details["verdict"])
}

// TestStore_AppendResult_NormalizesAuthor asserts the L2 fix:
// whitespace around the author handle is trimmed at persist time so
// the audit trail and event log do not carry `"  bwk"` values. The
// round 2 fix is normalization in AppendResult, not tightening
// Validate — Validate still accepts whitespace so pre-round-2
// files on disk still load.
func TestStore_AppendResult_NormalizesAuthor(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	r := resultFor(c, VerdictPass)
	r.Author = "  bwk  "
	require.NoError(t, s.AppendResult(c.MissionID, r))

	loaded, err := s.LoadResult(c.MissionID, 1)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "bwk", loaded.Author,
		"AppendResult must persist the trimmed author")

	// The event log must also carry the trimmed handle.
	events := readLog(t, s, c.MissionID)
	var resultEvent *Event
	for i := range events {
		if events[i].Event == "result" {
			resultEvent = &events[i]
			break
		}
	}
	require.NotNil(t, resultEvent)
	assert.Equal(t, "bwk", resultEvent.Actor,
		"event log actor must be the trimmed author")
}

// TestStore_AppendReflection_NormalizesAuthor asserts the L2 class
// fix is applied symmetrically to the reflection store. Fixing
// only AppendResult would ship asymmetric behavior across the two
// sibling stores — round 2 widened the fix to both.
func TestStore_AppendReflection_NormalizesAuthor(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	r := reflectionFor(1, RecommendationContinue)
	r.Author = "  claude  "
	require.NoError(t, s.AppendReflection(c.MissionID, r))

	rs, err := s.LoadReflections(c.MissionID)
	require.NoError(t, err)
	require.Len(t, rs, 1)
	assert.Equal(t, "claude", rs[0].Author,
		"AppendReflection must persist the trimmed author")

	events := readLog(t, s, c.MissionID)
	var reflectEvent *Event
	for i := range events {
		if events[i].Event == "reflect" {
			reflectEvent = &events[i]
			break
		}
	}
	require.NotNil(t, reflectEvent)
	assert.Equal(t, "claude", reflectEvent.Actor,
		"event log actor must be the trimmed author")
}

// TestStore_AppendResult_ConcurrentSerialization asserts that two
// concurrent AppendResult calls on the same mission/round cannot
// both succeed: the per-mission flock serializes them so exactly one
// wins and the other sees the append-only refusal.
func TestStore_AppendResult_ConcurrentSerialization(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	const n = 10
	var wg sync.WaitGroup
	errs := make(chan error, n)
	successes := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.AppendResult(c.MissionID, resultFor(c, VerdictPass)); err != nil {
				errs <- err
				return
			}
			successes <- struct{}{}
		}()
	}
	wg.Wait()
	close(errs)
	close(successes)

	var wins int
	for range successes {
		wins++
	}
	var failures []error
	for err := range errs {
		failures = append(failures, err)
	}
	assert.Equal(t, 1, wins, "exactly one concurrent AppendResult must win")
	assert.Len(t, failures, n-1)
	for _, err := range failures {
		assert.Contains(t, err.Error(), "append-only",
			"failure must be the append-only refusal, not a lock error")
	}
}
