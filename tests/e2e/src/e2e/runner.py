"""run_scenario: drive one scenario's claude subprocess against the proxy.

Isolates the invocation from the ambient session the same way
tests/token-harness/hello/run.sh did (``env -i`` cage) so a scenario's
capture reflects only what a bare ``claude --print`` sends, not
whatever hooks/MCP/config the *calling* Claude Code session carries.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import tempfile
from pathlib import Path

from e2e.capture import TokenCapture
from e2e.proxy import LiteLLMProxy
from e2e.scenario import Scenario, TokenScenario

# tests/e2e/src/e2e/runner.py -> tests/e2e — scenario.repo_fixture paths
# are relative to this directory (e.g. "fixtures/team-submodule").
_E2E_ROOT = Path(__file__).resolve().parents[2]

_CLAUDE_TIMEOUT_S = 60.0

# Minimal PATH for the caged subprocess. It isolates HOME and the
# ANTHROPIC_* vars from whatever hooks/MCP/config the calling session
# carries — that isolation is the point. It is not used to find the
# `claude` binary: install locations vary too much across workstations
# and CI runners (npm global prefix, hostedtoolcache, homebrew, etc.) to
# enumerate. Resolve `claude` on the caller's own PATH instead, once, at
# import time — see _CLAUDE_BIN below.
_CAGE_PATH = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:{home}/.local/bin"


def _resolve_claude_bin() -> str:
    path = shutil.which("claude")
    if path is None:
        raise RuntimeError(
            "runner: `claude` CLI not found on PATH — install @anthropic-ai/claude-code"
        )
    return path


_CLAUDE_BIN = _resolve_claude_bin()


def run_scenario(scenario: Scenario, proxy: LiteLLMProxy) -> TokenCapture:
    """Invoke ``scenario`` against ``proxy`` and return its capture.

    Currently only ``type: token`` scenarios are supported; other types
    get their own runner + capture type when they're added.
    """
    if not isinstance(scenario, TokenScenario):
        raise ValueError(f"run_scenario: unsupported scenario type {scenario.type!r}")

    with tempfile.TemporaryDirectory(prefix=f"e2e-{scenario.id}-") as cwd:
        _invoke_claude(scenario, proxy, Path(cwd))

    capture_file = _latest_capture(scenario, proxy)
    raw_bytes = capture_file.read_bytes()
    envelope = json.loads(raw_bytes)
    body = envelope.get("proxy_server_request", {}).get("body", {})
    return TokenCapture(scenario_id=scenario.id, raw_bytes=raw_bytes, body=body)


def _invoke_claude(scenario: TokenScenario, proxy: LiteLLMProxy, cwd: Path) -> None:
    if scenario.repo_fixture:
        shutil.copytree(_E2E_ROOT / scenario.repo_fixture, cwd, dirs_exist_ok=True)

    home = os.environ.get("HOME", "")
    env = {
        "HOME": home,
        "PATH": _CAGE_PATH.format(home=home),
        "ANTHROPIC_BASE_URL": proxy.base_url,
        "ANTHROPIC_AUTH_TOKEN": proxy.auth_token,
        "ANTHROPIC_MODEL": scenario.id,
    }
    invocation = scenario.claude_invocation
    subprocess.run(
        [
            _CLAUDE_BIN,
            "--print",
            "--output-format",
            "json",
            "--max-turns",
            str(invocation.max_turns),
            invocation.prompt,
        ],
        cwd=cwd,
        env=env,
        timeout=_CLAUDE_TIMEOUT_S,
        capture_output=True,
        check=False,  # a non-zero exit doesn't invalidate the capture; the
        # capture-file check below is the real pass/fail signal.
    )


def _latest_capture(scenario: TokenScenario, proxy: LiteLLMProxy) -> Path:
    matches = sorted(proxy.captures_dir.glob(f"{scenario.id}-*.jsonl"))
    if not matches:
        raise RuntimeError(
            f"no capture file for scenario {scenario.id!r} in {proxy.captures_dir} "
            f"— claude never reached the proxy, or the model name didn't match"
        )
    return matches[-1]
