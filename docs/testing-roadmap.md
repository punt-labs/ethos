# Ethos Testing Roadmap

Concrete implementation sequence for the five-level test pyramid
described in `docs/testing-strategy.tex`.

---

## Current State (v3.9.0)

- **2,199 tests** across 16 packages
- **24.4 KLOC production Go, 38.2 KLOC test Go** (1.56:1 test-to-production ratio)
- L1 content validation, L2 CLI subprocess, L3 MCP integration, L4 behavioral: all shipped in v3.1.0
- CI coverage reporting wired (`-coverprofile`, summary in CI)
- 8 behavioral scenarios: 4 deterministic (Layer A), 2 LLM-judged (Layer B), 2 adversarial (Layer C)
- Daily behavioral CI via `.github/workflows/behavioral.yml`
- v3.2.0 added archetypes (7 types), pipelines (3 templates), archetype validation, 10 lint heuristics
- v3.3.0 expanded to 8 pipeline templates with nature-based H10 decision tree
- v3.4.0 added pipeline instantiate, archetype constraint enforcement, inputs.ticket rename, 24 pipeline CLI tests, PostToolUse exit code fix
- v3.5.0 added automatic mission traceability (TraceSummary, appendTraceSummary, flock), deprecation warning dedup (sync.Once), integration test for Close-to-JSONL path

---

## Phase 1 — Content Validation (L1) — SHIPPED v3.1.0

**Bead**: `ethos-mhs`
**Shipped**: PR #231 (1532238)

### What shipped

1. `make validate-content` target walks both the submodule root and the consuming repo's `.punt-labs/ethos/`, instantiates real `Store` objects, and calls every existing validator: `Identity.Validate()`, `team.Validate()`, `attribute.ValidateSlug`, `ValidateStructural`.
2. `Store.Warnings` treated as errors — referential integrity failures are gate failures.
3. Duplicate handle detection, agent binding path resolution, slug format checks, markdown non-empty checks.
4. Wired into `make check` and CI.

### What it catches that nothing else does

- Identity references a personality slug that doesn't exist
- Team member handle typo (e.g., `sded` instead of `sdet`)
- Collaboration type misspelled
- Empty personality file committed

---

## Phase 2 — CLI Subprocess Tests (L2) — SHIPPED v3.1.0

**Bead**: `ethos-oto`
**Shipped**: 72592a9, plus handler tests in ethos-a1m (2f6ae9d)

### What shipped

`cmd/ethos/subprocess_test.go` follows the exact pattern from `internal/hook/subprocess_test.go`: compile binary in `TestMain`, spawn with controlled `HOME`/`USER`/git config.

Coverage targets met:

| Test | Assertion |
|------|-----------|
| `ethos whoami` with git config set | exit 0, name in output |
| `ethos whoami` with no identity | exit 1, error message to stderr |
| `ethos list` with populated store | exit 0, valid JSON |
| `ethos list` with empty store | exit 0, empty JSON array |
| `ethos show <handle>` valid | exit 0, handle in output |
| `ethos show missing-handle` | exit 1, stderr message |
| `ethos bogus-command` | exit 1, stderr message |
| `ethos --help` | exit 0 |
| `ethos doctor` | exit 0, table output with expected checks |
| `ethos iam <persona>` idempotent | second call exits 0, same session file |
| `ethos generate-agents` | creates `.claude/agents/*.md`, idempotent |
| `ethos mission` with open contract | exit 0, contract in output |
| `ethos mission --status=closed` | exit 0, no open contracts |

Also shipped: full coverage for `internal/doctor/` (was zero), RunE refactor for all CLI handlers (ethos-90e, ethos-yxk, ethos-2xz, ethos-aeb), zero `Run:` declarations remaining in `cmd/ethos/`.

---

## Phase 3 — MCP Integration Tests (L3) — SHIPPED v3.1.0

**Bead**: `ethos-764`
**Shipped**: a072d2b

### What shipped

`internal/mcp/integration_test.go` with a test helper that spawns `ethos serve` as a subprocess, sends JSON-RPC `tools/call` requests via stdin, reads responses from stdout using the `mark3labs/mcp-go` `StdioTransport` client.

Coverage targets met:

| Tool | Scenario |
|------|----------|
| `identity whoami` | Resolves from process context in the real binary |
| `identity` create/list/get | Filesystem write + read round-trip |
| `ext` set/get/del | Namespace isolation: two handles don't bleed |
| `session` roster | Auto-discovery via real subprocess PID |
| `mission` open/close | Status filter through wire protocol |
| `doctor` | All 4 checks present in table output |
| Error path | Invalid tool name returns `isError: true` |

### What this catches that L2 misses

- MCP serialization bugs (wrong JSON field names, missing `isError` flag)
- Session auto-discovery from a real subprocess PID (not a mocked one)
- Signal handling around `ethos serve` startup and shutdown

---

## Phase 4 — Agent Behavioral Tests (L4) — SHIPPED v3.1.0

**Bead**: `ethos-fal`
**Shipped**: 9bee971
**Architecture**: DES-043 (three-layer behavioral test architecture) in `DESIGN.md`

### What shipped

Three-layer behavioral test infrastructure behind a `//go:build behavioral` tag:

- **Layer A (deterministic)**: 4 scenarios. Mission event log, git diff, result YAML structure. No LLM calls. Catches protocol violations.
- **Layer B (LLM-judged)**: 2 scenarios. Agent output + persona definition sent to Claude Sonnet as judge. Returns `{violated, evidence, confidence}`. Catches persona constraint violations.
- **Layer C (adversarial)**: 2 scenarios. Deliberately tempts agents to break constraints. Combines deterministic + judge assertions. Proves the system holds under pressure.

Run via `make test-behavioral` (requires `ANTHROPIC_API_KEY` and `claude` CLI). Daily CI via `.github/workflows/behavioral.yml`.

### CI wiring

- Daily job, not per-commit
- `ANTHROPIC_API_KEY` required in CI environment
- Cost ceiling: `--max-budget-usd 0.10` per scenario
- `confidence < 0.8` → escalate to manual review, not auto-fail

---

## Phase 5 — Sprint Team Integration (L5) — PLANNED

**Bead to file**: `ethos-L5-sprint-integration`
**Effort**: 5 days
**Frequency**: per release (not per commit)

### Infrastructure deliverables

1. **Fixture repo**: `ethos-sprint-fixture` containing:
   - `pkg/counter/counter.go` with two seeded bugs (off-by-one in `Increment`, nil pointer in `Reset`)
   - `pkg/counter/counter_test.go` covering neither bug
   - `DESIGN.md` with an ADR in `PROPOSED` status
   - Mission contract YAML: `leader=claude`, `worker=bwk`, `reviewer=djb`, `write_set=[pkg/counter/*.go, DESIGN.md]`

2. **Test harness**: `scripts/run-sprint-test.sh` — sequences three Claude invocations:
   1. `sprint-architect` reviews the fixture and briefs the implementer
   2. `bwk` implements fixes and tests
   3. `djb` reviews the diff and reports findings

3. **Post-run checks**: automated assertions (from `testing-strategy.tex` section 5):
   - Both bugs fixed in committed diff
   - Only `bwk` committed Go files
   - `djb` audit log shows zero `Edit`/`Write` calls against `.go` files
   - `go test ./pkg/counter/...` passes
   - `DESIGN.md` contains `SETTLED`

### Release checklist integration

Add to the ethos release checklist (before tagging):

```text
[ ] Run make test-sprint and confirm all 5 checks pass
[ ] Record session IDs in the release notes for audit reference
```

---

## Phase 6 — Payload / Token Profiling (L6) — PLANNED

**Bead**: `ethos-l90t` (filed 2026-08-14)
**Effort**: 5-7 days for framework + baseline capture (4-6 scenarios)
**Frequency**: per release + on-demand (not per-commit)
**Depends on**: LiteLLM (`pip install 'litellm[proxy]==1.81.9'`) as the local test-harness proxy — MIT-licensed, already the standard tool for this pattern. NOT Bifrost — CI must run without any workstation-only proxy dependency.

**Version pin**: LiteLLM releases newer than `1.81.9` fail to boot on Python 3.12+ with `ImportError: cannot import name 'get_flat_dependant' from 'fastapi.dependencies.utils'` — FastAPI removed the symbol; LiteLLM's proxy still imports it. `1.81.9` boots cleanly. Revisit when a newer LiteLLM restores the import or caps its FastAPI ceiling.

**Hello world proof-of-integration shipped** at `tests/token-harness/hello/`. `make test-tokens-hello` starts the LiteLLM proxy, invokes `claude --print` against it, captures the full request body (~437K, 135 tools, 3-part system prompt, 2 messages), and prints a char-count attribution preview. Documented in `tests/token-harness/hello/README.md`. Not the full L6 framework (no tokenizer, no attribution parser, no baseline diff), but proves every primitive Phase 6 depends on is buildable.

### Why now

Ethos treats the model's context window as free. Two cost drivers are
already known from Bifrost-observed payloads:

1. **Team-submodule aggregation** — consumers vendoring the whole
   `team` submodule ship the full engineering identity closure on
   every session, whether or not the session ever needs those
   agents. DES-057's `ethos vendor` produces a minimal snapshot but
   nothing measures which consumers are still aggregated.

2. **Duplication** — the same identity content appears in the
   SessionStart persona block, the generated `.claude/agents/*.md`
   file, and (post-compact) in the PreCompact team-context section.
   Bytes counted three times are paid for three times.

L1-L5 don't catch either — content validation doesn't measure size,
CLI/MCP tests don't exercise the payload path, behavioral tests
verify correctness not cost, sprint integration verifies flow not
cost. L6 is the missing observability tier.

### Infrastructure deliverables

1. **Harness config**: `tests/token-harness/litellm.yaml` declares
   (a) an Anthropic-compatible model entry that returns a canned
   assistant turn — via `mock_response` if supported at
   proxy-config level, otherwise via a small
   `CustomLogger.async_pre_call_hook` that short-circuits before
   any upstream call; and (b) a
   `CustomLogger.async_post_call_success_hook` that dumps
   `proxy_server_request` (the full JSON body Claude Code sent) to
   `.tmp/token-captures/<scenario>.jsonl`. Test runner starts the
   proxy as a subprocess via
   `litellm --config tests/token-harness/litellm.yaml --port <ephemeral>`.
   Claude Code points at the proxy via `ANTHROPIC_BASE_URL` +
   `ANTHROPIC_AUTH_TOKEN` — LiteLLM's own Claude Code quickstart
   documents this exact wiring, and both the unified `/v1/messages`
   and pass-through `/anthropic/*` routes are natively served.
   No bespoke proxy code.
2. **Attribution parser**: `cmd/token-attribute/` — reads a LiteLLM
   capture file and slices the assembled system prompt by
   ethos-injected markers (`## Personality`, `## Writing Style`,
   `## Team Context`, generated-agent-manifest signature,
   per-MCP-server tool schemas). Uses persona-block markers already
   defined by `internal/hook/persona.go`.
3. **Tokenizer path (offline-only for CI)**: pin a local Claude-
   compatible tokenizer — Anthropic first-party if it ships one for
   the current model family, else `tiktoken` with an "approximate,
   calibrated against Anthropic's tokenizer" disclaimer stamped
   into every report. Anthropic's `messages/count_tokens` endpoint
   is NOT used for CI (requires live API key + network); it stays
   as an operator-invoked calibration tool via `make
   calibrate-tokens`.
4. **Fixture scenarios**: YAML per scenario in
   `tests/token-scenarios/`, minimum set:
   - `empty-repo` — Claude Code baseline, no ethos.
   - `ethos-single-agent` — one identity closure.
   - `ethos-team-submodule` — full team-submodule aggregation cost.
   - `ethos-vendored-minimal` — DES-057 `ethos vendor --all` snapshot.
   - `ethos-rich-mcp` — multi-MCP tool-schema cost.
   - `ethos-post-compact` — PreCompact hook fired, context
     restoration cost.
5. **Baseline captures**: `tests/token-baselines/<scenario>.json`
   committed. Recaptured via `make baseline-tokens`
   (operator-invoked on a controlled workstation, not automatic).
6. **Reporting**: `make test-tokens` boots LiteLLM, runs every
   scenario, tokenizes captures, writes reports to
   `.tmp/token-reports/`, compares to baseline, and prints a delta
   summary alongside the attribution tree. Exits 0 always; deltas
   are surfaced via PR comment, not the exit code.
7. **CI wiring**: per-release GitHub Actions job installs LiteLLM
   (`pip install 'litellm[proxy]'`) and runs `make test-tokens`
   against committed baselines. NO Anthropic API key needed —
   LiteLLM's mock provider returns a canned turn. Flags per policy:
   - Delta > 5% on any scenario → PR comment, human review before
     merge.
   - Delta > 15% → CI turns yellow, blocks auto-merge memory but
     not the merge button.
   - New source of tokens (a section not in the last baseline) → PR
     comment, no gate.
8. **Calibration recipe**: `make calibrate-tokens` — separate target
   (not CI) that re-runs a chosen scenario against Anthropic's
   `messages/count_tokens`, compares to the offline tokenizer's
   number, updates the calibration disclaimer if delta is
   significant. Operator-invoked, human reviewed.

### Operator policy calls needed before Phase 6 dispatch

1. **In-tree fixtures vs separate repo?** Simpler to start in-tree
   under `tests/token-scenarios/`; extract to a separate
   `ethos-token-fixture` repo (parallel to the L5 sprint fixture)
   only if scenarios grow.
2. **Regression threshold**: absolute (fail if > Y tokens) or
   relative (fail if > X% delta)? Recommend relative for now (5%
   comment, 15% yellow), revisit after first release cycle of data.
3. **Baseline recapture cadence**: recapture on every ethos release
   automatically, or only when a change touches persona/agents/hooks?
   Recommend automatic every release (bounds drift), manual
   recapture on-demand for suspected regressions.
4. **What counts as ``the'' ethos payload**: only ethos-injected
   content, or the entire session payload? Recommend the entire
   payload with per-source attribution — the goal is total cost
   observability, ethos is one contributor among several (MCP
   servers, tool descriptions, message history).

### Testing coverage that Phase 6 adds

- Fleet-wide answer to ``which repo pays the highest cost per
  session''.
- Per-release delta on ethos's own contribution to a representative
  session payload.
- Attribution of unexpected cost growth to a specific ethos
  component (persona-block section, generated-agents manifest,
  additionalContext, MCP tool schema).
- Regression detection when a refactor accidentally duplicates
  content across the persona block, the generated agent, and the
  PreCompact restoration.

### Related design context

- The bloat-drivers-observed section in `docs/testing-strategy.tex`'s
  L6 section carries the concrete evidence.
- `bd show ethos-livi` covers the vendored-vs-submodule resolution
  work, which is complementary — DES-057's vendor produces a
  minimal snapshot, L6 measures whether that reduction actually
  reaches the wire.
- Bifrost (workspace `.envrc.local` via `CLAUDE_PROVIDER=bifrost`,
  toggle script in `~/.local/bin/`) is a workstation-only eyeball
  tool for interactive local inspection. It is NOT the L6 harness
  — L6 must run in CI without external dependencies.

---

## CI Coverage Reporting — SHIPPED v3.1.0

**Bead**: `ethos-mhs`
**Shipped**: 7dc216f

Added `-coverprofile=coverage.out` to `make test` and CI summary reporting. Coverage regressions visible on every PR.

---

## Post-Roadmap Status

**v3.1.0** (April 12, 2026) shipped L1 through L4 of the test pyramid plus RunE refactor, ext bug fix, mission features (lint, walked-diff, PreToolUse allowlist), and verifier read policy. Coverage went from 63.5% to 75.6%.

**v3.2.0** (April 12, 2026) shipped archetypes (7 types), pipelines (3 sprint templates), the `Type` field on mission contracts, archetype validation, and 10 lint heuristics. No new test levels; test infrastructure stabilized.

**v3.3.0** (April 13, 2026) expanded pipelines from 3 to 8 templates with a nature-based H10 decision tree for template selection. No new test levels.

**v3.4.0** (April 14, 2026) shipped pipeline instantiate, archetype constraint enforcement (allow_empty_write_set, write_set_constraints, required_fields), inputs.bead to inputs.ticket rename with back-compat, 8 built-in pipeline templates with {feature}/{target} defaults, PostToolUse exit code propagation, and 24 pipeline CLI tests. Coverage went from 75.6% to 77.1%.

**v3.5.0** (April 14, 2026) shipped automatic mission traceability (`Store.Close` auto-appends summary JSONL to `<repo>/.punt-labs/ethos/missions.jsonl`, DES-050), deprecation warning deduplication via `sync.Once`, and an integration test for the Close-to-JSONL path.

**v3.6.0** (April 15, 2026) shipped mission dispatch one-liner, resilient conflict scan, `inputs.trigger` schema, and doctor orphan check (Phase 5 items).

**v3.7.0** (April 15, 2026) shipped team bundle activation (Phase 6.1): three-layer stores (repo, active bundle, global), bundle resolver, five CLI commands (`available`, `activate`, `active`, `deactivate`, `add-bundle`), embedded gstack bundle (5 pipeline templates), and `ethos team migrate` (removed post-ship, see DESIGN.md's DES-051 reversal note).

**v3.8.0** (April 15, 2026) fixed all core pipelines' review stages to use the `report` archetype.

**v3.9.0** (April 16, 2026) shipped `ethos setup` interactive wizard and the foundation bundle (4-agent general-purpose team). Quick Start reduced from 12 steps to 2. 2,199 tests across 16 packages, 24.4 KLOC production Go, 38.2 KLOC test Go.

L5 sprint integration tests remain the sole unimplemented phase. The pipeline instantiate primitive shipped in v3.4.0; L5 depends on fixture repo construction and harness authoring.

---

## Summary

| Phase | What shipped | Status | Version |
|-------|-------------|--------|---------|
| 1 — L1 Content Validation | `make validate-content`, CI wiring | SHIPPED | v3.1.0 |
| 2 — L2 CLI Subprocess | `cmd/ethos/subprocess_test.go`, doctor coverage, RunE refactor | SHIPPED | v3.1.0 |
| 3 — L3 MCP Integration | `internal/mcp/integration_test.go` | SHIPPED | v3.1.0 |
| CI coverage | `-coverprofile` in `make test`, CI summary | SHIPPED | v3.1.0 |
| 4 — L4 Behavioral | Harness, Layer A/B/C scenarios, daily CI workflow | SHIPPED | v3.1.0 |
| 5 — L5 Sprint Integration | Sprint fixture repo, harness, post-run checks | PLANNED | — |
| 6 — L6 Payload / Token Profiling | LiteLLM capture proxy, attribution parser, offline tokenizer, baseline scenarios, `make test-tokens` | PLANNED | — |
