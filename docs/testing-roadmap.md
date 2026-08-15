# Ethos Testing Roadmap

Concrete implementation sequence for the five-level test pyramid
described in `docs/testing-strategy.tex`.

Phase numbers below describe rollout order (when a phase was built).
Tier numbers (L1-L5) describe pyramid position (dependency cost). The
two don't have to match — Phase 6 delivers the L4 tier, built after
Phase 4's L5 tier because scheduling followed available specialists,
not pyramid order.

---

## Current State (v3.9.0)

- **2,199 tests** across 16 packages
- **24.4 KLOC production Go, 38.2 KLOC test Go** (1.56:1 test-to-production ratio)
- L1 content validation, L2 CLI subprocess, L3 MCP integration, L5 end-to-end agent behavioral: all shipped in v3.1.0
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

## Phase 4 — End-to-End Agent Tests (L5) — SHIPPED v3.1.0

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

## Phase 6 — Wire Observability Test Framework (L4) — SHIPPED

Framework design: `docs/design-e2e-test-framework.md`.

L4 is the wire-observability tier — real `claude --print` subprocess
against a locally-run mock upstream (LiteLLM proxy). Token/payload
profiling is the first assertion suite this tier carries and the
reason it was built; hallucination checks, feature-verification
runs, and future scenarios plug in through the same
`pytest_generate_tests` collector.

**Bead**: `ethos-l90t` (filed 2026-08-14)
**Depends on**: LiteLLM (`litellm[proxy]==1.81.9`) as the local test-harness proxy — MIT-licensed, already the standard tool for this pattern. NOT Bifrost — CI runs without any workstation-only proxy dependency.

**Version pin**: LiteLLM releases newer than `1.81.9` fail to boot on Python 3.12+ with `ImportError: cannot import name 'get_flat_dependant' from 'fastapi.dependencies.utils'` — FastAPI removed the symbol; LiteLLM's proxy still imports it. `1.81.9` boots cleanly. Revisit when a newer LiteLLM restores the import or caps its FastAPI ceiling.

### What shipped

- `tests/e2e/` — a scoped `uv` project (src-layout): `LiteLLMProxy` (session-scoped mock upstream, one litellm model entry per scenario keyed by scenario id), `Scenario`/`TokenScenario`/`ScenarioRegistry` (yaml scenario parsing), `ScenarioCapture`/`TokenCapture` (structural, size-ratchet, and secret-scan assertions), `custom_callbacks.py` (the LiteLLM `CustomLogger` that writes each captured request to disk), `run_scenario` (env-cages and invokes `claude --print` against the proxy).
- `conftest.py`'s `pytest_generate_tests` hook globs `tests/e2e/scenarios/*.yaml` at collection time — dropping a new scenario file needs zero conftest/Makefile/workflow edit.
- `tests/e2e/scenarios/empty-repo.yaml` — the first (smoke) scenario, absorbing every assertion the retired `tests/token-harness/hello/` proof-of-integration carried (structural shape, the 700KB payload ratchet, the nine-pattern secret scan).
- `cmd/e2e-attribute/` — a Go skeleton reading one capture file and reporting byte-level attribution (system prompt sliced by the `internal/hook/persona.go` markers, tool-schema bytes per tool, message bytes). Tokenizing and baseline diffing are deferred pending the tokenizer decision below.
- `make test-e2e-smoke` (every push) and `make test-e2e` (per-release) targets; `make lint` also runs `ruff`/`mypy` against `tests/e2e/` on every commit.
- `.github/workflows/test.yml`'s `e2e-smoke` (push/PR) and `e2e-release` (tag push or `workflow_dispatch`) jobs, replacing the retired `token-harness-hello` job. Neither job's YAML lists scenario names.
- `tests/token-harness/hello/`, the ad-hoc proof-of-integration, is retired — every assertion it made carries forward into `empty-repo.yaml`.

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

L1-L3 and L5 don't catch either — content validation doesn't measure
size, CLI/MCP tests don't exercise the payload path, end-to-end agent
tests verify correctness not cost. L4 is the missing observability
tier.

### Tokenizer (blocking prerequisite for baselines)

Design §6 flags the Anthropic first-party Python tokenizer question
as unverified. Initial land uses `tiktoken` (installed, not yet
wired to a report field) with a calibration disclaimer; the
attribution report's token fields are `TODO` until this is resolved.
`make baseline-tokens` and `make calibrate-tokens` land as stubs.

### Testing coverage that Phase 6 adds

- Fleet-wide answer to ``which repo pays the highest cost per
  session''.
- Per-release delta on ethos's own contribution to a representative
  session payload (once baselines land).
- Attribution of unexpected cost growth to a specific ethos
  component (persona-block section, generated-agents manifest,
  additionalContext, MCP tool schema).
- Regression detection when a refactor accidentally duplicates
  content across the persona block, the generated agent, and the
  PreCompact restoration.

### Related design context

- The bloat-drivers-observed section in `docs/testing-strategy.tex`'s
  Level 4 section carries the concrete evidence.
- `bd show ethos-livi` covers the vendored-vs-submodule resolution
  work, which is complementary — DES-057's vendor produces a
  minimal snapshot, L4 measures whether that reduction actually
  reaches the wire.
- Bifrost (workspace `.envrc.local` via `CLAUDE_PROVIDER=bifrost`,
  toggle script in `~/.local/bin/`) is a workstation-only eyeball
  tool for interactive local inspection. It is NOT the L4 harness
  — L4 runs in CI without external dependencies.

---

## CI Coverage Reporting — SHIPPED v3.1.0

**Bead**: `ethos-mhs`
**Shipped**: 7dc216f

Added `-coverprofile=coverage.out` to `make test` and CI summary reporting. Coverage regressions visible on every PR.

---

## Post-Roadmap Status

**v3.1.0** (April 12, 2026) shipped the first four implemented tiers of the test pyramid (content validation, CLI behavior, MCP server behavior, end-to-end agent behavioral) plus RunE refactor, ext bug fix, mission features (lint, walked-diff, PreToolUse allowlist), and verifier read policy. Coverage went from 63.5% to 75.6%.

**v3.2.0** (April 12, 2026) shipped archetypes (7 types), pipelines (3 sprint templates), the `Type` field on mission contracts, archetype validation, and 10 lint heuristics. No new test levels; test infrastructure stabilized.

**v3.3.0** (April 13, 2026) expanded pipelines from 3 to 8 templates with a nature-based H10 decision tree for template selection. No new test levels.

**v3.4.0** (April 14, 2026) shipped pipeline instantiate, archetype constraint enforcement (allow_empty_write_set, write_set_constraints, required_fields), inputs.bead to inputs.ticket rename with back-compat, 8 built-in pipeline templates with {feature}/{target} defaults, PostToolUse exit code propagation, and 24 pipeline CLI tests. Coverage went from 75.6% to 77.1%.

**v3.5.0** (April 14, 2026) shipped automatic mission traceability (`Store.Close` auto-appends summary JSONL to `<repo>/.punt-labs/ethos/missions.jsonl`, DES-050), deprecation warning deduplication via `sync.Once`, and an integration test for the Close-to-JSONL path.

**v3.6.0** (April 15, 2026) shipped mission dispatch one-liner, resilient conflict scan, `inputs.trigger` schema, and doctor orphan check — mission/pipeline infrastructure originally scoped for the Sprint Team Integration effort, reused independently of it.

**v3.7.0** (April 15, 2026) shipped team bundle activation (Phase 6.1): three-layer stores (repo, active bundle, global), bundle resolver, five CLI commands (`available`, `activate`, `active`, `deactivate`, `add-bundle`), embedded gstack bundle (5 pipeline templates), and `ethos team migrate` (removed post-ship, see DESIGN.md's DES-051 reversal note).

**v3.8.0** (April 15, 2026) fixed all core pipelines' review stages to use the `report` archetype.

**v3.9.0** (April 16, 2026) shipped `ethos setup` interactive wizard and the foundation bundle (4-agent general-purpose team). Quick Start reduced from 12 steps to 2. 2,199 tests across 16 packages, 24.4 KLOC production Go, 38.2 KLOC test Go.

Sprint Team Integration (Phase 5) was retired without ever being implemented — no fixture repo, no harness, no CI job ever landed under that name. Phase 6 (Wire Observability, L4) shipped in its place as the next tier built, occupying a different pyramid slot than Sprint Integration would have.

---

## Summary

| Phase | What shipped | Status | Version |
|-------|-------------|--------|---------|
| 1 — L1 Content Validation | `make validate-content`, CI wiring | SHIPPED | v3.1.0 |
| 2 — L2 CLI Subprocess | `cmd/ethos/subprocess_test.go`, doctor coverage, RunE refactor | SHIPPED | v3.1.0 |
| 3 — L3 MCP Integration | `internal/mcp/integration_test.go` | SHIPPED | v3.1.0 |
| CI coverage | `-coverprofile` in `make test`, CI summary | SHIPPED | v3.1.0 |
| 4 — L5 End-to-End Agent | Harness, Layer A/B/C scenarios, daily CI workflow | SHIPPED | v3.1.0 |
| 5 — Sprint Team Integration | Retired without implementation — no fixture repo, harness, or CI job ever landed | RETIRED | — |
| 6 — L4 Wire Observability | `tests/e2e/` pytest framework, LiteLLM mock proxy, `ScenarioCapture`, token profiling as first scenario type, `cmd/e2e-attribute/`, `make test-e2e` / `make test-e2e-smoke` | SHIPPED | Unreleased |
