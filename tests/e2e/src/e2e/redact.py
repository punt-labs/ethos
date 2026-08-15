"""Secret redaction sweep: scrub credential-shaped bytes from proxy artifacts.

CI uploads ``.tmp/e2e/`` (LiteLLM's own log, which echoes request bodies at
INFO level) and ``.tmp/e2e-captures/`` (the per-request capture files) as a
debugging artifact on failure. If a scenario leaks a real credential into a
payload — the exact condition ``TokenCapture.assert_no_secrets`` exists to
catch — that credential must not also leak into the uploaded artifact.

Runs from two places: ``conftest.py``'s ``pytest_sessionfinish`` hook, which
covers a normal (even failing) pytest exit, and ``python -m e2e.redact``,
callable directly from a CI workflow step after pytest exits — this covers a
hard-killed pytest (timeout, OOM, SIGKILL) that never reaches
``pytest_sessionfinish`` at all.
"""

from __future__ import annotations

import logging
import re
import sys
from pathlib import Path

from e2e.capture import DEFAULT_SECRET_PATTERNS

logger = logging.getLogger(__name__)

# Same patterns TokenCapture.assert_no_secrets scans for, recompiled over
# bytes — redaction reads and writes raw bytes (PY-TS-14: text-mode
# read_text(errors="replace") would silently corrupt any invalid-UTF-8
# bytes elsewhere in the file by substituting U+FFFD for them).
_BYTE_PATTERNS: tuple[tuple[re.Pattern[bytes], str], ...] = tuple(
    (re.compile(p.regex.pattern.encode()), p.label) for p in DEFAULT_SECRET_PATTERNS
)


def redact_file(path: Path) -> int:
    """Redact every secret-pattern match in ``path`` in place.

    Returns the number of matches redacted. Logs one warning per matching
    pattern — a developer reading a `[REDACTED]` artifact needs to know
    what leaked and which pattern caught it, not just that something did.
    """
    raw = path.read_bytes()
    redacted = raw
    total = 0
    for pattern, label in _BYTE_PATTERNS:
        redacted, count = pattern.subn(b"[REDACTED]", redacted)
        if count:
            total += count
            logger.warning("e2e: redacted %d match(es) of %s in %s", count, label, path)
    if redacted != raw:
        path.write_bytes(redacted)
    return total


def redact_tree(root: Path) -> int:
    """Redact every ``*.jsonl`` and ``*.log`` file under ``root``.

    Returns the total number of matches redacted. A missing ``root`` is a
    no-op, not an error — the proxy may never have started.
    """
    if not root.is_dir():
        return 0
    total = 0
    for pattern in ("*.jsonl", "*.log"):
        for path in root.rglob(pattern):
            total += redact_file(path)
    return total


def main(argv: list[str]) -> int:
    """CLI entry point: redact every directory named in ``argv``."""
    logging.basicConfig(level=logging.INFO, format="%(message)s")
    total = sum(redact_tree(Path(arg)) for arg in argv)
    print(f"e2e.redact: {total} match(es) redacted across {len(argv)} path(s)")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
