#!/usr/bin/env bash
# Phase 8: Close
#
# After the PR merges, close the mission, close the bead, pull main,
# and send the recap. This phase took ~33 seconds.

# 8a. Close the mission.
# Actual output:
#   Closed m-2026-04-10-003 as closed
ethos mission close m-2026-04-10-003 --status closed

# 8b. Close the bead.
# Actual output:
#   ✓ Closed ethos-db7: Closed
bd close ethos-db7

# 8c. Return to main and pull.
git checkout main
git pull
# Fast-forward: 1 file changed, 50 insertions(+), 23 deletions(-)

# 8d. Verify the mission event log.
# This is the complete audit trail — 4 events, 1 round:
#
#   ethos mission log m-2026-04-10-003
#
#   2026-04-10 07:19 PDT  create   by claude  worker=bwk evaluator=djb bead=ethos-db7
#   2026-04-10 07:22 PDT  result   by bwk     round=1 verdict=pass
#   2026-04-10 07:23 PDT  reflect  by claude  round=1 rec=continue
#   2026-04-10 07:31 PDT  close    by claude  status=closed verdict=pass round=1

# 8e. Send recap email.
# Actual email sent via beadle:
#
#   To: jim@punt-labs.com
#   Subject: [ethos] PR #212 merged: consolidate verifier contract load (ethos-db7)
#   Body:
#     Bead: ethos-db7
#     PR: https://github.com/punt-labs/ethos/pull/212
#     Mission: m-2026-04-10-003
#
#     checkVerifierHash now does one os.ReadFile + DecodeContractStrict
#     per contract instead of Store.Load + os.ReadFile. Same bytes for
#     hash verification and isolation block rendering. Eliminates TOCTOU
#     between reads. 1 file, +50/-23 lines.
#
#     This was executed as a live example of the full mission lifecycle.
#     Mission completed in 1 round, 12m55s wall time.

# At this point the repo is clean:
# - Bead ethos-db7: closed
# - Mission m-2026-04-10-003: closed (round 1, verdict pass)
# - PR #212: merged and branch deleted
# - main: up to date
# - Recap: sent
# - Event log: 4 events, auditable at any time
