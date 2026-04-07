//go:build !windows

package mission

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
func TestStore_CreateDoesNotMutateCallerOnValidationFailure(t *testing.T) {
	s := testStore(t)
	// Start with a valid contract, then break it by emptying WriteSet.
	c := newContract("m-2026-04-07-030")
	c.WriteSet = nil          // validation will reject this
	originalUpdatedAt := c.UpdatedAt // caller has "" here

	err := s.Create(c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write_set")
	assert.Equal(t, originalUpdatedAt, c.UpdatedAt, "Create must not default-fill UpdatedAt on failure")
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

	// Real mutation: add context. Wait briefly to guarantee the clock
	// has advanced past the create timestamp so the RFC3339 string
	// actually differs.
	loaded.Context = "post-create mutation"
	time.Sleep(1 * time.Second)
	require.NoError(t, s.Update(loaded))

	assert.NotEqual(t, originalUpdatedAt, loaded.UpdatedAt, "UpdatedAt must advance on success")
}

func TestStore_Close(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-07-003")
	require.NoError(t, s.Create(c))

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
			c := newContract(id)
			require.NoError(t, s.Create(c))
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

	require.NoError(t, s.Create(newContract("m-2026-04-07-001")))
	require.NoError(t, s.Create(newContract("m-2026-04-07-002")))

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
	require.NoError(t, s.Create(newContract("m-2026-04-07-001")))
	require.NoError(t, s.Create(newContract("m-2026-04-07-002")))
	require.NoError(t, s.Create(newContract("m-2026-04-08-001")))

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
	// Each goroutine creates a distinct mission ID. The test asserts the
	// store does not corrupt state under concurrent flock acquisition.
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
			c := newContract(id)
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
