# Hook Timing Spike

## Summary

**Pre-commit wins on atomicity and failure semantics; post-commit synchronous wins on nothing; post-commit asynchronous wins on user-visible latency only.** All three add the same ~1.2s of capture work. The differences are *where* that 1.2s lands and *what happens when it fails*. Pre-commit makes the user wait 1.2s but guarantees code+capture ship together and broken captures abort the commit. Synchronous post-commit makes the user wait the same 1.2s, splits the result into two commits, and silently loses the capture on failure. Async post-commit hides the wait (user sees 70ms) but commit history interleaves capture commits between user commits and the capture file is uncommitted for ~1.16s — long enough for races with subsequent user actions on a fast typist.

## Methodology

Three throwaway local-only git repos under `.tmp/spike-hook/{pre,post,post-async}/`. Each repo gets its mode-appropriate hook wired to the real `.bin/jsonl-to-quarry.py | .bin/scrub-pre-ingest.py` pipeline operating on a fixed 38 MB input JSONL (the ethos session) producing a 2.9 MB markdown file. A fourth repo (`baseline-repo`) measures bare `git commit` cost with no hook.

Commit profile per mode: 5 small commits (1 file × 1 KB), 2 medium commits (5 files × 10 KB), 1 large commit (30 files × 50 KB) = 8 commits per mode. Each `git commit` is timed by the harness from just-before-invocation to just-after-return; for the async mode, the capture-landed timestamp comes from a marker the detached child writes to `.tmp/lag.tsv`. Hook wall-time is timed independently inside each hook script.

Hardware: Apple M2, 24 GB RAM, macOS Darwin 25.4.0, APFS, warm disk cache (input JSONL was already resident from the baseline pipeline timing run).

## Raw numbers

Bare baseline (no hook): mean 0.051s, median 0.050s, range 0.048–0.059s across 8 commits.

Aggregates over all 8 commits per mode:

| Mode         | commit_wall mean | commit_wall median | total mean | lag mean |
|--------------|------------------|--------------------|------------|----------|
| pre-commit   | 1.244s           | 1.216s             | 1.244s     | 0.000s   |
| post-commit  | 1.257s           | 1.257s             | 1.257s     | 0.000s   |
| post-async   | 0.072s           | 0.073s             | 1.232s     | 1.160s   |

Per-commit detail:

| Commit # | Size   | pre commit_wall (s) | post commit_wall (s) | post-async commit_wall (s) | post-async total (s) | post-async lag (s) |
|----------|--------|---------------------|----------------------|----------------------------|----------------------|--------------------|
| 1        | small  | 1.465               | 1.258                | 0.066                      | 1.226                | 1.161              |
| 2        | small  | 1.220               | 1.265                | 0.077                      | 1.216                | 1.139              |
| 3        | small  | 1.222               | 1.257                | 0.078                      | 1.243                | 1.166              |
| 4        | small  | 1.209               | 1.250                | 0.078                      | 1.234                | 1.156              |
| 5        | small  | 1.215               | 1.256                | 0.065                      | 1.231                | 1.166              |
| 6        | medium | 1.191               | 1.257                | 0.077                      | 1.239                | 1.162              |
| 7        | medium | 1.216               | 1.262                | 0.067                      | 1.236                | 1.169              |
| 8        | large  | 1.213               | 1.251                | 0.069                      | 1.227                | 1.158              |

Observations:

- **Capture cost is dominant and size-insensitive.** The hook spends ~1.15s in `jsonl-to-quarry.py | scrub-pre-ingest.py` regardless of the user's commit size (large commit with 30×50KB = 1.5 MB of staged content takes the same 1.21s as a single 1 KB file). The capture work is the bottleneck; the actual `git commit` overhead is ~50ms.
- **Synchronous post-commit costs ~13ms more than pre-commit** (1.257 vs 1.244 mean). Post-commit also runs `git commit --no-verify` for the capture, which is one extra commit-object creation. For the user there is no perceived advantage to post-commit-sync over pre-commit.
- **Async post-commit makes the visible commit ~17x faster** than the sync variants (72ms vs 1.25s) but the capture trails by 1.16s on average. During that 1.16s the working tree is dirty (the new `.ethos/sessions/<id>.md` exists but isn't committed), and any subsequent user action — `git push`, another `git commit`, switching branches — races with the in-flight hook child.
- **Commit history shape differs.** Pre-commit produces N commits, each containing both code and capture. Post-commit (either variant) produces 2N commits, alternating `commit X: …` and `capture: dummy-session-0001`. Reviewers reading log/blame have to know to skip every other commit.

## Failure modes

Tested against three deliberately broken setups: (1) scrub script receives a non-existent input file (exit 1 with stderr), (2) hook script `chmod -x`'d so git refuses to run it, (3) `.ethos/sessions/` made read-only (mode 555) so the markdown write fails.

| Scenario                                | pre-commit behavior                                                                                                                                                                                | post-commit behavior                                                                                                                                                                                                                  |
|-----------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| 1. Scrub fails (bad input path)         | **Commit aborts** with rc=1. `error: not a file: …MISSING.jsonl` printed. Working tree unchanged (file.txt still staged, no new commit). User sees and must fix.                                   | **Commit succeeds** (rc=0). The user's code commit lands. Hook prints `error: not a file:` after the success line, but rc=0. Capture commit silently does NOT happen. (In our test the seed-time capture from the previous run still exists, masking the loss to a casual observer.) |
| 2. Hook not executable (`chmod -x`)     | **Commit succeeds** (rc=0) with hint: `The '.git/hooks/pre-commit' hook was ignored because it's not set as executable.` Capture file does not exist. Code lands, capture silently lost.          | **Commit succeeds** (rc=0) with the same hint. Capture file does not exist, no capture commit. Equivalent silent loss to pre-commit in this case.                                                                                     |
| 3. `.ethos/sessions/` read-only         | **Commit aborts** with rc=1. `Permission denied` from the redirect, `Broken pipe` from the upstream Python script. User sees and must fix.                                                          | **Commit succeeds** (rc=0). `Permission denied` printed, but the user's code commit landed. No capture commit, no capture file. Silent loss after a successful commit.                                                                  |

Failure-mode summary:

- Pre-commit treats capture as a commit precondition: any failure in the capture pipeline (bad input, write failure) blocks the user's commit. The user notices immediately. The exception is the `chmod -x` case, which git itself handles by silently skipping the hook — that's an inherent git-hook property, not a strategy difference.
- Post-commit treats capture as best-effort: every failure path lets the code commit succeed and silently loses the capture. The user has no signal until they look at `.ethos/sessions/` or scan their log for `capture:` commits and notice gaps.

## Recommendation

**Use pre-commit.** The numbers say pre-commit costs the user 1.24s per commit vs 0.07s + 1.16s lag for async post-commit — but cost is the wrong axis to optimize. The right axis is *correctness of the captured record*, because that's the entire point of the system. Pre-commit gives you an atomic guarantee: every user commit either ships with its capture or doesn't ship at all. Both post-commit variants degrade silently on three of three tested failure modes — they tell you "commit succeeded" while the capture is missing, which produces a corrupted historical record that the user only discovers later (if ever). A capture system whose failures are invisible is worse than no capture system, because it gives false confidence in the record.

The atomicity bonus is real beyond failure handling: pre-commit produces N commits with N captures; post-commit produces 2N alternating commits, doubling log/blame noise and making `git revert <code-commit>` not undo the matching capture. Async post-commit additionally races with subsequent user actions during the 1.16s lag window.

If 1.24s per commit is unacceptable UX, the answer is to make `jsonl-to-quarry.py | scrub-pre-ingest.py` faster (the entire ~1.15s is in those scripts; git's overhead is ~50ms), not to hide the cost behind async post-commit.

## Caveats

- **Single-machine, single-input.** All measurements are M2 with the input JSONL hot in cache. Cold-cache or larger sessions could shift baselines, but the *relative* ordering (pre ≈ sync-post >> async-post visible cost; sync >> async on freshness) won't change.
- **Sample size: 8 commits per mode.** Means are stable to ±0.05s but I didn't compute proper confidence intervals.
- **Capture content varies per commit.** The hooks append a unique marker (`<!-- capture-marker: timestamp -->`) so each capture is a real new file, not a no-op. Without that, post-commit's `git commit --no-verify` would have aborted with "nothing to commit" after the first iteration. In real ethos the file would change naturally as the session grows; the marker just simulates that for the spike.
- **Async-mode lag = wall-clock between the user's `git commit` returning and the capture commit landing.** It does not include OS process-spawn jitter, which on a busy laptop could add 100–300ms.
- **No concurrent commit testing.** Per the spec.
- **Recursion guard caveat for post-commit.** My `LAST_MSG=… case capture:*` guard works for this spike but a real implementation needs to handle merge commits, amend, and rebase, all of which fire post-commit. That's another point against post-commit.
- **The `.bin/jsonl-to-quarry.py` script accepts stdin; piping `cat input.jsonl |` would let us measure parse-only cost separately.** I didn't decompose the 1.15s into "JSONL parse" vs "scrub regex" vs "Python startup × 2." That decomposition is the next thing to measure if optimizing the pipeline becomes the chosen path.
