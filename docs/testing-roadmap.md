# Ethos Testing Roadmap

Concrete implementation sequence for the five-level test pyramid
described in `docs/testing-strategy.tex`.

---

## Current State (v3.5.0)

- **2,058 tests** across 14 packages, 89.8% mission package coverage
- **22.4 KLOC production Go, 35.8 KLOC test Go** (1.60:1 test-to-production ratio)
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

**v3.5.0** (April 14, 2026) shipped automatic mission traceability (`Store.Close` auto-appends summary JSONL to `<repo>/.ethos/missions.jsonl`, DES-050), deprecation warning deduplication via `sync.Once`, and an integration test for the Close-to-JSONL path. 2,058 tests across 14 packages, 89.8% mission package coverage.

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
