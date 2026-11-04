---
name: ethos-qodo-review
description: Route local Qodo review to the installed skill or the explicitly authorized CLI 1.6.0 compatibility path
---

# Project Qodo review adapter

Probe `qodo --version` unadorned first (if missing, try
`"${QODO_HOME:-$HOME/.qodo}/bin/qodo"`). For CLI >=2, load the installed
qodo-review skill and obey its runtime/API/auth gates. Do not copy, edit or
replace global skills. Missing/unparseable/other older versions are blockers.

Exactly 1.6.0 has a user-directed restoration exception covering the ethos-t1uy
bootstrap AND the approved DES-079 storage-boundary repair, including after
bootstrap closes, until a separately verified, user-directed upgrade. The operator
restored the official checksum-verified CLI after 2.1 A2A review failures. This is
legacy review, NOT success of the installed 2.x skill. Do not upgrade, login again,
change deployment or implement HTTP/credential plumbing. CLI >=2 uses the installed
skill route unchanged, not this exception.

For 1.6.0, inspect `qodo review --help`, `qodo review status --help` and live
managed-tool schemas before using findings/status tools. Reuse a successful
identity check only in the same executable/workspace/auth context; otherwise
use `qodo read whoami --json`. Missing catalog permits one refresh; explicit
missing credentials, entitlement, permission or rate-limit failures stop.

Resolve the intended pushed target's real merge-base, normally
`git merge-base HEAD origin/main`; verify it exists in fetched remote history.
Disclose stale/offline history. No fabricated empty base, target-tip substitution
or push. Preserve coding context in a UTF-8 JSON FILE under .tmp/opencode:
`summary` holds behavior, scope, evidence, uncertainty AND changes since the
previous review; `decisions` records actual choices, owners and rationale.
Do not rely on unsupported `notes`/`recent_work` alone. Never include secrets.

Final: `qodo review --base SHA --context-file FILE --json` without depth flags
or temporary path restrictions: review the full intended diff, including new
helpers. Auto selects depth; `--full` requests a fresh baseline, not depth.
Checkpoints use `--fast --async --json`; keep one outstanding operation ID and
collect it with `qodo review status ID --json` (0 finished, 2 pending, 1 failed).
Alternatively connected `--json --progress` separates stdout result from stderr
NDJSON; never combine progress with async. Retain context, receipts and exits.

Inspect actual findings, IDs, evidence and coverage; progress/acceptance/zero
findings alone is not clean. Assess fix/dismiss/investigate within authority;
no stored dismissal without authorization. Retry transport collection once after
the returned delay; uncertain submissions use the CLI recovery handle, not a
fresh review. Stop repeated/terminal failures, preserve handles and report gaps.
Leader owns final full-scope review and independent perspectives before PR.
