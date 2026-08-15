# L6 Token Harness: Framework Design

Read-only design mission (m-2026-08-15-011). Companion to
`docs/testing-strategy.tex` §"Level 6 — Payload/Token Profiling" and
`docs/testing-roadmap.md` §"Phase 6". No code in this PR.

## Report

- Lines: 468 (`wc -l docs/design-l6-token-harness.md`).
- Key decisions: pytest (PL-TT-1) · yaml scenarios discovered via
  `pytest_generate_tests`, zero conftest edits per scenario · one
  session-scoped `litellm_proxy` fixture, scenario identity carried in
  the LiteLLM `model` field (not the filesystem) · assertions are
  methods on a `ScenarioCapture` class, not free functions · tokenize/
  attribute/diff stays in Go (`cmd/token-attribute/`), reusing
  `internal/hook/persona.go` markers — Python's job stops at capture ·
  `tests/token-harness/hello/` is retired: its logic becomes the
  `empty-repo` scenario and its assertions move onto `ScenarioCapture`
  · `make test-tokens` stays out of `make check`; a new
  `lint-python` target (ruff + mypy, no proxy) joins `make check`
  every commit.
- Blocking prerequisite: whether Anthropic ships a first-party Python
  tokenizer for the current Claude family is unverified as of this
  design. `docs/testing-strategy.tex` already flags this ("Check
  first — the SDK docs are the ground truth"); resolve it at
  implementation time, not here. `tiktoken` is the fallback that is
  known to exist today.
- Everything else the roadmap already ruled on (in-tree scenarios vs.
  a separate fixture repo; relative 5%/15% thresholds; automatic
  per-release baseline recapture; whole-payload attribution) is
  restated in §6 for continuity, not reopened.

---

## 1. Framework choice

**Recommendation: pytest.**

PL-TT-1 and PL-TC-4 make this the default answer for any Python test
surface at Punt Labs, and there is no reason to deviate. Concretely,
pytest buys three things the bash+heredoc approach could not:

- `pytest_generate_tests` gives scenario discovery for free (§2).
- Markers (`@pytest.mark.e2e`, a project-local `@pytest.mark.token`)
  give tier separation and selective runs (`pytest -m token`) without
  a new flag on every script.
- Standard reporting — JUnit XML, `--co -q` collection listing,
  familiar failure output — for free, instead of hand-rolled
  `[hello] FAIL: ...` `print()` lines that a CI dashboard cannot
  parse.

No alternative was seriously in the running. `unittest` fails
PL-TC-4 outright. A bespoke YAML-driven runner (writing our own
collector loop, our own subprocess management) reinvents exactly what
pytest already does and forfeits the plugin ecosystem (pytest-xdist,
pytest-timeout) if L6 ever needs it.

## 2. Scenario collection mechanism

**Recommendation: `pytest_generate_tests` parametrizing over
`tests/token-scenarios/*.yaml`.**

The hard requirement is: a new scenario is a file drop, no Makefile
edit, no harness edit, no conftest edit. Three options were on the
table.

| Option | Verdict |
|---|---|
| `@pytest.mark.token` + one `test_*.py` file per scenario | Rejected as the *default* path — every new scenario requires writing a Python function, which is friction for a scenario author who just wants to declare "point at this repo fixture, expect these thresholds." Kept as the sanctioned escape hatch (below). |
| `pytest_collect_file` treating each `.yaml` as a collected item | Rejected. Implementing a custom `Collector`/`Item` pair is the heaviest of the three options for identical end behavior, and it is easy to get pytest's collection protocol subtly wrong (item IDs, `repr_failure`, `reportinfo`). No feature here needs that level of control. |
| `pytest_generate_tests` parametrizing a single test function | **Chosen.** One `conftest.py` hook globs `tests/token-scenarios/*.yaml` at collection time and calls `metafunc.parametrize("scenario", scenarios, ids=[s.id for s in scenarios])`. Dropping a new `.yaml` file changes nothing else — the next `pytest --co -q` picks it up. Test IDs read as `test_token_scenario[ethos-team-submodule]`, which is what a CI reviewer wants to see. |

The conftest hook is written once, when the framework ships. Adding
scenario #7 never touches it — that is the point of a *hook*, as
distinct from a per-scenario registration list.

**Escape hatch.** A scenario whose assertions genuinely cannot be
expressed by the declarative schema (§5) is written as an ordinary
`tests/token-harness/tests/test_<name>.py` marked
`@pytest.mark.token`. pytest autodiscovers `test_*.py` under
`testpaths` with zero config changes, so this preserves the same
"file drop, no other edits" property as the yaml path. This is the
release valve, not the default.

## 3. Proxy lifecycle as a fixture

**Recommendation: one session-scoped `litellm_proxy` fixture. No
shell wrapper.**

```python
@pytest.fixture(scope="session")
def litellm_proxy(
    scenario_registry: ScenarioRegistry,
    tmp_path_factory: pytest.TempPathFactory,
) -> Iterator[LiteLLMProxy]:
    proxy = LiteLLMProxy.start(scenario_registry, tmp_path_factory.mktemp("litellm"))
    yield proxy
    proxy.stop()
```

`LiteLLMProxy` is a class (`__new__`, not `__init__`, per PY-CC-1),
not a function returning a `Popen` handle:

- `LiteLLMProxy.start(registry, workdir)` — generates
  `litellm.yaml` from the scenario registry (one `mock-anthropic`
  model entry *per scenario*, keyed by scenario id — see §6 for why),
  writes it under `workdir`, spawns `litellm --config ... --port
  <ephemeral>` as a subprocess, polls the socket until it accepts a
  connection (the retry loop `run.sh` already has, moved into this
  method), and raises `RuntimeError` with the captured log tail if
  it never comes up.
- `LiteLLMProxy.base_url` / `.auth_token` — properties the test
  function reads to build the `claude --print` environment.
- `LiteLLMProxy.stop()` — `SIGTERM`, wait with timeout, `SIGKILL` on
  timeout. Called once from the fixture's teardown, after every
  scenario in the run has executed. This is the only place the
  proxy process is managed — no per-scenario spawn/kill.

One proxy per test session, not one per scenario. Scenario runs
against a shared proxy the same way L3's `ethos serve` subprocess
tests share one binary compile in `TestMain` — a proxy start/stop
cycle costs seconds, and with 6+ scenarios that adds up for no
benefit. Nothing in the config is scenario-specific except the model
list, and that is generated once, up front, from every scenario the
registry discovered.

## 4. Assertion primitives

**Recommendation: methods on a `ScenarioCapture` class in
`token_harness/capture.py`, not free functions in `conftest.py` or a
`helpers.py`.**

The mission brief names `assert_body_shape`, `assert_payload_size_within`,
`assert_no_secret_patterns` as free functions. Written that way they
would violate PY-OO-7 (PY-OO-5's mirror for modules that already have
a natural owning class): each of the three takes the captured payload
and reads multiple fields out of it — the textbook trigger for "this
should be a method." `ScenarioCapture` is that class:

```python
@dataclass(frozen=True, slots=True)
class ScenarioCapture:
    scenario_id: str
    raw_bytes: bytes
    body: Mapping[str, object]  # parsed Anthropic Messages request; wire boundary (PY-TS-14)

    def assert_body_shape(self) -> tuple[str, ...]: ...
    def assert_size_within(self, max_bytes: int) -> tuple[str, ...]: ...
    def assert_no_secrets(self, patterns: Sequence[SecretPattern] = DEFAULT_SECRET_PATTERNS) -> tuple[str, ...]: ...
```

Each method returns a tuple of violation strings — empty means pass —
rather than raising or bare-asserting internally. This mirrors what
`run.sh`'s embedded Python already does (accumulate every failure,
report all of them, not just the first) and keeps `assert` itself
out of library code per PY-EH-3, which restricts `assert` to internal
consistency checks in non-public methods. The *test* function is
where `assert` belongs:

```python
@pytest.mark.e2e
@pytest.mark.token
def test_token_scenario(scenario: Scenario, litellm_proxy: LiteLLMProxy) -> None:
    capture = run_scenario(scenario, litellm_proxy)
    failures = (
        capture.assert_body_shape()
        + capture.assert_size_within(scenario.max_bytes)
        + capture.assert_no_secrets()
    )
    assert not failures, "\n".join(failures)
```

`body: Mapping[str, object]` is the one place `object`-typed data is
justified under PY-TS-14: it is JSON parsed off the wire, from
LiteLLM's capture file, and the type system cannot know its shape
until `assert_body_shape` narrows it. `DEFAULT_SECRET_PATTERNS` is the
nine-pattern list already proven in `run.sh` (Anthropic/OpenAI keys,
AWS, Google, three GitHub token shapes, PEM blocks, Slack) — carried
forward verbatim, not reinvented. A scenario can override the pattern
list via its yaml (§5) for a fixture that legitimately contains a
credential-shaped string it needs to ignore, though none of the six
baseline scenarios need that.

## 5. Scenario file schema

**Recommendation: yaml-declarative, one file per scenario under
`tests/token-scenarios/`.**

```yaml
id: ethos-team-submodule
description: >
  Full team submodule vendored into a consuming repo. Isolates the
  aggregation cost called out in testing-strategy.tex's "known cost
  drivers" — every identity in the org ships whether or not the
  session needs it.
repo_fixture: fixtures/team-submodule   # dir under tests/token-harness/, or omitted for empty-repo
claude_invocation:
  prompt: "reply with the single word pong"
  max_turns: 1
expect:
  max_bytes: 900000
smoke: false
```

Yaml wins over py-imperative for the default path for the same reason
§2 picked declarative discovery: the person adding scenario #7 is
usually answering "what repo/team shape am I profiling and what's the
size ceiling," not writing test logic. That is data, and
`testing-roadmap.md`'s own Phase 6 write-up already describes
scenarios this way ("YAML per scenario in `tests/token-scenarios/`,
each declaring the repo/team configuration to reproduce") — this
design does not introduce that choice, it makes it concrete.

The `expect` block maps directly onto `ScenarioCapture`'s three
methods; nothing in the schema needs a fourth field until a fourth
assertion primitive exists. `smoke: true` (§7) marks the scenario(s)
that run on every push; everything else runs per-release. Anything
that needs logic beyond `expect`'s fixed vocabulary — a scenario
comparing two captures against each other, say — uses the py escape
hatch from §2 instead of growing the yaml schema into a second
programming language.

## 6. Baseline capture and delta

Carried forward from `testing-strategy.tex` and `testing-roadmap.md`,
which already settled this; restated for continuity, not re-litigated:

- **Location.** `tests/token-baselines/<scenario-id>.json`, committed.
- **Recapture.** `make baseline-tokens`, operator-invoked on a
  controlled workstation — never automatic from CI.
- **Tokenizer.** Local/offline for CI. Recommend `tiktoken` with an
  "approximate, calibrated against Anthropic's tokenizer" disclaimer
  stamped on every report, per testing-strategy.tex's own
  recommendation — *unless* Anthropic ships a first-party Python
  tokenizer for the current Claude family, which is unverified as of
  this design (flagged in the Report header as the one blocking
  prerequisite). `messages/count_tokens` stays reserved for
  `make calibrate-tokens`, operator-invoked, never CI.
- **Strictness.** Advisory, not a hard gate — `make test-tokens`
  exits 0 regardless of delta. >5% on any scenario → PR comment,
  human review before merge. >15% → CI turns yellow, blocks the
  auto-merge memory, not the merge button. A new token source not in
  the last baseline → PR comment, no gate. These thresholds are
  relative, matching the roadmap's explicit recommendation to revisit
  after one release cycle of real data.
- **Scope.** The delta compares whole-payload attribution, not just
  ethos-owned bytes — the roadmap's stated reason is that "the goal
  is total cost observability, ethos is one contributor among
  several," and nothing in this design changes that reasoning.

The one thing this design adds concretely: because §3 keys each
scenario's LiteLLM model entry by scenario id, the attribution
parser's job of "which baseline file does this capture belong to" is
answered by `capture["model"]` — no directory bookkeeping, no
timestamp-ordering heuristics.

## 7. Make integration

**Recommendation: `make test-tokens` stays outside `make check`.
Full sweep is per-release/on-demand; one smoke scenario runs on every
push, matching what already happens today.**

```makefile
test-tokens: ## Run the full L6 scenario sweep (per-release; requires litellm + claude CLI)
    uv run --project tests/token-harness pytest -m token
    go run ./cmd/token-attribute -captures .tmp/token-captures -baselines tests/token-baselines -out .tmp/token-reports

test-tokens-smoke: ## Run the fast L6 subset (every push; no baseline diff)
    uv run --project tests/token-harness pytest -m "token and smoke"
```

(Recipe lines above are shown space-indented for Markdown lint; a
real Makefile requires a literal tab.)

`test-tokens-hello` disappears (§10); `test-tokens-smoke` is its
replacement, running the `empty-repo` scenario's structural/size/PII
assertions on every push — the same signal the hello job gives today,
now inside the real framework instead of a parallel one. `test-tokens`
(the full sweep, baselines, attribution, delta report) is per-release,
exactly as `testing-roadmap.md` specifies, and stays out of `make
check` for the same reason `test-tokens-hello` already is: it shells
out to `litellm` and `claude`, external processes `make check` does
not otherwise depend on, and the roadmap explicitly scopes L6 to
"per release + on-demand," never per-commit.

What *does* join `make check` every commit is linting the Python
source itself — see §9.

## 8. CI wiring

**Recommendation: two jobs, both scenario-count-agnostic.**

- `token-harness-smoke` — every push and PR. Installs Python
  3.13 (per PL-TC-6; the `litellm==1.81.9` pin governs the litellm
  version, not the interpreter — verify at implementation time
  whether 3.13 also satisfies litellm's own `python_requires`),
  `litellm[proxy]==1.81.9`, `@anthropic-ai/claude-code`, runs `make
  test-tokens-smoke`. Uploads `.tmp/token-harness/` and
  `.tmp/token-captures/` on failure, same as today's
  `token-harness-hello` job.
- `token-harness-release` — triggered on release tag (or scheduled
  weekly, whichever the release cadence ends up being; this is a
  workflow-file decision for the implementation mission, out of
  scope here). Same install steps, runs `make test-tokens`, uploads
  `.tmp/token-reports/` and `.tmp/token-captures/` always (not just
  on failure — the delta report is the point of the job, not a
  failure artifact).

Neither job's YAML lists scenario names. A new `tests/token-scenarios/
*.yaml` file changes what `pytest_generate_tests` collects; the CI
job's command line (`make test-tokens[-smoke]`) does not change. This
is the same "no harness edit" property §2 established, extended
through to CI.

## 9. Cross-language boundary

This is ethos's first Python surface, and the line has to be drawn
deliberately rather than let Python creep to fill whatever's
convenient.

**Recommendation: Python's job stops at capture. Tokenize, attribute,
diff, and report stay in Go.**

`testing-strategy.tex` already specified `cmd/token-attribute/` as a
Go binary reading a LiteLLM capture file and emitting the attribution
report. This design keeps that boundary and gives it a concrete
justification beyond "it was already written down":

- **LiteLLM's plugin surface is Python-only.** `CustomLogger` is how
  the capture happens at all — there is no way to make *that* part
  Go without abandoning LiteLLM (rejected, §11). Everything on the
  other side of the capture file boundary has no such constraint.
- **Attribution needs ethos's own marker strings** (`## Personality`,
  `## Writing Style`, `## Team Context`) already defined once in
  `internal/hook/persona.go`. Doing attribution in Python means
  either duplicating those constants in a second language (drift
  risk — nothing enforces the two copies match) or having the Python
  side shell out to Go to ask for them, which is more moving parts
  than just doing the slicing in Go directly.
- **Minimizes what has to carry `uv`/`ruff`/`mypy` machinery.** The
  Python subtree's job is narrowly "drive a subprocess proxy, run a
  Claude Code invocation against it, write the wire body to disk."
  Tokenizing, diffing against a baseline, and formatting a report are
  ordinary data transformations with no dependency on LiteLLM or
  Claude Code — there's no reason they need Python's runtime at all.

**Concretely:**

- `tests/token-harness/pyproject.toml` — a `uv` project scoped to
  this subtree, per PL-PL-1's src layout:
  `tests/token-harness/src/token_harness/{proxy,capture,scenario,
  custom_callbacks}.py`, tests under `tests/token-harness/tests/
  test_scenarios.py` + `conftest.py`. This is not a package that ships
  anywhere — no `[project.scripts]`, no PyPI target, PL-DI-* does not
  apply. It exists to be `uv run` from inside the ethos repo, nothing
  more.
- `tests/token-harness/uv.lock` — committed, pins `litellm[proxy]
  ==1.81.9`, `pytest`, dev-only `ruff`/`mypy`. This replaces the
  `pip install 'litellm[proxy]==1.81.9'` step documented in
  `hello/README.md` today, which is a direct PL-TC-1 violation (never
  `pip install` outside a lockfile) that this design corrects rather
  than repeats.
- `Makefile` gains one target that joins the existing `lint`
  composite, cheap enough to run every commit because it never boots
  a proxy or touches the network:

  ```makefile
  lint: ## Lint (golangci-lint + shellcheck + ruff)
      @test -x $(GOLANGCI_LINT) || { echo "..."; exit 1; }
      $(GOLANGCI_LINT) run ./...
      shellcheck hooks/*.sh install.sh
      uv run --project tests/token-harness ruff check .
      uv run --project tests/token-harness ruff format --check .
      uv run --project tests/token-harness mypy src/ tests/
  ```

  This is what answers the mission's "how does `make check` aggregate
  lint across both languages without shredding the Go-focused
  Makefile" question: it doesn't shred it, it appends three lines to
  the existing `lint` target, exactly the way `shellcheck hooks/*.sh`
  already sits next to `golangci-lint run ./...`. `make check` still
  means "everything that's fast enough to run before every commit
  passes," now including Python style/type checks; it does not mean
  "every test tier including the ones that shell out to litellm and
  claude," which was never true even for the Go behavioral tests
  (`test-behavioral` is its own target, deliberately outside `check`,
  same pattern this reuses).
- `cmd/token-attribute/` — plain Go, `go build`/`go test` covered by
  the existing `make test`/`make lint` targets with zero new
  Makefile plumbing, because it's just another Go package.

## 10. What replaces `tests/token-harness/hello/`

**Recommendation: retire it. Its logic is absorbed, not deleted and
not kept as a second path.**

Concrete mapping, for the implementation mission to execute (this
mission does not touch `hello/`, `Makefile`, or `.github/workflows/`):

| `hello/` today | Becomes |
|---|---|
| `run.sh`'s proxy start/wait/teardown | `LiteLLMProxy` fixture (§3). Bash orchestration deleted — keeping both means every proxy-lifecycle fix has to land twice. |
| `run.sh`'s `claude --print ... "reply with the single word pong"` invocation | `tests/token-scenarios/empty-repo.yaml`'s `claude_invocation` block. Same prompt, same `--max-turns 1`. |
| `run.sh`'s structural / 700KB ratchet / 9-pattern PII checks | `ScenarioCapture.assert_body_shape` / `.assert_size_within` / `.assert_no_secrets` (§4), called with `empty-repo.yaml`'s `max_bytes: 700000`. Values carried forward unchanged — this is not a re-derivation of the ratchet, just a relocation of the code that enforces it. |
| `litellm.yaml` (static, one `mock-anthropic` model) | Generated dynamically by `LiteLLMProxy.start` from the full scenario registry (§3), not hand-maintained. |
| `custom_callbacks.py`'s `TokenCaptureLogger` | Moves to `token_harness/custom_callbacks.py` near-verbatim — it is already correctly designed (env-configured capture dir, minimal `CustomLogger` subclass) and needs no rewrite, only a new import path. |
| `README.md` | Content merges into this design doc and the new `tests/token-harness/README.md`; the "known issue: pin litellm==1.81.9" note carries forward unchanged — the FastAPI incompatibility it documents is still live. |
| `make test-tokens-hello`, the `token-harness-hello` CI job | Replaced by `make test-tokens-smoke` / `token-harness-smoke` (§7, §8), running the same `empty-repo` scenario so the every-push signal does not regress during the cutover. |

Nothing from `hello/` is kept running in parallel once the cutover
lands — a smoke-test-shaped scenario and a real framework both
capturing "empty repo, bare Claude Code" would be the exact
duplication L6 exists to catch elsewhere in ethos.

## 11. Rejected alternatives

**Bash + heredoc (the current `hello/` shape), generalized to all of
L6.** This is the design the operator already rejected, restated for
the record: `run.sh` treats an entire pyramid level as one scenario.
Adding scenario #2 means editing the embedded Python inside `run.sh`
and adding a second Makefile target (`test-tokens-hello-2`, or
branching the existing one on an argument) — there is no collection
mechanism, no marker, no discovery. At six scenarios this becomes six
near-identical bash scripts or one script with six embedded blocks,
neither of which pytest's parametrization, fixtures, or reporting
would have to be reinvented to get for free.

**Go-native harness (no LiteLLM).** Rejected because LiteLLM's
`CustomLogger.async_post_call_success_hook` — the mechanism that
hands us `proxy_server_request.body`, the exact JSON Claude Code
sent — is a Python-only plugin API with no Go equivalent. Building an
Anthropic-compatible mock-and-capture proxy from scratch in Go is not
a smaller version of this design, it's a second project: reimplement
`/v1/messages`, request/response mocking, and a callback hook, all to
avoid one `uv run` dependency. `testing-strategy.tex` already priced
this out — "Reusing LiteLLM shaves the earlier estimate by 1–2 days
over building a bespoke capture proxy" — and this design's §9 explains
why the resulting two-language split is still the right shape rather
than a compromise: only the capture step needs Python, and that step
is a hard requirement, not a preference.

This is a different situation from DES-043's rejected alternative
"Python test harness" for L4 behavioral tests. DES-043's reasoning —
"the Anthropic API call is a single `net/http` POST, no SDK needed" —
is true for L4 (call the API, grade the response) and false for L6
(intercept the *exact* wire body Claude Code independently constructs
and sends, without Claude Code knowing it's being intercepted). L4's
Go-only choice and L6's Python-for-capture choice are both instances
of the same underlying rule — use the language that can do the job
with the least reinvention — applied to two different jobs.

**LiteLLM's own test hooks / evaluation frameworks (promptfoo,
LiteLLM's internal test suite).** Not sufficient because they test
the wrong thing: LiteLLM's own request-handling correctness, or an
LLM's response quality. L6 needs LiteLLM purely as a passive,
transparent proxy sitting between Claude Code and a mocked upstream —
the thing under test is Claude Code's *outgoing* payload, which
neither promptfoo nor LiteLLM's own test suite is built to capture.
`testing-strategy.tex` reaches the same conclusion for L4 (promptfoo
"doesn't support `claude --bare` with MCP config and per-agent system
prompts") for an adjacent reason: these tools assume they are
grading a model's *output*, not observing a client's *input
construction*.

**Pure py-imperative scenarios (no yaml) as the default.** Addressed
in §2/§5: rejected as the default because it puts a Python-authorship
tax on every new scenario, kept as the explicit escape hatch for the
scenarios that need it.
