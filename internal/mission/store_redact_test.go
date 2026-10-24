package mission

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// leakFixture is one string carrying every class of content that must
// not reach a tracked file: the operator's home path, an address, and
// each credential shape. It is built at run time so this file does not
// itself contain a credential.
type leakFixture struct {
	home string
	text string
	raw  []string // substrings that must be absent from every written byte
}

func newLeakFixture(t *testing.T) leakFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	secrets := []string{
		"ghp_" + strings.Repeat("a1B2", 9),
		"gho_" + strings.Repeat("a", 36),
		"ghu_" + strings.Repeat("b", 36),
		"ghs_" + strings.Repeat("c", 36),
		"ghr_" + strings.Repeat("d", 36),
		"github_pat_" + strings.Repeat("E1_", 12),
		"sk-" + strings.Repeat("x9Y8", 8),
		"sk-ant-api03-" + strings.Repeat("Zz09", 6),
		"AKIA" + "IOSFODNN7EXAMPLE",
		"ASIA" + "ABCDEFGHIJKLMNOP",
		"Bearer " + strings.Repeat("q7", 10),
		"-----BEGIN " + "PRIVATE KEY----- MIIEvQIBADANBg -----END " + "PRIVATE KEY-----",
	}
	text := "at " + filepath.Join(home, "work", "x.go") + " mail jane.doe@example.com" +
		" GITHUB_TOKEN=hunter2pass --password hunter2pass " + `{"api_key": "hunter2pass"}` +
		" Authorization: Bearer " + strings.Repeat("q7", 10)
	for _, sec := range secrets {
		text += " " + sec
	}
	raw := append([]string{home, "jane.doe@example.com", "hunter2pass", "MIIEvQIBADANBg"}, secrets...)
	return leakFixture{home: home, text: text, raw: raw}
}

func (f leakFixture) assertClean(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, path)
	for _, r := range f.raw {
		assert.NotContains(t, string(data), r, "%s leaks %q", filepath.Base(path), r)
	}
	assert.Contains(t, string(data), "~/work/x.go", "%s lost the redacted path", filepath.Base(path))
}

func TestStore_Create_RedactsContract(t *testing.T) {
	f := newLeakFixture(t)
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	c.Context = f.text + "\n-----BEGIN " + "RSA PRIVATE KEY-----\nMIIEvQIBADANBg\n-----END " + "RSA PRIVATE KEY-----\n"
	c.SuccessCriteria = []string{f.text}
	c.Inputs.Trigger = &Trigger{Type: "email", From: "jane.doe@example.com", Subject: f.text}
	c.Evaluator.Hash = strings.Repeat("ab", 32)
	require.NoError(t, s.Create(c))

	f.assertClean(t, mustContractPath(t, s, c.MissionID))

	got, err := s.Load(c.MissionID)
	require.NoError(t, err, "a redacted contract must still load")
	assert.Equal(t, c.MissionID, got.MissionID)
	assert.Equal(t, strings.Repeat("ab", 32), got.Evaluator.Hash, "the evaluator hash is pinned and must not change")
	assert.Equal(t, c.Evaluator.PinnedAt, got.Evaluator.PinnedAt)
}

func TestStore_Update_RedactsContract(t *testing.T) {
	f := newLeakFixture(t)
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))
	c.Context = f.text
	require.NoError(t, s.Update(c))
	f.assertClean(t, mustContractPath(t, s, c.MissionID))
}

func TestStore_AppendResult_RedactsResult(t *testing.T) {
	f := newLeakFixture(t)
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))
	r := &Result{
		Mission: c.MissionID, Round: 1, Author: c.Worker,
		Verdict: VerdictPass, Confidence: 0.8,
		Evidence:      []EvidenceCheck{{Name: f.text, Status: EvidenceStatusPass}},
		OpenQuestions: []string{f.text},
		Prose:         f.text,
	}
	require.NoError(t, s.AppendResult(c.MissionID, r))

	f.assertClean(t, mustResultsPath(t, s, c.MissionID))
	got, err := s.LoadResults(c.MissionID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, VerdictPass, got[0].Verdict)
	assert.Equal(t, 1, got[0].Round)
}

func TestStore_AppendReflection_RedactsReflection(t *testing.T) {
	f := newLeakFixture(t)
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))
	r := reflectionFor(c.MissionID, 1, RecommendationContinue)
	r.Signals = []string{f.text}
	r.Reason = f.text
	require.NoError(t, s.AppendReflection(c.MissionID, r))

	f.assertClean(t, mustReflectionsPath(t, s, c.MissionID))
	got, err := s.LoadReflections(c.MissionID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, RecommendationContinue, got[0].Recommendation)
}

func TestStore_Abandon_RedactsEventLog(t *testing.T) {
	f := newLeakFixture(t)
	repoRoot := t.TempDir()
	s := NewStoreWithRoots(repoRoot, t.TempDir())
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))
	_, err := s.Abandon(c.MissionID, f.text)
	require.NoError(t, err)
	f.assertClean(t, audit.LiveMissionLogPath(repoRoot, c.MissionID, sessionlessID))
}

func TestAppendTraceSummary_Redacts(t *testing.T) {
	f := newLeakFixture(t)
	dir := t.TempDir()
	s := &Store{repoRoot: dir}
	c := &Contract{
		MissionID: "m-2026-01-01-001", CreatedAt: "2026-01-01T00:00:00Z",
		ClosedAt: "2026-01-02T00:00:00Z", Status: StatusClosed,
		Leader: "alice", Worker: "bob", Evaluator: Evaluator{Handle: "carol"},
		WriteSet:        []string{"a.go"},
		SuccessCriteria: []string{f.text},
		Budget:          Budget{Rounds: 1}, CurrentRound: 1,
		Repo: filepath.Join(f.home, "work", "x.go"),
	}
	r := &Result{Verdict: VerdictPass, FilesChanged: []FileChange{{Path: filepath.Join(f.home, "work", "x.go")}}}
	require.NoError(t, s.appendTraceSummary(c, r))

	f.assertClean(t, filepath.Join(dir, ".punt-labs", "ethos", "missions.jsonl"))
}

func TestWriters_FailClosedWithoutUsableHome(t *testing.T) {
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	t.Setenv("HOME", "/")
	dir := t.TempDir()

	c2 := newContract("m-2026-04-08-002")
	c2.WriteSet = []string{"other/"}
	require.Error(t, s.Create(c2), "contract writer must refuse")
	_, statErr := os.Stat(mustContractPath(t, s, c2.MissionID))
	assert.True(t, os.IsNotExist(statErr), "refused create must leave no file")

	r := &Result{Mission: c.MissionID, Round: 1, Author: c.Worker, Verdict: VerdictPass,
		Evidence: []EvidenceCheck{{Name: "x", Status: EvidenceStatusPass}}}
	require.Error(t, s.AppendResult(c.MissionID, r), "result writer must refuse")
	require.Error(t, s.AppendReflection(c.MissionID, reflectionFor(c.MissionID, 1, RecommendationContinue)),
		"reflection writer must refuse")

	ts := &Store{repoRoot: dir}
	require.Error(t, ts.appendTraceSummary(c, &Result{Verdict: VerdictPass}), "trace writer must refuse")
	_, statErr = os.Stat(filepath.Join(dir, ".punt-labs", "ethos", "missions.jsonl"))
	assert.True(t, os.IsNotExist(statErr), "refused trace append must leave no file")
}

func TestDelegationWriters_Redact(t *testing.T) {
	f := newLeakFixture(t)
	repoRoot := t.TempDir()
	const mID, dID = "m-2026-04-08-001", "d-2026-04-08-001"

	rec, err := WriteDelegationSkeleton(repoRoot, mID, dID, DelegationSkeleton{
		Tier: TierB, AgentType: "bwk", Session: f.text,
		Prompt: []byte(f.text), PromptHash: strings.Repeat("cd", 32),
	})
	require.NoError(t, err)
	dir := filepath.Dir(rec)
	f.assertClean(t, rec)
	f.assertClean(t, filepath.Join(dir, "prompt.md"))

	d, err := LoadDelegation(rec)
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("cd", 32), d.PromptHash, "the prompt hash is a pinned value")

	require.NoError(t, CloseDelegation(rec, DelegationVerdictFail, f.text))
	f.assertClean(t, rec)

	require.NoError(t, CloseDelegationSkeleton(repoRoot, mID, dID, DelegationVerdictAborted, "2026-04-08T12:00:00Z"))
	f.assertClean(t, rec)
}

// TestCloseDelegation_UsesRepoToken: CloseDelegation gets only a record
// path, and must still tag the checkout as <repo> like every other writer.
func TestCloseDelegation_UsesRepoToken(t *testing.T) {
	newLeakFixture(t)
	repoRoot := t.TempDir()
	rec, err := WriteDelegationSkeleton(repoRoot, "m-2026-04-08-001", "d-2026-04-08-001",
		DelegationSkeleton{Tier: TierB, AgentType: "bwk"})
	require.NoError(t, err)

	require.NoError(t, CloseDelegation(rec, DelegationVerdictFail, "failed in "+filepath.Join(repoRoot, "internal", "x.go")))
	d, err := LoadDelegation(rec)
	require.NoError(t, err)
	assert.Equal(t, "failed in <repo>/internal/x.go", d.Reason)
}

// TestStore_Close_RefusesUpFrontWithoutRedactor: Close builds the redactor
// before any mutation, so a failure refuses the close and leaves the
// mission open, instead of closing it and silently dropping the
// missions.jsonl line (the trace write is non-fatal, DES-050).
func TestStore_Close_RefusesUpFrontWithoutRedactor(t *testing.T) {
	newLeakFixture(t)
	repoRoot := t.TempDir()
	s := NewStoreWithRoots(repoRoot, t.TempDir())
	c := newContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))
	submitRoundResult(t, s, c, VerdictPass)

	t.Setenv("HOME", "relative/path")
	_, err := s.Close(c.MissionID, StatusClosed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "building path redactor")

	loaded, err := s.Load(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, StatusOpen, loaded.Status, "a refused close must leave the mission open")
	_, statErr := os.Stat(filepath.Join(repoRoot, ".punt-labs", "ethos", "missions.jsonl"))
	assert.True(t, os.IsNotExist(statErr), "no trace line for a close that did not happen")
}

// enforcementContract returns a contract whose every path-tagged field, at
// every depth, holds a value the credential pass would rewrite.
func enforcementContract(id string) *Contract {
	c := newContract(id)
	c.Tools = []string{"Bash(gh auth token:*)", "Bash(export API_KEY=abc:*)"}
	c.WriteSet = []string{"cfg/token=abc", "internal/secret_store/"}
	c.ExtractInto = []string{"a/token=abc"}
	c.Inputs.Files = []string{"Bash(gh auth token:*)"}
	c.Preconditions = []Precondition{{
		Form:        PreconditionFormExplicit,
		RequireRead: []string{"cfg/token=abc"},
		Message:     "read the rules first",
	}}
	c.Delegations = []DelegationTemplate{{
		Role: "token=abc", SpawnPattern: "password: b", ExtractInto: []string{"a/password: b"},
	}}
	return c
}

// TestStore_Create_EnforcementFieldsSurviveRedaction: tool grants, write
// sets and the other fields the hooks act on get the path pass only, at
// every depth. The credential pass would turn "Bash(gh auth token:*)" into
// a different, and broken, grant.
func TestStore_Create_EnforcementFieldsSurviveRedaction(t *testing.T) {
	newLeakFixture(t)
	s := testStore(t)
	c := enforcementContract("m-2026-04-08-001")
	require.NoError(t, s.Create(c))

	got, err := s.Load(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, c.Tools, got.Tools)
	assert.Equal(t, c.WriteSet, got.WriteSet)
	assert.Equal(t, c.ExtractInto, got.ExtractInto)
	assert.Equal(t, c.Inputs.Files, got.Inputs.Files)
	assert.Equal(t, c.Preconditions[0].RequireRead, got.Preconditions[0].RequireRead)
	assert.Equal(t, c.Delegations, got.Delegations)
}

// TestWriterAndScrubAgree: RedactRecord (the writer) and redactYAMLDocPaths
// (the scrub) must treat the same contract the same way, or a file the
// scrub rewrites would differ from one the writer produced.
func TestWriterAndScrubAgree(t *testing.T) {
	newLeakFixture(t)
	repoRoot := t.TempDir()
	c := enforcementContract("m-2026-04-08-001")
	c.Context = "token=abc at /home/x and jane@example.com"

	viaWriter, err := RedactRecord(repoRoot, *c)
	require.NoError(t, err)

	r, err := NewPathRedactor(repoRoot)
	require.NoError(t, err)
	raw, err := yaml.Marshal(c)
	require.NoError(t, err)
	scrubbed, _, err := redactYAMLDocPaths(r.Content, r.Path, raw)
	require.NoError(t, err)
	var viaScrub Contract
	require.NoError(t, yaml.Unmarshal(scrubbed, &viaScrub))

	assert.Equal(t, viaWriter, viaScrub)
}

func TestStore_Create_EnforcementFieldsStillLosePaths(t *testing.T) {
	f := newLeakFixture(t)
	s := testStore(t)
	c := newContract("m-2026-04-08-001")
	c.Tools = []string{"Read(" + filepath.Join(f.home, "work", "x.go") + ")"}
	require.NoError(t, s.Create(c))
	got, err := s.Load(c.MissionID)
	require.NoError(t, err)
	assert.Equal(t, []string{"Read(~/work/x.go)"}, got.Tools)
}

// TestCheckRedactedContract: a redacted contract that no longer validates
// is refused, with the validator naming the field.
func TestCheckRedactedContract(t *testing.T) {
	c := newContract("m-2026-04-08-001")
	require.NoError(t, checkRedactedContract(*c), "a valid contract passes")

	bad := *c
	bad.WriteSet = []string{"a:b"}
	err := checkRedactedContract(bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write_set[0]")
	assert.Contains(t, err.Error(), "redacted contract")
}

// TestCheckPathFields: a redacted contract whose path-tagged field differs
// from the path-pass-only result is refused, naming the field, whether or
// not the original contract validated.
func TestCheckPathFields(t *testing.T) {
	newLeakFixture(t)
	repoRoot := t.TempDir()
	c := enforcementContract("m-2026-04-08-001")

	good, err := RedactRecord(repoRoot, *c)
	require.NoError(t, err)
	require.NoError(t, checkPathFields(repoRoot, *c, good))

	bad := good
	bad.Delegations = []DelegationTemplate{{
		Role: "w", SpawnPattern: "password: [redacted-secret]", ExtractInto: c.Delegations[0].ExtractInto,
	}}
	err = checkPathFields(repoRoot, *c, bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Delegations[0].Role")
	assert.Contains(t, err.Error(), "Delegations[0].SpawnPattern")
}
