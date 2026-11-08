package ui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/provenance"
)

func TestBrowseSignedManifestCommit(t *testing.T) {
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("SSH commit signing requires ssh-keygen")
	}
	root := browseRepository(t)
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("SSH_ASKPASS_REQUIRE", "never")
	key := filepath.Join(t.TempDir(), "signing-key")
	cmd := exec.CommandContext(t.Context(), keygen, "-q", "-t", "ed25519", "-N", "", "-C", "fixture@example.invalid", "-f", key)
	cmd.Env = gitenv.Parsed()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	public, err := os.ReadFile(key + ".pub")
	require.NoError(t, err)
	allowed := filepath.Join(t.TempDir(), "allowed-signers")
	require.NoError(t, os.WriteFile(allowed, append([]byte("fixture@example.invalid "), public...), 0o600))
	for _, setting := range [][2]string{
		{"gpg.format", "ssh"},
		{"gpg.ssh.program", keygen},
		{"user.signingkey", key},
		{"gpg.ssh.allowedSignersFile", allowed},
		{"log.showSignature", "true"},
	} {
		browseGit(t, root, "config", setting[0], setting[1])
	}
	browseGit(t, root, "commit", "--allow-empty", "-m", "base")
	base := browseGit(t, root, "rev-parse", "HEAD")
	browseGit(t, root, "checkout", "-b", "signed-feature")
	browseWrite(t, root, "signed.txt", []byte("signed source\n"))
	browseGit(t, root, "add", "signed.txt")
	browseGit(t, root, "commit", "-m", "branch source")
	head := browseGit(t, root, "rev-parse", "HEAD")
	blob := browseGit(t, root, "rev-parse", "HEAD:signed.txt")
	const id = "m-2026-11-05-001"
	const stamp = "2026-11-05T10:00:00Z"
	pair := provenance.Association{Mission: id, Delegation: "d-signed"}
	prefix := ".punt-labs/ethos/missions/" + id + "/"
	c := mission.Contract{MissionID: id, Status: mission.StatusOpen, CreatedAt: stamp, UpdatedAt: stamp, Leader: "lead", Worker: "bwk", Evaluator: mission.Evaluator{Handle: "rsc", PinnedAt: stamp}, WriteSet: []string{"signed.txt"}, SuccessCriteria: []string{"checked"}, Budget: mission.Budget{Rounds: 1}, CurrentRound: 1, RoundDecisions: &mission.RoundDecisions{Version: 1, Entries: []mission.RoundDecision{}}}
	contract, err := yaml.Marshal(c)
	require.NoError(t, err)
	delegation, err := yaml.Marshal(mission.Delegation{ID: pair.Delegation, Mission: id, Tier: mission.TierB, AgentType: "bwk", CreatedAt: stamp, Verdict: "pass"})
	require.NoError(t, err)
	records := map[string][]byte{
		prefix + "contract.yaml":                                   contract,
		prefix + "corrections.yaml":                                []byte("version: 1\nmission: " + id + "\nentries: []\n"),
		prefix + "delegations/" + pair.Delegation + "/record.yaml": delegation,
	}
	m := provenance.Manifest{SchemaVersion: 1, BranchHead: head, Base: base, Missions: []string{id}, Associations: []provenance.Association{pair}}
	for name, data := range records {
		browseWrite(t, root, name, data)
		m.Records = append(m.Records, provenance.Record{Path: name, SHA256: provenance.Digest(data)})
	}
	m.Sources = []provenance.Source{{Path: "signed.txt", Blob: blob, Ranges: []provenance.Range{{
		Start: 1, End: 1, Candidates: []provenance.Association{pair},
		Evidence: provenance.Evidence{Commit: head, Path: "signed.txt", Line: 1, Missions: []string{id}, Delegations: []string{pair.Delegation}},
	}}}}
	name, data, err := provenance.Encode(m)
	require.NoError(t, err)
	browseWrite(t, root, name, data)
	browseGit(t, root, "add", ".punt-labs")
	browseGit(t, root, "commit", "-S", "-m", "signed publication\n\nEthos-Provenance: "+name)
	browseGit(t, root, "verify-commit", "HEAD")
	browseGit(t, root, "checkout", "main")
	browseGit(t, root, "merge", "--squash", "signed-feature")
	// The actual local squash introduces source and publication together.
	// No Mission/Delegation trailers can mask a lost manifest reference.
	browseGit(t, root, "commit", "-S", "-m", "signed squash\n\nEthos-Provenance: "+name)
	commit := browseGit(t, root, "rev-parse", "HEAD")
	browseGit(t, root, "verify-commit", commit)
	require.Contains(t, browseGit(t, root, "log", "-1", "--format=%s", commit), "Good \"git\" signature")
	r := provenance.Repository{Root: root, Env: gitenv.Parsed()}
	_, err = r.VerifiedManifest(t.Context(), commit, name)
	require.NoError(t, err)
	s, err := NewServer(root, t.TempDir())
	require.NoError(t, err)
	lines, notices := s.gitBlameWithTrailers("signed.txt")
	require.Len(t, lines, 1)
	assert.Empty(t, notices)
	assert.Equal(t, commit, lines[0].Commit)
	assert.Equal(t, []provenance.Association{pair}, lines[0].Candidates)
	assert.Equal(t, id, lines[0].Mission)
	assert.Equal(t, pair.Delegation, lines[0].Delegation)
	assert.Equal(t, "bwk", lines[0].Agent)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/browse/signed.txt", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), id)
	assert.Contains(t, rec.Body.String(), pair.Delegation)
	assert.Contains(t, rec.Body.String(), "bwk")
	assert.NotContains(t, rec.Body.String(), "ethos-notice")
	assert.Equal(t, "true", browseGit(t, root, "config", "--get", "log.showSignature"))
	browseGit(t, root, "verify-commit", commit)
	assert.Equal(t, commit, browseGit(t, root, "rev-parse", "HEAD"), "read-only UI must preserve the signed commit")
}

func TestBrowseEmptyTrailersAreNotMalformed(t *testing.T) {
	root := browseRepository(t)
	browseWrite(t, root, "plain.txt", []byte("plain source\n"))
	browseGit(t, root, "add", "plain.txt")
	browseGit(t, root, "commit", "-m", "no recorded attribution")
	s := &Server{repoRoot: root, storeRoot: t.TempDir()}
	lines, notices := s.gitBlameWithTrailers("plain.txt")
	require.Len(t, lines, 1)
	assert.Empty(t, notices, "three empty fields are a valid absence of trailers")
	assert.Empty(t, lines[0].Mission)
	assert.Empty(t, lines[0].Delegation)
	assert.Empty(t, lines[0].Agent)
}

func TestBrowseRejectsMalformedTrailerFraming(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the injected Git executable is a POSIX shell fixture")
	}
	for _, tc := range []struct {
		name, response string
	}{
		{"empty output", ""},
		{"truncated fields", "\n\n"},
		{"missing terminator", "\n\nmanifest"},
		{"extra field", "\n\n\nunexpected\n"},
		{"signature prefix", "Good signature\n\n\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := browseRepository(t)
			browseWrite(t, root, "a.txt", []byte("visible source\n"))
			browseGit(t, root, "add", "a.txt")
			browseGit(t, root, "commit", "-m", "source")
			git, err := exec.LookPath("git")
			require.NoError(t, err)
			response := filepath.Join(t.TempDir(), "response")
			require.NoError(t, os.WriteFile(response, []byte(tc.response), 0o600))
			bin := t.TempDir()
			// Delegate real blame to Git; corrupt only the machine trailer
			// response. Arguments are passed unchanged to the real executable.
			script := fmt.Sprintf("#!/bin/sh\nwhile [ \"$1\" = -c ]; do shift 2; done\nif [ \"$1\" = log ]; then /bin/cat '%s'; else exec '%s' \"$@\"; fi\n", strings.ReplaceAll(response, "'", "'\\''"), strings.ReplaceAll(git, "'", "'\\''"))
			require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755))
			t.Setenv("PATH", bin)
			s, err := NewServer(root, t.TempDir())
			require.NoError(t, err)
			lines, notices := s.gitBlameWithTrailers("a.txt")
			require.Len(t, lines, 1)
			assert.Empty(t, lines[0].Mission)
			assert.Empty(t, lines[0].Delegation)
			assert.Empty(t, lines[0].Agent)
			assert.Contains(t, strings.Join(notices, "\n"), "malformed git trailer response")
			rec := httptest.NewRecorder()
			s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/browse/a.txt", nil))
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Body.String(), "visible source")
			assert.Contains(t, rec.Body.String(), "malformed git trailer response")
		})
	}
}
