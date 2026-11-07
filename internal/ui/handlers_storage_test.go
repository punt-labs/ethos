//go:build !windows

package ui

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

func TestHandlersAuditUsesRequestPolicy(t *testing.T) {
	for _, checkoutRecord := range []string{"full", "invalid"} {
		t.Run(checkoutRecord, func(t *testing.T) {
			storeRoot, checkoutRoot := t.TempDir(), t.TempDir()
			p, err := layout.NewRepository(storeRoot, checkoutRoot, layout.RecordBlame)
			require.NoError(t, err)
			id, delegation := "m-2026-11-04-968", "d-ui-policy"
			store := mission.NewRepositoryStore(p, t.TempDir()).WithSessionID("s-ui")
			require.NoError(t, store.Create(uiTestContract(id)))
			_, err = mission.WriteDelegationSkeletonWithPolicy(p, id, delegation, mission.DelegationSkeleton{Tier: mission.TierB, AgentType: "bwk"})
			require.NoError(t, err)
			writeUIFixture(t, filepath.Join(storeRoot, ".punt-labs", "ethos.yaml"), []byte("record: blame\n"))
			writeUIFixture(t, filepath.Join(checkoutRoot, ".punt-labs", "ethos.yaml"), []byte("record: "+checkoutRecord+"\n"))
			input := `{"session_id":"s-ui","delegation_id":"d-ui-policy","tool_name":"Read","tool_input":{"file_path":"canonical-ui-audit.go"}}`
			require.NoError(t, hook.HandleAuditLogWithPolicy(strings.NewReader(input), p, ""))
			stream := audit.Stream{Namespace: audit.SessionNS, Session: "s-ui"}
			data, err := audit.ReadStream(p, stream)
			require.NoError(t, err)
			require.Len(t, data.Lines, 1)
			assert.Contains(t, string(data.Lines[0].Raw), `"record":"blame"`)
			assert.NoDirExists(t, filepath.Join(checkoutRoot, ".punt-labs", "local"))
			srv, err := NewServer(checkoutRoot, storeRoot)
			require.NoError(t, err)
			srv.globalRoot, srv.globalSessionsDir = t.TempDir(), t.TempDir()
			pages := []string{"/missions/" + id, "/delegations/" + id + "/" + delegation}
			for _, source := range []string{"live-only", "archive-only"} {
				if source == "archive-only" {
					sealed, err := hook.SealRepoWithPolicy(p, time.Now(), hook.SealOptions{})
					require.NoError(t, err)
					require.Positive(t, sealed.LinesSealed)
					require.NoError(t, os.Remove(stream.LivePath(p)))
					data, err := audit.ReadStream(p, stream)
					require.NoError(t, err)
					require.Len(t, data.Lines, 1, "fixture must retain the S-only archive without its live tail")
				}
				for _, page := range pages {
					rec := httptest.NewRecorder()
					srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, page, nil))
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					assert.Contains(t, rec.Body.String(), "canonical-ui-audit.go", source+page)
					assert.Contains(t, rec.Body.String(), "Audit Trail (1 calls", source+page)
					assert.NotContains(t, rec.Body.String(), "could not be queried", source+page)
				}
				assert.NoDirExists(t, filepath.Join(checkoutRoot, ".punt-labs", "local"))
				assert.NoDirExists(t, filepath.Join(checkoutRoot, ".punt-labs", "ethos"), "blame audit is not published")
			}
			// A broken canonical stream must still produce a visible query error
			// on both pages, not a clean empty audit section.
			writeUIFixture(t, stream.LivePath(p), []byte("{malformed audit\n"))
			for _, page := range pages {
				rec := httptest.NewRecorder()
				srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, page, nil))
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Contains(t, rec.Body.String(), "audit entries for delegation "+delegation+" could not be queried", page)
				assert.NotContains(t, rec.Body.String(), "canonical-ui-audit.go", page)
			}
		})
	}
}

func TestHandleDelegationPromptSharedRecordOnly(t *testing.T) {
	for _, recorded := range []bool{true, false} {
		t.Run(fmt.Sprint("recorded=", recorded), func(t *testing.T) {
			source, clone := t.TempDir(), t.TempDir()
			p, err := layout.NewRepository(source, source, layout.RecordBlame)
			require.NoError(t, err)
			id, delegation := "m-2026-11-04-969", "d-ui-prompt"
			store := mission.NewRepositoryStore(p, t.TempDir())
			require.NoError(t, store.Create(uiTestContract(id)))
			payload := mission.DelegationSkeleton{Tier: mission.TierB, AgentType: "bwk"}
			if recorded {
				payload.Prompt = []byte("prompt retained only on the originating machine")
				payload.PromptHash = fmt.Sprintf("%x", sha256.Sum256(payload.Prompt))
			}
			_, err = mission.WriteDelegationSkeletonWithPolicy(p, id, delegation, payload)
			require.NoError(t, err)
			if recorded {
				original, err := NewServer(source, source)
				require.NoError(t, err)
				original.globalRoot, original.globalSessionsDir = t.TempDir(), t.TempDir()
				rec := httptest.NewRecorder()
				original.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/delegations/"+id+"/"+delegation, nil))
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				assert.Contains(t, rec.Body.String(), string(payload.Prompt))
				assert.NotContains(t, rec.Body.String(), "unavailable here")
			}
			// Copy only shared blame records. The source's local prompt remains
			// intact but is not available to this filesystem-only clone.
			for _, rel := range []string{"contract.yaml", "corrections.yaml", filepath.Join("delegations", delegation, "record.yaml")} {
				data, err := os.ReadFile(mission.RepoStatePath(source, "missions", id, rel))
				require.NoError(t, err)
				writeUIFixture(t, mission.RepoStatePath(clone, "missions", id, rel), data)
			}
			srv, err := NewServer(clone, clone)
			require.NoError(t, err)
			srv.globalRoot, srv.globalSessionsDir = t.TempDir(), t.TempDir()
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/delegations/"+id+"/"+delegation, nil))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			if recorded {
				assert.Contains(t, rec.Body.String(), "the prompt of "+delegation+" was recorded but detail is unavailable here")
			} else {
				assert.NotContains(t, rec.Body.String(), "unavailable here")
				assert.NotContains(t, rec.Body.String(), "could not be read")
			}
			assert.NotContains(t, rec.Body.String(), "prompt retained only on the originating machine")
			assert.NoDirExists(t, filepath.Join(clone, ".punt-labs", "local"), "readers must not create recovery state")
		})
	}
}

func TestHandleDelegationPromptUnreadableRemainsVisible(t *testing.T) {
	for _, fault := range []string{"directory", "invalid-utf8", "conflicting-copies"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			p, err := layout.NewRepository(root, root, layout.RecordBlame)
			require.NoError(t, err)
			id, delegation := "m-2026-11-04-970", "d-ui-unreadable"
			// No hash is deliberate: only quiet absence is exempt from notices.
			_, err = mission.WriteDelegationSkeletonWithPolicy(p, id, delegation, mission.DelegationSkeleton{Tier: mission.TierB})
			require.NoError(t, err)
			path := filepath.Join(p.LocalRoot(), "trace", "missions", id, "delegations", delegation, "prompt.md")
			switch fault {
			case "directory":
				require.NoError(t, os.MkdirAll(path, 0o700))
			case "invalid-utf8":
				writeUIFixture(t, path, []byte{0xff})
			case "conflicting-copies":
				writeUIFixture(t, path, []byte("retained detail"))
				writeUIFixture(t, filepath.Join(mission.DelegationDir(root, id, delegation), "prompt.md"), []byte("different published detail"))
			}
			srv, err := NewServer(root, root)
			require.NoError(t, err)
			srv.globalRoot, srv.globalSessionsDir = t.TempDir(), t.TempDir()
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/delegations/"+id+"/"+delegation, nil))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "the prompt of "+delegation+" could not be read")
		})
	}
}

func writeUIFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, data, 0o600))
}
