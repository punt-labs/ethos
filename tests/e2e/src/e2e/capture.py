"""Assertion primitives for a captured Claude Code request.

Every E2E capture carries a scenario id, the raw bytes LiteLLM wrote to
disk, and the parsed Anthropic Messages body. ``ScenarioCapture``
carries structural assertions every scenario type needs.
``TokenCapture`` adds the size-ratchet and secret-scan assertions that
``type: token`` scenarios use. Each assertion method returns a tuple of
violation strings — empty means pass — so a test can accumulate every
failure in one report instead of stopping at the first.
"""

from __future__ import annotations

import re
from collections.abc import Mapping, Sequence
from dataclasses import dataclass


@dataclass(frozen=True, slots=True)
class SecretPattern:
    """One regex/label pair used by ``TokenCapture.assert_no_secrets``."""

    regex: re.Pattern[str]
    label: str


# Nine credential shapes proven in CI. False positives are cheaper than
# false negatives, so patterns stay conservative.
DEFAULT_SECRET_PATTERNS: tuple[SecretPattern, ...] = (
    SecretPattern(re.compile(r"sk-live-[A-Za-z0-9]{16,}"), "Anthropic/OpenAI live key"),
    SecretPattern(re.compile(r"sk-ant-api03-[A-Za-z0-9_\-]{20,}"), "Anthropic API key"),
    SecretPattern(re.compile(r"AKIA[0-9A-Z]{16}"), "AWS access key"),
    SecretPattern(re.compile(r"AIza[0-9A-Za-z_\-]{35}"), "Google API key"),
    SecretPattern(re.compile(r"ghp_[A-Za-z0-9]{36,}"), "GitHub personal token"),
    SecretPattern(re.compile(r"ghs_[A-Za-z0-9]{36,}"), "GitHub server-to-server token"),
    SecretPattern(
        re.compile(r"github_pat_[A-Za-z0-9_]{80,}"), "GitHub fine-grained PAT"
    ),
    SecretPattern(
        re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----"), "PEM private key block"
    ),
    SecretPattern(re.compile(r"xox[baprs]-[A-Za-z0-9\-]{10,}"), "Slack token"),
)

# Minimum Anthropic Messages API shape every capture body must carry.
_REQUIRED_BODY_KEYS = ("model", "messages")


@dataclass(frozen=True, slots=True)
class ScenarioCapture:
    """A single captured request, parsed from a LiteLLM capture file.

    ``body`` is typed as ``Mapping[str, object]`` because it is JSON
    decoded off the wire — the type system cannot know its shape until
    ``assert_body_shape`` narrows it (PY-TS-14 wire-boundary exception).
    """

    scenario_id: str
    raw_bytes: bytes
    body: Mapping[str, object]

    def assert_body_shape(self) -> tuple[str, ...]:
        """Check the capture body has the minimum Anthropic Messages shape."""
        return tuple(
            f"structural: capture body missing key {key!r}"
            for key in _REQUIRED_BODY_KEYS
            if key not in self.body
        )


@dataclass(frozen=True, slots=True)
class TokenCapture(ScenarioCapture):
    """A capture from a ``type: token`` scenario — adds size and PII checks."""

    def assert_size_within(self, max_bytes: int) -> tuple[str, ...]:
        """Check the capture stayed under the scenario's byte ceiling."""
        total = len(self.raw_bytes)
        if total > max_bytes:
            return (
                f"size: capture is {total:,} bytes; ceiling is "
                f"{max_bytes:,} — payload grew past the ratchet",
            )
        return ()

    def assert_no_secrets(
        self, patterns: Sequence[SecretPattern] = DEFAULT_SECRET_PATTERNS
    ) -> tuple[str, ...]:
        """Check no credential-shaped substring appears in the raw capture."""
        raw = self.raw_bytes.decode("utf-8", errors="replace")
        return tuple(
            f"pii: capture contains what looks like a {pattern.label}"
            for pattern in patterns
            if pattern.regex.search(raw)
        )
