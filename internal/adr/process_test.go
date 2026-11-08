//go:build linux || darwin

package adr

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestADRProcessHelper(t *testing.T) {
	action := os.Getenv("ETHOS_ADR_TEST_ACTION")
	if action == "" {
		return
	}
	mode := layout.RecordBlame
	if os.Getenv("ETHOS_ADR_TEST_FULL") == "1" {
		mode = layout.RecordFull
	}
	p, err := layout.NewRepository(os.Getenv("ETHOS_ADR_TEST_ROOT"), os.Getenv("ETHOS_ADR_TEST_CHECKOUT"), mode)
	require.NoError(t, err)
	s, err := NewRepositoryStore(p, os.Getenv("ETHOS_ADR_TEST_LEGACY"))
	require.NoError(t, err)
	switch action {
	case "create":
		for i := 0; i < 4; i++ {
			require.NoError(t, s.Create(sampleADR()))
		}
	case "update":
		for i := 0; i < 4; i++ {
			require.NoError(t, s.Update("DES-001", func(a *ADR) { a.Alternatives = append(a.Alternatives, "process update") }))
		}
	case "interrupt":
		s.link = func(src, dest string) error {
			// Exit at the last pre-commit point without executing deferred
			// cleanup. The only orphan must be local and the lock must release.
			os.Exit(23)
			return nil
		}
		require.NoError(t, s.Create(sampleADR()))
	case "interrupt-after-create", "interrupt-after-update":
		s.syncDir = func(string) error {
			// Link/rename has already installed complete bytes. Die before
			// syncing any destination directory or running deferred cleanup.
			os.Exit(24)
			return nil
		}
		if action == "interrupt-after-create" {
			require.NoError(t, s.Create(sampleADR()))
		} else {
			require.NoError(t, s.Update("DES-001", func(a *ADR) { a.Context = "process committed" }))
		}
	default:
		t.Fatalf("unknown action %q", action)
	}
}

func adrChild(t *testing.T, p layout.Policy, legacy string, full bool, action string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestADRProcessHelper$", "-test.count=1")
	cmd.Env = []string{
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
		"ETHOS_ADR_TEST_ACTION=" + action,
		"ETHOS_ADR_TEST_ROOT=" + filepath.Dir(filepath.Dir(p.RecordRoot())),
		"ETHOS_ADR_TEST_CHECKOUT=" + filepath.Dir(filepath.Dir(p.CheckoutRecordRoot())),
		"ETHOS_ADR_TEST_LEGACY=" + legacy,
		fmt.Sprintf("ETHOS_ADR_TEST_FULL=%d", map[bool]int{true: 1}[full]),
	}
	return cmd
}

func TestRepositoryStore_ProcessConcurrency(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, legacy, p := repositoryStore(t, layout.RecordBlame)
	a := sampleADR()
	require.NoError(t, legacy.Create(a))
	const processes = 6
	for _, action := range []string{"create", "update"} {
		t.Run(action, func(t *testing.T) {
			type outcome struct {
				output []byte
				err    error
			}
			results := make(chan outcome, processes)
			var wg sync.WaitGroup
			for i := 0; i < processes; i++ {
				// Separate checkout snapshots share one store and lock domain.
				policy, err := layout.NewRepository(filepath.Dir(filepath.Dir(p.RecordRoot())), canonicalTempDir(t), layout.RecordBlame)
				require.NoError(t, err)
				cmd := adrChild(t, policy, legacy.dir, i%2 == 1, action)
				wg.Add(1)
				go func() {
					defer wg.Done()
					output, err := cmd.CombinedOutput()
					results <- outcome{output, err}
				}()
			}
			wg.Wait()
			close(results)
			for result := range results {
				require.NoError(t, result.err, "%s", result.output)
			}
		})
	}
	ids, err := s.List()
	require.NoError(t, err)
	assert.Len(t, ids, processes*4+1)
	for i, id := range ids {
		assert.Equal(t, fmt.Sprintf("DES-%03d", i+1), id)
	}
	got, err := s.Load("DES-001")
	require.NoError(t, err)
	assert.Len(t, got.Alternatives, processes*4)
	global, err := legacy.Load("DES-001")
	require.NoError(t, err)
	assert.Empty(t, global.Alternatives, "copy-on-write never updates the legacy source")
	assertOnlyADRs(t, p.RecordRoot())
}

func TestRepositoryStore_ProcessInterruptionLeavesOnlyLocalScratch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, _, p := repositoryStore(t, layout.RecordFull)
	output, err := adrChild(t, p, "", false, "interrupt").CombinedOutput()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, "%s", output)
	assert.Equal(t, 23, exit.ExitCode())
	assert.NoFileExists(t, s.path("DES-001"))
	assertOnlyADRs(t, p.RecordRoot())
	orphans, err := os.ReadDir(p.TempDir())
	require.NoError(t, err)
	require.Len(t, orphans, 1)
	assert.True(t, strings.HasSuffix(orphans[0].Name(), layout.TmpMarker))
	// The child died holding the lock. A new process must still create without
	// deadlock or a leaked counter, despite the abandoned local staging file.
	output, err = adrChild(t, p, "", true, "create").CombinedOutput()
	require.NoError(t, err, "%s", output)
	ids, err := s.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"DES-001", "DES-002", "DES-003", "DES-004"}, ids)
	assertOnlyADRs(t, p.RecordRoot())
}

func TestRepositoryStore_ProcessInterruptionAfterPublication(t *testing.T) {
	t.Setenv("HOME", canonicalTempDir(t))
	for _, action := range []string{"interrupt-after-create", "interrupt-after-update"} {
		t.Run(action, func(t *testing.T) {
			s, _, p := repositoryStore(t, layout.RecordBlame)
			if action == "interrupt-after-update" {
				require.NoError(t, s.Create(sampleADR()))
			}
			output, err := adrChild(t, p, "", true, action).CombinedOutput()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit, "%s", output)
			assert.Equal(t, 24, exit.ExitCode())
			stored, err := s.loadOwn("DES-001")
			require.NoError(t, err, "post-publication death cannot remove or roll back an installed record")
			if action == "interrupt-after-update" {
				assert.Equal(t, "process committed", stored.Context)
			}
			// A new process acquires the released lock, observes the installed
			// ID and allocates new IDs rather than reusing the uncertain one.
			output, err = adrChild(t, p, "", false, "create").CombinedOutput()
			require.NoError(t, err, "%s", output)
			ids, err := s.List()
			require.NoError(t, err)
			assert.Equal(t, []string{"DES-001", "DES-002", "DES-003", "DES-004", "DES-005"}, ids)
			assertOnlyADRs(t, p.RecordRoot())
			entries, err := os.ReadDir(p.TempDir())
			require.NoError(t, err)
			if action == "interrupt-after-create" {
				require.Len(t, entries, 1, "hard-link publication can leave only local staging on process death")
				assert.True(t, strings.HasSuffix(entries[0].Name(), layout.TmpMarker))
			} else {
				assert.Empty(t, entries, "rename already consumed its staging path")
			}
		})
	}
}

func TestRepositoryStore_HoldsMutationLockThroughDirectorySync(t *testing.T) {
	t.Setenv("HOME", canonicalTempDir(t))
	s, _, p := repositoryStore(t, layout.RecordBlame)
	path, err := p.LockPath("adr", "store")
	require.NoError(t, err)
	s.syncDir = func(dir string) error {
		probe, err := os.OpenFile(path, os.O_RDWR, 0o600)
		require.NoError(t, err)
		// A separately opened description in the same process conflicts with
		// the writer's flock. A nonblocking attempt proves the lock spans
		// post-publication sync without timing or a sleeping competitor.
		err = syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		assert.ErrorIs(t, err, syscall.EWOULDBLOCK)
		require.NoError(t, probe.Close())
		return syncDirectory(dir)
	}
	a := sampleADR()
	require.NoError(t, s.Create(a))
	require.NoError(t, s.Update(a.ID, func(a *ADR) { a.Status = StatusSettled }))
}
