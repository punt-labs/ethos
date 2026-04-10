#!/usr/bin/env bash
# Phase 1: Claim (18 seconds)
#
# Before writing any code, claim the bead and create a branch.
# This prevents two agents from working on the same item.

# 1a. Check the bead exists and is open.
bd show ethos-db7
# ○ ethos-db7 · mission: consolidate verifier contract load ...  [● P3 · OPEN]

# 1b. Claim it.
bd update ethos-db7 --status=in_progress
# ✓ Updated issue: ethos-db7

# 1c. Announce what you're working on (visible to other agents via /who).
# Actual output:
#   Plan: ethos-db7: consolidate verifier contract load (live ex
biff plan "ethos-db7: consolidate verifier contract load"

# 1d. Create a feature branch from main.
git checkout -b fix/verifier-toctou main
# Switched to a new branch 'fix/verifier-toctou'

# Branch prefixes follow punt-kit/standards/workflow.md:
#   feat/ = new feature
#   fix/  = bug fix or hardening
#   docs/ = documentation only
#   chore/ = maintenance

# At this point:
# - The bead is claimed (no one else will pick it up)
# - Other agents can see what you're doing (/who, /finger)
# - You have an isolated branch for your work
# - Elapsed: 18 seconds
