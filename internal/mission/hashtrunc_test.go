package mission

import (
	"fmt"
	"reflect"
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
// trailing comments. A handle, an enum, a mission ID, and a number
// cannot contain " #", so a comment there discards nothing and
// rejecting it would both refuse ethos's own template and assert a loss
// that did not happen.
//
// path is deliberately not in that set — a path admits whitespace — so
// the scaffold quotes it and so does this fixture.
func TestDecodeResultAcceptsCommentsOnConstrainedFields(t *testing.T) {
	body := []byte(`mission: m-2026-09-10-001   # string, required
round: 1                    # int, required
author: bwk                 # string, required
verdict: pass               # one of: pass, fail, escalate
confidence: 0.9             # float in [0.0, 1.0]
files_changed:
  - path: "internal/mission/result.go" # must live inside write_set
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
			// The gate-denial string a blocked worker is shown.
			// Validate rejects an empty one so a failed gate is never
			// silently named; a shortened one is the same harm without
			// the noise.
			name: "precondition message truncated",
			body: `leader: claude
worker: bwk
evaluator:
  handle: rsc
write_set:
  - internal/mission
success_criteria:
  - "make check passes"
preconditions:
  - form: explicit
    require_read:
      - "DESIGN.md"
    message: read DESIGN.md before editing — see PR #516
budget:
  rounds: 2
  reflection_after_each: true
`,
			wantErr: "preconditions[0].message",
		},
		{
			name: "write_set entry truncated",
			body: `leader: claude
worker: bwk
evaluator:
  handle: rsc
write_set:
  - reports/PR #515.md
success_criteria:
  - "make check passes"
budget:
  rounds: 2
  reflection_after_each: true
`,
			wantErr: "write_set[0]",
		},
		{
			name: "trigger subject truncated",
			body: `leader: claude
worker: bwk
evaluator:
  handle: rsc
inputs:
  trigger:
    type: email
    subject: build broken again #515
write_set:
  - internal/mission
success_criteria:
  - "make check passes"
budget:
  rounds: 2
  reflection_after_each: true
`,
			wantErr: "inputs.trigger.subject",
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

// TestDecodeResultRejectsHashTruncatedPath covers the path fields. A
// path is not a constrained value: validateWriteSetEntry rejects null
// bytes, colons, drive letters, traversal, and control characters, but
// not a space, so `reports/PR #515.txt` silently becomes `reports/PR`
// and can still satisfy write_set containment — persisting a different
// file than the worker changed.
func TestDecodeResultRejectsHashTruncatedPath(t *testing.T) {
	body := []byte(`mission: m-2026-09-10-001
round: 1
author: bwk
verdict: pass
confidence: 0.9
files_changed:
  - path: reports/PR #515.txt
    added: 3
    removed: 0
evidence:
  - name: "make check"
    status: pass
`)
	r, err := DecodeResultStrict(body, "result.yaml")
	if err == nil {
		t.Fatalf("submission accepted; path parsed as %q", r.FilesChanged[0].Path)
	}
	if !strings.Contains(err.Error(), "files_changed[0].path") {
		t.Errorf("error %q does not name the entry", err)
	}
}

// TestDecodeResultAcceptsOmittedOptionalField closes the false-positive
// class. `prose:` with a trailing comment parses to a null-tagged node,
// not to a shortened string: the operator omitted an optional field and
// said why. Nothing was lost, so reporting a truncation would be the
// same overstatement this check exists to stop.
func TestDecodeResultAcceptsOmittedOptionalField(t *testing.T) {
	body := []byte(`mission: m-2026-09-10-001
round: 1
author: bwk
verdict: pass
confidence: 0.9
evidence:
  - name: "make check"
    status: pass
prose: # no narrative this round
`)
	r, err := DecodeResultStrict(body, "result.yaml")
	if err != nil {
		t.Fatalf("DecodeResultStrict: %v", err)
	}
	if r.Prose != "" {
		t.Errorf("prose = %q, want empty", r.Prose)
	}
	if err := r.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// TestDecodeAcceptsEmptySequenceItemWithComment pins the sequence
// counterpart of the omitted-field case, in the list-shaped paths the
// check now walks. Two reviewers have independently suspected that
// `- # placeholder` is flagged; it is not, and this is the answer to
// the next one who asks.
//
// The reason is not the one either suspected. A valueless item does not
// hang its comment on the null value node — yaml.v3 moves it to the
// HeadComment of the *next* item, and hashTruncated reads LineComment
// only. Probed directly:
//
//	/0/1/0  scalar  tag=!!null  value=""      line_comment=""
//	/0/1/1  scalar  tag=!!str   value="a.go"  line_comment="# note"  head="# placeholder"
//
// So the null item returns at hashTruncated's first condition, the
// value-bearing item is flagged on its own text, and a head comment is
// never flagged anywhere — which is the same mechanism that keeps a
// comment on its own line legal.
func TestDecodeAcceptsEmptySequenceItemWithComment(t *testing.T) {
	t.Run("result open_questions", func(t *testing.T) {
		body := []byte(`mission: m-2026-09-10-001
round: 1
author: bwk
verdict: pass
confidence: 0.9
evidence:
  - name: "make check"
    status: pass
open_questions:
  - # nothing outstanding
`)
		if _, err := DecodeResultStrict(body, "result.yaml"); err != nil {
			t.Fatalf("DecodeResultStrict: %v", err)
		}
	})

	t.Run("contract write_set", func(t *testing.T) {
		body := []byte(`leader: claude
worker: bwk
evaluator:
  handle: rsc
write_set:
  - # to be filled in
  - "internal/mission/"
success_criteria:
  - "make check passes"
budget:
  rounds: 2
  reflection_after_each: true
`)
		if err := CheckContractHashTruncation(body, "contract.yaml"); err != nil {
			t.Fatalf("CheckContractHashTruncation: %v", err)
		}
	})
}

// TestDecodeResultRejectsRequiredFieldLeftEmpty is the other half: when
// the omitted field is required, the schema validator — not the
// truncation check — is what refuses it, and it says something true.
func TestDecodeResultRejectsRequiredFieldLeftEmpty(t *testing.T) {
	body := []byte(`mission: m-2026-09-10-001
round: 1
author: bwk
verdict: pass
confidence: 0.9
evidence:
  - name: # the check I ran
    status: pass
`)
	r, err := DecodeResultStrict(body, "result.yaml")
	if err != nil {
		t.Fatalf("DecodeResultStrict: %v", err)
	}
	err = r.Validate()
	if err == nil {
		t.Fatal("Validate accepted an empty evidence name")
	}
	if !strings.Contains(err.Error(), "name cannot be empty") {
		t.Errorf("error %q does not name the real problem", err)
	}
}

// TestDecodeRejectsIndirection covers the bypass: the check reads the
// node tree, but typed decoding resolves an alias and a merge key
// first, so a truncated scalar anchored on one field lands in another
// with no scalar node of its own to inspect. Both forms are refused
// outright — a mission artifact is an audit record, and one that needs
// alias resolution to read is a bad audit record.
func TestDecodeRejectsIndirection(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "alias feeds a checked field",
			want: "alias",
			body: `mission: m-2026-09-10-001
round: 1
author: &claim PR #515 merged, 6/6 checks green
verdict: pass
confidence: 0.9
evidence:
  - name: *claim
    status: pass
`,
		},
		{
			// Not demonstrably exploitable against today's result
			// schema — the only site that accepts a `name:` key is an
			// evidence entry, which is already checked, so the
			// truncation is caught on the way in. It is refused as the
			// same family: whether a merge is exploitable turns on
			// which fields happen to exist, which any new field can
			// change, and a guard that holds only by schema accident
			// is not a guard.
			name: "merge key",
			want: "merge key",
			body: `mission: m-2026-09-10-001
round: 1
author: bwk
verdict: pass
confidence: 0.9
evidence:
  - &first
    name: "make check"
    status: pass
  - <<: *first
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := DecodeResultStrict([]byte(tc.body), "result.yaml")
			if err == nil {
				t.Fatalf("submission accepted; evidence = %+v", r.Evidence)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name the %s", err, tc.want)
			}
		})
	}
}

// TestDecodeAcceptsAnchorWithoutAlias keeps the rejection narrow: an
// anchor nothing refers to moves no text and stays legal.
func TestDecodeAcceptsAnchorWithoutAlias(t *testing.T) {
	body := []byte(`mission: m-2026-09-10-001
round: 1
author: &worker bwk
verdict: pass
confidence: 0.9
evidence:
  - name: "make check"
    status: pass
`)
	if _, err := DecodeResultStrict(body, "result.yaml"); err != nil {
		t.Fatalf("DecodeResultStrict: %v", err)
	}
}

// TestEveryScopedFieldExistsAndIsChecked is the guard against the
// defect that shipped in round 1: the contract scope named
// `delegations[].message`, which is not a field of DelegationTemplate,
// so it protected nothing — while `preconditions[].message`, which is
// real and is a human-authored gate-denial string, went unprotected.
//
// For every path in every scope it synthesizes a fragment carrying a
// truncated value at exactly that path and requires the decoder to (a)
// accept the shape structurally, proving the field exists — a wrong
// name fails KnownFields with "field ... not found" — and (b) report
// the truncation, proving the path is reachable by the walk. A scope
// entry that names a field the schema does not have cannot pass both.
func TestEveryScopedFieldExistsAndIsChecked(t *testing.T) {
	const truncated = "PR #515 merged, 6/6 checks green"

	decoders := []struct {
		kind   string
		scope  []fieldPath
		typ    reflect.Type
		decode func([]byte) error
	}{
		{
			kind:  "result",
			scope: resultHashScope,
			typ:   reflect.TypeOf(Result{}),
			decode: func(b []byte) error {
				_, err := DecodeResultStrict(b, "probe")
				return err
			},
		},
		{
			kind:  "reflection",
			scope: reflectionHashScope,
			typ:   reflect.TypeOf(Reflection{}),
			decode: func(b []byte) error {
				_, err := DecodeReflectionStrict(b, "probe")
				return err
			},
		},
		{
			kind:  "correction",
			scope: correctionHashScope,
			typ:   reflect.TypeOf(Correction{}),
			decode: func(b []byte) error {
				_, err := DecodeCorrectionStrict(b, "probe")
				return err
			},
		},
		{
			kind:  "contract",
			scope: contractHashScope,
			typ:   reflect.TypeOf(Contract{}),
			decode: func(b []byte) error {
				// Contracts run the check beside the decoder rather
				// than inside it, so the probe has to exercise both:
				// the decoder proves the field exists, the check
				// proves the path is walked.
				if _, err := DecodeContractStrict(b, "probe"); err != nil {
					return err
				}
				return CheckContractHashTruncation(b, "probe")
			},
		},
	}

	for _, d := range decoders {
		if len(d.scope) == 0 {
			t.Errorf("%s scope is empty", d.kind)
		}
		for _, path := range d.scope {
			t.Run(d.kind+"/"+path.String(), func(t *testing.T) {
				// Resolve the path against the Go type first. The
				// decode probe below cannot carry this on its own:
				// Inputs has a custom UnmarshalYAML that decodes its
				// subtree without KnownFields, so a typo under
				// inputs.trigger would parse, be walked, and report a
				// truncation for a field the schema does not have.
				if err := resolveAgainstType(d.typ, path); err != nil {
					t.Fatalf("scope entry does not match the schema: %v", err)
				}

				body := yamlAt(path, truncated)
				err := d.decode([]byte(body))
				if err == nil {
					t.Fatalf("accepted a truncated value at %s\n%s", path, body)
				}
				if !strings.Contains(err.Error(), "ends at an unquoted") {
					t.Fatalf("error %q is not the truncation error; the field may not exist\n%s", err, body)
				}
				// The fragment carries exactly one item per sequence,
				// so the error must name index 0 at every seq step.
				indexed := strings.ReplaceAll(path.String(), seq, "[0]")
				if !strings.Contains(err.Error(), indexed) {
					t.Errorf("error %q does not name %s", err, indexed)
				}
			})
		}
	}
}

// resolveAgainstType follows path through the Go type the schema is
// declared in, matching each step against a yaml struct tag and each
// seq against a slice element. It reports the first step that does not
// resolve, and requires the path to land on a string — the only kind of
// value a '#' can shorten.
func resolveAgainstType(t reflect.Type, path fieldPath) error {
	for i, step := range path {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if step == seq {
			if t.Kind() != reflect.Slice {
				return fmt.Errorf("step %d (%s): %s is not a sequence", i, step, t)
			}
			t = t.Elem()
			continue
		}
		if t.Kind() != reflect.Struct {
			return fmt.Errorf("step %d (%s): %s is not a mapping", i, step, t)
		}
		f, ok := fieldByYAMLTag(t, step)
		if !ok {
			return fmt.Errorf("step %d: %s has no yaml field %q", i, t, step)
		}
		t = f.Type
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.String {
		return fmt.Errorf("%s resolves to %s, not a string", path, t)
	}
	return nil
}

// fieldByYAMLTag finds the struct field whose yaml tag names key.
func fieldByYAMLTag(t reflect.Type, key string) (reflect.StructField, bool) {
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == key {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

// yamlAt renders a single-field YAML document carrying value at the
// given path, so a scope entry can be probed against the real schema
// without hand-writing a fixture per field.
func yamlAt(path fieldPath, value string) string {
	if len(path) == 0 {
		return value + "\n"
	}
	step, rest := path[0], path[1:]
	if step == seq {
		// "- " already fills the first two columns, so the tail of the
		// nested block indents but its first line does not.
		return "- " + indentBy(yamlAt(rest, value), "  ", false)
	}
	if len(rest) == 0 {
		return step + ": " + value + "\n"
	}
	return step + ":\n" + indentBy(yamlAt(rest, value), "  ", true)
}

// indentBy prefixes every line of s with pad, skipping the first line
// when first is false.
func indentBy(s, pad string, first bool) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i := range lines {
		if i == 0 && !first {
			continue
		}
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}
