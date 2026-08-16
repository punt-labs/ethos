# E2E test tier (L4)

A real `claude --print` subprocess driven against a local mock-upstream
LiteLLM proxy, so we can observe exactly what Claude Code puts on the
wire without paying Anthropic tokens or requiring an API key. Token
and payload profiling is the first scenario `type` this tier carries;
hallucination checks, feature-verification, and other E2E scenarios
plug in the same way. See `docs/design-e2e-test-framework.md` for the
full design.

## What this proves

- **LiteLLM proxy** serves Claude Code at Anthropic's Messages endpoint
  (`/v1/messages`) with only a Python install and a subprocess spawn —
  no external network dependency, no Anthropic API key, no
  workstation-specific setup. Works in CI.
- **`mock_response` at proxy level** returns a canned assistant turn.
  Claude Code accepts it and exits cleanly. Zero Anthropic tokens
  consumed per scenario run.
- **`CustomLogger.async_post_call_success_hook`** captures the full
  `proxy_server_request.body` — the exact JSON Claude Code POSTed —
  to a per-request `.jsonl` file. That body includes the assembled
  system prompt, the full tool list with schemas, and the message
  history.
- **Scenario collection is a file drop.** Add a `.yaml` file under
  `scenarios/`; nothing else changes. See "Adding a scenario" below.

## Running locally

```bash
# One-time: LiteLLM (pinned; see "Known issue" below).
uv sync --project tests/e2e

# Fast subset — one smoke scenario, runs on every push.
make test-e2e-smoke

# Full sweep — every scenario, plus the Go attribution report.
make test-e2e
```

Both assume `claude` is on `PATH`. Both also build `.tmp/e2e-bin/ethos`
from this checkout first (the `e2e-bin` Make target) and put it ahead
of everything else on the caged subprocess's `PATH` — a non-hermetic
scenario's `SessionStart` hook then runs the code under test, not
whatever `ethos` version happens to be installed globally. Outputs
land under `.tmp/e2e/` (proxy config/log) and `.tmp/e2e-captures/`
(captured request bodies), both gitignored.

## Hermetic vs. non-hermetic

Every scenario declares `hermetic: bool` (default `true`). Hermetic
scenarios invoke `claude --print --bare`, which strips hooks, MCP
servers, and CLAUDE.md — this is the Claude Code baseline `empty-repo`
exists to pin. `hermetic: false` drops `--bare`: the scenario runs a
real session against `repo_fixture`, so that repo's own `SessionStart`
hooks and config fire exactly as they would for a user. `ethos-loaded`
is the first such scenario — a minimal fixture (one identity,
personality, writing style, talent, single-member team) that proves
`cmd/e2e-attribute` can see ethos's own persona-block markers on the
wire, not just the SDK baseline. A non-hermetic scenario's cwd is a
throwaway copy of `repo_fixture` with no `.git` of its own, so the
runner sets `ETHOS_REPO_ROOT` to that cwd — without it, a repo-root
walk keeps climbing past the fixture and lands on whichever real repo
happens to contain the test run's tmpdir.

## Adding a scenario

Drop a new `.yaml` file under `scenarios/` following the schema in
`docs/design-e2e-test-framework.md` §5. No Makefile edit, no
`conftest.py` edit, no workflow edit.

Verify the autopickup with pytest's collector, not by running the
full (proxy-booting) suite:

```bash
uv run --project tests/e2e pytest --co -q
```

The new scenario's id should appear as
`test_token_scenario[<scenario-id>]` in the collected list. Remove the
throwaway file and re-run to confirm it disappears.

## Known issue: pin `litellm==1.81.9`

The latest LiteLLM release imports `get_flat_dependant` from
`fastapi.dependencies.utils`, which was removed in a recent FastAPI
version. The proxy fails to boot on Python 3.12+ with:

```text
ImportError: cannot import name 'get_flat_dependant' from
    'fastapi.dependencies.utils'
```

Version `1.81.9` boots cleanly. Pin it (see `pyproject.toml`). Revisit
when a newer LiteLLM restores the import or pins its own FastAPI
ceiling.

## Layout

- `src/e2e/proxy.py` — `LiteLLMProxy`: generates `litellm.yaml` from the
  scenario registry, spawns/tears down the proxy subprocess.
- `src/e2e/scenario.py` — `Scenario`/`TokenScenario`/`ScenarioRegistry`:
  parses scenario yaml, discovers scenario files.
- `src/e2e/capture.py` — `ScenarioCapture`/`TokenCapture`: assertion
  methods on a captured request.
- `src/e2e/custom_callbacks.py` — the LiteLLM `CustomLogger` that writes
  each captured request to disk.
- `src/e2e/runner.py` — `run_scenario`: env-cages and invokes `claude
  --print` against the proxy, returns the matching capture.
- `conftest.py` — scenario discovery (`pytest_generate_tests`) and the
  session-scoped `litellm_proxy` fixture.
- `tests/test_scenarios.py` — `test_token_scenario`, one per `type:
  token` scenario.
- `scenarios/*.yaml` — the scenario declarations themselves.
- `baselines/` — committed per-scenario token baselines (empty for the
  initial land; populated by a future `make baseline-tokens`).

## What's not proven yet

- **Tokenizer.** Reports bytes/chars, not tokens — `tiktoken` lands
  once the calibration question (design §6) is settled; the Go
  attribution report marks the token fields `TODO`.
- **Baseline diff.** `baselines/` is currently empty; `make
  baseline-tokens` / `make calibrate-tokens` are stubs.
