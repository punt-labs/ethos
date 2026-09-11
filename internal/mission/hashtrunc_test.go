package mission

import (
	"strings"
	"testing"
)

// resultWithEvidence renders a minimal well-formed result whose single
// evidence entry carries the supplied name, so a test can vary the one
// scalar under examination and nothing else.
func resultWithEvidence(name string) []byte {
	return []byte(`mission: m-2026-09-10-001
round: 1
author: bwk
verdict: pass
confidence: 0.9
evidence:
  - name: ` + name + `
    status: pass
`)
}

// TestDecodeResultRejectsHashTruncatedEvidenceName reproduces the
// artifact that shipped on PR #516. YAML starts a comment at the
// unquoted '#', so the recorded name was the two characters "PR" — non
// empty, so Validate passed, so a result asserting a merge and a
// review-thread count round-tripped through `ethos mission close` with
// status: pass and no longer described anything.
func TestDecodeResultRejectsHashTruncatedEvidenceName(t *testing.T) {
	body := resultWithEvidence("PR #515 merged as 6f61406 — 6/6 CI checks green, 17 review threads resolved")

	r, err := DecodeResultStrict(body, "result.yaml")
	if err == nil {
		t.Fatalf("submission accepted; evidence name parsed as %q", r.Evidence[0].Name)
	}

	msg := err.Error()
	for _, want := range []string{
		"result.yaml",   // the file the operator can fix
		"evidence[0]",   // which entry
		`"PR"`,          // what YAML actually kept
		"6/6 CI checks", // the raw line, so the loss is visible
		"quote",         // what to do about it
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}

// TestDecodeResultAcceptsQuotedHash is the other half of the rule: a
// quoted value keeps its '#' and must not be flagged.
func TestDecodeResultAcceptsQuotedHash(t *testing.T) {
	const want = "PR #515 merged as 6f61406 — 6/6 CI checks green"
	cases := []struct {
		name string
		body []byte
	}{
		{name: "double quoted", body: resultWithEvidence(`"` + want + `"`)},
		{name: "single quoted", body: resultWithEvidence(`'` + want + `'`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := DecodeResultStrict(tc.body, "result.yaml")
			if err != nil {
				t.Fatalf("DecodeResultStrict: %v", err)
			}
			if got := r.Evidence[0].Name; got != want {
				t.Errorf("name = %q, want %q", got, want)
			}
		})
	}
}

// TestDecodeResultAcceptsHashWithoutLeadingSpace pins the YAML rule the
// check rides on: '#' opens a comment only after whitespace, so a bead
// reference like ethos-56a#2 is ordinary content and must pass
// unquoted.
func TestDecodeResultAcceptsHashWithoutLeadingSpace(t *testing.T) {
	const want = "regression ethos-56a#2 reproduced"

	r, err := DecodeResultStrict(resultWithEvidence(want), "result.yaml")
	if err != nil {
		t.Fatalf("DecodeResultStrict: %v", err)
	}
	if got := r.Evidence[0].Name; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
}

// TestDecodeResultAcceptsCommentOnItsOwnLine keeps the remedy the error
// message offers actually available: a comment the operator meant as a
// comment stays legal.
func TestDecodeResultAcceptsCommentOnItsOwnLine(t *testing.T) {
	body := []byte(`# submitted by the round-2 worker
mission: m-2026-09-10-001
round: 1
author: bwk
verdict: pass
confidence: 0.9
evidence:
  # the gate that matters
  - name: make check
    status: pass
`)
	if _, err := DecodeResultStrict(body, "result.yaml"); err != nil {
		t.Fatalf("DecodeResultStrict: %v", err)
	}
}

// TestDecodeResultAcceptsCommentsOnConstrainedFields guards the
// scaffold ethos itself ships: ScaffoldResultYAML annotates mission,
// round, author, verdict, confidence, and the files_changed counts with
// trailing comments. Those fields are pattern, enum, or numeric, so a
// discarded comment cannot change what they mean, and rejecting it
// would refuse ethos's own template.
func TestDecodeResultAcceptsCommentsOnConstrainedFields(t *testing.T) {
	body := []byte(`mission: m-2026-09-10-001   # string, required
round: 1                    # int, required
author: bwk                 # string, required
verdict: pass               # one of: pass, fail, escalate
confidence: 0.9             # float in [0.0, 1.0]
files_changed:
  - path: internal/mission/result.go   # must live inside write_set
    added: 12                          # lines added
    removed: 0                         # lines removed
evidence:
  - name: "make check"      # short label
    status: pass            # one of: pass, fail, skip
`)
	r, err := DecodeResultStrict(body, "result.yaml")
	if err != nil {
		t.Fatalf("DecodeResultStrict: %v", err)
	}
	if r.Author != "bwk" || r.Verdict != VerdictPass {
		t.Errorf("author/verdict = %q/%q, want bwk/pass", r.Author, r.Verdict)
	}
}

func TestDecodeResultRejectsHashTruncatedOpenQuestion(t *testing.T) {
	body := []byte(`mission: m-2026-09-10-001
round: 1
author: bwk
verdict: pass
confidence: 0.9
evidence:
  - name: "make check"
    status: pass
open_questions:
  - should the fix also cover PR #514's contract path?
`)
	r, err := DecodeResultStrict(body, "result.yaml")
	if err == nil {
		t.Fatalf("submission accepted; open_questions[0] parsed as %q", r.OpenQuestions[0])
	}
	if !strings.Contains(err.Error(), "open_questions[0]") {
		t.Errorf("error %q does not name the entry", err)
	}
}

// TestDecodeResultAcceptsHashInsideProseBlock records the one field
// where '#' needs no defence. A literal block scalar has no comment
// syntax — every '#' inside it is content — so the check cannot fire
// and must not be made to.
func TestDecodeResultAcceptsHashInsideProseBlock(t *testing.T) {
	body := []byte(`mission: m-2026-09-10-001
round: 1
author: bwk
verdict: pass
confidence: 0.9
evidence:
  - name: "make check"
    status: pass
prose: |
  Round 2 fixed the truncation found in PR #516.
  See also PR #515 for the original report.
`)
	r, err := DecodeResultStrict(body, "result.yaml")
	if err != nil {
		t.Fatalf("DecodeResultStrict: %v", err)
	}
	for _, want := range []string{"PR #516", "PR #515"} {
		if !strings.Contains(r.Prose, want) {
			t.Errorf("prose = %q, lost %q", r.Prose, want)
		}
	}
}

// TestDecodeResultRejectsHashTruncatedPlainProse covers the same field
// written as a plain scalar, where the comment syntax is live again.
func TestDecodeResultRejectsHashTruncatedPlainProse(t *testing.T) {
	body := []byte(`mission: m-2026-09-10-001
round: 1
author: bwk
verdict: pass
confidence: 0.9
evidence:
  - name: "make check"
    status: pass
prose: round 2 fixed the truncation found in PR #516
`)
	r, err := DecodeResultStrict(body, "result.yaml")
	if err == nil {
		t.Fatalf("submission accepted; prose parsed as %q", r.Prose)
	}
	if !strings.Contains(err.Error(), "prose") {
		t.Errorf("error %q does not name the field", err)
	}
}

func TestDecodeReflectionRejectsHashTruncation(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		field string
	}{
		{
			name:  "reason",
			field: "reason",
			body: `mission: m-2026-09-10-001
round: 1
author: claude
converging: true
signals:
  - "tests green"
recommendation: advance
reason: round 1 closed the gap reported in PR #516
`,
		},
		{
			name:  "signal",
			field: "signals[0]",
			body: `mission: m-2026-09-10-001
round: 1
author: claude
converging: true
signals:
  - worker cited PR #515 as evidence
recommendation: advance
reason: "converging"
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeReflectionStrict([]byte(tc.body), "reflection.yaml")
			if err == nil {
				t.Fatal("submission accepted")
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error %q does not name %q", err, tc.field)
			}
		})
	}
}

func TestDecodeCorrectionRejectsHashTruncation(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		field string
	}{
		{
			name:  "claim",
			field: "claim",
			body: `mission: m-2026-09-10-001
round: 1
kind: evidence
author: bwk
claim: PR #515 merged with 6/6 checks green
corrected: "the PR was still open"
`,
		},
		{
			name:  "corrected",
			field: "corrected",
			body: `mission: m-2026-09-10-001
round: 1
kind: evidence
author: bwk
claim: "the PR merged"
corrected: PR #515 was still open at submission time
`,
		},
		{
			name:  "evidence name",
			field: "evidence[0].name",
			body: `mission: m-2026-09-10-001
round: 1
kind: evidence
author: bwk
claim: "the PR merged"
corrected: "the PR was still open"
evidence:
  - name: gh pr view 515 #  shows state OPEN
    status: pass
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeCorrectionStrict([]byte(tc.body), "correction.yaml")
			if err == nil {
				t.Fatal("submission accepted")
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error %q does not name %q", err, tc.field)
			}
		})
	}
}

func TestCheckContractHashTruncation(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string // empty means the contract must be accepted
	}{
		{
			name: "success criterion truncated",
			body: `leader: claude
worker: bwk
evaluator:
  handle: rsc
write_set:
  - internal/mission
success_criteria:
  - PR #516 review threads all resolved
budget:
  rounds: 2
  reflection_after_each: true
`,
			wantErr: "success_criteria[0]",
		},
		{
			name: "context truncated",
			body: `leader: claude
worker: bwk
evaluator:
  handle: rsc
write_set:
  - internal/mission
success_criteria:
  - "make check passes"
context: follow-up to PR #516
budget:
  rounds: 2
  reflection_after_each: true
`,
			wantErr: "context",
		},
		{
			name: "scaffold is accepted",
			body: ScaffoldContractYAML("claude"),
		},
		{
			name: "quoted criterion is accepted",
			body: `leader: claude
worker: bwk
evaluator:
  handle: rsc
write_set:
  - internal/mission
success_criteria:
  - "PR #516 review threads all resolved"
budget:
  rounds: 2
  reflection_after_each: true
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckContractHashTruncation([]byte(tc.body), "contract.yaml")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("CheckContractHashTruncation: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("contract accepted")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not name %q", err, tc.wantErr)
			}
		})
	}
}

// TestScaffoldResultSurvivesTheCheck is the regression guard for the
// template ethos hands operators: `ethos mission result --scaffold`
// output must still decode.
func TestScaffoldResultSurvivesTheCheck(t *testing.T) {
	if _, err := DecodeResultStrict([]byte(ScaffoldResultYAML()), "scaffold"); err != nil {
		t.Fatalf("DecodeResultStrict(scaffold): %v", err)
	}
}
