"""Unit tests for runner helpers that don't need a proxy or claude subprocess."""

from __future__ import annotations

import subprocess
from pathlib import Path
from unittest.mock import MagicMock

import pytest

from e2e import runner
from e2e.proxy import LiteLLMProxy
from e2e.scenario import TokenScenario


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


def test_wire_child_receives_statement_coverage_directory(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    scenario = TokenScenario.load(runner._E2E_ROOT / "scenarios/ethos-loaded.yaml")
    assert isinstance(scenario, TokenScenario)
    monkeypatch.setenv("ETHOS_TEST_COVERDIR", str(tmp_path))
    monkeypatch.setattr(runner, "_require_e2e_bin", lambda: None)
    monkeypatch.setattr(runner, "_claude_bin", lambda: "claude")

    def check_env(
        argv: list[str], *, env: dict[str, str], **kwargs: object
    ) -> subprocess.CompletedProcess[bytes]:
        assert env.get("GOCOVERDIR") == str(tmp_path)
        assert (Path(env["HOME"]) / ".punt-labs" / "ethos").is_dir()
        assert argv[argv.index("--plugin-dir") + 1] == str(runner._REPO_ROOT / "plugin")
        return subprocess.CompletedProcess(argv, 0, b"", b"")

    monkeypatch.setattr(subprocess, "run", check_env)
    proxy = MagicMock(spec=LiteLLMProxy, base_url="http://127.0.0.1", auth_token="test")
    runner._invoke_claude(scenario, proxy, tmp_path / "repo")
