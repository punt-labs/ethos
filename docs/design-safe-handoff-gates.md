# Safe-by-default SubagentStop handoff gates

**Status**: PROPOSED as DES-086 (PL-1627). For operator review; the spike
is not a release candidate. This design does not change commit or edit checks.

Read first: DES-085 and [project-defined agent checks](design-agent-checks.md),
DES-083 on binary-owned hook policy, and DES-079/DES-084 on local storage.

## 1. Problem

SubagentStop runs on replies, not just completed work. A Vox unit reported
about 18 runs of the same full suite, including four runs on one reviewer's
retries. Each run could fan out to all cores. The reported one-minute load on
keble exceeded 160. Capping pytest workers is useful but does not stop 18
launches, and it cannot bound an unrelated command or a second repository's
gate. The admission policy belongs in Ethos; the check command belongs to
the project.

PR #654 does not classify Bash calls as writes. It compares the selected
policy and checkout fingerprint when the audit shows no own or recent
unattributed file-tool edit. A Bash call with no change does not defeat that
rule. If a worker changes the shared checkout while a reviewer is active,
the fingerprint differs when the reviewer stops, regardless of which agent
ran Bash. This path is reproducible; which path each production stop took
on keble was not measured.

## 2. Separate eligibility, identity and admission

1. Keep roster leave and the current read-only skip for policies without a
   worktree sidecar. A sidecar requires system and runtime proofs that are
   not part of the checkout fingerprint, so even unchanged stops go through
   admission for the whole policy, including its other checks. Admission can
   still reuse a matching verified verdict. Do not launch anything unless a
   configured check matches the handoff inputs. Keep
   `~/.punt-hooks-kill` ahead of input, storage, and scheduling.
2. Record dispatch role at SubagentStart from the declared mission and
   validated binding: worker, non-worker or unknown. A seeded checklist
   agent can also be classified when the harness can verify the deployed
   definition; a handle alone is not proof. Skip a confirmed non-worker
   only when its execution context enforces read-only access to the checked
   tree (including Bash) and its readable audit shows no own or recent
   unattributed Write, Edit, MultiEdit or NotebookEdit. Treat a reviewer
   with write-capable Bash like an unknown for gate eligibility. An audited
   non-worker edit goes through admission, not silently past it.
3. Preserve the checkout fingerprint fallback for workers and unknowns.
   Neither an empty audit nor a Bash record proves authorship of a change.
   A fingerprint change is sufficient evidence to request admission, not
   evidence that this agent authored it. A capability or isolated-worktree
   binding, where available, is stronger than inference from a shared tree.
4. Admission is per checkout and execution plan, not per reply, agent ID
   or session. Use a versioned key over the physical checkout identity,
   HEAD, index and effective dirty content, resolved policy, logical argv
   and cwd, and the enforced execution profile. Represent the checkout
   root symbolically and file and directory operands relative to it;
   the key never includes the temporary snapshot path. Policy may select
   different commands for different roles; two genuinely different plans
   must not inherit each other's result.

The current roster contains the harness agent ID, agent type and persona;
it has no general worker/non-worker field. A verifier spawn with a validated
declared mission supplies a positive evaluator binding; ad-hoc reviewers
do not necessarily have mission records. A file-tool audit proves a
positive attribution when present but cannot prove that a Bash write did
not happen. The spike recognizes only exact project-installed checklist
definitions (and bound evaluators). The Vox checkout examined here did not
have project files for the two named checklist agents; exact project file
matching is not a production deployment plan for them.
The spike does not enforce non-worker write isolation; its classifier
therefore cannot be shipped as a proof that those agents did not edit.

## 3. Single-flight and bounded execution

For an eligible key, a coordinator takes a keyed lease before consulting the
result or launching. A second request is a waiter, not a second launch. Check
the result again after acquiring the lease. Use one machine-wide heavy
automatic gate slot per Ethos installation, initially one per operator on
a normal single-user host. A managed multi-user machine needs a host
coordinator; separate home-directory locks are not machine-wide across UIDs.
Do not hold a roster or audit lock while waiting or running a check.

Use one end-to-end deadline for SubagentStop. Today the wrapper has a
1,800-second harness limit and the binary separately has a 1,800-second
handoff cap. The two limits must not renew each other. Set the internal
deadline before the harness limit, accounting for wrapper input and startup.
Start the clock at wrapper entry when a compatible wrapper supplies its
start time; reserve at least 5 seconds before the harness cutoff for
scheduling and output delivery. When an older wrapper supplies no timing
metadata, default to a shorter binary budget (proposed: 29 minutes from
binary entry under the existing 30-minute hook timeout), leaving headroom
for its bounded stdin read and startup.

Key preparation, admission, all invocations, process-tree cleanup and
feedback spend from that same deadline. Reserve the final 10 seconds of the
internal budget for cleanup and feedback; do not start an invocation if
that reserve would be consumed. Cap each command by its configured timeout
and the remaining execution time. When the shared deadline cuts a check
short, report **deferred** before the harness cutoff, with timeout as the
reason, whether or not cleanup has finished. Never cache this outcome.
The coordinator retains the slot until cleanup completes. The next stop
with the same key may run again once the slot is free, subject to the same
keyed admission, wait budget and host-load rules. Do not retry in the
background. The current `runEditCheck` formats a deadline expiry as failure
text; preserve the timeout as a distinct status for the handoff decision
rather than treating that text as a completed failing check.

Within the execution portion of that deadline, use a single wait budget of
at most 10 seconds for a running owner, a free slot or load to drop. Sample
one-minute host load immediately
before dispatch and poll at most once a second while waiting. Default limit:
40, with an operator setting to lower it. Observe that load is a lagging
signal, not a parallelism limit. A cached result can be used at high load
because it does not start a command. If the deadline, load probe or
coordinator prevents admission, report **deferred**, never **passed**.

Today check sets have no resource classification. Add a handoff check
classification with `heavy` as the default, including for existing check
sets, arbitrary argv, shell scripts, and wrappers such as `make check`.
Do not infer cost from the adapter or executable name. A project may
explicitly declare a check `light`, but that declaration is not a resource
guarantee. Light checks still use bounded admission and execution; only
binary-approved, resource-bounded light operations may use a lighter profile.
An arbitrary command without such a profile uses the heavy limits even if
declared light.

For automatic heavy gates, default to the one slot and a process-tree CPU
budget equivalent to **two cores**. The binary must enforce the budget,
not merely offer a Make variable. In the initial Linux implementation,
evaluate a delegated cgroup v2 with CPU quota, PID and memory ceilings,
I/O weight and process-tree cleanup. Resolve limits against available
resources; do not claim a CPU quota alone bounds disk or memory pressure.
Automatic execution requires a verified
enforcer; if none is available, defer visibly and offer the explicit
operator gate. Explore equivalent enforcement on other platforms before
automatically running arbitrary heavy commands there. Setting worker hints
for supported tools makes runs more efficient but is not an enforcement
mechanism. A gate process that leaves behind a child must not release the
slot while that child is running. Do not start a queued gate in the
background after the hook has said it deferred.

Store minimal redacted terminal feedback and versioned state atomically
below the canonical store's `.punt-labs/local/ethos/`. Bound retention,
including lock files, and keep stable lock identity for keys that can still
be in use. An interrupted or lost owner is not a passing result. Reconcile
the process tree before releasing its slot; do not automatically relaunch an
ambiguous attempt. An explicit retry can invalidate that result, after the
owner has exited, without changing the tree. A new tree or policy qualifies
normally. Keep hit/miss, wait, launch, defer and reuse outcomes in redacted
hook diagnostics, with durations and a reason code.

The initial cache keeps up to eight results per lock stripe. It retires
results older than 24 hours when the stripe is visited, then retires the
oldest other keys if necessary to stay within the cap. Requests pin the
stripe before waiting. A full pinned stripe defers a new key rather than
discarding a result a waiter can still read. The kernel releases the
exclusive lease on owner death; once the lease is acquired again, a
pending record denotes an orphan, not a terminal result, and the next
eligible request runs the gate.

## 4. Correctness, costs and feedback

The fingerprint must cover staged changes, unstaged bytes and non-ignored
untracked input, including relevant nested repos and status-hidden tracked
paths. Git status alone or a list of edited paths is insufficient. Bound the
amount of key work and do not accept a partial digest: start with a one-second
wall budget and 256 MiB of dirty bytes as proposed ceilings to validate.
Exhaustion produces a visible deferral, not an unkeyed full-suite launch.
Reuse a fingerprint computed for the same decision instead of doing three
unconditional full scans as the spike can on a miss.

For either a reusable pass or a reusable failure, run against an immutable
materialized input snapshot whose verified digest matches the selected
key, or require equivalent proof of snapshot consistency. A mismatched
or unverified run is indeterminate: never cache it as a failure or reuse
it to block a restored tree. Reuse a terminal result only after matching
the current key to its recorded snapshot proof. A
before-and-after hash on a live checkout can catch many races, but misses
a change followed by a revert, and does not prove what a running command
read. Locking Ethos requests does not lock human or Bash edits. Start with
checks that are compatible with an isolated snapshot. Resolve matching and
grouping as checkout-relative paths. At launch, set the working directory
under the snapshot root and expand `{root}` to that root and `{files}` to
snapshot-local paths; resolve `{package_dir}` in the snapshot as well.
Build the key from the logical invocation before substituting the
temporary snapshot root for execution. Materializing the same plan in
another directory must not change the key. Changing only the working
directory is insufficient: no expanded
argument may still point to the live checkout. Literal paths into that
checkout or wrappers that reopen it are ineligible unless execution
isolation makes those accesses resolve to the same verified snapshot.

A project that
depends on ignored files, mutable tools, ambient environment or network
state must declare inputs and a cache epoch, or use an explicit gate until
its dependencies can be represented. Do not put environment values,
credential material or mutable host paths in a result record.

Use explicit outcomes:

| Outcome | Hook behavior |
| --- | --- |
| Ineligible, unconfigured or killed | No execution; preserve the existing quiet path. |
| Passed for the key | Skip execution; record a cache hit if reused. |
| Failed for the key | Reuse the redacted result without rerunning. Respect advisory/block policy; on `stop_hook_active` emit a visible notice without blocking re-entry. |
| Deferred or indeterminate | Do not record pass or fail. Report the reason and what can be retried. A required gate blocks the initial stop; advisory deferrals and re-entry get a visible notice without blocking. |

Shell exits 129, 130, 137 and 143 are treated as likely interruptions: defer visibly, never cache the outcome, and allow the next stop to retry.

The current plugin wrapper redirects stderr to the hook log. Send advisory
and re-entry feedback on stdout as a single Claude Code hook JSON response
with a top-level `systemMessage`, exit zero, and omit `decision` for
non-blocking outcomes. Include `decision: block` and `reason` only when the
initial stop must block; if advisory feedback also exists, include it in the
same response. Keep redacted diagnostic details in the hook log rather
than relying on stderr to reach the session. Verify that an actual Claude
Code SubagentStop visibly renders the message and permits re-entry.

Unconfigured handoffs remain fail-open. An unavailable safety mechanism
never turns into permission to launch. An operator can still run the
explicit project gate. A later mission-result gate must not infer that a
stop allowed on re-entry was a verified pass.

### Snapshot check profile

The Linux worktree profile is declared in
`.punt-labs/ethos/checks/snapshots/<check-set>.yaml` beside the selected
check set. There is no separate `handoff-worktree.yaml` to install.

For a selected check set named `project`, define a handoff check such as
`verify-manifest` in `.punt-labs/ethos/checks/project.yaml`. Configure it in
`.punt-labs/ethos/checks/snapshots/project.yaml`:

```yaml
checks:
  verify-manifest:
    profile: linux-worktree-v1
    epoch: v1
    sources: {mode: worktree}
```

The worktree profile selects Git-visible checkout files.

Explicit `ethos handoff fetch-runtime` downloads honor the operator's
`HTTPS_PROXY` and `NO_PROXY` settings. For a tunnel the fetcher resolves
the asset hostname locally and sends CONNECT only to a checked public IP,
while TLS still verifies the original asset identity. The operator's HTTP
or HTTPS proxy can be local; the checkout cannot choose the proxy endpoint.

The older inline `linux-shell-v1` profile instead lists its inputs:

```yaml
- name: verify-manifest
  stage: handoff
  files: all
  run: [/bin/sh, verify-manifest.sh]
  snapshot:
    profile: linux-shell-v1
    inputs: [verify-manifest.sh, manifest.txt]
    epoch: v1
```

List every required checkout file as a literal relative path. The runner
copies the declared regular files into a private in-memory filesystem and
verifies their digest before launch. Directories, symlinks, Git metadata,
and Ethos local state are not inputs. Checks run in a read-only filesystem
with a minimal environment, no host checkout, no home directory, and a
private network namespace. The profile supplies the host's standard system
shell, binaries and libraries on read-only mounts; change the epoch when the
check depends on a changed system runtime. Do not opt in a command that
depends on additional host files, live Git state, or nondeterministic inputs.
A host without the required Linux namespaces defers rather than running
live. An undeclared gate such as `make check` must be run explicitly.

Keep scheduling in the binary. No new wrapper flags, stdin behavior or
generated SubagentStop shim are required. A newer wrapper can pass timing
metadata via an environment variable that older binaries ignore. Older
plugins can invoke the new binary with the existing command and use the
conservative deadline fallback. A new plugin against an old binary cannot
enforce these limits; doctor/rollout must distinguish that state rather than
promise protection. If a later shim depends on a new binary capability, use
an explicit probe and keep the old path and kill switch intact.

## 5. Spike evidence

Baseline: Ethos `1b98594`. Reference spike: `00375835`. All measurements
below were on the local Linux development host, not keble. No Vox files or
settings were changed, and no real Vox suite was launched.

| Experiment | Baseline or input | Spike / result |
| --- | --- | --- |
| One fingerprint, Vox `cfeb1cf` | Clean checkout, 1,929 tracked files | 10.9 ms median, 10.0–13.8 ms, 11 samples |
| One fingerprint, synthetic | 1,024 tracked dirty files, 256 MiB of changed content | 704.9 ms median, 665.2–754.0 ms, 11 samples |
| 18 stops, passing command | 18 launches, 18.876 s | 4 launches, 4.667 s |
| 18 stops, failing command | 18 launches, 18.726 s | 4 launches, 4.845 s; re-entry replayed the original failure |
| Two simultaneous stops, same key | 2 launches | 1 launch, both stops received the same result for pass and failure |
| Load above threshold | Measured 2.36, test threshold 0.001 | 10.033 s wait, zero launches; visible deferral |

The fingerprint measurement calls `handoffState(root, "")`: policy resolve
and hash plus checkout state, not a full stop, audit scan, or all the
spike's revalidations. To reproduce the synthetic case, commit 1,024 small
files and the Vox check policy, replace each file with 256 KiB, then time
11 calls, including the first. The observed result justifies hashing for
a normal checkout and a hard budget for exceptional trees.

The replay used the built CLI with an isolated home and checkout. It sent
SessionStart, then SubagentStart for all reviewers, and interleaved seven
worker stops, two evaluator stops, four silent-failure-hunter stops and five
code-reviewer stops. Three Bash-style writes and one intervening commit
created four distinct states; three worker stops replayed the prior stop.
The `files: all` gate appends to a counter outside the checkout, sleeps one
second, and exits with a controlled status. Seeded reviewers had exact
fixture definitions; the synthetic evaluators intentionally had no dispatch
binding and exercised cache fallback. For the contention test, two worker
stops against a new unchanged state were launched together. For load
deferral, the threshold was set below ambient load without generating load.
These measurements demonstrate reuse and local Linux contention, not the
proposed host-wide slot, process-tree budget or immutable snapshot.

The exact load feedback was: “handoff checks deferred after waiting:
one-minute host load 2.36 is above the limit of 0.001”. A later stop with
the threshold above ambient load ran one check; deferral had not poisoned
the cache.

## 6. Alternatives and acceptance

- **Repo-only wrapper or a worker-count knob.** Reject as the whole fix:
  it cannot coordinate multiple projects or callers. Keep a small Vox
  worker default and correct test selection as separate repo work; projects
  still own commands, checked paths, required checks and CI policy.
- **HEAD alone, mtimes, audit alone, or skip every dirty tree.** Reject:
  shell edits, staged state or untracked inputs would go unverified. Retain
  scoped content hashing and make time/byte limits explicit.
- **Trust reviewer names or classify every Bash tool call as a write.**
  Reject: names can be overridden and Bash can be read-only; neither proves
  attribution.
- **Only a per-tree lock or only a load threshold.** Reject: different
  trees still launch together and a load probe lags. Require both keyed
  coalescing and host admission with execution enforcement.

Before releasing, prove concurrent same-key and different-key requests
across linked worktrees and two repositories, stale owner recovery,
failure replay and explicit retry, timeout and process-tree cleanup,
policy/content invalidation, snapshot consistency under concurrent Bash
edits, and old/current plugin-binary pairings. Demonstrate the Linux
resource budget with a bounded synthetic fan-out, then do a supervised
Vox canary with load sampled every ten seconds and correlated hook log
events. Obtain operator sign-off on limits, platform deferral and required
gate feedback before merging implementation.

### Consumer rollout and release gate

Do not release the change that enables snapshot-only handoff execution
until the consuming projects have verified replacement profiles. Land
those profiles with or immediately after the Ethos change and before
its release. A visible deferral on an unsupported host is intentional;
it is not a replacement for the existing automatic gate on supported
hosts.

Stage the policy definitions before activation. The older check-set
parser rejects unknown fields, so don't point older binaries at a check
set containing `snapshot`. Canary with a snapshot-capable binary and a
separate check set; keep the existing check set active elsewhere until
the binary and policy can be switched together. Verify that mixed-version
deployment before publishing the release.

Start with Vox. Preserve all eight components of its `make check`
target: lint, type checking, Markdown lint, API docs, tests, and the
OO, coupling, and suppression ratchets. The initial `linux-shell-v1`
profile only exposes declared regular files and a read-only system
runtime. It does not provide Vox's UV environment, Node tooling,
writable build paths, or Git merge-base inputs. Do not declare the
existing command snapshot-compatible as-is. First prototype an offline,
pinned runtime and writable scratch isolated from the source snapshot;
have the project specify the source files, dependency locks, tool
versions, and Git baseline inputs it needs. Extend the execution profile
if the full gate cannot be reproduced within the current one. Bump the
epoch on runtime changes. Compare the replacement against the explicit
`make check` for known passing and failing changes before enabling it.

Use the same acceptance test for the other handoff gate policies:

| Consumers | Profile work to verify |
| --- | --- |
| Beadle, Biff, Cryptd, Ethos, MCP Proxy, Points, Quarry | Pin the Go and native tools; include module, build, quality gate, and baseline inputs; pre-provision dependencies offline. |
| Lux, PRFAQ, Public Website | Inventory locked dependencies and test, docs, and build tooling; provide offline runtimes and isolated output paths. |
| Postern | Verify ShellCheck and Markdown lint against the declared config and selected files with pinned runtimes. |
| Anthropic SDK Pharo, Claude Agent SDK Smalltalk, Koch Trainer Swift, Homebrew Tap | Verify the image, SDK, or Homebrew runtime and platform requirements; preserve each gate's required or advisory setting. |
| Punt Kit, Punt Labs workspace, Z Spec | Inventory tool-specific binaries, specs, scripts, and cross-repository dependencies before choosing one or more profiles. |

Re-scan consumers' effective check sets before release, including layered
and workspace policies. For each, retain evidence that a dirty edit
launches the automatic gate, a known failure is surfaced with the
existing severity, and reuse does not hide a later change. Verify
unsupported hosts get the named manual gate on stdout; macOS continues
to defer until its isolation mechanism is verified on a real host.
