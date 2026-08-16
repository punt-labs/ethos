"""Unit tests for runner helpers that don't need a proxy or claude subprocess."""

from __future__ import annotations

from pathlib import Path

import pytest

from e2e import runner


def test_require_e2e_bin_raises_when_missing(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setattr(runner, "_E2E_BIN_DIR", tmp_path / "no-such-dir")
    with pytest.raises(RuntimeError, match="make e2e-bin"):
        runner._require_e2e_bin()


def test_require_e2e_bin_passes_when_present(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    (tmp_path / "ethos").write_text("")
    monkeypatch.setattr(runner, "_E2E_BIN_DIR", tmp_path)
    runner._require_e2e_bin()  # does not raise
