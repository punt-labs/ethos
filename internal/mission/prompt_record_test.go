package mission

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

const promptMission = "m-2026-11-05-901"
const promptDelegation = "d-2026-11-05-901"

func promptTempDirTest(t *testing.T) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return real
}

func promptPolicyTest(t *testing.T, mode layout.RecordMode) layout.Policy {
	t.Helper()
	t.Setenv("HOME", promptTempDirTest(t))
	p, err := layout.NewRepository(promptTempDirTest(t), promptTempDirTest(t), mode)
	require.NoError(t, err)
	return p
}

func promptFileTest(t *testing.T, path string, data []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

func promptBytesTest(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func publishedPromptTest(p layout.Policy) string {
	return filepath.Join(p.CheckoutRecordRoot(), "missions", promptMission, "delegations", promptDelegation, "prompt.md")
}

func TestPromptRecordingCapturesGuardedPayloadAndPreservesExplicitHash(t *testing.T) {
	for _, mode := range []layout.RecordMode{layout.RecordBlame, layout.RecordFull} {
		for _, supplied := range []string{"", "explicit-caller-hash"} {
			t.Run(fmt.Sprintf("mode-%d/hash-%s", mode, supplied), func(t *testing.T) {
				p := promptPolicyTest(t, mode)
				secret := "ghp_abcdefghijklmnopqrstuvwxyz0123456789abcd"
				prompt := []byte("read " + os.Getenv("HOME") + "/notes; private@example.invalid token=" + secret)
				path, err := WriteDelegationSkeletonWithPolicy(p, promptMission, promptDelegation, DelegationSkeleton{
					Tier: TierB, AgentType: "bwk", Prompt: prompt, PromptHash: supplied,
				})
				require.NoError(t, err)
				body := promptBytesTest(t, delegationPromptPath(p, promptMission, promptDelegation))
				assert.NotEqual(t, prompt, body)
				assert.NotContains(t, string(body), os.Getenv("HOME"))
				assert.NotContains(t, string(body), "private@example.invalid")
				assert.NotContains(t, string(body), secret)
				wantDigest := fmt.Sprintf("%x", sha256.Sum256(body))
				consent, err := readPromptConsent(p, promptMission, promptDelegation)
				require.NoError(t, err)
				require.NotNil(t, consent)
				assert.Equal(t, wantDigest, consent.SHA256, "consent never trusts a caller-supplied hash")
				assert.Equal(t, mode == layout.RecordFull, consent.Publish)
				d, err := LoadDelegation(path)
				require.NoError(t, err)
				if supplied == "" {
					assert.Equal(t, wantDigest, d.PromptHash)
				} else {
					assert.Equal(t, supplied, d.PromptHash)
				}
				if mode == layout.RecordFull {
					assert.Equal(t, body, promptBytesTest(t, publishedPromptTest(p)))
				} else {
					assert.NoFileExists(t, publishedPromptTest(p))
				}
				store := NewRepositoryStore(p, promptTempDirTest(t))
				loaded, err := store.LoadDelegationPrompt(promptMission, promptDelegation)
				require.NoError(t, err)
				assert.Equal(t, body, loaded)
				entries, err := os.ReadDir(p.TempDir())
				require.NoError(t, err)
				assert.Empty(t, entries)
				_, err = os.Lstat(filepath.Join(filepath.Dir(filepath.Dir(p.CheckoutRecordRoot())), ".punt-labs", "local", "ethos"))
				assert.ErrorIs(t, err, os.ErrNotExist, "C must not own local prompt state")
			})
		}
	}
}

func TestPromptPublicationFailureRetainsCapturedConsentForBlameRetry(t *testing.T) {
	p := promptPolicyTest(t, layout.RecordFull)
	blocker := filepath.Join(p.CheckoutRecordRoot(), "missions")
	promptFileTest(t, blocker, []byte("unrelated existing file"))
	body := []byte("retained full prompt")
	_, err := WriteDelegationSkeletonWithPolicy(p, promptMission, promptDelegation, DelegationSkeleton{Tier: TierB, Prompt: body})
	require.ErrorContains(t, err, "pending")
	var reserved *DelegationReservedError
	require.ErrorAs(t, err, &reserved)
	assert.Equal(t, []byte("unrelated existing file"), promptBytesTest(t, blocker))
	assert.Equal(t, body, promptBytesTest(t, delegationPromptPath(p, promptMission, promptDelegation)))
	before := promptBytesTest(t, promptConsentPath(p, promptMission, promptDelegation))
	consent, err := readPromptConsent(p, promptMission, promptDelegation)
	require.NoError(t, err)
	require.True(t, consent.Publish)
	assert.NoFileExists(t, filepath.Join(p.RecordRoot(), "missions", promptMission, "delegations", promptDelegation, "record.yaml"))
	require.NoError(t, os.Remove(blocker))
	blame, err := layout.NewRepository(filepath.Dir(filepath.Dir(p.RecordRoot())), filepath.Dir(filepath.Dir(p.CheckoutRecordRoot())), layout.RecordBlame)
	require.NoError(t, err)
	require.NoError(t, RetryPromptPublications(blame))
	assert.Equal(t, body, promptBytesTest(t, publishedPromptTest(p)))
	assert.Equal(t, before, promptBytesTest(t, promptConsentPath(p, promptMission, promptDelegation)))
	require.NoError(t, RetryPromptPublications(blame), "equal publication is an idempotent retry")
}

func TestPromptPrivateEntriesNeverGainConsentFromFullMode(t *testing.T) {
	for _, source := range []string{"captured-blame", "old-local", "old-authority", "old-published"} {
		t.Run(source, func(t *testing.T) {
			p := promptPolicyTest(t, layout.RecordBlame)
			body := []byte("private prompt")
			storeDir := filepath.Join(p.RecordRoot(), "missions", promptMission, "delegations", promptDelegation)
			switch source {
			case "captured-blame":
				_, err := WriteDelegationSkeletonWithPolicy(p, promptMission, promptDelegation, DelegationSkeleton{Tier: TierB, Prompt: body})
				require.NoError(t, err)
			case "old-local":
				promptFileTest(t, delegationPromptPath(p, promptMission, promptDelegation), body)
			case "old-authority":
				promptFileTest(t, filepath.Join(storeDir, "record.yaml"), []byte("id: "+promptDelegation+"\n"))
			case "old-published":
				promptFileTest(t, filepath.Join(storeDir, "prompt.md"), body)
			}
			full, err := layout.NewRepository(filepath.Dir(filepath.Dir(p.RecordRoot())), filepath.Dir(filepath.Dir(p.CheckoutRecordRoot())), layout.RecordFull)
			require.NoError(t, err)
			require.NoError(t, RetryPromptPublications(full))
			assert.NoFileExists(t, publishedPromptTest(p))
			_, err = WriteDelegationSkeletonWithPolicy(full, promptMission, promptDelegation, DelegationSkeleton{Tier: TierB, Prompt: body})
			require.NoError(t, err)
			require.NoError(t, RetryPromptPublications(full))
			assert.NoFileExists(t, publishedPromptTest(p), "an exact later full rewrite is not new consent")
			consent, err := readPromptConsent(p, promptMission, promptDelegation)
			require.NoError(t, err)
			if source == "captured-blame" {
				require.NotNil(t, consent)
				assert.False(t, consent.Publish)
			} else {
				assert.Nil(t, consent, "no consent backfill")
			}
		})
	}
}

func TestPromptConsentPreparationIsRecoverableWithoutChangingMode(t *testing.T) {
	p := promptPolicyTest(t, layout.RecordBlame)
	body := []byte("the exact guarded payload")
	// Model interruption after durable consent installation, before prompt.md.
	consent := promptConsent{Version: 1, Mission: promptMission, Delegation: promptDelegation,
		SHA256: fmt.Sprintf("%x", sha256.Sum256(body)), Publish: true}
	data, err := yaml.Marshal(consent)
	require.NoError(t, err)
	path := promptConsentPath(p, promptMission, promptDelegation)
	require.NoError(t, layout.EnsureLocalDir(p, filepath.Dir(path)))
	data, err = guardWrite(path, data)
	require.NoError(t, err)
	require.NoError(t, withPromptLock(p, promptMission, promptDelegation, func() error {
		return installPromptArtifact(p, path, data)
	}))
	require.ErrorContains(t, RetryPromptPublications(p), "missing")
	assert.NoFileExists(t, publishedPromptTest(p))
	_, err = WriteDelegationSkeletonWithPolicy(p, promptMission, promptDelegation, DelegationSkeleton{Tier: TierB, Prompt: []byte("different payload")})
	require.ErrorContains(t, err, "captured consent")
	assert.Equal(t, data, promptBytesTest(t, path))
	_, err = WriteDelegationSkeletonWithPolicy(p, promptMission, promptDelegation, DelegationSkeleton{Tier: TierB, Prompt: body})
	require.NoError(t, err)
	assert.Equal(t, body, promptBytesTest(t, publishedPromptTest(p)), "recovery retains the originally captured full consent")
	assert.Equal(t, data, promptBytesTest(t, path))
}

func TestPromptSkeletonFailureReportsReservedIdentity(t *testing.T) {
	for _, failure := range []string{"consent-sync", "payload-sync", "record-install", "empty-record-install"} {
		t.Run(failure, func(t *testing.T) {
			p := promptPolicyTest(t, layout.RecordBlame)
			body := []byte("immutable prompt before record")
			recordPath := filepath.Join(p.RecordRoot(), "missions", promptMission, "delegations", promptDelegation, "record.yaml")
			if failure == "empty-record-install" {
				require.NoError(t, os.MkdirAll(recordPath, 0o700))
			}
			if failure == "empty-record-install" {
				body = nil
			}
			injected := errors.New("injected prompt directory sync failure")
			originalSync := syncDir
			t.Cleanup(func() { syncDir = originalSync })
			syncDir = func(dir string) error {
				if dir == filepath.Dir(promptConsentPath(p, promptMission, promptDelegation)) {
					if failure == "consent-sync" {
						return injected
					}
					if failure == "payload-sync" || failure == "record-install" {
						_, err := os.Stat(delegationPromptPath(p, promptMission, promptDelegation))
						if err == nil {
							if failure == "payload-sync" {
								return injected
							}
							// Obstruct record installation only after retention's
							// old-record check, leaving consent and payload intact.
							if err := os.MkdirAll(recordPath, 0o700); err != nil {
								return err
							}
						}
						if err != nil && !errors.Is(err, os.ErrNotExist) {
							return err
						}
					}
				}
				return originalSync(dir)
			}
			_, err := WriteDelegationSkeletonWithPolicy(p, promptMission, promptDelegation, DelegationSkeleton{Tier: TierB, Prompt: body})
			require.Error(t, err)
			var reserved *DelegationReservedError
			require.ErrorAs(t, err, &reserved, "errors after possible persistence must forbid ID recycling")
			if failure == "consent-sync" || failure == "payload-sync" {
				assert.ErrorIs(t, err, injected, "the typed outcome preserves the original persistence error")
			} else {
				assert.Contains(t, err.Error(), "writing delegation record")
			}
			if body != nil {
				assert.FileExists(t, promptConsentPath(p, promptMission, promptDelegation))
				if failure == "consent-sync" {
					assert.NoFileExists(t, delegationPromptPath(p, promptMission, promptDelegation))
				} else {
					assert.Equal(t, body, promptBytesTest(t, delegationPromptPath(p, promptMission, promptDelegation)))
				}
			}
			// Exact recovery remains valid; reservation never authorizes a spawn.
			syncDir = originalSync
			if failure == "record-install" || failure == "empty-record-install" {
				require.NoError(t, os.Remove(recordPath))
			}
			_, err = WriteDelegationSkeletonWithPolicy(p, promptMission, promptDelegation, DelegationSkeleton{Tier: TierB, Prompt: body})
			require.NoError(t, err)
		})
	}
}

func TestPromptConflictsPreserveExistingCopies(t *testing.T) {
	for _, target := range []string{"retained", "published", "legacy"} {
		t.Run(target, func(t *testing.T) {
			p := promptPolicyTest(t, layout.RecordFull)
			body := []byte("original prompt")
			_, err := WriteDelegationSkeletonWithPolicy(p, promptMission, promptDelegation, DelegationSkeleton{Tier: TierB, Prompt: body})
			require.NoError(t, err)
			path := delegationPromptPath(p, promptMission, promptDelegation)
			switch target {
			case "published":
				path = publishedPromptTest(p)
			case "legacy":
				path = filepath.Join(p.RecordRoot(), "missions", promptMission, "delegations", promptDelegation, "prompt.md")
			}
			other := []byte("conflicting other writer")
			promptFileTest(t, path, other)
			receipt := promptBytesTest(t, promptConsentPath(p, promptMission, promptDelegation))
			recordPath := filepath.Join(p.RecordRoot(), "missions", promptMission, "delegations", promptDelegation, "record.yaml")
			record := promptBytesTest(t, recordPath)
			_, err = WriteDelegationSkeletonWithPolicy(p, promptMission, promptDelegation, DelegationSkeleton{Tier: TierB, Prompt: body})
			require.ErrorContains(t, err, "conflict")
			assert.Equal(t, other, promptBytesTest(t, path))
			assert.Equal(t, receipt, promptBytesTest(t, promptConsentPath(p, promptMission, promptDelegation)))
			assert.Equal(t, record, promptBytesTest(t, recordPath))
			require.ErrorContains(t, RetryPromptPublications(p), "conflict")
			assert.Equal(t, other, promptBytesTest(t, path))
		})
	}
}

func TestPromptReaderChecksActualDigestEvenWithExplicitHash(t *testing.T) {
	p := promptPolicyTest(t, layout.RecordBlame)
	_, err := WriteDelegationSkeletonWithPolicy(p, promptMission, promptDelegation, DelegationSkeleton{Tier: TierB, Prompt: []byte("original"), PromptHash: "explicit"})
	require.NoError(t, err)
	promptFileTest(t, delegationPromptPath(p, promptMission, promptDelegation), []byte("changed"))
	s := NewRepositoryStore(p, promptTempDirTest(t))
	_, err = s.LoadDelegationPrompt(promptMission, promptDelegation)
	require.ErrorContains(t, err, "captured payload digest")
}

func TestPromptRetryRefusesMalformedConsent(t *testing.T) {
	for _, mutation := range []string{"duplicate", "unknown", "digest", "mission", "version", "trailing", "utf8"} {
		t.Run(mutation, func(t *testing.T) {
			p := promptPolicyTest(t, layout.RecordFull)
			_, err := WriteDelegationSkeletonWithPolicy(p, promptMission, promptDelegation, DelegationSkeleton{Tier: TierB, Prompt: []byte("prompt")})
			require.NoError(t, err)
			require.NoError(t, os.Remove(publishedPromptTest(p)))
			path := promptConsentPath(p, promptMission, promptDelegation)
			data := promptBytesTest(t, path)
			switch mutation {
			case "duplicate":
				data = append(data, []byte("publish: true\n")...)
			case "unknown":
				data = append(data, []byte("future: true\n")...)
			case "digest":
				data = bytes.ReplaceAll(data, []byte(promptDigest([]byte("prompt"))), []byte("not-a-digest"))
			case "mission":
				data = bytes.ReplaceAll(data, []byte(promptMission), []byte("m-other"))
			case "version":
				data = bytes.ReplaceAll(data, []byte("version: 1"), []byte("version: 2"))
			case "trailing":
				data = append(data, []byte("---\npublish: true\n")...)
			case "utf8":
				data = append(data, 0xff)
			}
			promptFileTest(t, path, data)
			require.ErrorContains(t, RetryPromptPublications(p), "consent")
			assert.Equal(t, data, promptBytesTest(t, path))
			assert.NoFileExists(t, publishedPromptTest(p))
		})
	}
}

func TestPromptRecordingRefusesInvalidUTF8AndEmptyDoesNotClaimPresence(t *testing.T) {
	p := promptPolicyTest(t, layout.RecordFull)
	_, err := WriteDelegationSkeletonWithPolicy(p, promptMission, promptDelegation, DelegationSkeleton{Tier: TierB, Prompt: []byte{0xff}})
	require.ErrorContains(t, err, "UTF-8")
	var reserved *DelegationReservedError
	assert.False(t, errors.As(err, &reserved), "input refusal before persistence permits counter rollback")
	assert.NoFileExists(t, promptConsentPath(p, promptMission, promptDelegation))
	path, err := WriteDelegationSkeletonWithPolicy(p, promptMission, promptDelegation, DelegationSkeleton{Tier: TierB})
	require.NoError(t, err)
	d, err := LoadDelegation(path)
	require.NoError(t, err)
	assert.Empty(t, d.PromptHash)
	assert.NoFileExists(t, delegationPromptPath(p, promptMission, promptDelegation))
	assert.NoFileExists(t, promptConsentPath(p, promptMission, promptDelegation))
	require.Error(t, RetryPromptPublications(layout.Policy{}))
}

func TestPromptConcurrentCapturesShareConsentAndCannotOverwrite(t *testing.T) {
	for _, different := range []bool{false, true} {
		t.Run(fmt.Sprintf("different-%t", different), func(t *testing.T) {
			p := promptPolicyTest(t, layout.RecordBlame)
			full, err := layout.NewRepository(filepath.Dir(filepath.Dir(p.RecordRoot())), filepath.Dir(filepath.Dir(p.CheckoutRecordRoot())), layout.RecordFull)
			require.NoError(t, err)
			start := make(chan struct{})
			done := make(chan error, 2)
			for i, policy := range []layout.Policy{p, full} {
				body := []byte("same prompt")
				if different && i == 1 {
					body = []byte("other prompt")
				}
				go func() {
					<-start
					done <- retainDelegationPrompt(policy, promptMission, promptDelegation, body)
				}()
			}
			close(start)
			errorsSeen := 0
			for range 2 {
				if err := <-done; err != nil {
					require.Contains(t, err.Error(), "conflict")
					errorsSeen++
				}
			}
			if different {
				assert.Equal(t, 1, errorsSeen)
			} else {
				assert.Zero(t, errorsSeen)
			}
			consent, err := readPromptConsent(p, promptMission, promptDelegation)
			require.NoError(t, err)
			require.NotNil(t, consent)
			body := promptBytesTest(t, delegationPromptPath(p, promptMission, promptDelegation))
			assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256(body)), consent.SHA256)
			if consent.Publish {
				assert.Equal(t, body, promptBytesTest(t, publishedPromptTest(p)))
			} else {
				assert.NoFileExists(t, publishedPromptTest(p))
			}
		})
	}
}

func TestPromptRetryAndReadRefuseRedirectedCopies(t *testing.T) {
	for _, target := range []string{"consent", "retained-parent", "published-parent"} {
		t.Run(target, func(t *testing.T) {
			p := promptPolicyTest(t, layout.RecordFull)
			_, err := WriteDelegationSkeletonWithPolicy(p, promptMission, promptDelegation, DelegationSkeleton{Tier: TierB, Prompt: []byte("prompt")})
			require.NoError(t, err)
			path := promptConsentPath(p, promptMission, promptDelegation)
			var dest string
			if target == "consent" {
				dest = filepath.Join(promptTempDirTest(t), "consent.yaml")
				require.NoError(t, os.Rename(path, dest))
			} else {
				path = filepath.Dir(delegationPromptPath(p, promptMission, promptDelegation))
				if target == "published-parent" {
					path = filepath.Dir(publishedPromptTest(p))
				}
				dest = filepath.Join(promptTempDirTest(t), "moved")
				require.NoError(t, os.Rename(path, dest))
			}
			if err := os.Symlink(dest, path); err != nil {
				if errors.Is(err, os.ErrPermission) {
					t.Skip("symlinks unavailable on this platform")
				}
				require.NoError(t, err)
			}
			require.ErrorContains(t, RetryPromptPublications(p), "symlink")
			s := NewRepositoryStore(p, promptTempDirTest(t))
			_, err = s.LoadDelegationPrompt(promptMission, promptDelegation)
			require.ErrorContains(t, err, "symlink")
			entries, err := os.ReadDir(p.TempDir())
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}

func TestPromptNoClobberInstallationPreservesExistingArtifact(t *testing.T) {
	p := promptPolicyTest(t, layout.RecordFull)
	path := publishedPromptTest(p)
	promptFileTest(t, path, []byte("editor contents"))
	require.ErrorContains(t, installPromptArtifact(p, path, []byte("replacement")), "conflict")
	assert.Equal(t, "editor contents", string(promptBytesTest(t, path)))
	// Reject a directory instead of replacing it with a regular file.
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Mkdir(path, 0o700))
	require.ErrorContains(t, installPromptArtifact(p, path, []byte("replacement")), "nonregular")
	assert.DirExists(t, path)
}
