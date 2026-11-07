package provenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

const missionA = "m-2026-11-04-001"
const missionB = "m-2026-11-04-002"
const stamp = "2026-11-04T10:00:00Z"

func gitTest(t *testing.T, r Repository, args ...string) string {
	t.Helper()
	out, err := r.git(context.Background(), args...)
	require.NoError(t, err, "git %v", args)
	return strings.TrimSpace(string(out))
}

func writeTest(t *testing.T, root, name string, data []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
	require.NoError(t, os.WriteFile(p, data, 0o600))
}

func yamlTest[T any](t *testing.T, value T) []byte {
	t.Helper()
	data, err := yaml.Marshal(value)
	require.NoError(t, err)
	return data
}

func selectedTest(t *testing.T) mission.PublicationSnapshot {
	t.Helper()
	s := mission.PublicationSnapshot{Missions: []string{missionA, missionB}}
	for _, id := range s.Missions {
		prefix := ".punt-labs/ethos/missions/" + id + "/"
		c := mission.Contract{MissionID: id, Status: mission.StatusClosed, CreatedAt: stamp, UpdatedAt: stamp, ClosedAt: stamp,
			Leader: "lead", Worker: "bwk", Evaluator: mission.Evaluator{Handle: "rsc", PinnedAt: stamp}, WriteSet: []string{"source.txt"},
			SuccessCriteria: []string{"checked"}, Budget: mission.Budget{Rounds: 2}, CurrentRound: 2,
			RoundDecisions: &mission.RoundDecisions{Version: 1, Entries: []mission.RoundDecision{{ID: strings.Repeat("a", 32), Round: 1, CreatedAt: stamp, Author: "lead", Recommendation: "continue"}}}}
		s.Records = append(s.Records, mission.PublicationRecord{Path: prefix + "contract.yaml", Data: yamlTest(t, c)})
		results := struct {
			Results []mission.Result `yaml:"results"`
		}{}
		for round := 1; round <= 2; round++ {
			results.Results = append(results.Results, mission.Result{Mission: id, Round: round, Author: "bwk", Verdict: "pass", Confidence: 1, Evidence: []mission.EvidenceCheck{{Name: "check", Status: "pass"}}})
		}
		s.Records = append(s.Records, mission.PublicationRecord{Path: prefix + "results.yaml", Data: yamlTest(t, results)},
			mission.PublicationRecord{Path: prefix + "corrections.yaml", Data: []byte("version: 1\nmission: " + id + "\nentries: []\n")})
		dids := []string{"d-c"}
		if id == missionA {
			dids = []string{"d-a", "d-b"}
		}
		for _, did := range dids {
			s.Records = append(s.Records, mission.PublicationRecord{Path: pairPath(Association{id, did}), Data: yamlTest(t, mission.Delegation{ID: did, Mission: id, Tier: mission.TierB, AgentType: did, CreatedAt: stamp, Verdict: "pass"})})
		}
		row, err := json.Marshal(mission.TraceSummary{ID: id, Status: c.Status, ClosedAt: c.ClosedAt, RoundsUsed: c.CurrentRound})
		require.NoError(t, err)
		s.Summary = append(s.Summary, append(row, '\n')...)
	}
	require.NoError(t, mission.ValidatePublicationSnapshot(s))
	return s
}

func repositoryTest(t *testing.T) (Publisher, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	r := Repository{Root: t.TempDir(), Env: gitenv.Parsed()}
	gitTest(t, r, "init", "-b", "main")
	gitTest(t, r, "config", "user.name", "Fixture")
	gitTest(t, r, "config", "user.email", "fixture@example.invalid")
	writeTest(t, r.Root, "source.txt", []byte("base one\nbase two\nbase three\nbase four\n"))
	gitTest(t, r, "add", "source.txt")
	gitTest(t, r, "commit", "-m", "base")
	base := gitTest(t, r, "rev-parse", "HEAD")
	gitTest(t, r, "checkout", "-b", "feature")
	contents := []string{"known A\nbase two\nbase three\nbase four\n", "known A\nknown B\nbase three\nbase four\n", "known A\nknown B\nambiguous\nbase four\n"}
	messages := []string{"A\n\nMission: " + missionA + "\nDelegation: d-a", "B\n\nMission: " + missionB + "\nDelegation: d-c", "ambiguous\n\nMission: " + missionA + "\nDelegation: d-a\nDelegation: d-b"}
	for i := range contents {
		writeTest(t, r.Root, "source.txt", []byte(contents[i]))
		gitTest(t, r, "add", "source.txt")
		gitTest(t, r, "commit", "-m", messages[i])
	}
	policy, err := layout.NewRepository(r.Root, r.Root, layout.RecordBlame)
	require.NoError(t, err)
	return Publisher{Policy: policy, Repository: r}, base
}

func indexBytesTest(t *testing.T, r Repository) (string, []byte) {
	t.Helper()
	index := gitTest(t, r, "rev-parse", "--path-format=absolute", "--git-path", "index")
	data, err := os.ReadFile(index)
	require.NoError(t, err)
	return index, data
}

func TestPublicationPreservesIndependentSummariesAndActualIndex(t *testing.T) {
	for _, alternate := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "alternate"}[alternate], func(t *testing.T) {
			p, base := repositoryTest(t)
			r := p.Repository
			_, normal := indexBytesTest(t, r)
			if alternate {
				index := filepath.Join(t.TempDir(), "committing-index")
				require.NoError(t, os.WriteFile(index, normal, 0o600))
				t.Setenv("GIT_INDEX_FILE", index)
				var err error
				r.Env, err = gitenv.ActualIndexEnv()
				require.NoError(t, err)
				p.Repository = r
			}
			writeTest(t, r.Root, "unrelated.txt", []byte("staged\n"))
			gitTest(t, r, "add", "unrelated.txt")
			staged := gitTest(t, r, "rev-parse", ":unrelated.txt")
			writeTest(t, r.Root, "unrelated.txt", []byte("unstaged\n"))
			indexedRow := []byte(" {\"id\":\"m-2026-11-04-090\", \"future\":true, \"write_set\":[\"app/home/page.tsx\",\"src/private@example.invalid.go\",\"Bash(gh auth token:*)\"]} \n")
			workRow := []byte("\n{\"id\":\"m-2026-11-04-091\",\"only_work\":true,\"files_changed\":[\"app/Users/page.tsx\"]}\n")
			writeTest(t, r.Root, SummaryPath, indexedRow)
			gitTest(t, r, "add", SummaryPath)
			writeTest(t, r.Root, SummaryPath, workRow)
			receipt, err := p.Publish(context.Background(), selectedTest(t), base)
			require.NoError(t, err)
			assert.True(t, receipt.IndexInstalled)
			assert.Equal(t, directoryDurability, receipt.Durability)
			assert.Equal(t, staged, gitTest(t, r, "rev-parse", ":unrelated.txt"))
			data, err := os.ReadFile(filepath.Join(r.Root, "unrelated.txt"))
			require.NoError(t, err)
			assert.Equal(t, "unstaged\n", string(data))
			data, err = r.read(context.Background(), ":", SummaryPath)
			require.NoError(t, err)
			assert.True(t, bytes.HasPrefix(data, indexedRow), "unrelated indexed bytes retained")
			assert.NotContains(t, string(data), "only_work")
			data, err = os.ReadFile(filepath.Join(r.Root, filepath.FromSlash(SummaryPath)))
			require.NoError(t, err)
			assert.True(t, bytes.HasPrefix(data, workRow), "unrelated checkout bytes retained")
			require.NoError(t, r.VerifyIndex(context.Background(), []string{receipt.Manifest}))
			if alternate {
				_, actualNormal := indexBytesTest(t, Repository{Root: r.Root, Env: gitenv.Parsed()})
				assert.Equal(t, normal, actualNormal, "ordinary index untouched")
			}
			entries, err := os.ReadDir(p.Policy.TempDir())
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}

func TestPublicationLinkedCheckoutKeepsLocalStateAtStore(t *testing.T) {
	p, base := repositoryTest(t)
	store := p.Repository
	storeIndex, storeBefore := indexBytesTest(t, store)
	checkout := t.TempDir()
	gitTest(t, store, "worktree", "add", "--detach", checkout, "HEAD")
	r := Repository{Root: checkout, Env: store.Env}
	var err error
	p.Policy, err = layout.NewRepository(store.Root, checkout, layout.RecordBlame)
	require.NoError(t, err)
	p.Repository = r
	checkoutIndex, _ := indexBytesTest(t, r)
	require.NotEqual(t, storeIndex, checkoutIndex)
	summaryLock, err := p.Policy.LockPath("mission-summary", "missions")
	require.NoError(t, err)
	checkoutLocal := filepath.Join(checkout, ".punt-labs", "local", "ethos")
	_, err = os.Lstat(checkoutLocal)
	require.ErrorIs(t, err, os.ErrNotExist)
	p.beforeLock = func() error {
		assert.FileExists(t, summaryLock, "summary lock belongs to S")
		entries, err := os.ReadDir(p.Policy.TempDir())
		if err != nil {
			return err
		}
		require.Len(t, entries, 1, "candidate staging belongs to S")
		assert.FileExists(t, filepath.Join(p.Policy.TempDir(), entries[0].Name(), "index"))
		return nil
	}

	receipt, err := p.Publish(t.Context(), selectedTest(t), base)
	require.NoError(t, err)
	assert.True(t, receipt.IndexInstalled)
	assert.Equal(t, checkoutIndex, receipt.Index)
	require.NoError(t, r.VerifyIndex(t.Context(), []string{receipt.Manifest}))
	require.NotEmpty(t, receipt.Copied)
	assert.Contains(t, receipt.Copied, receipt.Manifest)
	assert.Contains(t, receipt.Copied, SummaryPath)
	for _, name := range receipt.Copied {
		data, err := os.ReadFile(filepath.Join(checkout, filepath.FromSlash(name)))
		require.NoError(t, err)
		indexed, err := r.read(t.Context(), ":", name)
		require.NoError(t, err)
		assert.Equal(t, data, indexed, "publication installed in C and its actual index: %s", name)
		assert.NoFileExists(t, filepath.Join(store.Root, filepath.FromSlash(name)))
	}
	storeAfter, err := os.ReadFile(storeIndex)
	require.NoError(t, err)
	assert.Equal(t, storeBefore, storeAfter, "S index is untouched")
	assert.FileExists(t, summaryLock)
	_, err = os.Lstat(checkoutLocal)
	assert.ErrorIs(t, err, os.ErrNotExist, "publication must not create C-local state or locks")
	entries, err := os.ReadDir(p.Policy.TempDir())
	require.NoError(t, err)
	assert.Empty(t, entries, "S staging is cleaned after installation")
}

func TestPublicationFailureBoundaries(t *testing.T) {
	for _, kind := range []string{"lock", "cas-before", "cas-after-copy", "copy", "cancel", "head", "exdev", "checkout-race"} {
		t.Run(kind, func(t *testing.T) {
			p, base := repositoryTest(t)
			r := p.Repository
			index, original := indexBytesTest(t, r)
			var newer []byte
			if strings.HasPrefix(kind, "cas-") {
				other := filepath.Join(t.TempDir(), "other-index")
				require.NoError(t, os.WriteFile(other, original, 0o600))
				otherRepo := Repository{Root: r.Root, Env: gitenv.WithIndex(r.Env, other)}
				writeTest(t, r.Root, "another-writer", []byte("do not erase\n"))
				gitTest(t, otherRepo, "add", "another-writer")
				_, newer = indexBytesTest(t, otherRepo)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "lock":
				require.NoError(t, os.WriteFile(index+".lock", []byte("other lock owner"), 0o600))
			case "cas-before":
				p.beforeLock = func() error { return os.WriteFile(index, newer, 0o600) }
			case "cas-after-copy":
				p.afterCopy = func(string) error { return os.WriteFile(index, newer, 0o600) }
			case "copy":
				p.afterCopy = func(string) error { return errors.New("injected interruption") }
			case "cancel":
				p.afterCopy = func(string) error { cancel(); return nil }
			case "head":
				p.beforeLock = func() error { _, err := r.git(ctx, "update-ref", "HEAD", base); return err }
			case "exdev":
				p.renameRecord = func(string, string) error { return syscall.EXDEV }
			case "checkout-race":
				p.beforeLock = func() error {
					writeTest(t, r.Root, pairPath(Association{missionA, "d-a"}), []byte("other checkout writer"))
					return nil
				}
			}
			receipt, err := p.Publish(ctx, selectedTest(t), base)
			require.Error(t, err)
			assert.False(t, receipt.IndexInstalled)
			actual, err := os.ReadFile(index)
			require.NoError(t, err)
			if len(newer) > 0 {
				assert.Equal(t, newer, actual)
			} else {
				assert.Equal(t, original, actual)
			}
			if kind == "copy" || kind == "cancel" || kind == "cas-after-copy" {
				require.NotEmpty(t, receipt.Copied)
				for _, name := range receipt.Copied {
					assert.FileExists(t, filepath.Join(r.Root, filepath.FromSlash(name)))
				}
			}
			if kind == "copy" {
				p.afterCopy = nil
				retry, err := p.Publish(context.Background(), selectedTest(t), base)
				require.NoError(t, err)
				assert.True(t, retry.IndexInstalled)
				assert.Equal(t, receipt.Manifest, retry.Manifest)
			}
			if kind == "lock" {
				data, err := os.ReadFile(index + ".lock")
				require.NoError(t, err)
				assert.Equal(t, "other lock owner", string(data))
			} else {
				assert.NoFileExists(t, index+".lock")
			}
		})
	}
}

func TestPublicationRefusesUnsafeUnrelatedSummaryWithoutChangingTargets(t *testing.T) {
	tests := []struct {
		name   string
		fields string
	}{
		{"home", `"title":"/home/private-user/work"`},
		{"email", `"title":"private@example.invalid"`},
		{"credential", `"title":"ghp_abcdefghijklmnopqrstuvwxyz1234567890ABCD"`},
		{"escaped-home", `"title":"\u002fhome\u002fprivate-user\u002fwork"`},
		{"unsafe-path-field", `"write_set":["/home/private-user/work"]`},
		{"escaped-email", `"title":"private\u0040example.invalid"`},
		{"unknown-nested-secret", `"future":{"password":"private-value"}`},
		{"escaped-key", `"future":{"private\u0040example.invalid":true}`},
		{"invalid-utf8", "\"title\":\"invalid-\xff\""},
		{"lone-surrogate", `"title":"\ud800"`},
	}
	for _, target := range []string{"checkout", "index"} {
		for _, tt := range tests {
			t.Run(target+"/"+tt.name, func(t *testing.T) {
				p, base := repositoryTest(t)
				r := p.Repository
				unsafe := []byte(" {\"id\":\"m-2026-11-04-090\", " + tt.fields + "} \n")
				indexed := []byte(" {\"id\":\"m-2026-11-04-090\", \"future\":true} \n")
				work := []byte("\n{\"id\":\"m-2026-11-04-091\",\"only_work\":true}\n")
				if target == "index" {
					indexed = unsafe
				} else {
					work = unsafe
				}
				writeTest(t, r.Root, SummaryPath, indexed)
				gitTest(t, r, "add", SummaryPath)
				writeTest(t, r.Root, SummaryPath, work)
				index, before := indexBytesTest(t, r)
				snapshot := selectedTest(t)
				receipt, err := p.Publish(t.Context(), snapshot, base)
				require.ErrorContains(t, err, "merged "+target+" summary")
				assert.NotContains(t, err.Error(), "private-user")
				assert.NotContains(t, err.Error(), "private@example.invalid")
				assert.NotContains(t, err.Error(), "private-value")
				assert.NotContains(t, err.Error(), "ghp_")
				assert.False(t, receipt.IndexInstalled)
				assert.Empty(t, receipt.Copied)
				after, err := os.ReadFile(index)
				require.NoError(t, err)
				assert.Equal(t, before, after, "actual index remains byte-for-byte unchanged")
				after, err = r.read(t.Context(), ":", SummaryPath)
				require.NoError(t, err)
				assert.Equal(t, indexed, after, "unrelated indexed row is not rewritten")
				after, err = os.ReadFile(filepath.Join(r.Root, filepath.FromSlash(SummaryPath)))
				require.NoError(t, err)
				assert.Equal(t, work, after, "unrelated checkout row is not rewritten")
				for _, record := range snapshot.Records {
					assert.NoFileExists(t, filepath.Join(r.Root, filepath.FromSlash(record.Path)))
				}
				assert.NoFileExists(t, filepath.Join(r.Root, filepath.FromSlash(receipt.Manifest)))
				assert.NoFileExists(t, index+".lock")
				entries, err := os.ReadDir(p.Policy.TempDir())
				require.NoError(t, err)
				assert.Empty(t, entries, "private candidate index is removed")
			})
		}
	}
}

func TestPublicationSummaryCaptureIsCASBaseline(t *testing.T) {
	p, base := repositoryTest(t) // S = C: the summary is also shared authority.
	rowA := []byte("{\"id\":\"m-2026-11-04-090\"}\n")
	rowB := []byte("{\"id\":\"m-2026-11-04-091\"}\n")
	writeTest(t, p.Repository.Root, SummaryPath, rowA)
	p.afterSummaryCapture = func() error {
		// Reproduce the old window exactly: B appears after the merge's read
		// but before the path-loop's formerly independent CAS-baseline read.
		// This writer deliberately bypasses the lock to exercise the CAS too.
		return os.WriteFile(filepath.Join(p.Repository.Root, filepath.FromSlash(SummaryPath)), append(bytes.Clone(rowA), rowB...), 0o600)
	}
	receipt, err := p.Publish(t.Context(), selectedTest(t), base)
	require.ErrorContains(t, err, "selected checkout target changed during publication: "+SummaryPath)
	assert.False(t, receipt.IndexInstalled)
	data, err := os.ReadFile(filepath.Join(p.Repository.Root, filepath.FromSlash(SummaryPath)))
	require.NoError(t, err)
	assert.Equal(t, append(bytes.Clone(rowA), rowB...), data, "publication must not erase B")
	p.afterSummaryCapture = nil
	receipt, err = p.Publish(t.Context(), selectedTest(t), base)
	require.NoError(t, err)
	assert.True(t, receipt.IndexInstalled)
	data, err = os.ReadFile(filepath.Join(p.Repository.Root, filepath.FromSlash(SummaryPath)))
	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(data, append(bytes.Clone(rowA), rowB...)))
}

func TestPublicationSummaryWriterWaitsThroughInstallation(t *testing.T) {
	p, base := repositoryTest(t)
	rowB := []byte("{\"id\":\"m-2026-11-04-091\"}\n")
	attempting := make(chan struct{})
	acquired := make(chan struct{})
	done := make(chan error, 1)
	p.afterSummaryCapture = func() error {
		go func() {
			close(attempting)
			done <- mission.WithSummaryLock(p.Repository.Root, func() error {
				close(acquired)
				f, err := os.OpenFile(filepath.Join(p.Repository.Root, filepath.FromSlash(SummaryPath)), os.O_APPEND|os.O_WRONLY, 0o600)
				if err != nil {
					return err
				}
				_, writeErr := f.Write(rowB)
				return errors.Join(writeErr, f.Close())
			})
		}()
		<-attempting
		select {
		case <-acquired:
			return errors.New("summary writer acquired lock before publication installed")
		case <-time.After(40 * time.Millisecond):
			return nil
		}
	}
	receipt, err := p.Publish(t.Context(), selectedTest(t), base)
	require.NoError(t, err)
	assert.True(t, receipt.IndexInstalled)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("summary writer did not resume after publication released its lock")
	}
	data, err := os.ReadFile(filepath.Join(p.Repository.Root, filepath.FromSlash(SummaryPath)))
	require.NoError(t, err)
	assert.True(t, bytes.HasSuffix(data, rowB))
	require.NoError(t, p.Repository.VerifyIndex(t.Context(), []string{receipt.Manifest}))
}

func TestPublicationRejectsDivergentSelectedTargets(t *testing.T) {
	for _, kind := range []string{"work", "index", "source", "summary", "symlink", "parent-symlink"} {
		t.Run(kind, func(t *testing.T) {
			p, base := repositoryTest(t)
			r := p.Repository
			s := selectedTest(t)
			name := s.Records[0].Path
			switch kind {
			case "work", "index":
				writeTest(t, r.Root, name, []byte("divergent\n"))
				if kind == "index" {
					gitTest(t, r, "add", name)
					require.NoError(t, os.Remove(filepath.Join(r.Root, filepath.FromSlash(name))))
				}
			case "source":
				writeTest(t, r.Root, "source.txt", []byte("staged after source history\n"))
				gitTest(t, r, "add", "source.txt")
			case "summary":
				writeTest(t, r.Root, SummaryPath, []byte("{\"id\":\""+missionA+"\",\"conflict\":true}\n"))
			case "symlink":
				writeTest(t, r.Root, name, []byte("old\n"))
				require.NoError(t, os.Remove(filepath.Join(r.Root, filepath.FromSlash(name))))
				require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "absent"), filepath.Join(r.Root, filepath.FromSlash(name))))
			case "parent-symlink":
				require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(r.Root, ".punt-labs")))
			}
			index, original := indexBytesTest(t, r)
			receipt, err := p.Publish(context.Background(), s, base)
			require.Error(t, err)
			assert.False(t, receipt.IndexInstalled)
			assert.Empty(t, receipt.Copied)
			actual, err := os.ReadFile(index)
			require.NoError(t, err)
			assert.Equal(t, original, actual)
		})
	}
}

func TestPublicationSplitIndex(t *testing.T) {
	p, base := repositoryTest(t)
	gitTest(t, p.Repository, "update-index", "--split-index")
	receipt, err := p.Publish(context.Background(), selectedTest(t), base)
	require.NoError(t, err)
	assert.True(t, receipt.IndexInstalled)
	require.NoError(t, p.Repository.VerifyIndex(context.Background(), []string{receipt.Manifest}))
}

func TestPublicationRefusesUnsafeBranchTrailerEvidence(t *testing.T) {
	for _, trailers := range []string{
		"Mission: private@example.invalid\nDelegation: d-a",
		"Mission: " + missionA + "\nDelegation: /home/private-user/work",
	} {
		t.Run(strings.Split(trailers, "\n")[0], func(t *testing.T) {
			p, base := repositoryTest(t)
			gitTest(t, p.Repository, "commit", "--amend", "-m", "unsafe evidence\n\n"+trailers)
			index, before := indexBytesTest(t, p.Repository)
			receipt, err := p.Publish(t.Context(), selectedTest(t), base)
			require.ErrorContains(t, err, "unsafe branch trailer evidence")
			assert.NotContains(t, err.Error(), "private@example.invalid")
			assert.NotContains(t, err.Error(), "/home/private-user/work")
			assert.False(t, receipt.IndexInstalled)
			assert.Empty(t, receipt.Copied)
			after, err := os.ReadFile(index)
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

func TestPublicationDirectorySyncFailureReceipts(t *testing.T) {
	for _, target := range []string{"checkout", "index"} {
		t.Run(target, func(t *testing.T) {
			p, base := repositoryTest(t)
			index, original := indexBytesTest(t, p.Repository)
			injected := errors.New("injected directory sync failure")
			p.syncDir = func(dir string) error {
				if target == "checkout" || dir == filepath.Dir(index) {
					return injected
				}
				return syncDirectory(dir)
			}
			receipt, err := p.Publish(t.Context(), selectedTest(t), base)
			require.ErrorIs(t, err, injected)
			require.NotEmpty(t, receipt.Copied, "post-rename failure must report installed checkout copies")
			for _, name := range receipt.Copied {
				assert.FileExists(t, filepath.Join(p.Repository.Root, filepath.FromSlash(name)))
			}
			actual, err := os.ReadFile(index)
			require.NoError(t, err)
			if target == "checkout" {
				assert.False(t, receipt.IndexInstalled)
				assert.Equal(t, original, actual)
			} else {
				assert.True(t, receipt.IndexInstalled)
				assert.Equal(t, "unconfirmed", receipt.Durability)
				assert.NotEqual(t, original, actual)
				require.NoError(t, p.Repository.VerifyIndex(t.Context(), []string{receipt.Manifest}))
			}
			assert.NoFileExists(t, index+".lock")
		})
	}
}
