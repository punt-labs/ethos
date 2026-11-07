package provenance

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
)

func TestProvenanceSummaryValidationAndBytePreservation(t *testing.T) {
	for _, data := range []string{"{}", "null", "{", "{\"id\":\"../escape\"}", "{\"id\":\"m-a\",\"x\":1}\n{\"id\":\"m-a\",\"x\":2}", "{\"id\":\"m-a\",\"id\":\"m-b\"}"} {
		_, _, err := SummaryRows([]byte(data))
		require.Error(t, err, data)
	}
	existing := []byte(" \n { \"extra\": 7, \"id\": \"m-old\" }  \n")
	selected := []byte("{\"id\":\"m-new\"}\n")
	merged, err := MergeSummary(existing, selected)
	require.NoError(t, err)
	assert.Equal(t, string(existing)+string(selected), string(merged))
	_, err = MergeSummary(existing, []byte("{\"id\":\"m-old\",\"extra\":8}"))
	require.Error(t, err)
}

func TestProvenanceDoesNotGuessUnpublishedCandidatesOrParallelPairs(t *testing.T) {
	pairs := []Association{{missionA, "d-a"}, {missionA, "d-b"}, {missionB, "d-c"}}
	assert.Nil(t, associations([]string{missionA, missionB}, []string{"d-a", "d-c"}, pairs))
	assert.Nil(t, associations([]string{missionA}, []string{"d-a", "missing"}, pairs))
	assert.Equal(t, pairs[:2], associations([]string{missionA}, []string{"d-a", "d-b"}, pairs))
	assert.Nil(t, trailerValues("Mission: prose\n\nThis is prose, not a trailer.", "Mission"))
	assert.Nil(t, trailerValues("subject\n\nNarrative paragraph\nMission: "+missionA+"\nDelegation: d-a", "Mission"))
	assert.Nil(t, trailerValues("Mission: "+missionA, "Mission"))
}

func TestProvenanceSourceTrailersUseGitSyntax(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  [][2]string
		message string
		want    sourceTrailers
	}{
		{"configured separator", [][2]string{{"trailer.separators", "%"}}, "subject\n\nMission% " + missionA + "\nDelegation% d-a", sourceTrailers{[]string{missionA}, []string{"d-a"}}},
		{"canonical keys", [][2]string{{"trailer.separators", "%"}, {"trailer.mission.key", "Mission-Id% "}, {"trailer.delegation.key", "Delegation-Id% "}}, "subject\n\nMission-Id% " + missionA + "\nDelegation-Id% d-a", sourceTrailers{[]string{missionA}, []string{"d-a"}}},
		{"short aliases", [][2]string{{"trailer.mis.key", "Mission"}, {"trailer.del.key", "Delegation"}}, "subject\n\nMis: " + missionA + "\nDel: d-a", sourceTrailers{[]string{missionA}, []string{"d-a"}}},
		{"short aliases with separators", [][2]string{{"trailer.separators", "%"}, {"trailer.mis.key", "Mission% "}, {"trailer.del.key", "Delegation% "}}, "subject\n\nMis% " + missionA + "\nDel% d-a", sourceTrailers{[]string{missionA}, []string{"d-a"}}},
		{"canonical prefix aliases", [][2]string{{"trailer.separators", "%"}, {"trailer.mis.key", "Mission-Id% "}, {"trailer.del.key", "Delegation-Id% "}}, "subject\n\nMission-Id% " + missionA + "\nDelegation-Id% d-a", sourceTrailers{[]string{missionA}, []string{"d-a"}}},
		{"folded and comments", nil, "subject\n\nMission:\n " + missionA + "\nDelegation: d-a\n# editor comment", sourceTrailers{[]string{missionA}, []string{"d-a"}}},
		{"all candidates", nil, "subject\n\nMission: " + missionA + "\nDelegation: d-a\nDelegation: d-b", sourceTrailers{[]string{missionA}, []string{"d-a", "d-b"}}},
		{"multiple missions", nil, "subject\n\nMission: " + missionA + "\nMission: " + missionB + "\nDelegation: d-a", sourceTrailers{[]string{missionA, missionB}, []string{"d-a"}}},
		{"duplicate missions", nil, "subject\n\nMission: " + missionA + "\nMission: " + missionA + "\nDelegation: d-a", sourceTrailers{[]string{missionA, missionA}, []string{"d-a"}}},
		{"narrative", nil, "subject\n\nNarrative paragraph\nMission: " + missionA + "\nDelegation: d-a", sourceTrailers{}},
		{"configured narrative", [][2]string{{"trailer.mission.key", "Mission"}}, "subject\n\nNarrative paragraph\nMission: " + missionA + "\nDelegation: d-a", sourceTrailers{}},
		{"subject only", nil, "Mission: " + missionA, sourceTrailers{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := repositoryTest(t)
			r := p.Repository
			for _, setting := range tc.config {
				gitTest(t, r, "config", setting[0], setting[1])
			}
			gitTest(t, r, "commit", "--allow-empty", "--cleanup=verbatim", "-m", tc.message)
			keys, err := r.sourceTrailerKeys(t.Context())
			require.NoError(t, err)
			values, err := r.sourceCommitTrailers(t.Context(), "HEAD", keys)
			require.NoError(t, err)
			assert.Equal(t, tc.want, values)
		})
	}
}

func TestProvenanceConfiguredSourceTrailerPublication(t *testing.T) {
	for _, tc := range []struct {
		name, trailers string
		want           []Association
		invalid        bool
	}{
		{"ambiguous", "Mission% " + missionA + "\nDelegation% d-a\nDelegation% d-b", []Association{{missionA, "d-a"}, {missionA, "d-b"}}, false},
		{"parallel tuples", "Mission% " + missionA + "\nMission% " + missionB + "\nDelegation% d-a\nDelegation% d-c", nil, false},
		{"missing candidate", "Mission% " + missionA + "\nDelegation% d-a\nDelegation% missing", nil, false},
		{"mixed prose", "Narrative paragraph\nMission% " + missionA + "\nDelegation% d-a", nil, false},
		{"malformed ID", "Mission% ../outside\nDelegation% d-a", nil, true},
		{"folded invalid ID", "Mission% " + missionA + "\nDelegation% d-a\n d-b", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, base := repositoryTest(t)
			r := p.Repository
			gitTest(t, r, "config", "trailer.separators", "%")
			gitTest(t, r, "config", "trailer.mission.key", "Mission")
			// Read-only parsing must never execute configured trailer commands.
			sentinel := filepath.Join(r.Root, "must-not-exist")
			gitTest(t, r, "config", "trailer.mission.cmd", "echo unsafe > '"+sentinel+"'")
			writeTest(t, r.Root, "source.txt", []byte("new source\n"))
			gitTest(t, r, "add", "source.txt")
			gitTest(t, r, "commit", "-m", "source\n\n"+tc.trailers)
			index, before := indexBytesTest(t, r)
			receipt, err := p.Publish(t.Context(), selectedTest(t), base)
			if tc.invalid {
				require.Error(t, err)
				assert.False(t, receipt.IndexInstalled)
				after, err := os.ReadFile(index)
				require.NoError(t, err)
				assert.Equal(t, before, after)
				assert.NoDirExists(t, filepath.Join(r.Root, filepath.FromSlash(Directory)))
			} else {
				require.NoError(t, err)
				m, err := r.LoadManifest(t.Context(), ":", receipt.Manifest)
				require.NoError(t, err)
				require.Len(t, m.Sources, 1)
				require.Len(t, m.Sources[0].Ranges, 1)
				rng := m.Sources[0].Ranges[0]
				assert.ElementsMatch(t, tc.want, rng.Candidates)
				assert.NotEmpty(t, rng.Unknown)
			}
			assert.NoFileExists(t, sentinel)
		})
	}
}

func TestProvenanceSignedSourceTrailerPublication(t *testing.T) {
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("SSH commit signing requires ssh-keygen")
	}
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("SSH_ASKPASS_REQUIRE", "never")
	p, base := repositoryTest(t)
	r := p.Repository
	// Generate a disposable key outside the repository. No user keys, agent,
	// global Git configuration or network services participate in this fixture.
	key := filepath.Join(t.TempDir(), "signing-key")
	cmd := exec.CommandContext(t.Context(), keygen, "-q", "-t", "ed25519", "-N", "", "-C", "fixture@example.invalid", "-f", key)
	cmd.Env = r.Env
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
		{"trailer.separators", "%"},
		{"trailer.mission.key", "Mission-Id% "},
		{"trailer.delegation.key", "Delegation-Id% "},
	} {
		gitTest(t, r, "config", setting[0], setting[1])
	}
	writeTest(t, r.Root, "source.txt", []byte("signed source\n"))
	gitTest(t, r, "add", "source.txt")
	gitTest(t, r, "commit", "-S", "-m", "signed source\n\nMission-Id% "+missionA+"\nDelegation-Id% d-a")
	commit := gitTest(t, r, "rev-parse", "HEAD")
	gitTest(t, r, "verify-commit", commit)
	require.Contains(t, gitTest(t, r, "log", "-1", "--format=%s", commit), "Good \"git\" signature",
		"the fixture must actually enable signature display")

	receipt, err := p.Publish(t.Context(), selectedTest(t), base)
	require.NoError(t, err)
	require.True(t, receipt.IndexInstalled)
	m, err := r.LoadManifest(t.Context(), ":", receipt.Manifest)
	require.NoError(t, err)
	require.Len(t, m.Sources, 1)
	require.Len(t, m.Sources[0].Ranges, 1)
	rng := m.Sources[0].Ranges[0]
	assert.Equal(t, []Association{{missionA, "d-a"}}, rng.Candidates)
	assert.Empty(t, rng.Unknown)
	assert.Equal(t, Evidence{Commit: commit, Path: "source.txt", Line: 1, Missions: []string{missionA}, Delegations: []string{"d-a"}}, rng.Evidence)
	require.NoError(t, r.VerifyIndex(t.Context(), []string{receipt.Manifest}))
	assert.Equal(t, "true", gitTest(t, r, "config", "--get", "log.showSignature"))
	gitTest(t, r, "verify-commit", commit)
}

func TestProvenanceSourceTrailerConfigurationUsesRequestEnvironment(t *testing.T) {
	p, _ := repositoryTest(t)
	r := p.Repository
	gitTest(t, r, "commit", "--allow-empty", "-m", "subject\n\nMission-Id% "+missionA+"\nDelegation-Id% d-a")
	config := filepath.Join(t.TempDir(), "gitconfig")
	require.NoError(t, os.WriteFile(config, []byte("[trailer]\nseparators = %\n[trailer \"mission\"]\nkey = Mission-Id%\n[trailer \"delegation\"]\nkey = Delegation-Id%\n"), 0o600))
	// Capture the request environment, then contradict the process environment.
	// No parser/config read may quietly recapture the latter.
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	r.Env = gitenv.Parsed()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	keys, err := r.sourceTrailerKeys(t.Context())
	require.NoError(t, err)
	values, err := r.sourceCommitTrailers(t.Context(), "HEAD", keys)
	require.NoError(t, err)
	assert.Equal(t, sourceTrailers{[]string{missionA}, []string{"d-a"}}, values)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = r.sourceTrailerKeys(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = r.sourceCommitTrailers(ctx, "HEAD", keys)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = r.sourceCommitTrailers(t.Context(), strings.Repeat("f", 40), keys)
	assert.Error(t, err, "missing commits must not become empty evidence")
	// No request-local trailer settings is a valid default, even when the
	// process-global configuration has become unreadable since capture.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	r.Env = gitenv.Parsed()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "broken"))
	require.NoError(t, os.WriteFile(os.Getenv("GIT_CONFIG_GLOBAL"), []byte("[broken\n"), 0o600))
	keys, err = r.sourceTrailerKeys(t.Context())
	require.NoError(t, err)
	assert.Equal(t, sourceTrailerConfig{mission: "Mission", delegation: "Delegation", separators: ":"}, keys)
	r.Env = gitenv.Parsed()
	_, err = r.sourceTrailerKeys(t.Context())
	assert.Error(t, err, "broken config must not become default keys")
}

func TestProvenanceParseBlameCoordinatesAndLongLines(t *testing.T) {
	sha := strings.Repeat("a", 40)
	text := strings.Repeat("x", 100000)
	lines, err := ParseBlame([]byte(sha + " 12 19 1\nauthor A\nfilename \"old\\tname.txt\"\n\t" + text + "\n"))
	require.NoError(t, err)
	require.Len(t, lines, 1)
	assert.Equal(t, 12, lines[0].Original)
	assert.Equal(t, 19, lines[0].Final)
	assert.Equal(t, "old\tname.txt", lines[0].Path)
	assert.Equal(t, text, lines[0].Text)
	for _, input := range []string{"\tcontent\n", sha + " bad 1\n\tcontent\n", sha + " 1 0\n\tcontent\n", sha + " 1 1\nfilename \"bad\\z\"\n\tcontent\n"} {
		_, err := ParseBlame([]byte(input))
		require.Error(t, err, input)
	}
}

func TestProvenanceDecodeFailsClosed(t *testing.T) {
	p, base := repositoryTest(t)
	receipt, err := p.Publish(t.Context(), selectedTest(t), base)
	require.NoError(t, err)
	data, err := p.Repository.read(t.Context(), ":", receipt.Manifest)
	require.NoError(t, err)
	_, err = Decode(Directory+strings.Repeat("0", 64)+".yaml", data)
	require.Error(t, err)
	for _, suffix := range []string{"\nunknown: true\n", "\n---\nschema_version: 1\n", "\nschema_version: 1\n"} {
		bad := append(append([]byte(nil), data...), []byte(suffix)...)
		_, err = Decode(Directory+Digest(bad)+".yaml", bad)
		require.Error(t, err)
	}
}

func TestProvenanceParseBlameSHAContent(t *testing.T) {
	for _, size := range []int{40, 64} {
		for _, suffix := range []string{" 1 2", " nonnumeric words"} {
			text := strings.Repeat("a", size) + suffix
			sha := strings.Repeat("b", size)
			lines, err := ParseBlame([]byte(sha + " 12 19 1\nauthor A\nfilename source.txt\n\t" + text + "\n"))
			require.NoError(t, err)
			require.Equal(t, []BlameLine{{Commit: sha, Path: "source.txt", Text: text, Author: "A", Original: 12, Final: 19}}, lines)
		}
	}
}

func TestProvenanceRejectsConflictingOrOverlappingRangeEvidence(t *testing.T) {
	for _, kind := range []string{"overlap", "unknown-with-one-candidate", "wrong-candidate", "missing-candidate", "duplicate-candidate"} {
		t.Run(kind, func(t *testing.T) {
			p, base := repositoryTest(t)
			receipt, err := p.Publish(t.Context(), selectedTest(t), base)
			require.NoError(t, err)
			m, err := p.Repository.LoadManifest(t.Context(), ":", receipt.Manifest)
			require.NoError(t, err)
			require.NotEmpty(t, m.Sources)
			source := &m.Sources[0]
			require.NotEmpty(t, source.Ranges)
			rng := &source.Ranges[0]
			switch kind {
			case "overlap":
				source.Ranges = append(source.Ranges, *rng)
			case "unknown-with-one-candidate":
				rng.Unknown = "incomplete evidence"
			case "wrong-candidate":
				rng.Candidates = []Association{{missionA, "d-b"}}
			case "missing-candidate":
				rng.Candidates = nil
				rng.Unknown = "not enough evidence"
			case "duplicate-candidate":
				rng.Candidates = append(rng.Candidates, rng.Candidates[0])
			}
			name, data, err := Encode(*m)
			require.NoError(t, err)
			_, err = Decode(name, data)
			require.Error(t, err)
		})
	}
}
