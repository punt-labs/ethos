"""Scenario definitions: the yaml schema under tests/e2e/scenarios/.

A scenario is a file drop — see conftest.py's ``pytest_generate_tests``
hook, which globs this directory at collection time. Adding a scenario
never touches this module; adding a scenario *type* does (a new
``TokenScenario``-shaped subclass here, a new ``test_<type>_scenario``
function in tests/test_scenarios.py).
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Self

import yaml

# Registered on the generated litellm.yaml's mock model so the proxy
# returns a stable, non-empty assistant turn without hitting a real
# upstream. Every scenario's model entry uses this canned response.
_MOCK_RESPONSE = "ack from the ethos L6 mock upstream"


@dataclass(frozen=True, slots=True)
class ClaudeInvocation:
    """The ``claude --print`` prompt and turn cap a scenario exercises."""

    prompt: str
    max_turns: int


@dataclass(frozen=True, slots=True)
class Scenario:
    """A scenario declared by a yaml file under tests/e2e/scenarios/."""

    id: str
    type: str
    description: str
    repo_fixture: str | None
    claude_invocation: ClaudeInvocation
    smoke: bool

    @classmethod
    def load(cls, path: Path) -> Scenario:
        """Parse a scenario yaml file, dispatching on its ``type`` key."""
        raw = yaml.safe_load(path.read_text())
        kind = raw.get("type")
        if kind == "token":
            return TokenScenario._from_raw(raw)
        raise ValueError(f"{path}: unknown scenario type {kind!r}")

    def litellm_model_entry(self) -> dict[str, object]:
        """A litellm.yaml ``model_list`` entry keyed by this scenario's id.

        Keying the model name by scenario id means the attribution
        parser can find the baseline for a capture via
        ``capture["model"]`` alone — no directory bookkeeping.
        """
        return {
            "model_name": self.id,
            "litellm_params": {
                "model": "openai/mock-model",
                "api_key": "sk-fake-not-used",
                "mock_response": _MOCK_RESPONSE,
            },
        }

    @staticmethod
    def _invocation_from_raw(raw: dict[str, object]) -> ClaudeInvocation:
        block = raw["claude_invocation"]
        if not isinstance(block, dict):
            raise ValueError(
                f"claude_invocation must be a mapping, got {type(block)!r}"
            )
        return ClaudeInvocation(
            prompt=str(block["prompt"]), max_turns=int(block["max_turns"])
        )


@dataclass(frozen=True, slots=True)
class TokenScenario(Scenario):
    """A ``type: token`` scenario: payload-size ratchet + secret scan."""

    max_bytes: int
    no_secret_patterns: bool

    @classmethod
    def _from_raw(cls, raw: dict[str, object]) -> Self:
        expect = raw.get("expect", {})
        if not isinstance(expect, dict):
            raise ValueError(f"expect must be a mapping, got {type(expect)!r}")
        return cls(
            id=str(raw["id"]),
            type=str(raw["type"]),
            description=str(raw.get("description", "")),
            repo_fixture=(
                str(raw["repo_fixture"]) if raw.get("repo_fixture") else None
            ),
            claude_invocation=Scenario._invocation_from_raw(raw),
            smoke=bool(raw.get("smoke", False)),
            max_bytes=int(expect["max_bytes"]),
            no_secret_patterns=bool(expect.get("no_secret_patterns", True)),
        )


@dataclass(frozen=True, slots=True)
class ScenarioRegistry:
    """Every scenario discovered under a scenarios directory."""

    scenarios: tuple[Scenario, ...]

    @classmethod
    def discover(cls, scenarios_dir: Path) -> Self:
        """Load every ``*.yaml`` file in ``scenarios_dir``, sorted by id."""
        found = tuple(
            sorted(
                (Scenario.load(p) for p in scenarios_dir.glob("*.yaml")),
                key=lambda s: s.id,
            )
        )
        return cls(scenarios=found)

    def by_type(self, kind: str) -> tuple[Scenario, ...]:
        """Every scenario of the given ``type``, in id order."""
        return tuple(s for s in self.scenarios if s.type == kind)

    def litellm_model_list(self) -> tuple[dict[str, object], ...]:
        """One litellm.yaml model entry per scenario, keyed by scenario id."""
        return tuple(s.litellm_model_entry() for s in self.scenarios)
