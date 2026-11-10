# Legacy corpus and damage fixtures

Test data for the automatic upgrader (`internal/autorepair`, epic ethos-ftey).
Everything here is generated; do not edit it by hand.

## Regenerating

```sh
make legacy-corpus
# which runs:
scripts/legacy-corpus.sh
# one tag only (the damage fixtures and this README are rewritten from what is present):
scripts/legacy-corpus.sh --versions "v4.22.1"
```

`make test-legacy-corpus` (`scripts/legacy_corpus_test.sh`) rebuilds one tag into a temporary directory
and checks it against the committed copy. Neither target is part of `make check`: they build nine
binaries from tags and need full history (`git fetch --tags`).

## What a snapshot holds

`corpus/<tag>/` is the state one released binary wrote while running a fixed scenario in an
isolated HOME and a fresh scratch git repository:

- `repo/` is the scratch repository's working tree, without `.git` and without
  `.punt-labs/local/ethos/locks/` (lock file names are hashes of the scratch path).
- `home/` is the scratch HOME limited to `.punt-labs/ethos`. Files `ethos seed` deployed and the
  scenario never changed are omitted (starter roles, talents, bundles: about 700 KB per version,
  a function of the binary); they are listed in `seed-omitted.txt`.
- `git/` replaces `.git`, which git cannot commit under testdata and whose objects hold the scratch
  paths. `repo.bundle` and `index` are re-created from the rewritten tree with the same tracked path
  set (`tracked-files.txt`), the original commit message (including any trailer the installed hooks
  added) and a fixed date; `hooks/` are the non-sample hooks `ethos enable` installed.
- `PROVENANCE.txt` records the tag, commit, toolchain and the status of every scenario step.

Absolute scratch paths are rewritten to `@REPO@` and `@HOME@`; the host name to `@HOST@`, the user name to
`@USER@` and the fake process id (a file name under `sessions/current/` and the roster agent id) to
`@PID@`. A hash that covered a rewritten path is not recomputed. Identities are synthetic
(Test Person, `person@example.com`). The only secret-shaped string is the fake token
`ghp_FAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE`, which pre-v4.20.0 binaries store unredacted in their audit logs.

The scenario, per version and only with commands that version supports: seed, setup from a file,
identity creation (worker, evaluator, and one with a top-level `voice:` field), a vox extension and a
local overlay, a team, enable, session-start, a mission from a contract file using `inputs.bead`, a
mission from flags, result, reflect, advance, a second result and close on the first mission, a claim
of the second and a delegation (`hook pre-tool-use` with an Agent payload), an ADR, three
`hook audit-log` calls whose tool input holds the scratch HOME path and the fake token, and one
`git commit` through the installed hooks.

## Binaries

| Tag | Commit | Go toolchain |
|---|---|---|
| v3.11.0 | 8298203b1dc7b53dc4a29799b63f0fbb7acbaff6 | go1.26.1 |
| v3.12.0 | c2662fa09459501c8753498f6d1fce7ba5f460c0 | go1.26.1 |
| v4.0.0 | b379a7970a85781f01a4bd285fbbb179165ca11f | go1.26.1 |
| v4.13.0 | 448015ccfae7fb72a6686c8c8361056aedd3c69b | go1.26.1 |
| v4.17.0 | 611ab904f0ba510c86d59d5ee4f3c767012c657b | go1.26.5 |
| v4.20.4 | e3a246bf2d6eebc86a4e4c313b6840d32d3f192e | go1.26.5 |
| v4.21.0 | 8ebb77f52b896192d0d7767112b68e86a5b707ca | go1.26.5 |
| v4.22.1 | c1655476a608a7d41bb0d977edce4e5bc2703953 | go1.26.5 |
| v4.8.0 | 2dd69a22b48224e3f184c9b1bf67a0fce02fb832 | go1.26.1 |

## Steps a version could not do

Recorded, not faked: a step is listed when the version lacks the command or flag, drops the data,
or the step failed. Every other step ran and succeeded.

- v3.11.0
  - voice field: skipped: this version moves a top-level voice field into the vox extension on write, so no top-level voice: reaches disk (see damage/top-level-voice-field)
  - ext set --local (overlay): skipped: this version has no local extension overlay
  - enable: skipped: this version has no enable command (no hook install)
  - mission claim: skipped: this version has no mission claim, or no mission was dispatched
- v3.12.0
  - voice field: skipped: this version moves a top-level voice field into the vox extension on write, so no top-level voice: reaches disk (see damage/top-level-voice-field)
  - ext set --local (overlay): skipped: this version has no local extension overlay
  - enable: skipped: this version has no enable command (no hook install)
  - mission claim: skipped: this version has no mission claim, or no mission was dispatched
- v4.0.0
  - voice field: skipped: this version moves a top-level voice field into the vox extension on write, so no top-level voice: reaches disk (see damage/top-level-voice-field)
  - ext set --local (overlay): skipped: this version has no local extension overlay
  - enable: skipped: this version has no enable command (no hook install)
- v4.13.0
  - voice field: skipped: this version moves a top-level voice field into the vox extension on write, so no top-level voice: reaches disk (see damage/top-level-voice-field)
- v4.17.0
  - voice field: skipped: this version moves a top-level voice field into the vox extension on write, so no top-level voice: reaches disk (see damage/top-level-voice-field)
- v4.20.4
  - voice field: skipped: this version moves a top-level voice field into the vox extension on write, so no top-level voice: reaches disk (see damage/top-level-voice-field)
- v4.21.0
  - voice field: skipped: this version moves a top-level voice field into the vox extension on write, so no top-level voice: reaches disk (see damage/top-level-voice-field)
- v4.22.1
  - voice field: skipped: this version moves a top-level voice field into the vox extension on write, so no top-level voice: reaches disk (see damage/top-level-voice-field)
- v4.8.0
  - voice field: skipped: this version moves a top-level voice field into the vox extension on write, so no top-level voice: reaches disk (see damage/top-level-voice-field)
  - ext set --local (overlay): skipped: this version has no local extension overlay

## Damage fixtures

`damage/<name>/` holds a minimal `repo/` (and `home/`, `git/` or `tracked-files.txt` where the shape needs
them) with one defect each. `README` in each directory states the shape.

- `both-config-files`: Both .punt-labs/ethos/config.yaml (pre-v2.1) and .punt-labs/ethos.yaml, naming different agents.
- `conflicting-recovered-line`: A corrupt chunk whose only decodable line has the same ts as a line in a surviving valid chunk but a different payload (ethos-h5z8 shape); recovery must not install the conflicting line.
- `corrupt-sealed-chunk`: A sealed audit chunk whose bytes are not decodable JSON lines; no surviving chunk covers it (the audit-quarantine case).
- `force-added-local-file`: A live audit file under .punt-labs/local that git add -f put into the index; tracked-files.txt is the index's path set.
- `gitignore-ignores-sessions`: A repository .gitignore that ignores .punt-labs/ethos/sessions, with an untracked audit file there (ethos-7ax7 shape).
- `invalid-escape-lines`: A tracked chunk with a line carrying an invalid backslash escape from hand redaction, \[REDACTED:path] (ethos-jn25 shape); the other line is valid.
- `live-audit-pre-redaction`: A live session audit file whose first line was written before redaction (v4.20.0): it holds a home path and the fake token unredacted; every commit warns and quarantine, repair and scrub refuse it.
- `overlay-both-locations`: A repository extension overlay present both beside the identity (legacy v4.10-v4.20 location) and under .punt-labs/local/ethos/identities (new location), with different values (legacy: notify "y"; new: notify "n", voice "newer").- `roster-without-checkout`: A session roster with no checkout field whose claude participant pid (2147483646) is not running, plus the sessions/current pointer file named for that pid.
- `stale-hook-section`: A commit-msg hook section from before DES-083 that calls ethos hook commit-trailers (the retired subcommand) instead of the hook commit-msg shim; under git/hooks.
- `top-level-voice-field`: A global identity file with a top-level voice: field (pre-v2.0); hand-made because every released binary migrates the field into the vox extension on write.
- `tracked-trace-under-sessions`: Audit trace written under the tracked record tree .punt-labs/ethos/sessions (a v3.12-v4.20 layout) and committed; tracked-files.txt is the index's path set.

## Size

517536 bytes in 521 files under `corpus/` and `damage/` (target: under 2 MB = 2097152 bytes).
