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
| Pre-commit | Local, `make hooks` once, then `git commit`; also `make secret-scan-staged` and `make test-changed` directly. Go with race support, Git and Bash. The timer covers the pinned gitleaks scan of the Git index and the subsequent staged whitespace and affected Go package checks from an isolated copy of the index. | <30 s on a provisioned checkout for a typical small change | A scan or test failure stops the commit; the existing Git hook still runs on a pass. |
| Pre-push | Local, the hook runs `make test-push` in a detached worktree of the commit being pushed. Full credential-free package suite plus `make test-subprocess-fast`. Requires the Go toolchain, full Git history, Bash and Python. | <3 min | A failure stops the push; branch and non-release tag updates must be pushed from the corresponding checkout. Release tags skip the local gate; the [Release] candidate smoke gates publication. |
| PR | [Test] and [Docs], hosted runners. Full `make test`, legacy corpus regeneration, storage and plugin-cache checks, Linux/macOS installed verification [#670], offline OpenCode checks, Z specs, wire smoke against a local stub, and docs. `secret-scan` scans introduced commits using the same pinned gitleaks as the hook. | <10 min | Required `test` (including `legacy-corpus`) and `docs` block merge. `secret-scan` reports independently pending ruleset rollout, as do wire smoke and tool-install verification. |
| Merge to main | Repeat PR jobs on the main commit and compare package statement coverage with its parent. | <10 min | A red main requires triage. |
| Nightly | [Behavioral] (Nightly [#671]), daily 06:07 UTC or manual dispatch on upstream main. The hosted Claude job runs `make test-quarantine` and then `go test` with the `behavioral` tag and a 20-minute test timeout, using Claude CLI, an Anthropic test key and the configured judge model. The local counterpart is `make test-nightly`. A separate hosted Linux job runs `bash scripts/ci-opencode.sh smoke`, including doctor; the workstation counterpart is `make smoke-opencode`. | ≤1 h (30-minute Claude and 55-minute OpenCode jobs in parallel) | Live failures are red and Biff alerts on failed or canceled runs. Not a PR check. |
| Release candidate | [Release] stages four checksummed binaries; each native Linux/macOS runner installs through shipped `install.sh` in a temporary HOME and exercises CLI identity round trip, plugin manifest and `claude plugin validate`, MCP stdio (`ethos serve`), and release-critical hooks (`session-start`, `pre-tool-use`, `audit-log`, `suppress-output`, `session-end`). On tag pushes, `candidate-behavioral` runs the live behavioral suite against the staged `ETHOS_BINARY`. PR path or dispatch runs the native smoke without publishing. Local: `make test-release-candidate`. | 20-minute build, 10 minutes per native smoke, 25 minutes for candidate behavioral | All four native smokes and the tag behavioral job gate tag publication; publish uses the staged bytes. |
| Post-release | [Release] native jobs install the published GitHub Release binary and marketplace plugin (`ethos@punt-labs`) without source fallback; verify exact binary version, candidate SHA-256, `claude plugin list`, CLI identity, MCP stdio, and installed marketplace plugin hooks. A manual dispatch with `force-published-failure=true` drills the `notify-post-release` Biff alert and wall readback. Local: `make test-post-release`. | <5 min; job timeout 5 min | A smoke failure fails release verification and alerts the release owner via Biff wall; it cannot undo publication. |

[Test]: .github/workflows/test.yml
[Docs]: .github/workflows/docs.yml
[Behavioral]: .github/workflows/behavioral.yml
[Release]: .github/workflows/release.yml

`make hooks` (Python 3 and Go) adds the development gates before any existing
pre-commit and pre-push code in Git's repository-local hooks. It leaves
`core.hooksPath` and commit-msg untouched, so the Ethos audit seal and
provenance trailer remain in the hooks Git runs and `ethos doctor` checks.
Pre-push preserves Git's original remote arguments and input. Like the
[Release] workflow's `v*` filter, the exemption applies only to tag names
starting with `v` and containing no slash. Deletions also skip the local
test suite; branch and other tag updates in the same push still run it.
`punt release` can push a release tag that differs from checkout HEAD;
the workflow tests that tagged candidate before publication. The opt-in is
per checkout: other
linked worktrees and branches without `.githooks` still run their existing
hooks but not the development gates. Remove
`"$(git rev-parse --absolute-git-dir)/ethos-dev-hooks"` to opt out.
If Git uses a different hooks directory or the existing gate hook cannot
be safely edited, installation refuses and leaves the chain to the operator.
If it finds a legacy standalone Ethos audit seal, run `ethos enable` to
upgrade that seal before retrying `make hooks`.

The development gates leave unstaged and untracked files out of both snapshots
and remove their temporary worktrees. Pre-commit selects the packages owning
staged sources or test fixtures and their transitive reverse dependents,
including dependencies imported only by tests. Staging a change to `go.mod`
or `go.sum` selects the full package set.

`make secret-scan-history` is the local history audit for all commits reachable
from HEAD. Both scan targets download and build the pinned gitleaks Go module
on first use and redact findings. CI limits the scan to the PR base or
pre-push main tip through the checked-out commit.

The trusted `main` Nightly uses a GitHub-hosted Ubuntu runner and installs
checksum-pinned OpenCode 2.2.5 and the [OpenCode setup] dependencies. Configure
Actions secrets `OPENAI_API_KEY` (with access to `gpt-6-astra`,
`gpt-6.1-sol`, and `gpt-5.6-terra`) and `CONTEXT7_API_KEY`.
The job uses OpenCode's native OpenAI provider; only the disposable Actions
checkout switches model routes. Workstations still use Bifrost.
Vox uses local eSpeak and Beadle uses a disposable loopback IMAP server;
the other local MCP state is isolated as well. Missing live credentials fail
the job and trigger the existing Nightly failure notification.

[OpenCode setup]: docs/opencode-setup.md

For a local release check, use the shipped `install.sh` for the intended
version and place the version-matched staged artifact in `dist/`. Candidate
smoke needs the native binary and `dist/checksums.txt`; post-release smoke
needs that same candidate manifest and access to the public release.
Both require a native Linux/macOS host, Bash, Python 3, and `shasum`.

## Test kinds and selectors

| Kind | Entry point | Regular placement and boundary |
| --- | --- | --- |
| Unit and component | `make test-unit` (default Go tests without a real subprocess) | Local, PR and main. Go selectors use `-race -count=1`; `make test` runs both kinds. |
| Subprocess | `make test-subprocess-fast`, `make test-subprocess`, `make check-storage`, `make coverage-subprocess` | Fast Git-hook paths at pre-push; broader Git, installer-fixture, CLI and MCP checks in PR/main. The subprocess statement profile includes the built Go child binaries. `check-storage` executes a real binary in an isolated fixture. |
| End-to-end | `make check-installed` [#670], `make test-e2e-smoke`, `make test-e2e`; native candidate and published smokes in [Release] | Installed Linux/macOS proof on PR/main; the separate wire smoke drives a real Claude CLI against local LiteLLM and stubbed providers. The full wire sweep runs on tags against a checkout-built binary, not the installed candidate. See [wire harness]. |
| Domain-specific | `make validate-content`, `make fuzz-check`, `make check-opencode`, `make check-plugin-cache`, `make test-legacy-corpus`, `make test-performance` | Validate Ethos content, Z specs, OpenCode config, cache switching, repair fixtures and redaction. The corpus runs on PR/main with release tag history. PR/main run the audit latency case twice: a dedicated race run verifies correctness, and an uninstrumented run enforces the timing budget without race or coverage overhead. |
| Live | `make test-live` (`make test-behavioral`), `make doctor-opencode` | Trusted Nightly/authorized local execution only. The live Anthropic judge scores outcomes from synthetic fixtures. The OpenCode smoke starts with doctor, which checks live dependencies; doctor is not an offline PR gate. |
| Agent | `make test-agent` (`make test-behavioral`), `make smoke-opencode` | Trusted Nightly/authorized local execution only. Claude follows or rejects mission instructions; OpenCode exercises all configured specialists and native tool paths. The wire stub is not live provider evidence. |
| Quarantined | `make test-quarantine` (inventory), `make test-quarantine-run` (execute only listed tests) | Never in ordinary Go selection. PR/main validate the inventory; Nightly reviews it daily. Use the separate run to diagnose and restore tests. |

[wire harness]: tests/e2e/README.md

The Go selectors split mixed packages by package directory and top-level test name using
[`tests/go-subprocess-tests.txt`](tests/go-subprocess-tests.txt). Add or update
an entry whenever a test or any of its subtests starts a real child process,
including Git processes started by fixture setup. An in-process mock does not
count.

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
An empty inventory is valid if no quarantined tests are selected. Validation
rejects unlisted quarantined tests in packages that gain test files under
the tag on the current platform. It also rejects a listed test that is still
selected by default; a listed test that does not collect is an error when
running quarantined tests.

[inventory]: tests/quarantine.tsv

## Conformance by clause

| Clause | Ethos evidence |
| --- | --- |
| TST-1 / GO-TST-1 | The trigger table names the gates and budgets. The local timer reports overruns without reducing the suite. |
| TST-2 / GO-TST-1 | The selectors above separate the applicable kinds. Release candidate and published runs select artifact evidence, not just Go packages. |
| TST-3 / GO-TST-1 | `make help` lists selectors; [#670] makes PR/main use `make test`. Local and CI use the same Go tests and race/count flags, apart from the explicit performance measurement. |
| TST-4 / GO-TST-2 | Offline suites use temporary repos and HOME, explicit subprocess fixtures and loopback provider stubs; [#670] isolates Git and prevents the regenerated agents from touching the checkout. |
| TST-5 / GO-TST-3 | `coverage.out` is Go package **statement** coverage and is ratcheted against the equivalently measured exact merge base (parent on main). CI separately reports subprocess, installed storage [#670], wire, and native release companion statement coverage. The reports exclude branch coverage, external tools/providers, historical compatibility builds, and the exact staged and published release bytes. |
| TST-6 / GO-TST-4 | [#671] gates secrets and live execution to upstream main; live requests, job time, and spend are bounded. Missing required credentials are red. |
| TST-7 / GO-TST-4 | [Release] verifies staged, installed native bytes across CLI, plugin manifest (`claude plugin validate`), MCP stdio, and release-critical hooks on all four runners, plus tag-time live behavioral verification against the exact `ETHOS_BINARY`, blocking promotion on any failed leg. |
| TST-8 / GO-TST-4 | [Release] installs the production binary and marketplace plugin (`ethos@punt-labs`) after publication, checks digest, version, CLI, MCP, and hooks, and delivers a verified Biff wall alert on failure (drillable via `force-published-failure=true`). |
| TST-9 / GO-TST-5 | Separately selected, owned, dated quarantines are listed in the tracked inventory and reviewed on Nightly. |
| TST-10 / GO-TST-5 | This guide names scopes, owners, and proposed expiries for remaining deviations. |

## Residual deviations

This table records the remaining deviations and the Nightly verification
completed after the four PRs.

| Clause | Scope and reason for deferral | Owner | Proposed expiry | Linear |
| --- | --- | --- | --- | --- |
| TST-1 / GO-TST-1 | Heavy affected packages may miss 30 s locally; measure representative changes and select smaller safe subsets without weakening the other gates. | Jim Freeman | 2026-12-08 | [PL-1585](https://linear.app/punt-labs/issue/PL-1585) |
| TST-1 / GO-TST-1 | Full pre-push took 654 s against a 180 s budget; optimizing and sharding the suite are outside the hook changes. | Jim Freeman | 2026-12-08 | [PL-1581](https://linear.app/punt-labs/issue/PL-1581) |
| TST-1 / GO-TST-1 | PR/main exceed 10 min and wire/tool jobs are not required; tuning and branch ruleset changes need separate work. | Jim Freeman | 2026-12-08 | [PL-1591](https://linear.app/punt-labs/issue/PL-1591) |
| TST-1 / TST-4 | The pinned scanner reports on PRs; add the `secret-scan` check to the main ruleset after it has reported success. | Jim Freeman | 2026-12-08 | [PL-1586](https://linear.app/punt-labs/issue/PL-1586) |
| TST-2 / GO-TST-1 | The corpus runs in PR CI. Verify the hosted Nightly's first successful live OpenCode run with direct OpenAI credentials. | Jim Freeman | 2026-12-22 | [PL-1584](https://linear.app/punt-labs/issue/PL-1584) |
| TST-6 / GO-TST-4 | Green Nightly on main and alert delivery: proved post-merge of [#671]. | Jim Freeman | — | — |
| TST-6 / GO-TST-4 | Git-visible assertions miss ignored agent writes; distinguishing them from legitimate Ethos runtime files requires a separate write-set design. | Jim Freeman | 2026-12-22 | [PL-1579](https://linear.app/punt-labs/issue/PL-1579) |
| TST-6 / GO-TST-4 | Overlapping Biff alerts are last-writer-wins; ordering pending alerts needs a separate policy. | Jim Freeman | 2026-12-22 | [PL-1580](https://linear.app/punt-labs/issue/PL-1580) |
| TST-8 / GO-TST-4 | Post-release Biff alert delivery drill (`force-published-failure=true`) requires running on `main` after merge. | Jim Freeman | — | — |
