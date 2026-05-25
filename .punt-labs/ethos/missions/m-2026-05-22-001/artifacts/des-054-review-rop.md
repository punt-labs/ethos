# DES-054 review — minimalism (rop)

**Verdict: ITERATE.** The unification is right. Three of the four primitives earn their keep. The predicate language as drafted does not — close it harder, or drop it from this DES.

## Form

One DES, three pressure tests, three findings. Read in order.

## 1. Is audited delegation one concept or five things shipped together?

One concept. The unification holds.

The DES lists five symptoms: parent linkage, prompt loss, contract bypass, precondition-impossibility, and durable-mission-state-in-git. Read with care, four of these are facets of one missing record. The fifth is a storage decision that travels alongside.

- Symptoms 1 (no parent link), 2 (no prompt), 3 (no contract), 4 (no preconditions) all want the same field: a typed handle on the `Agent(...)` spawn that connects parent context, child output, prompt, and contract. A delegation record carries all four. Removing any one of the four still leaves a thing the other three want to point at. They belong in one struct.

- Symptom 5 (state in git) is the storage-location decision. It is largely independent: ethos could have moved `~/.punt-labs/ethos/missions/` into `<repo>/.ethos/missions/` six months ago, without ever introducing delegations. The DES bundles it because the new layout has a natural home for delegations as a subdirectory; that is convenience, not necessity.

The DES would still be coherent if symptom 5 were extracted to a separate DES ("DES-054a: missions in-tree"). I do not recommend doing so — the cost of two coordinated migrations is higher than one — but the reader should know the seam exists. The minimalism question is whether **delegation** is one concept; it is. The minimalism question is *not* whether the **storage move** had to ride with it; it didn't, and that is fine.

The unification is correct: delegation is the missing noun. The other four entities (identity, mission, audit, session) all want to point at it.

## 2. Does the predicate language earn its keep?

No, as drafted. The closure is not closed.

The DES proposes:

```text
audit_contains_tool(Read)
audit_contains_path(<arg>, <pattern>)
audit_contains_path(file_path, ${tool_input.file_path})
```

Three lines. Two named forms. One substitution syntax (`${tool_input.file_path}`). The DES calls this "closed and small." It is not. The third form introduces a templating language that can read fields out of the gated tool call. Once you accept `${tool_input.file_path}`, the question of `${tool_input.path[0]}` and `${tool_input.path[1].name}` is six months away. A closed grammar must enumerate the *substitution sites*, not just the *predicate names*. The DES does not.

The contract's own claim is that four predicates are the floor. The draft only shows three forms, and they collapse pairwise:

- `audit_contains_tool(Read)` is `audit_contains_path(file_path, *)` with the tool fixed to Read.
- `audit_contains_path(file_path, ${tool_input.file_path})` is the same form with the pattern bound to a substitution.

Two predicates, one with an optional pattern that may be a literal or a substitution. If you read the three lines carefully you find one predicate with a knob. The "four predicates are the floor" argument is undefended in the draft. Name them.

Now the Pike question. Could one fixed predicate replace the lot? In the demonstrated case — "the judge must Read the PNG before writing the verdict" — yes. The fixed predicate is:

> Before any Write or Edit to a path in `write_set`, the delegate's audit log must contain a Read whose `file_path` matches every distinct file path referenced in the delegation's `prompt.md`.

It needs no grammar. It needs no substitution. The delegation already has `prompt.md` byte-for-byte. The hook scans the prompt for filesystem-looking tokens (you can lift the same canonical-path code already in `pretooluse.go`), intersects with what the delegate Read, and blocks Writes until the intersection is the whole set. One predicate. Zero contract syntax.

The case where one is insufficient — and where I will concede the floor must be higher than one — is the *output-shape* case, not the *input-read* case. Example: a contract that says "verdict YAML must declare `verdict: pass` only if the delegate's audit log shows the criteria checklist was Read." That is a content predicate, not an input predicate. It cannot be derived from the prompt; it requires the contract to name what the delegate must do.

So the floor is two, not four:

1. **must-read-inputs** — implicit, no syntax, derived from prompt tokens. Always on for any delegation under a contract that has a `write_set`.
2. **must-touch** — explicit, single form: `require_read: [<path-or-glob>]` in the contract. The delegate must have Read at least one matching path before any `Write`/`Edit` in `write_set`.

That is the closed set. Two predicates, no substitution language, no `${...}` syntax, no parser. If a third predicate is ever needed, it gets a DES of its own. The DES as drafted is a half-built grammar — it has the seeds of a language but pretends it doesn't. Either close it harder (the two above) or admit it is a language and design the language. The middle position the draft occupies is the one that always grows.

**Recommendation: replace the three forms with the two above. Remove `${tool_input.file_path}` substitution. Predicates that need richer matching wait for a future DES.**

## 3. Drop the synthesized ad-hoc contract and require every `Agent(...)` to declare?

Keep it. The ergonomics judgment is right; the draft's defense is weak.

The DES rejects the "always declare" alternative on ergonomics. I asked the contract for a concrete past mission shape that defeats the strict rule. The contract itself is one: m-2026-05-22-001 — the mission you are reading right now — spawns me as worker without me having a separate `Agent(...)` contract distinct from the mission contract. If every `Agent(...)` had to author a contract, the mission system would need to either (a) self-declare every dispatch as also being its own delegation contract — fine, but circular and verbose — or (b) refuse the spawn. Neither is the operator's expected experience.

A sharper case: the review-cycle Copilot/Bugbot fix rounds documented in the parent CLAUDE.md — *"review-cycle fix rounds use bare `Agent()`, not missions — these are mechanical fixes where the write-set is obvious"*. That is the canonical case the DES is designed for. The operator will not author a contract for a typo fix. They will spawn a bare Agent, get the typo fixed, and ship. Synthesis exists precisely so those calls are still legible to the audit trail without imposing the ceremony tax.

The DES has the right answer. Its defense ("ergonomics") is a one-word answer; promote it to a worked example. Cite mechanical fix rounds. Cite this review mission. The argument becomes "the most common Agent call in this codebase has no separately-authored contract" — which is the strongest possible form of the ergonomics argument.

The synthesized contract is also small. The struct literal is:

```go
Contract{Type: "task", WriteSet: nil, ExtractInto: nil,
  SuccessCriteria: []string{"delegation completed"},
  Budget: Budget{Rounds: 1, ReflectionAfterEach: false},
  Evaluator: Evaluator{Handle: "claude"}}
```

Six lines. Earns its keep.

**Recommendation: keep the synthesis; rewrite the rejected-alternative paragraph to cite review-cycle fix rounds as the canonical case.**

## Per-delegation flock — does it earn its keep?

Yes. The DES gets this right and the case is short. Concurrent delegations under one parent mission must not serialize on the parent's flock or the whole mission system loses its concurrency model. Per-delegation flock is the smallest correct primitive. No alternative cheaper.

One nit: the DES locates the per-delegation lock under `~/.punt-labs/ethos/delegations/<id>.lock` but locates the mission ID counter under `~/.punt-labs/ethos/missions/counter.yaml`. The concurrency table on line 207 says delegations go under `~/.punt-labs/ethos/delegations/`. The storage-layout diagram does not mention this directory. Reconcile the table and the diagram before merge.

## Rejected alternatives the DES missed

The DES's list of six rejected alternatives is good. Two more it did not enumerate:

1. **Make delegations reuse the existing session ID.** Skip the new ID space; use `session_id` as the delegation key. *Rejected*: a session can contain multiple `Agent(...)` calls (the leader spawns four reviewers in parallel). One session, many delegations. Session is not a delegation. The DES already says this on line 218 ("Both fields coexist") but does not frame it as a rejected alternative. Promote it.

2. **Express the delegation chain as a directed graph in a single repo-root JSONL.** One file, `<repo>/.ethos/delegations.jsonl`, append-only, every spawn writes one line. *Rejected*: violates the per-delegation directory layout the DES wants for `git log --` granularity, and forces every reader to scan the whole file to reconstruct one delegation's prompt + audit + result. The DES's directory-per-delegation layout is correct; this alternative is the one that "looks simpler on paper" but breaks the per-artifact lookup. Worth naming, because someone will propose it after the DES ships.

## Summary of named edits

The DES is close. Required edits before approval:

1. **Close the predicate language.** Replace the three predicate forms with the two-predicate floor argued above (`must-read-inputs` implicit; `require_read: [...]` explicit). Remove `${tool_input.file_path}` substitution. State explicitly that the predicate set is **two**, not "four supported forms."

2. **Defend ad-hoc contract synthesis with a worked example.** Cite review-cycle fix rounds and this review mission. One paragraph replacing the current one-line rejection of "always declare."

3. **Reconcile the delegation-lock directory.** The concurrency table says `~/.punt-labs/ethos/delegations/`; the storage-layout diagram does not show it. Add it to the diagram or correct the table.

4. **Add two rejected alternatives** to the existing six: "reuse session_id as delegation_id" and "single repo-root delegations.jsonl."

5. **Optional but recommended.** Note in the DES that symptom 5 (in-tree mission state) is logically separable from delegations 1–4 and is bundled here as a coordinated migration. This is honesty about the seam; it does not require changing the design.

Edits 1–4 are blocking. Edit 5 is editorial.

## On the operational test (for mcg)

Does the reduction actually work for real teams?

The two-predicate proposal:

- Works for the verdict-requires-Read case (the DES's worked example): the implicit `must-read-inputs` predicate scans `prompt.md` for the PNG path, intersects with the delegate's audit log, blocks the Write until matched. Zero contract syntax. The operator writes a mission contract; ethos derives the predicate.

- Works for the output-shape case (the `require_read` explicit form): the contract names the file the delegate must Read. One field. One list. No grammar.

- Fails — by construction — for predicates that look at the *content* of an audit entry beyond its path. That class of predicate is correctly deferred to a future DES.

The synthesized ad-hoc contract:

- Works for every bare `Agent(...)` call in the current codebase without operator action. The contract is auto-attached; the audit trail is whole; the leader writes nothing extra.

- Costs one struct literal at the hook layer. No new operator-facing surface.

The per-delegation flock:

- Costs one file descriptor per open delegation. Concurrent reviewers under one mission contract no longer serialize. The cost is real but bounded by the number of in-flight delegations — which the operator controls.

The unification:

- Reduces the operator's mental model from "five problems with subagents" to "one thing called a delegation." Teams onboarding to ethos read one section instead of five. That is the operational win the DES claims, and it survives the reduction.

The minimalism critique is operationally grounded: the predicate language as drafted is the only piece that fails the "does this actually need to ship now?" test. Closing it to two predicates removes the parser, removes the substitution syntax, removes a future-DES escape hatch, and still covers the demonstrated need.

## Status

ITERATE. The DES is one round away. With edits 1–4 applied, I would APPROVE.

— rop
