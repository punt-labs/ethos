package mission

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestAuthor_NotAHandleGoesThroughTheEmailAndCredentialPasses: Author and
// Actor hold identity handles. A value that is not a handle is not exempt from
// redaction: an email address or a credential in it is replaced, in the
// result, the reflection and the events they append.
func TestAuthor_NotAHandleGoesThroughTheEmailAndCredentialPasses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := NewStoreWithRoots(t.TempDir(), t.TempDir())
	c := newContract("m-2026-08-06-140")
	require.NoError(t, s.Create(c))

	res := resultFor(c, VerdictFail)
	res.Author = "jane@example.com"
	require.NoError(t, s.AppendResult(c.MissionID, res))
	ref := reflectionFor(c.MissionID, 1, RecommendationContinue)
	ref.Author = "jane@example.com"
	require.NoError(t, s.AppendReflection(c.MissionID, ref))

	rs, err := s.LoadResults(c.MissionID)
	require.NoError(t, err)
	require.Len(t, rs, 1)
	assert.Equal(t, "[redacted-email]", rs[0].Author)
	fs, err := s.LoadReflections(c.MissionID)
	require.NoError(t, err)
	require.Len(t, fs, 1)
	assert.Equal(t, "[redacted-email]", fs[0].Author)

	events, _, err := s.LoadEvents(c.MissionID)
	require.NoError(t, err)
	for _, e := range events {
		assert.NotContains(t, e.Actor, "@", "event %s", e.Event)
	}
	var got []string
	for _, e := range events {
		got = append(got, e.Actor)
	}
	assert.Contains(t, got, "[redacted-email]", "the events carry the redacted author")
}

// TestAuthor_ScrubRedactsAnEmailAuthor: a results file an older ethos wrote
// with an address in author is scrubbed the way a new record is, and a handle
// is left alone.
func TestAuthor_ScrubRedactsAnEmailAuthor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, global := t.TempDir(), t.TempDir()
	s := NewStoreWithRoots(repo, global)
	c := newContract("m-2026-08-06-141")
	require.NoError(t, s.Create(c))
	require.NoError(t, s.AppendResult(c.MissionID, resultFor(c, VerdictFail)))
	rs, err := s.LoadResults(c.MissionID)
	require.NoError(t, err)
	rs[0].Author = "jane@example.com"
	raw, err := yaml.Marshal(resultsFile{Results: rs})
	require.NoError(t, err)
	path := mustResultsPath(t, s, c.MissionID)
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	rel, err := filepath.Rel(repo, path)
	require.NoError(t, err)
	res, err := ScrubFiles(repo, global, []string{filepath.ToSlash(rel)}, false)
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.ToSlash(rel)}, res.Changed)
	got, err := s.LoadResults(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, "[redacted-email]", got[0].Author)
}

// TestAuthor_CredentialIsRedacted: a token typed where a handle belongs is
// replaced in the stored records and in the events; a result and a reflection
// are each submitted with it.
func TestAuthor_CredentialIsRedacted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	token := "ghp_" + strings.Repeat("aBcD1234", 4) + "aBcD"
	s := NewStoreWithRoots(t.TempDir(), t.TempDir())
	c := newContract("m-2026-08-06-142")
	require.NoError(t, s.Create(c))
	res := resultFor(c, VerdictFail)
	res.Author = token
	require.NoError(t, s.AppendResult(c.MissionID, res))
	ref := reflectionFor(c.MissionID, 1, RecommendationContinue)
	ref.Author = token
	require.NoError(t, s.AppendReflection(c.MissionID, ref))

	rs, err := s.LoadResults(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, "[redacted-secret]", rs[0].Author)
	fs, err := s.LoadReflections(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, "[redacted-secret]", fs[0].Author)
	events, _, err := s.LoadEvents(c.MissionID)
	require.NoError(t, err)
	var actors []string
	for _, e := range events {
		assert.NotContains(t, e.Actor, "ghp_")
		actors = append(actors, e.Actor)
	}
	assert.Contains(t, actors, "[redacted-secret]")
}

// TestAuthor_NonHandleGetsTheFullContentPass: an Author that is not a handle
// gets the same passes as any record text, in the writers as in scrub and
// migrate. A home path is rewritten, not refused. A results file an older ethos
// wrote with a home path in author accepts the next round's result (appending
// rewrites the whole file). The event of a reflection whose author looks like a
// slug is stored with the rewritten actor and loads.
func TestAuthor_NonHandleGetsTheFullContentPass(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo, global := t.TempDir(), t.TempDir()
	s := NewStoreWithRoots(repo, global)
	c := newContract("m-2026-08-06-143")
	require.NoError(t, s.Create(c))

	res := resultFor(c, VerdictFail)
	res.Author = "/home/bob/x"
	require.NoError(t, s.AppendResult(c.MissionID, res))
	rs, err := s.LoadResults(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, "~[redacted-user]/x", rs[0].Author)

	// A results file an older ethos wrote with the path in author.
	rs[0].Author = "/home/bob/x"
	raw, err := yaml.Marshal(resultsFile{Results: rs})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(mustResultsPath(t, s, c.MissionID), raw, 0o600))
	next := reflectionFor(c.MissionID, 1, RecommendationContinue)
	require.NoError(t, s.AppendReflection(c.MissionID, next), "a later append is accepted")
	_, err = s.AdvanceRound(c.MissionID, "claude")
	require.NoError(t, err)
	round2 := resultFor(c, VerdictFail)
	round2.Round = 2
	require.NoError(t, s.AppendResult(c.MissionID, round2), "the next round's result is accepted")

	slug := reflectionFor(c.MissionID, 2, RecommendationContinue)
	slug.Author = "x/-home-bob"
	require.NoError(t, s.AppendReflection(c.MissionID, slug))
	events, _, err := s.LoadEvents(c.MissionID)
	require.NoError(t, err)
	var actors []string
	for _, e := range events {
		assert.NotContains(t, e.Actor, "bob")
		actors = append(actors, e.Actor)
	}
	assert.Contains(t, actors, "x/-~-[redacted-slug]", "the event of the slug-like actor loads, rewritten")
}

// TestContractRoutingNames_NotAHandleIsRedacted: the leader, worker and
// evaluator handle of a contract, and the same fields of its trace summary,
// are kept when they are handles and otherwise get the content pass.
func TestContractRoutingNames_NotAHandleIsRedacted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	s := NewStoreWithRoots(repo, t.TempDir())
	c := newContract("m-2026-08-06-160")
	c.Leader = "jane@example.com"
	c.Worker = "bob@example.com"
	c.Evaluator.Handle = "carol@example.com"
	require.NoError(t, s.Create(c))

	path, err := s.ContractPath(c.MissionID)
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, a := range []string{"jane@example.com", "bob@example.com", "carol@example.com"} {
		assert.NotContains(t, string(data), a, "contract.yaml")
	}
	assert.Contains(t, string(data), "[redacted-email]")

	line, _, err := s.prepareTraceSummary(c, resultFor(c, VerdictPass))
	require.NoError(t, err)
	for _, a := range []string{"jane@example.com", "bob@example.com", "carol@example.com"} {
		assert.NotContains(t, string(line), a, "trace summary")
	}
	assert.Contains(t, string(line), "[redacted-email]")

	// A real handle is kept.
	ok := newContract("m-2026-08-06-161")
	ok.WriteSet = []string{"docs/"}
	require.NoError(t, s.Create(ok))
	got, err := s.Load(ok.MissionID)
	require.NoError(t, err)
	assert.Equal(t, ok.Leader, got.Leader)
	assert.Equal(t, ok.Worker, got.Worker)
}

// TestHandleFields_CredentialShapedHandleIsRedacted: a value that looks like an
// identity handle but is a credential (sk- or glpat- and 24 lowercase letters)
// is not exempt: it is replaced in the result, reflection, event, contract and
// trace summary, while real handles are kept.
func TestHandleFields_CredentialShapedHandleIsRedacted(t *testing.T) {
	sk := "sk-" + strings.Repeat("abcdefgh", 3)
	gl := "glpat-" + strings.Repeat("abcdefgh", 3)
	sk2 := "sk-" + strings.Repeat("ijklmnop", 3)
	secrets := []string{sk, gl, sk2}
	t.Setenv("HOME", t.TempDir())
	s := NewStoreWithRoots(t.TempDir(), t.TempDir())
	c := newContract("m-2026-08-06-200")
	c.Leader, c.Worker, c.Evaluator.Handle = sk, gl, sk2
	require.NoError(t, s.Create(c))
	path, err := s.ContractPath(c.MissionID)
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, secret := range secrets {
		assert.NotContains(t, string(data), secret, "contract.yaml")
	}
	assert.Contains(t, string(data), "[redacted-secret]")

	line, _, err := s.prepareTraceSummary(c, resultFor(c, VerdictPass))
	require.NoError(t, err)
	for _, secret := range secrets {
		assert.NotContains(t, string(line), secret, "trace summary")
	}

	res := resultFor(c, VerdictFail)
	res.Author = gl
	require.NoError(t, s.AppendResult(c.MissionID, res))
	ref := reflectionFor(c.MissionID, 1, RecommendationContinue)
	ref.Author = sk
	require.NoError(t, s.AppendReflection(c.MissionID, ref))
	rs, err := s.LoadResults(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, "[redacted-secret]", rs[0].Author)
	fs, err := s.LoadReflections(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, "[redacted-secret]", fs[0].Author)
	events, _, err := s.LoadEvents(c.MissionID)
	require.NoError(t, err)
	for _, e := range events {
		for _, secret := range secrets {
			assert.NotContains(t, e.Actor, secret, e.Event)
		}
	}

	// Real handles are untouched.
	ok := newContract("m-2026-08-06-201")
	ok.WriteSet = []string{"docs/"}
	ok.Worker = "bwk"
	require.NoError(t, s.Create(ok))
	got, err := s.Load(ok.MissionID)
	require.NoError(t, err)
	assert.Equal(t, "bwk", got.Worker)
	assert.Equal(t, ok.Leader, got.Leader)
}

// TestSessionField_OnlyASessionIDShapeIsKept: Session is caller-controlled, so a
// value that does not have the shape of a session ID (the file-name-safe token a
// harness UUID or ETHOS_SESSION is), or that the credential pass changes, gets
// the content pass in the contract and the trace summary; a real session ID is
// kept.
func TestSessionField_OnlyASessionIDShapeIsKept(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := NewStoreWithRoots(t.TempDir(), t.TempDir())
	c := newContract("m-2026-08-06-210")
	c.Session = "alice@example.com"
	require.NoError(t, s.Create(c))
	path, err := s.ContractPath(c.MissionID)
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "alice@example.com")
	assert.Contains(t, string(data), "session: '[redacted-email]'")

	line, _, err := s.prepareTraceSummary(c, resultFor(c, VerdictPass))
	require.NoError(t, err)
	assert.NotContains(t, string(line), "alice@example.com")
	assert.Contains(t, string(line), `"session":"[redacted-email]"`)

	ok := newContract("m-2026-08-06-211")
	ok.WriteSet = []string{"docs/"}
	const sid = "94901c4f-5d86-41c5-bd06-98cbd86216b8"
	ok.Session = sid
	require.NoError(t, s.Create(ok))
	got, err := s.Load(ok.MissionID)
	require.NoError(t, err)
	assert.Equal(t, sid, got.Session)
	line, _, err = s.prepareTraceSummary(ok, resultFor(ok, VerdictPass))
	require.NoError(t, err)
	assert.Contains(t, string(line), `"session":"`+sid+`"`)

	// A token has the character class of a session ID but is a credential.
	tok := newContract("m-2026-08-06-212")
	tok.WriteSet = []string{"pkg/"}
	tok.Session = "ghp_" + strings.Repeat("aBcD1234", 4) + "aBcD"
	require.NoError(t, s.Create(tok))
	tokPath, err := s.ContractPath(tok.MissionID)
	require.NoError(t, err)
	data, err = os.ReadFile(tokPath)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "ghp_")
}
