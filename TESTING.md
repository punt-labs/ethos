# Ethos testing guide

This guide maps the maintained suites to the Punt Labs [Testing standard]
and [Go testing rules]. The required repository gate is `make check`.
The installed PR/main and Nightly rows describe the target state after
[#670] and [#671]; those PRs are independent of the local selectors.

[Testing standard]: https://github.com/punt-labs/punt-kit/blob/main/standards/testing.md
[Go testing rules]: https://github.com/punt-labs/punt-kit/blob/main/lang-rules/go/go-testing-tiers.md
[#670]: https://github.com/punt-labs/ethos/pull/670
[#671]: https://github.com/punt-labs/ethos/pull/671

## Trigger tiers

| Tier | Runner, selection and prerequisites | Time budget | Gate behavior |
| --- | --- | --- | --- |
| Pre-commit | Local, `make hooks` once, then `git commit`; also `make test-changed` directly. Go with race support, Git and Bash. Checks staged whitespace and tests working-tree packages selected by staged paths. | <30 s on a provisioned checkout for a typical small change | A test failure stops the commit; the existing Git hook still runs on a pass. |
| Pre-push | Local, `make test-push` (invoked by the hook). Full credential-free package suite plus `make test-subprocess-fast`. Requires the Go toolchain, full Git history, Bash and Python. | <3 min | A failure stops the push; pushes of other commits must be made from the corresponding checkout. |
| PR | [Test] and [Docs], hosted runners. Full `make test`, storage and plugin-cache checks, Linux/macOS installed verification [#670], offline OpenCode checks, Z specs, wire smoke against a local stub, and docs. | <10 min | Required `test` and `docs` block merge. Wire smoke and tool-install verification also report independently. |
| Merge to main | Repeat PR jobs on the main commit. Coverage uploads are tied to that run and commit. | <10 min | A red main requires triage; do not update coverage from an unsuccessful run. |
| Nightly | [Behavioral] (Nightly [#671]), daily 06:07 UTC or manual dispatch, hosted Linux on upstream main. `make test-nightly` is the authorized local equivalent: review the quarantine inventory, then run the `behavioral` selection. Requires Claude CLI, an Anthropic test key and the configured judge model after [#671]. | ≤1 h (30-minute hosted live job) | Live failures are red and Biff alerts on failed or canceled runs after [#671]. Not a PR check. |
| Release candidate | [Release] stages four checksummed binaries; each native Linux/macOS runner installs through shipped `install.sh` in a temporary HOME and verifies version and identity create/get. PR path or dispatch runs this stage without publishing. Local: `make test-release-candidate`. | 20-minute build, 5 minutes per native smoke | All four smokes gate tag publication; publish uses the staged bytes. |
| Post-release | [Release] native jobs install the published GitHub Release binary without source fallback; require the exact version, candidate SHA-256 and the same identity round trip. Local: `make test-post-release`. | <2 min; job timeout 2 min | A smoke failure fails release verification; it cannot undo publication. |

[Test]: .github/workflows/test.yml
[Docs]: .github/workflows/docs.yml
[Behavioral]: .github/workflows/behavioral.yml
[Release]: .github/workflows/release.yml

`make hooks` sets the repository's `core.hooksPath` to `.githooks`; it refuses
to replace a different configured path or to disable an executable hook
without a tracked counterpart. The tracked hooks chain the common
directory's pre-commit, commit-msg and pre-push hooks. In particular, opt-in
keeps Ethos's audit seal and provenance trailers working. Pre-push passes
Git's original remote arguments and input and skips the test suite for
deletion-only pushes. When switching to a branch without these tracked hooks,
unset the opt-in with `git config --local --unset core.hooksPath` to restore
the default hooks.

For a local release check, use the shipped `install.sh` for the intended
version and place the version-matched staged artifact in `dist/`. Candidate
smoke needs the native binary and `dist/checksums.txt`; post-release smoke
needs that same candidate manifest and access to the public release.
Both require a native Linux/macOS host, Bash, Python 3, and `shasum`.

## Test kinds and selectors

| Kind | Entry point | Regular placement and boundary |
| --- | --- | --- |
| Unit and component | `make test-unit` (in-process redaction sample); `make test` (all default packages) | Local, PR and main. Go selectors use `-race -count=1`; the full suite also contains real subprocess tests. |
| Subprocess | `make test-subprocess-fast`, `make test-subprocess`, `make check-storage` | Fast Git-hook paths at pre-push; broader Git, installer-fixture, CLI and MCP coverage in PR/main. `check-storage` executes a real binary in an isolated fixture. |
| End-to-end | `make check-installed` [#670], `make test-e2e-smoke`, `make test-e2e`; native candidate and published smokes in [Release] | Installed Linux/macOS proof on PR/main; the separate wire smoke drives a real Claude CLI against local LiteLLM and stubbed providers. The full wire sweep runs on tags against a checkout-built binary, not the installed candidate. See [wire harness]. |
| Domain-specific | `make validate-content`, `make fuzz-check`, `make check-opencode`, `make check-plugin-cache`, `make test-legacy-corpus`, `make test-performance` | Validate Ethos content, Z specs, OpenCode config, cache switching, repair fixtures and redaction. The corpus maintenance check is manual. The race-instrumented performance selector reports time; the existing dedicated CI budget is deliberately uninstrumented because race/coverage overhead changes the workload. |
| Live | `make test-live` (`make test-behavioral`) | Trusted Nightly/authorized local execution only. The live Anthropic judge scores outcomes from synthetic fixtures. `make doctor-opencode` explicitly checks workstation live dependencies; it is not an offline PR gate. |
| Agent | `make test-agent` (`make test-behavioral`), `make smoke-opencode` | Claude follows or rejects mission instructions in live behavioral tests. The OpenCode smoke uses live child/tool execution and remains operator-run. Live and agent kinds overlap in the behavioral suite; the wire stub is not live provider evidence. |
| Quarantined | `make test-quarantine` (inventory), `make test-quarantine-run` (execute only listed tests) | Never in ordinary Go selection. PR/main validate the inventory; Nightly reviews it daily. Use the separate run to diagnose and restore tests. |

[wire harness]: tests/e2e/README.md

Provision tools and module caches first (see [development guide]). Offline
gates do not need a production key. Run live selectors only in a disposable
checkout or authorized trusted job. Do not put provider secrets or private
payloads in fixtures or artifacts.

[development guide]: docs/development.md

### Timing

The local Make gates report elapsed wall-clock seconds, the budget verdict,
and the underlying exit code. Overruns are reported, not made into timeouts
that drop required tests. Use the wall clock for the full gate, not summed
test process durations. Compare hosted wall time from workflow start through
required jobs completing, not the sum of parallel runner times.

An isolated historical full Go suite took 11m08s before pre-push boundary
checks. In the original audit, the [PR run] and [main run] took 10m35s each
from workflow start to completion. These are baseline examples, not upper
bounds. Record overruns and the runner configuration in the deviation's issue.

[PR run]: https://github.com/punt-labs/ethos/actions/runs/45343838313
[main run]: https://github.com/punt-labs/ethos/actions/runs/45329527170

### Quarantine

The [inventory] is intentionally empty. To quarantine a Go test, move it
to a `*_test.go` file gated by `//go:build quarantine` (combine it with any
existing platform constraints), name it `TestQuarantined_Example`, and put the
Linear issue alongside the test. Add the package, exact test name, Linear
issue, named owner, UTC expiry (within 14 days), and reason to the inventory.
Keep the first failure evidence and diagnose before rerunning. Review the
inventory daily; fix or remove the test by expiry. Restore the ordinary
name and selection and remove the inventory row when the test is fixed.
An empty inventory is valid; a listed test that does not collect is an error
when running quarantined tests.

[inventory]: tests/quarantine.tsv

## Conformance by clause

| Clause | Ethos evidence |
| --- | --- |
| TST-1 / GO-TST-1 | The trigger table names the gates and budgets. The local timer reports overruns without reducing the suite. |
| TST-2 / GO-TST-1 | The selectors above separate the applicable kinds. Release candidate and published runs select artifact evidence, not just Go packages. |
| TST-3 / GO-TST-1 | `make help` lists selectors; [#670] makes PR/main use `make test`. Local and CI use the same Go tests and race/count flags, apart from the explicit performance measurement. |
| TST-4 / GO-TST-2 | Offline suites use temporary repos and HOME, explicit subprocess fixtures and loopback provider stubs; [#670] isolates Git and prevents the regenerated agents from touching the checkout. |
| TST-5 / GO-TST-3 | `coverage.out` is Go package **statement** coverage. [#670] publishes a separately instrumented installed-binary statement profile from the storage acceptance run; neither report implies branch coverage or coverage of uninstrumented release, hook or provider child processes. |
| TST-6 / GO-TST-4 | [#671] gates secrets and live execution to upstream main; live requests, job time, and spend are bounded. Missing required credentials are red. |
| TST-7 / GO-TST-4 | [Release] verifies staged, installed native bytes and blocks promotion on any failed leg. |
| TST-8 / GO-TST-4 | [Release] checks the production binary download, digest, version and operation after publication; workflow errors remain visible. |
| TST-9 / GO-TST-5 | Separately selected, owned, dated quarantines are listed in the tracked inventory and reviewed on Nightly. |
| TST-10 / GO-TST-5 | This guide names scopes, owners, and proposed expiries for remaining deviations. |

## Residual deviations

These are expected after the four PRs. Owner roles and expiry dates are
proposed until the maintainers file and assign the Linear issues.

| Clause | Scope and reason for deferral | Owner | Proposed expiry | Linear |
| --- | --- | --- | --- | --- |
| TST-1 / GO-TST-1 | Heavy affected packages may miss 30 s locally; measure representative changes and select smaller safe subsets without weakening the other gates. | Ethos maintainers | 2026-12-08 | TODO |
| TST-1 / GO-TST-1 | Full pre-push exceeds 3 min; optimizing and sharding the suite are outside the hook changes. | Ethos maintainers | 2026-12-08 | TODO |
| TST-1 / GO-TST-1 | PR/main exceed 10 min and wire/tool jobs are not required; tuning and branch ruleset changes need separate work. | Ethos maintainers | 2026-12-08 | TODO |
| TST-1 / TST-4 | No pinned pre-commit/required PR secret scanner is configured; needs a scanner policy, config and ruleset rollout. | Ethos maintainers | 2026-12-08 | TODO |
| TST-2 / GO-TST-1 | Unit-only and subprocess-only selectors cover representative packages, not all tests of each kind; exhaustive separation needs classification of mixed packages. | Ethos maintainers | 2026-12-22 | TODO |
| TST-2 / GO-TST-1 | `test-legacy-corpus` and live OpenCode smoke are still manual; move them to suitable CI tiers after provisioning fixtures and the trusted runner. | Ethos maintainers | 2026-12-22 | TODO |
| TST-3 / GO-TST-1 | The audit latency budget deliberately omits the race flag to measure uninstrumented performance; keep the race companion and track the exception. | Ethos maintainers | 2026-12-22 | TODO |
| TST-3 / GO-TST-1 | Local hooks execute working-tree sources, not an isolated staged or pushed snapshot; snapshot testing needs an independent budget and worktree strategy. | Ethos maintainers | 2026-12-22 | TODO |
| TST-5 / GO-TST-3 | Coverage ratchet compares with a committed reference, not an equivalently measured exact merge base; replacing the gate needs matched base/head profiles. | Ethos maintainers | 2026-12-22 | TODO |
| TST-5 / GO-TST-3 | Extra subprocess, wire and release processes remain outside the installed-storage coverage report; instrumentation for those paths is independent work. | Ethos maintainers | 2026-12-22 | TODO |
| TST-6 / GO-TST-4 | Green Nightly on main and its failure/cancellation alerts still need hosted evidence after [#671] merges. | Ethos maintainers | 2026-12-08 | TODO |
| TST-6 / GO-TST-4 | Git-visible assertions miss ignored agent writes; distinguishing them from legitimate Ethos runtime files requires a separate write-set design. | Ethos maintainers | 2026-12-22 | TODO |
| TST-6 / GO-TST-4 | Overlapping Biff alerts are last-writer-wins; ordering pending alerts needs a separate policy. | Ethos maintainers | 2026-12-22 | TODO |
| TST-7 / GO-TST-4 | Candidate gate proves CLI identity only; installed plugin, release-critical MCP/hook paths and applicable live agent cases need candidate-aware harnesses. | Release maintainers | 2026-12-22 | TODO |
| TST-8 / GO-TST-4 | Published CLI smoke does not exercise the installed marketplace plugin or verify a release-owner alert for post-publication failure; those need separate coverage and alert proof. | Release maintainers | 2026-12-22 | TODO |
| TST-10 / GO-TST-5 | Linear issue links and named assignments await the maintainer's issue filing. | Ethos maintainers | 2026-12-08 | TODO |
