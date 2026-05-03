# Persistent Memory and Auditability — Design Starting Point

*Prep material for a forthcoming design effort on conversation and mission capture across Punt Labs tools. Replaces the earlier survey-with-recommendations. Settled decisions, open questions, and reference primitives — no premature solutioning.*

## TL;DR

1. **Conversations** (human + lead agent) and **missions** (lead agent + sub-agents) produce the same kind of artifact: a markdown document distilled from the raw event stream, scrubbed for secrets and profanity, committed to git in the same operation as the code it produced.
2. **Capture is an ethos feature.** The harness owns the hook; it does not live in `punt-labs/.bin/`. The scripts there are reference primitives, not the final home.
3. **Git is the primary store.** Quarry is rebuildable and may need to be reconstructed at any time.
4. **Quarry ingestion is out of scope for this design.** Repo-content ingestion is already configured; the new markdown files will be picked up automatically.
5. **Hook on git operations, not session lifecycle.** Specifically `pre-commit` (settled by spike — see below). The capture commits *with* the code, so the deliberation evidence is durable, signed, and audit-linked instead of dangling on someone's laptop.
6. **Sessions are 1:N with commits and PRs.** A single session may produce many commits and span multiple PRs. The capture file is replaced on each commit so HEAD always holds the full session-so-far.
7. **Reasoning and activity are preserved, not just outcomes.** Mission capture must include all three of: contract YAML, event log, and each round's worker JSONL — the worker's transcript is where the actual reasoning lives.

## Architecture

```
session events (human + lead)        mission events (lead + sub-agents)
    │                                       │
    │                                       ├─ contract YAML
    │                                       ├─ event log (rounds, results, reflections)
    │                                       └─ worker session JSONLs (per round)
    │                                       │
    └────────────┬──────────────────────────┘
                 │
                 ▼
       distill (jsonl-to-quarry-style:
       per-tool stubs, signal preserved verbatim)
                 │
                 ▼
       scrub (secrets + profanity → [REDACTED:<category>])
                 │
                 ▼
       write to .ethos/sessions/<session-id>.md
                or .ethos/missions/<mission-id>.md
       (replaced each commit; full content at HEAD)
                 │
                 ▼
       git stage + commit alongside the code change
                 │
                 ▼
       quarry auto-ingests via existing repo registration
```

## Settled

| | Decision |
|---|---|
| Owner | Ethos. Capture is a harness feature. |
| Format | Markdown. One file per session, one per mission. |
| Location | `.ethos/sessions/<session-id>.md`, `.ethos/missions/<mission-id>.md`. Tracked in git. |
| Replacement strategy | Replaced on each commit — HEAD always holds the latest full content. 1:N session → commits/PRs. |
| Mission inputs | All three: contract YAML, event log, per-round worker JSONLs. Reasoning is non-negotiable. |
| Capture trigger | `pre-commit` git hook. Settled by spike — see below. |
| Quarry coupling | None. Quarry will pick up files via repo ingestion. Out of scope for this design. |
| Backfill | User-invokable, idempotent command. Runs initially AND after each capture-process enhancement. |
| Reference primitives | `.bin/jsonl-to-quarry.py` and `.bin/scrub-pre-ingest.py` exist and work. Their *logic* lifts into ethos when the design lands; the CLIs can stay as standalone tools. |

## Spike result: pre-commit (settled)

A throwaway harness in `.tmp/spike-hook/` ran the real distill+scrub pipeline behind three hook variants — pre-commit, sync post-commit, async post-commit — across 8 representative commits per mode (5 small, 2 medium, 1 large), plus deliberate failure-mode probes. Full report: `.tmp/spike-hook-timing.md`.

**Headline numbers** (Apple M2, warm cache, 38 MB input JSONL → 2.9 MB markdown):

| Mode | Commit wall (mean) | Total wall | Lag | Commits per code change |
|---|---|---|---|---|
| Bare git (no hook) | 0.051s | — | — | 1 |
| **pre-commit** | **1.244s** | **1.244s** | **0** | **1** |
| sync post-commit | 1.257s | 1.257s | 0 | 2 |
| async post-commit | 0.072s | 1.232s | 1.160s | 2 |

Capture cost is dominated by the Python pipeline (~1.15s) and is **insensitive to commit size** — a 30-file 1.5 MB commit costs the same as a single 1 KB edit.

**Failure modes (3/3 tested)** — bad input path, hook script not executable, output dir read-only:

- **pre-commit** aborts the user's commit with a non-zero exit code on every failure (except the inherent git-hook `chmod -x` skip). User sees the failure immediately and must fix.
- **post-commit (both variants)** lets `git commit` succeed with rc=0 while silently dropping the capture on every failure. No signal to the user. Corrupted historical record under the appearance of success.

**Why pre-commit wins** despite costing the user 1.24s vs 0.07s on async post-commit:

1. Cost is the wrong axis to optimize. The right axis is *correctness of the captured record*. Atomic guarantee — code commits with capture or doesn't commit — is the entire point.
2. Async post-commit hides the cost behind a 1.16s window in which the working tree is dirty (unmcommitted capture file present). Subsequent user actions race with the in-flight hook child.
3. Both post-commit variants double commit count (alternating `code` and `capture: …`), making `git log`, `git blame`, and `git revert` noisier and more error-prone.
4. Recursion-guard complexity: post-commit fires on merge, amend, and rebase too. Pre-commit doesn't.

**The 1.24s/commit is real cost** that will be felt on every commit. If unacceptable, the path forward is making the Python pipeline faster (Python startup × 2 + JSONL parse + regex scrub all live in that 1.15s — not yet decomposed). It is not making the cost invisible via async.

## Open for design (driven by data, not opinion)

### 1. Mission markdown representation

Three structured inputs (contract YAML, event log, N worker JSONLs) need to combine into one cohesive readable document. Open structural questions:

- Front matter vs. inline section for the contract YAML
- How rounds are delimited (heading per round? horizontal rules?)
- How worker reasoning interleaves with leader framing — leader's commentary as outer narrative, worker's transcript as quoted/embedded; or alternating sections; or worker transcript folded into a `<details>`-style collapsible
- How reflections render (per-round footer? top-of-document summary? both?)
- How a re-tried round (failure → revised contract → re-spawn) shows up — full new section, or amendment to the previous round?
- How to keep the document readable at 30+ rounds

Worth sketching 2-3 candidate layouts on a real (small) mission before committing to one. Defer the choice until a representative mission exists to design against.

### 2. Backfill semantics

"User-invokable, idempotent, re-runs after enhancement" — the precise meaning is open:

- Full overwrite of every existing capture file, or only changed ones? How is "changed" detected — content hash of the source JSONL? Schema version of the captures themselves?
- What about sessions whose code is on a branch that was never merged? Branch deleted? Squashed?
- What about sessions on the `main` history that pre-date the capture system being installed? Map by timestamp? By cwd metadata?
- What about sessions whose author was a sub-agent (mission worker), already captured as part of that mission's document — do they also need a standalone session capture, or is the mission document the canonical home?
- Re-running after a *scrubber* enhancement: do older captures get re-scrubbed in place? What if the older capture committed a secret that the new scrubber would catch?
- Re-running after a *distiller* enhancement: same question for noise/signal classification changes.

### 3. Hook performance optimization

Pre-commit costs the user 1.24s per commit. That is dominated by the Python pipeline (jsonl-to-quarry + scrub-pre-ingest in sequence; ~50ms is git itself). If that latency is unacceptable in practice, paths forward worth measuring:

- **Decompose the 1.15s** into Python startup × 2, JSONL parse, distill, scrub. The spike did not break it down. First measurement worth doing once optimization is on the table.
- **Avoid forking twice.** The current pipeline is two separate `python3` invocations chained by a shell pipe. A combined entrypoint that imports both modules and runs them in one Python process would save one startup (~50–100ms typical).
- **Avoid re-reading the entire JSONL on each commit.** A long session may have grown by only a few records since the last commit; an incremental distiller that consumes only new records and appends to the markdown could collapse the per-commit cost dramatically.
- **Background the pipeline behind an in-process daemon.** A long-lived Python process (managed by ethos) with hot regex caches and pre-loaded session state would respond in tens of milliseconds. Adds operational complexity.
- **Native rewrite.** Go or Rust port of the distill+scrub primitives. Highest ceiling, biggest investment.

These are options, not commitments. The right time to optimize is when the 1.24s is felt as a problem, not before.

### Other open

- **Multi-repo missions.** A single mission's write set may span multiple repos via cross-repo coordination. Where does its capture file live — the leader's repo? The first-touched repo? All of them? In `.punt-labs/ethos/missions/` (the team submodule)?
- **Privacy.** Should the user be able to mark a session or mission `--private` to suppress capture entirely? What about tool-result-level redaction beyond the regex scrubber (e.g., a particular Read of a sensitive file)?
- **Encryption at rest.** Captured markdown is plaintext on disk. Acceptable? Or should `.ethos/sessions/` be git-crypt'd or similar?
- **Session-id discovery.** Hook must know the active session id to name the output file. How does the hook find that — env var, runtime API, parsing `~/.claude/projects/`?
- **Worker JSONL discovery.** Same question for missions — given a mission id, where does ethos find the worker session JSONLs that belong to its rounds?

## Reference primitives (proven, ready to lift)

| Primitive | What it does | Performance |
|---|---|---|
| `.bin/prune-history.sh` | In-place truncation of noisy fields in a JSONL. Archival, not capture. | ~13–17% reduction. Linear. |
| `.bin/jsonl-to-quarry.py` | JSONL → markdown distillation with per-tool stubs. Preserves user prompts, assistant text, thinking, tool inputs, sub-agent reports. Compresses Read/Grep/Bash bodies that are re-derivable. | ~80 MB/s after Python startup. 11 ns/byte CPU. Sub-second on every real session. 88–92% size reduction. |
| `.bin/scrub-pre-ingest.py` | Secret + profanity redaction. Idempotent. Categories: gh-pat, aws-access-key, aws-secret-key, anthropic-key, openai-key, bearer, jwt, pem-private-key, gpg-private-key, env-secret, slack-token, profanity. | Regex-driven; same order of magnitude as the distiller. Not benchmarked end-to-end. |
| `.bin/test_jsonl_to_quarry.py`, `.bin/test_scrub_pre_ingest.py` | Test patterns covering the markdown shape, signal preservation, redaction categories, idempotence, CRLF, and structural invariants. | — |

These are the proven shapes. They don't need to be re-implemented when ethos integrates capture — the logic can be imported as a Python library facade, or the scripts can be invoked as subprocesses. Decision lives with the ethos team.

## Surfaces deliberately out of scope for this design

The earlier survey covered all persistent-memory surfaces across the org — biff messages, vox audio, recap emails, GitHub PRs/Actions, ethos team registry, beads JSONL/SQLite. This redirected design is narrower: it covers *only* conversation and mission capture. The other surfaces are listed below for reference but their design status is unchanged.

| Surface | Current state | Out-of-scope reason |
|---|---|---|
| Beads (`bd`) | SQLite + JSONL daemon-export to git. Working well. | Daemon-export pattern is the model this design extends, not a thing to redesign. |
| Recap emails | Sent to jim@punt-labs.com via beadle. External, Proton Mail-durable. | Provider-managed; orthogonal to in-repo capture. |
| Biff `/talk`, `/wall`, messages | Relay-mediated (NGS). Persistence model uncertain. | Different scope; revisit separately. |
| Vox audio + spoken decisions | Cache + ephemeral. | Same — separate scope. |
| GitHub PRs / Actions / review threads | External, GitHub-managed retention. | External-tier; not subject to in-repo design. |
| Ethos identity registry (`team` submodule) | In-git, version-controlled. | Already correctly placed. |

## What I should have done differently

The earlier draft of this doc led with "three durability gaps" framing and recommended *minimizing* what goes into git ("track activity, not content"). That was a projection of my own conservatism, not informed by what Punt Labs actually wants. The pipeline shipped over the last day (`prune-history.sh` retune, `jsonl-to-quarry.py`, `scrub-pre-ingest.py`) is investment in *more* capture, with redaction as the safety mechanism that lowers the cost of in-git storage. The new framing follows that direction: capture more, scrub well, commit with the code, let the audit chain be structural rather than procedural.

This doc replaces the earlier framing. The "three durability gaps" framing should not be cited.

## Next concrete steps

This is exploration prep, not a build queue. The real design effort is yet to happen. Inputs that effort needs from this doc:

1. The architecture sketch (above)
2. ~~Spike data on hook timing~~ — done. `.tmp/spike-hook-timing.md`. Verdict: pre-commit, with a 1.24s per-commit cost that becomes a future optimization candidate if felt as a problem.
3. 2–3 sketched layouts for mission markdown structure — pending a representative mission to design against
4. A precise definition of backfill idempotence semantics — pending review of how schema versioning would work

Drive the design when ready; this doc and the spike output will be inputs.
