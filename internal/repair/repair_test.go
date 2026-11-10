//go:build linux || darwin

package repair

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

func put(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func fixture(t *testing.T) layout.Policy {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_INDEX_FILE", "")
	require.NoError(t, os.Unsetenv("GIT_INDEX_FILE"))
	root := t.TempDir()
	_, err := gitenv.Output(root, "git", "init", "-q")
	require.NoError(t, err)
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	return p
}

func TestInventoryAndCleanupRetainProtectedData(t *testing.T) {
	p := fixture(t)
	m := filepath.Join(p.RecordRoot(), "missions", "m-old")
	deleted := []string{
		filepath.Join(m, "reflections.yaml"),
		filepath.Join(m, "delegations", "d-old", "prompt.md"),
		filepath.Join(m, "log.jsonl"),
		filepath.Join(m, audit.MissionChunkFile("sid", 100, 200)),
		filepath.Join(m, audit.MissionChunkFile("sid", 100, 200)+".corrupt"),
		filepath.Join(m, strings.TrimSuffix(audit.MissionChunkFile("sid", 100, 200), ".jsonl")+".quarantine"),
		filepath.Join(m, audit.MissionTempFile("sid", 100, 200)),
		filepath.Join(m, "contract.yaml"+layout.TmpMarker),
		filepath.Join(m, ".lock"),
		filepath.Join(p.RecordRoot(), "missions.jsonl.lock"),
		filepath.Join(p.RecordRoot(), "sessions", "2026-01-01-sid", audit.SessionChunkFile(100, 200)),
	}
	for _, path := range deleted {
		put(t, path, "raw malformed historical bytes\x00")
	}
	retained := []string{
		filepath.Join(m, "contract.yaml"), filepath.Join(m, "results.yaml"), filepath.Join(m, "corrections.yaml"),
		filepath.Join(m, "delegations", "d-old", "record.yaml"),
		filepath.Join(m, "delegations", "d-old", "output.ethos-tmp"),
		filepath.Join(m, "artifacts", "notes.lock"), filepath.Join(m, "personal.ethos-tmp"),
		filepath.Join(p.RecordRoot(), "identities", "bwk.ext", "secret.local.yaml"),
		filepath.Join(p.RecordRoot(), "adrs", "des-1.yaml"), filepath.Join(p.RecordRoot(), "provenance", "proof.json"),
		filepath.Join(p.LocalRoot(), "trace", "missions", "m-old", "log.jsonl"),
		filepath.Join(p.LocalRoot(), "quarantine", "raw.corrupt"),
		filepath.Join(p.LocalRoot(), "sessions", "sid.audit.jsonl"),
	}
	for _, path := range retained {
		put(t, path, "unchanged: bytes\n")
	}
	current := filepath.Join(p.RecordRoot(), "missions", "m-current")
	put(t, filepath.Join(current, "contract.yaml"), "round_decisions: {version: 1, entries: []}\n")
	put(t, filepath.Join(current, "reflections.yaml"), "current publication")
	full := filepath.Join(m, audit.MissionChunkFile("full", 300, 400))
	put(t, full, "broken tail\n{\"record\":\"full\"}\n")
	plan, err := Inventory(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, plan.Items, len(deleted))
	for _, path := range deleted {
		assert.FileExists(t, path, "inventory must not delete")
	}
	removed, err := plan.Apply(context.Background())
	require.NoError(t, err)
	assert.ElementsMatch(t, deleted, removed)
	for _, path := range retained {
		b, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "unchanged: bytes\n", string(b))
	}
	assert.FileExists(t, filepath.Join(current, "reflections.yaml"))
	assert.FileExists(t, full)
	plan, err = Inventory(context.Background(), p)
	require.NoError(t, err)
	assert.Empty(t, plan.Items)
}

func TestBusyOldAndNewLocksSkipTheirMission(t *testing.T) {
	for _, current := range []bool{false, true} {
		t.Run(map[bool]string{false: "old", true: "new"}[current], func(t *testing.T) {
			p := fixture(t)
			trace := filepath.Join(p.RecordRoot(), "missions", "m-old", "log.jsonl")
			put(t, trace, "obsolete")
			put(t, filepath.Join(filepath.Dir(trace), "contract.yaml"), "mission_id: m-old\n")
			lock := filepath.Join(filepath.Dir(trace), ".lock")
			if current {
				var err error
				lock, err = p.LockPath("mission-state", "m-old")
				require.NoError(t, err)
			}
			put(t, lock, "")
			f, err := os.OpenFile(lock, os.O_RDWR, 0)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, f.Close()) })
			require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
			plan, err := Inventory(context.Background(), p)
			require.NoError(t, err)
			removed, err := plan.Apply(context.Background())
			require.NoError(t, err)
			assert.Empty(t, removed)
			assert.Contains(t, plan.Skipped, trace)
			assert.FileExists(t, trace)
			assert.FileExists(t, lock)
		})
	}
}

func TestChangedInventoryAndCancellationRefuse(t *testing.T) {
	p := fixture(t)
	path := filepath.Join(p.RecordRoot(), "missions", "m-old", "log.jsonl")
	put(t, path, "before")
	put(t, filepath.Join(filepath.Dir(path), "contract.yaml"), "mission_id: m-old\n")
	plan, err := Inventory(context.Background(), p)
	require.NoError(t, err)
	put(t, path, "after!")
	removed, err := plan.Apply(context.Background())
	require.ErrorContains(t, err, "inventory changed")
	assert.Empty(t, removed)
	assert.FileExists(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = plan.Apply(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.FileExists(t, path)
}

func TestInterruptedDeletionReportsProgressAndRetries(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure requires an unprivileged process")
	}
	p := fixture(t)
	first := filepath.Join(p.RecordRoot(), "missions", "m-a", "log.jsonl")
	last := filepath.Join(p.RecordRoot(), "missions", "m-b", "log.jsonl")
	put(t, first, "first")
	put(t, last, "last")
	put(t, filepath.Join(filepath.Dir(first), "contract.yaml"), "mission_id: m-a\n")
	put(t, filepath.Join(filepath.Dir(last), "contract.yaml"), "mission_id: m-b\n")
	require.NoError(t, os.Chmod(filepath.Dir(last), 0o500))
	t.Cleanup(func() { require.NoError(t, os.Chmod(filepath.Dir(last), 0o700)) })
	plan, err := Inventory(context.Background(), p)
	require.NoError(t, err)
	removed, err := plan.Apply(context.Background())
	require.ErrorContains(t, err, "deleting "+last)
	assert.Equal(t, []string{first}, removed)
	assert.NoFileExists(t, first)
	assert.FileExists(t, last)
	require.NoError(t, os.Chmod(filepath.Dir(last), 0o700))
	plan, err = Inventory(context.Background(), p)
	require.NoError(t, err)
	removed, err = plan.Apply(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{last}, removed)
}

func TestLinkedRootsInventoryBothAndPreserveCanonicalLocal(t *testing.T) {
	p := fixture(t)
	checkout := t.TempDir()
	_, err := gitenv.Output(checkout, "git", "init", "-q")
	require.NoError(t, err)
	p, err = layout.NewRepository(filepath.Dir(filepath.Dir(p.RecordRoot())), checkout, layout.RecordBlame)
	require.NoError(t, err)
	storeLog := filepath.Join(p.RecordRoot(), "missions", "m-one", "log.jsonl")
	checkoutLog := filepath.Join(p.CheckoutRecordRoot(), "missions", "m-two", "log.jsonl")
	local := filepath.Join(p.LocalRoot(), "missions", "m-one", "sid.log.jsonl")
	for _, path := range []string{storeLog, checkoutLog, local} {
		put(t, path, "history")
	}
	put(t, filepath.Join(filepath.Dir(storeLog), "contract.yaml"), "mission_id: m-one\n")
	put(t, filepath.Join(filepath.Dir(checkoutLog), "contract.yaml"), "mission_id: m-two\n")
	plan, err := Inventory(context.Background(), p)
	require.NoError(t, err)
	removed, err := plan.Apply(context.Background())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{storeLog, checkoutLog}, removed)
	assert.FileExists(t, local)
}

func TestSymlinkAndMalformedAuthorityHaveExactBlockers(t *testing.T) {
	p := fixture(t)
	m := filepath.Join(p.RecordRoot(), "missions", "m-old")
	put(t, filepath.Join(m, "reflections.yaml"), "detail")
	put(t, filepath.Join(m, "contract.yaml"), "[broken")
	_, err := Inventory(context.Background(), p)
	require.ErrorContains(t, err, "left in place: "+filepath.Join(m, "contract.yaml"))
	require.NoError(t, os.Remove(filepath.Join(m, "contract.yaml")))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(m, "outside")))
	_, err = Inventory(context.Background(), p)
	require.ErrorContains(t, err, "symlink or nested repository")
	assert.FileExists(t, filepath.Join(m, "reflections.yaml"))
}

func TestCanonicalAuthorityRetainsCheckoutOnlyPublication(t *testing.T) {
	p := fixture(t)
	checkout := fixture(t)
	p, err := layout.NewRepository(filepath.Dir(filepath.Dir(p.RecordRoot())), filepath.Dir(filepath.Dir(checkout.RecordRoot())), layout.RecordBlame)
	require.NoError(t, err)
	id := "m-current"
	authority := filepath.Join(p.RecordRoot(), "missions", id, "contract.yaml")
	put(t, authority, "round_decisions: {version: 1, entries: []}\n")
	detail := []string{
		filepath.Join(p.CheckoutRecordRoot(), "missions", id, "reflections.yaml"),
		filepath.Join(p.CheckoutRecordRoot(), "missions", id, "delegations", "d-one", "prompt.md"),
		filepath.Join(p.CheckoutRecordRoot(), "missions", id, audit.MissionChunkFile("sid", 100, 200)),
	}
	for _, path := range detail {
		put(t, path, "current publication without a sibling contract")
	}
	assert.NoFileExists(t, filepath.Join(p.CheckoutRecordRoot(), "missions", id, "contract.yaml"))
	plan, err := Inventory(context.Background(), p)
	require.NoError(t, err)
	assert.Empty(t, plan.Items)
	removed, err := plan.Apply(context.Background())
	require.NoError(t, err)
	assert.Empty(t, removed)
	for _, path := range detail {
		b, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "current publication without a sibling contract", string(b))
	}
}

func TestCanonicalAuthorityChangeInvalidatesDeletionPlan(t *testing.T) {
	p := fixture(t)
	checkout := fixture(t)
	p, err := layout.NewRepository(filepath.Dir(filepath.Dir(p.RecordRoot())), filepath.Dir(filepath.Dir(checkout.RecordRoot())), layout.RecordBlame)
	require.NoError(t, err)
	contract := filepath.Join(p.RecordRoot(), "missions", "m-one", "contract.yaml")
	reflection := filepath.Join(p.CheckoutRecordRoot(), "missions", "m-one", "reflections.yaml")
	put(t, contract, "mission_id: m-one\n")
	put(t, reflection, "retained detail")
	plan, err := Inventory(context.Background(), p)
	require.NoError(t, err)
	require.Len(t, plan.Items, 1)
	put(t, contract, "round_decisions: {version: 1, entries: []}\n")
	removed, err := plan.Apply(context.Background())
	require.ErrorContains(t, err, "inventory changed")
	assert.Empty(t, removed)
	assert.FileExists(t, reflection)
}

func TestRetainCurrentExportAndAmbiguousMissionTrace(t *testing.T) {
	for _, tc := range []struct {
		name, canonical, checkout, exportSuffix string
	}{
		{name: "canonical current", canonical: "round_decisions: {version: 1, entries: []}\n"},
		{name: "checkout current", checkout: "round_decisions: {version: 1, entries: []}\n"},
		{name: "canonical current outranks checkout legacy", canonical: "round_decisions: {version: 1, entries: []}\n", checkout: "mission_id: m-one\n"},
		{name: "null decisions are ambiguous", canonical: "round_decisions: null\n"},
		{name: "no authority is ambiguous"},
		{name: "flat export without canonical authority", exportSuffix: ".contract.yaml"},
		{name: "flat export outranks legacy authority", canonical: "mission_id: m-one\n", exportSuffix: ".contract.yaml"},
		{name: "incomplete export is ambiguous", exportSuffix: ".result.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := layout.NewRepository(t.TempDir(), t.TempDir(), layout.RecordBlame)
			require.NoError(t, err)
			id := "m-one"
			if tc.canonical != "" {
				put(t, filepath.Join(p.RecordRoot(), "missions", id, "contract.yaml"), tc.canonical)
			}
			if tc.checkout != "" {
				put(t, filepath.Join(p.CheckoutRecordRoot(), "missions", id, "contract.yaml"), tc.checkout)
			}
			if tc.exportSuffix != "" {
				put(t, filepath.Join(p.CheckoutRecordRoot(), "missions", id+tc.exportSuffix), "export record\n")
			}
			for _, name := range []string{"reflections.yaml", "delegations/d-one/prompt.md", "log.jsonl", audit.MissionChunkFile("sid", 100, 200)} {
				rel := "missions/" + id + "/" + name
				put(t, filepath.Join(p.CheckoutRecordRoot(), filepath.FromSlash(rel)), "{\"record\":\"blame\"}\n")
				remove, lock, err := obsolete(p, p.CheckoutRecordRoot(), rel)
				require.NoError(t, err)
				assert.False(t, remove, rel)
				assert.False(t, lock, rel)
			}
		})
	}
}

func TestAmbiguousAuthorityCannotLicenseDeletion(t *testing.T) {
	for _, body := range []string{"[broken", "null\n", "mission_id: m-one\n---\nround_decisions: null\n", "mission_id: m-one\nmission_id: m-two\n"} {
		t.Run(body, func(t *testing.T) {
			p, err := layout.NewRepository(t.TempDir(), t.TempDir(), layout.RecordBlame)
			require.NoError(t, err)
			path := filepath.Join(p.RecordRoot(), "missions", "m-one", "contract.yaml")
			put(t, path, body)
			remove, _, err := obsolete(p, p.CheckoutRecordRoot(), "missions/m-one/reflections.yaml")
			require.ErrorContains(t, err, "left in place: "+path)
			assert.False(t, remove)
		})
	}
}

// A live audit.jsonl in a directory that matches no session is legacy, yet a
// copy carrying visible full consent is retained; Obsolete and Inventory agree.
func TestObsoleteAndInventoryAgreeOnUnmatchedSessionAudit(t *testing.T) {
	p := fixture(t)
	root := p.RecordRoot()
	put(t, filepath.Join(root, "sessions", "sess-a", "audit.jsonl"), "{\"record\":\"full\"}\n")
	put(t, filepath.Join(root, "sessions", "sess-b", "audit.jsonl"), "{\"record\":\"blame\"}\n")

	full, err := Obsolete(p, root, "sessions/sess-a/audit.jsonl", nil)
	require.NoError(t, err)
	assert.False(t, full)
	blame, err := Obsolete(p, root, "sessions/sess-b/audit.jsonl", nil)
	require.NoError(t, err)
	assert.True(t, blame)

	plan, err := Inventory(context.Background(), p)
	require.NoError(t, err)
	var paths []string
	for _, it := range plan.Items {
		paths = append(paths, filepath.ToSlash(strings.TrimPrefix(it.Path, root+"/")))
	}
	assert.Equal(t, []string{"sessions/sess-b/audit.jsonl"}, paths)

	// Working file gone: the fallback bytes decide.
	gone, err := Obsolete(p, root, "sessions/sess-c/audit.jsonl", func() ([]byte, error) { return []byte("{\"record\":\"full\"}\n"), nil })
	require.NoError(t, err)
	assert.False(t, gone)
}
