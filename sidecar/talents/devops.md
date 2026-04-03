# DevOps

Engineering discipline that unifies development and operations. The goal
is short, reliable feedback loops: code committed to code running in
production, with every step automated, observable, and reproducible.

## CI/CD Principles

### Automate Everything

Any manual step in the path from commit to production is a step that
will be skipped, done wrong, or forgotten. Automate it or accept that
it will fail eventually.

This includes:

- Building artifacts (binaries, wheels, containers).
- Running tests (unit, integration, end-to-end).
- Linting and static analysis.
- Publishing releases and changelogs.
- Deploying to staging and production.
- Rolling back failed deployments.

### Fail Fast

The cheapest place to catch a bug is in the developer's editor. The
next cheapest is CI. The most expensive is production. Structure your
pipeline so the fastest checks run first:

1. **Lint and format** -- seconds. Catches syntax errors and style
   violations immediately.
2. **Unit tests** -- seconds to low minutes. Catches logic errors.
3. **Build** -- minutes. Catches compilation and dependency errors.
4. **Integration tests** -- minutes. Catches interface mismatches.
5. **End-to-end tests** -- minutes to tens of minutes. Catches system
   behavior regressions.

If step 1 fails, do not run steps 2-5. Every minute of CI time spent
on a doomed build is wasted.

### Green Main Branch

The main branch must always be in a deployable state. Broken main
blocks every developer on the team. Enforce this with:

- Required status checks before merge.
- Branch protection rules (no force push, no direct commits).
- Automatic rollback or revert when post-merge checks fail.

If main is red, fixing it takes priority over all feature work.

## GitHub Actions

### Workflow Structure

```yaml
name: check
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

permissions:
  contents: read

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: make lint

  test:
    runs-on: ubuntu-latest
    needs: lint
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: make test
```

Key practices:

- **Pin action versions** to full SHA or major version tag. `@v4` is
  acceptable. `@main` is not -- it can break without warning.
- **Set minimal permissions.** Start with `contents: read` and add
  only what the job needs. Never use `permissions: write-all`.
- **Use `needs` for dependencies.** Express the job DAG explicitly.
  Independent jobs run in parallel by default.
- **Read versions from source.** `go-version-file: go.mod` instead of
  hardcoding `go-version: 1.26`. One source of truth.

### Matrix Builds

Test across multiple OS and language versions when your project
supports them:

```yaml
strategy:
  matrix:
    os: [ubuntu-latest, macos-latest]
    go-version: ['1.25', '1.26']
  fail-fast: false
```

Set `fail-fast: false` so all combinations run even when one fails.
You need to see the full failure matrix, not just the first failure.

### Caching

Cache dependencies to cut build times. Go modules and build cache:

```yaml
- uses: actions/setup-go@v5
  with:
    go-version-file: go.mod
    cache: true
```

Python with uv:

```yaml
- uses: actions/cache@v4
  with:
    path: ~/.cache/uv
    key: uv-${{ runner.os }}-${{ hashFiles('uv.lock') }}
```

Cache keys must include the lock file hash. A stale cache that serves
old dependencies is worse than no cache.

### Artifacts

Upload build artifacts for debugging failed runs:

```yaml
- uses: actions/upload-artifact@v4
  if: failure()
  with:
    name: test-logs
    path: .tmp/test-output/
    retention-days: 7
```

Set retention to the minimum useful period. Artifact storage costs
accumulate.

## Containers

### Dockerfile Best Practices

```dockerfile
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/app ./cmd/app

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/app /bin/app
USER nobody:nobody
ENTRYPOINT ["/bin/app"]
```

Principles:

- **Multi-stage builds.** Build in a full SDK image, run in a minimal
  image. The final image contains only the binary and its runtime
  dependencies.
- **Copy dependency files first.** `COPY go.mod go.sum` before
  `COPY .` so dependency download is cached across builds. The full
  source copy busts the cache; dependency resolution does not change
  on every commit.
- **Run as non-root.** `USER nobody:nobody` or create a dedicated user.
  Running as root inside a container is unnecessary for most workloads
  and widens the blast radius of a compromise.
- **No latest tag.** Pin base images to specific versions: `alpine:3.21`,
  not `alpine:latest`. Reproducibility requires fixed inputs.
- **Minimal final image.** `alpine` or `distroless`. The fewer packages
  installed, the fewer CVEs to patch. If the application is a static
  binary, `FROM scratch` is even better.
- **One process per container.** Do not run supervisord or multiple
  daemons. Let the orchestrator handle process lifecycle.

### Image Size

Measure your images. A Go binary in a `scratch` image is typically
5-20MB. If your image exceeds 100MB, audit what is in it:

```bash
docker history --no-trunc <image>
```

Every layer is a cost: build time, push time, pull time, storage.

## Infrastructure as Code

### Declarative Over Imperative

Describe the desired state, not the steps to reach it. Declarative
definitions are idempotent -- applying them twice produces the same
result. Imperative scripts accumulate state and drift.

- Terraform, Pulumi, CloudFormation -- infrastructure.
- Kubernetes manifests, Helm charts -- workload orchestration.
- Ansible (declarative subset), Nix -- machine configuration.

### Version Everything

Infrastructure definitions live in version control alongside
application code. Every change is reviewed, tested, and auditable.

- Terraform state files are not "versioned infrastructure." They are
  runtime state. Store them in a remote backend with locking.
- Never apply infrastructure changes from a local workstation in
  production. Changes go through CI/CD.
- Tag infrastructure releases the same way you tag application releases.

### Environments

Maintain parity between environments. Differences between staging and
production are where production bugs hide.

- Same OS, same runtime versions, same configuration structure.
- Environment-specific values (endpoints, credentials, scaling
  parameters) injected via environment variables or secrets manager.
- Infrastructure code is parameterized, not duplicated per environment.

## Deployment Patterns

### Blue-Green

Two identical environments. One serves live traffic (blue), the other
is idle (green). Deploy to green, verify, switch the router.

- **Advantage**: instant rollback by switching back to blue.
- **Cost**: double the infrastructure during deployment.
- **Best for**: stateless services where instant rollback is critical.

### Canary

Route a small percentage of traffic (1-5%) to the new version. Monitor
error rates and latency. If healthy, gradually increase to 100%.

- **Advantage**: limits blast radius of bad deploys.
- **Cost**: requires traffic splitting and per-version monitoring.
- **Best for**: high-traffic services where gradual confidence matters.

### Rolling

Replace instances one at a time. Each new instance runs the new version;
each old instance is drained and terminated.

- **Advantage**: no extra infrastructure needed.
- **Cost**: mixed versions serve traffic simultaneously during rollout.
- **Best for**: services that are backward-compatible across one version.

### Feature Flags

Decouple deployment from release. Deploy the code, but gate the
behavior behind a flag. Enable the flag for internal users, then
a percentage of external users, then everyone.

- **Advantage**: deploy anytime, release when ready. Instant disable
  without redeployment.
- **Cost**: flag management overhead. Stale flags accumulate as tech
  debt.
- **Rule**: every flag gets a removal date. Review flags quarterly.
  Remove flags for features that have been fully released for more
  than 30 days.

## Monitoring and Observability

### The Three Pillars

Observability is the ability to understand a system's internal state
from its external outputs. Three signal types provide this:

**Metrics** -- numeric measurements over time. CPU usage, request
latency, error rate, queue depth. Metrics tell you what is happening
at aggregate level.

- Use histograms for latency, not averages. Averages hide tail latency.
- Define SLIs (Service Level Indicators) for each service: latency
  p50/p95/p99, error rate, availability.
- Alert on SLO (Service Level Objective) burn rate, not raw thresholds.
  "Error rate exceeded 1% for 5 minutes" is better than "error rate
  exceeded 0.1% for 1 second."

**Logs** -- discrete events with context. Request received, error
occurred, config loaded. Logs tell you what happened at a specific
moment.

- Structured logging (JSON or key-value pairs). Not free-form strings.
- Include: timestamp, level, message, request ID, service name.
- Do not log sensitive data (passwords, tokens, PII).
- Log at the right level: ERROR for things that need human attention,
  WARN for degraded operation, INFO for significant state changes,
  DEBUG for development only.

**Traces** -- request paths across services. A trace shows the full
journey of a request: which services it touched, how long each step
took, where it failed.

- Propagate trace IDs across service boundaries via headers.
- Instrument at service entry/exit points and at significant internal
  operations (database queries, cache lookups, external API calls).
- Sample traces in production. 100% trace capture is prohibitively
  expensive at scale; 1-10% sampling with full capture on errors gives
  sufficient visibility.

### Dashboards

Every service gets a dashboard showing its SLIs. The dashboard answers:
"Is this service healthy right now?"

Four golden signals (from Google SRE):

1. **Latency** -- time to serve a request.
2. **Traffic** -- requests per second.
3. **Errors** -- rate of failed requests.
4. **Saturation** -- how full the service is (CPU, memory, connections).

## Incident Response

### Runbooks

Every alert links to a runbook. A runbook answers:

1. What does this alert mean?
2. What is the user impact?
3. What should the on-call engineer check first?
4. What are the mitigation steps (in order)?
5. When should this be escalated, and to whom?

A runbook that says "investigate and fix" is not a runbook. Be specific:
"Check the database connection pool (`SELECT * FROM pg_stat_activity`).
If connections exceed 90% of `max_connections`, restart the pooler."

### Escalation

Define escalation tiers before an incident happens:

- **Tier 1**: on-call engineer. Follows the runbook. Mitigates within
  30 minutes or escalates.
- **Tier 2**: team lead or domain expert. Diagnoses root cause.
- **Tier 3**: cross-team coordination for systemic failures.

### Postmortems

After every significant incident, write a postmortem within 48 hours.

Structure:

1. **Summary** -- one paragraph. What happened, when, how long, who
   was impacted.
2. **Timeline** -- minute-by-minute from detection to resolution.
3. **Root cause** -- the actual cause, not "human error." Dig deeper:
   why was it possible for a human to make that error?
4. **Contributing factors** -- what made detection or resolution slower.
5. **Action items** -- specific, assigned, with deadlines. "Improve
   monitoring" is not an action item. "Add latency p99 alert for
   service X with 5-minute window, assigned to Alice, due March 15"
   is an action item.

### Blameless Culture

Postmortems identify system failures, not human failures. "Alice
deployed without checking" is not a root cause. "The deployment
pipeline does not require a health check before routing traffic" is
a root cause. Fix the system so the mistake is impossible, not so
the person is punished.

## Secret Management

### Never in Code

Secrets do not belong in source code, environment files committed to
git, CI configuration visible to all contributors, or container images.

- `.env` files: gitignored, never committed. Use `.env.example` with
  placeholder values.
- CI secrets: use the platform's secret store (GitHub Actions secrets,
  GitLab CI variables marked as protected and masked).
- Application secrets: use a vault (HashiCorp Vault, AWS Secrets
  Manager, GCP Secret Manager).

### Rotation

Every secret has an expiration and a rotation procedure:

- API keys: rotate every 90 days or on team member departure.
- Database credentials: rotate every 90 days. Use short-lived
  credentials where possible (IAM database auth, Vault dynamic
  secrets).
- TLS certificates: automate renewal (Let's Encrypt, cert-manager).
  Never manually manage certificate expiry.

### Least Privilege

Grant the minimum access required. A CI job that deploys does not
need read access to the secret store's admin API. A web server does
not need database admin credentials.

## Dependency Management

### Lock Files

Every project commits its lock file: `go.sum`, `uv.lock`,
`package-lock.json`, `Cargo.lock`. Lock files ensure reproducible
builds -- every developer and every CI run resolves the same versions.

- Never gitignore lock files for applications. (Libraries are different;
  some ecosystems intentionally omit locks for libraries.)
- Review lock file changes in PRs. A surprising version bump in a
  lock file is a signal worth investigating.

### Automated Updates

Use Dependabot, Renovate, or equivalent to propose dependency updates
as PRs. Review and merge them weekly.

- Group related updates (e.g., all `actions/*` updates in one PR).
- Run the full test suite on dependency update PRs. A green CI is
  the minimum bar for merging.
- Do not auto-merge major version bumps. Read the changelog.

### CVE Scanning

Scan dependencies for known vulnerabilities on every build:

- Go: `govulncheck ./...`
- Python: `pip-audit` or `safety`
- Node: `npm audit`
- Containers: `trivy image <image>`

Block merges when critical or high CVEs are found. Medium and low
CVEs get tracked and patched within 30 days.

## Shell Scripting

### POSIX Portability

Write for `/bin/sh` when the script must run on unknown systems
(install scripts, CI setup). Use `#!/bin/bash` only when you need
bash-specific features (arrays, `[[ ]]`, process substitution).

### Strict Mode

Every bash script starts with:

```bash
#!/usr/bin/env bash
set -euo pipefail
```

- `set -e` -- exit on first error.
- `set -u` -- treat unset variables as errors.
- `set -o pipefail` -- a pipeline fails if any command in it fails.

Without these, scripts silently swallow errors and continue executing
with corrupted state.

### ShellCheck

Run ShellCheck on every shell script in CI. It catches:

- Unquoted variables that break on spaces in paths.
- Useless use of cat and other antipatterns.
- Bash-isms in scripts with `#!/bin/sh` shebangs.
- Word splitting and globbing bugs.

Zero ShellCheck warnings is the standard. Disable specific warnings
only with an inline comment explaining why.

### Quoting

Quote all variable expansions: `"${var}"`, not `$var`. Unquoted
variables undergo word splitting and glob expansion, which breaks on
filenames with spaces or special characters.

The only place to omit quotes is when you intentionally want word
splitting (rare) and you document why.

## Makefile Conventions

### Phony Targets

Declare all non-file targets as `.PHONY`:

```makefile
.PHONY: build test lint check clean

build:
    go build -o ethos ./cmd/ethos

test:
    go test -race -count=1 ./...

lint:
    go vet ./...
    staticcheck ./...

check: lint test

clean:
    rm -f ethos
```

Without `.PHONY`, a directory named `test` would prevent `make test`
from running.

### Help Text

Provide a `help` target that lists available targets with descriptions.
Make it the default:

```makefile
.DEFAULT_GOAL := help

.PHONY: help
help: ## Show available targets
    @grep -E '^[a-zA-Z_-]+:.*##' $(MAKEFILE_LIST) | \
        awk 'BEGIN {FS = ":.*## "}; {printf "  %-15s %s\n", $$1, $$2}'
```

Annotate each target with `## Description` on the same line as the
target declaration.

### Dependency Chains

Express dependencies between targets:

```makefile
check: lint test          # lint runs before test
install: build            # build runs before install
```

Do not duplicate commands. If `check` needs `lint` and `test`, list
them as prerequisites -- do not copy the lint and test commands into
the check recipe.

## Release Management

### Semantic Versioning

`MAJOR.MINOR.PATCH`:

- **MAJOR** -- breaking changes. Users must modify their code.
- **MINOR** -- new features, backward compatible.
- **PATCH** -- bug fixes, backward compatible.

Pre-release: `1.2.0-rc.1`. Build metadata: `1.2.0+build.42`.

### Changelog

Every release has a changelog entry. See the Documentation talent for
format details. The changelog is written incrementally under
`[Unreleased]` as changes merge, then stamped with a version number
at release time.

### Release Automation

Tag-driven releases. The process:

1. Update `CHANGELOG.md`: move `[Unreleased]` entries under a new
   version header with today's date.
2. Update version constants in source code.
3. Commit: `chore: release vX.Y.Z`.
4. Tag: `git tag vX.Y.Z`.
5. Push tag: `git push origin vX.Y.Z`.
6. CI builds artifacts and creates a GitHub release with the changelog
   entry as the body.

Automate steps 4-6 completely. Steps 1-3 may be manual or scripted
depending on the project.

### Post-Release

After tagging a release:

1. Create a post-release commit that bumps the version to the next
   development version and adds a fresh `[Unreleased]` section.
2. Update dependent projects to reference the new version.
3. Verify downstream CI is green after the version bump.

## Anti-Patterns

- **Snowflake servers.** Servers configured by hand that cannot be
  reproduced. If you cannot destroy and recreate it in under an hour
  from code, it is a snowflake.
- **CI as testing theater.** A green pipeline that runs no meaningful
  tests. Passing lint and one happy-path test is not confidence.
- **Alert fatigue.** Hundreds of alerts, most of which are noise.
  Every alert must be actionable. If it fires and the response is
  "ignore it," delete the alert.
- **Deployment fear.** Deploying is scary because it is rare and
  manual. Deploy frequently (daily or more) with automation so it
  becomes routine.
- **"It works on my machine."** If the CI environment differs from
  local development, you will ship bugs. Containerize the development
  environment or document exact setup requirements.
- **Manual rollback.** If rolling back requires SSH access and 15
  minutes of commands, you will hesitate to roll back. Automate
  rollback to a single command or button.
