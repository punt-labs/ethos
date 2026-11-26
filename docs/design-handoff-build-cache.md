# DES-086 amendment: opt-in snapshot build cache

**Status:** Approved by the operator in PR #706 and implemented for
Linux worktree snapshots by this branch. Gates remain cold by default.

## Scope and key

An unchanged snapshot gate may opt in to a Go build cache in its check
entry in `.punt-labs/ethos/checks/snapshots/<slug>.yaml`:

```yaml
cache:
  - name: gocache
    env: GOCACHE
```

Omitting `cache` keeps the existing behavior. The Linux runner supports
only the pair above, once, with a pinned runtime for the current
platform. It does not share writable module, npm, or other caches. The
resolved declaration belongs in the handoff result key alongside the
runtime pin and existing snapshot proofs. A cache hit does not replace
a gate run on a result-cache miss.

The runner resolves the canonical shared repository store
root `S` as specified in [Storage layout](storage-layout.md).
Keep cache artifacts and their lock and generation state under
`S/.punt-labs/local/ethos/handoff-cache/<runtime-digest>/`. A cache
namespace is identified by the store root, runtime digest, and cache
name. Linked checkouts share that namespace and its lock; distinct
local clones and different runtime digests do not. Invalidate the build
cache if the verified system-input fingerprint changes: Go does not
necessarily notice changes to libraries used through cgo.

## Containment and detection

Hold one writer lock per namespace from preparation through promotion.
Open the persistent cache only on the host. Mount a separate writable
copy in the sandbox, set `GOCACHE` to it, and never mount the persistent
directory writable. After the gate passes and the closing proofs still
match, validate the private tree and atomically promote it under the
same lock. A failure, timeout, interruption, or proof mismatch discards
the private copy without changing the persisted cache. The runner must
reject links, special files, and over-budget trees. No cache error
authorizes a live run or turns an incomplete gate into a pass.

Go uses content-derived action and output IDs to distinguish compiler
inputs and outputs. That is a reuse mechanism, not an authentication
boundary: a passing command with write access to the private copy can
plant deliberately crafted artifacts, and ordinary cache lookups need
not independently rebuild them. A poisoned entry is restricted to the
same repository and runtime digest, but it can influence later gates in
that namespace. Host processes with the operator's permissions can
also alter the stored cache. Do not opt untrusted gate commands into
this feature. For suspected corruption, compare against a cold run;
`GODEBUG=gocacheverify=1` is an additional Go diagnostic, not a guarantee.

Add `ethos handoff cache clear` to clear the invoking repository's
build-cache namespaces under their writer locks. The repository budget
is 10 GiB and four runtime-digest namespaces, with at most 100,000
filesystem entries per published artifact tree. Count every generation
directory on disk, including retired generations whose cleanup failed, against the
repository's disk budget. Evict least-recently-used inactive namespaces
within that repository; skip active locks. There is no cross-repository
inventory or machine-wide budget: total usage depends on the budgets
of the repositories using the cache. Use `ethos handoff cache clear`
to reclaim space. If that command is unavailable, stop all processes
using the cache and remove only `.punt-labs/local/ethos/handoff-cache/`
from the shared store. This subtree contains no audit logs or receipts.
No follow-up generation bump is required to invalidate verdicts that
used the deleted cache.

To publish, take a single repository-level budget lock covering every
runtime digest while holding the namespace lock. Reserve space before
promotion; when eviction needs another namespace lock, try it without
waiting and skip active namespaces. If eviction cannot make room, skip
publication, leave that namespace's persisted cache unchanged, and log
the skip. The completed gate's verdict still stands.

Clearing or evicting a namespace must invalidate verdicts that could have
used it. Store a generation for each namespace in the sibling
`handoff-cache-state/` directory alongside a stable namespace lock
that also survives deletion. Keep artifacts at
`S/.punt-labs/local/ethos/handoff-cache/<runtime-digest>/g<N>/<name>`,
and seed the private copy only from the current generation's directory.
A missing directory means a cold cache. Under the lock, both
`cache clear` and eviction persist a new, never-reused generation
before attempting to delete non-current directories. Each cache preparation
also attempts this cleanup under the same lock, before mounting the
private copy. Deletion is best-effort: a crash after the bump leaves old
artifacts on disk but makes them ineligible for reuse, and any remaining
directories still count against the disk budget.
For an opted-in check, read the generation and include it in the
verdict key before looking for a reusable verdict; hold the lock
through the reuse decision. A running gate may publish artifacts and
record a verdict only under the generation it opened with.
For each verdict backed by a persisted cache, also record the ID of
the generation that supplied it. At generation creation, write a fresh
random ID to a host-owned file in the generation directory, outside the
artifact tree that is copied and promoted. Promotions within that
generation preserve the ID; clear and eviction start a generation
with a new ID. On a candidate verdict hit, before skipping preparation,
read the current generation's persisted ID file under a shared lock on
the repository's cache namespace. Reuse only if the generation
directory and ID file exist and the ID matches the recorded ID. Treat
a missing directory, missing or unreadable ID, or mismatched ID as a
verdict miss and proceed to gate execution. Manual deletion of the
cache subtree also deletes the ID.
During preparation, compare the ID file with the expected ID in the
persisted generation metadata. If the directory or ID is missing, or
the ID does not match, delete that entire generation directory under
the namespace lock and recreate it empty with a fresh random ID.
Persist the replacement ID before mounting a private copy. If reset
cannot complete, do not mount those artifacts. A cache without a valid
ID is never reused.

## Measurements

The throwaway Linux amd64 prototype was based on Ethos `8c89c4c`.
It used the original home-scoped layout for the benchmark:
`~/.punt-labs/local/ethos/handoff-cache/<repo-id>/<runtime-digest>/<name>`,
with `repo-id` derived from the canonical local store path.
These are wall-clock seconds for the unchanged `make check` gates.
The hook log confirmed a gate launch for every snapshot in the table;
no cell is a reused hook verdict.

| Repository | Tree | Cold snapshot | Warm snapshot | Warm live |
| --- | --- | ---: | ---: | ---: |
| Points `ffa4d06` | Clean | 75.067 | 29.892 | 12.795 |
| Points `ffa4d06` | Broken | 33.735 | 16.495 | 1.107 |
| Cryptd `e0db489` | Clean | 75.389 | 24.117 | 6.611 |
| Cryptd `e0db489` | Broken | 27.966 | 10.738 | 0.233 |

Warm snapshots saved 45.175 s and 51.272 s on the clean gates.
The prototype copied caches of about 585 MB and 711 MB respectively;
runtime verification, extraction, and sandbox setup still ran. Warm
snapshots were slower than warm live gates in all four cases. A
separate sandbox fixture verified that a failing gate could overwrite
and add files in its private copy without changing the published tree.

### Replay

This replay requires the throwaway prototype branch
`devin/1795676128-build-cache-prototype` at `864d201` (based on `8c89c4c`;
not for merge). Building `8c89c4c` or main alone does not enable
snapshot build-cache reuse.

1. Use disposable checkouts of Points #38 at `ffa4d06` and Cryptd #131
   at `e0db489`. Install each sidecar's pinned Linux amd64 archive with
   `ethos handoff fetch-runtime`, or use `fetch-runtime --file <archive>`
   for the digest-matching Cryptd archive.
2. Add the opt-in to the existing sidecar check. In the local Ethos
   checkout of `864d201`, run `mkdir -p .tmp/opencode` and
   `go build -o .tmp/opencode/ethos-build-cache ./cmd/ethos`. With an
   empty build-cache scope, invoke the binary by absolute path with
   `hook subagent-stop` from the consumer checkout. Use a payload with
   `session_id`, `agent_id`, and `agent_type`. Record wall time, JSON,
   and the gate launch and verdict in the hook log.
3. For the clean warm run, keep the cache published by the clean pass
   and add a harmless comment to tracked `README.md` to change the
   snapshot key without changing the Go inputs. For the broken runs,
   add unmatched Go syntax to the tracked CLI entry point. Use a
   separate empty cache scope for cold and the clean pass's cache for
   warm. Use distinct comments on the two broken edits so a cached
   failure cannot masquerade as a warm gate run.
4. For live warm, extract the same archive, use its Go and tooling
   paths, run `make check` once on the clean tree to prime a separate
   `GOCACHE`, and time `make check` on the clean and broken inputs.
   Clean must pass and broken must fail in both modes.
