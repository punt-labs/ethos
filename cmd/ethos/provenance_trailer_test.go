//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/provenance"
)

func TestProvenanceCLIInstalledHookConfiguredTrailersSquashClone(t *testing.T) {
	for _, aliases := range []bool{false, true} {
		t.Run(map[bool]string{false: "separator", true: "separator and aliases"}[aliases], func(t *testing.T) {
			se, base, id := publicationFixtureCLI(t)
			common := publicationGitCLI(t, se, "rev-parse", "--path-format=absolute", "--git-common-dir")
			store := filepath.Dir(common)
			did := "d-2026-11-04-001"
			delegations := filepath.Join(store, ".punt-labs", "ethos", "missions", id, "delegations")
			require.NoError(t, os.Rename(filepath.Join(delegations, "d-one"), filepath.Join(delegations, did)))
			record := filepath.Join(delegations, did, "record.yaml")
			data, err := os.ReadFile(record)
			require.NoError(t, err)
			var delegation mission.Delegation
			require.NoError(t, yaml.Unmarshal(data, &delegation))
			delegation.ID = did
			data, err = yaml.Marshal(delegation)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(record, data, 0o600))

			bin := t.TempDir()
			require.NoError(t, os.Symlink(ethosBinary, filepath.Join(bin, "ethos")))
			for i, entry := range se.env {
				if strings.HasPrefix(entry, "PATH=") {
					se.env[i] = "PATH=" + bin + string(os.PathListSeparator) + strings.TrimPrefix(entry, "PATH=")
				}
			}
			_, stderr, exit := runCLI(t, se, "enable", "--json")
			require.Equal(t, 0, exit, stderr)
			hook := publicationGitCLI(t, se, "rev-parse", "--path-format=absolute", "--git-path", "hooks/commit-msg")
			data, err = os.ReadFile(hook)
			require.NoError(t, err)
			require.Contains(t, string(data), "hook commit-message")
			publicationGitCLI(t, se, "config", "trailer.separators", "%")
			missionKey, delegationKey := "Mission", "Delegation"
			if aliases {
				missionKey, delegationKey = "Mission-Id", "Delegation-Id"
				publicationGitCLI(t, se, "config", "trailer.mission.key", missionKey+"% ")
				publicationGitCLI(t, se, "config", "trailer.delegation.key", delegationKey+"% ")
			}
			se.env = append(se.env, "MISSION_ID="+id, "DELEGATION_ID="+did)
			require.NoError(t, os.WriteFile(filepath.Join(se.repo, "source.txt"), []byte("source committed through installed hook\n"), 0o600))
			publicationGitCLI(t, se, "add", "source.txt")
			publicationGitCLI(t, se, "commit", "-m", "source through installed hook")
			sourceCommit := publicationGitCLI(t, se, "rev-parse", "HEAD")
			message := publicationGitCLI(t, se, "log", "-1", "--format=%B")
			require.Contains(t, message, missionKey+"% "+id)
			require.Contains(t, message, delegationKey+"% "+did)
			se.env = se.env[:len(se.env)-2]

			stdout, stderr, exit := runCLI(t, se, "mission", "publish", id, "--base", base, "--json")
			require.Equal(t, 0, exit, stderr)
			var output struct {
				Receipt provenance.Receipt `json:"receipt"`
			}
			require.NoError(t, json.Unmarshal([]byte(stdout), &output))
			data, err = os.ReadFile(filepath.Join(se.repo, filepath.FromSlash(output.Receipt.Manifest)))
			require.NoError(t, err)
			manifest, err := provenance.Decode(output.Receipt.Manifest, data)
			require.NoError(t, err)
			require.Len(t, manifest.Sources, 1)
			require.Len(t, manifest.Sources[0].Ranges, 1)
			rng := manifest.Sources[0].Ranges[0]
			pair := provenance.Association{Mission: id, Delegation: did}
			require.Equal(t, []provenance.Association{pair}, rng.Candidates)
			assert.Empty(t, rng.Unknown)
			assert.Equal(t, []string{id}, rng.Evidence.Missions)
			assert.Equal(t, []string{did}, rng.Evidence.Delegations)
			assert.Equal(t, sourceCommit, rng.Evidence.Commit)
			publicationGitCLI(t, se, "commit", "-m", "publication")
			body, stderr, exit := runCLI(t, se, "provenance", "message", "HEAD")
			require.Equal(t, 0, exit, stderr)
			publicationGitCLI(t, se, "checkout", "--detach", base)
			publicationGitCLI(t, se, "merge", "--squash", "feature")
			publicationGitCLI(t, se, "commit", "-m", body)
			merge := publicationGitCLI(t, se, "rev-parse", "HEAD")
			publicationGitCLI(t, se, "branch", "squashed")
			publicationGitCLI(t, se, "branch", "-D", "feature")
			fresh := &cliSubprocessEnv{home: t.TempDir(), repo: filepath.Join(t.TempDir(), "clone")}
			publicationGitCLI(t, se, "clone", "--no-local", "--single-branch", "-b", "squashed", store, fresh.repo)
			fresh.env = []string{"HOME=" + fresh.home, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull}
			_, err = gitenv.OutputEnv(fresh.repo, "git", fresh.env, "cat-file", "-e", sourceCommit)
			require.Error(t, err, "source branch object must be absent in fresh clone")
			_, stderr, exit = runCLI(t, fresh, "provenance", "verify", merge, "--json")
			require.Equal(t, 0, exit, stderr)
			r := provenance.Repository{Root: fresh.repo, Env: fresh.env}
			pairs, err := r.AtLine(t.Context(), merge, "source.txt", 1)
			require.NoError(t, err)
			require.Equal(t, []provenance.Association{pair}, pairs)
			agent, err := r.AgentAt(t.Context(), merge, pairs[0])
			require.NoError(t, err)
			assert.Equal(t, "bwk", agent)
			assert.NoDirExists(t, filepath.Join(fresh.repo, ".punt-labs", "local"))
		})
	}
}
