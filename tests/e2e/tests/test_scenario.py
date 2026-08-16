"""Unit tests for scenario yaml parsing — no proxy, no claude subprocess."""

from __future__ import annotations

from pathlib import Path

import pytest

from e2e.scenario import Scenario

_BASE = """
id: t
type: token
claude_invocation:
  prompt: "hi"
  max_turns: 1
expect:
  max_bytes: 1000
"""


def _write(tmp_path: Path, extra: str) -> Path:
    path = tmp_path / "scenario.yaml"
    path.write_text(_BASE + extra)
    return path


def test_hermetic_defaults_true(tmp_path: Path) -> None:
    scenario = Scenario.load(_write(tmp_path, ""))
    assert scenario.hermetic is True


def test_hermetic_false(tmp_path: Path) -> None:
    scenario = Scenario.load(_write(tmp_path, "hermetic: false\n"))
    assert scenario.hermetic is False


def test_hermetic_rejects_non_bool_string(tmp_path: Path) -> None:
    with pytest.raises(ValueError, match="hermetic"):
        Scenario.load(_write(tmp_path, 'hermetic: "false"\n'))


def test_smoke_rejects_non_bool_string(tmp_path: Path) -> None:
    with pytest.raises(ValueError, match="smoke"):
        Scenario.load(_write(tmp_path, 'smoke: "yes"\n'))


def test_no_secret_patterns_rejects_non_bool_string(tmp_path: Path) -> None:
    path = tmp_path / "scenario.yaml"
    path.write_text(
        _BASE.replace("max_bytes: 1000", 'max_bytes: 1000\n  no_secret_patterns: "no"')
    )
    with pytest.raises(ValueError, match="no_secret_patterns"):
        Scenario.load(path)
