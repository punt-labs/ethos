"""run_scenario: drive one scenario's claude subprocess against the proxy.

Isolates the invocation from the ambient session with an ``env -i`` cage
so a scenario's capture reflects only what a bare ``claude --print
--bare`` sends, not whatever hooks/MCP/config the *calling* Claude Code
session carries.
"""

from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import tempfile
from collections.abc import Mapping
from functools import cache
from pathlib import Path

from e2e.capture import TokenCapture
from e2e.proxy import LiteLLMProxy
from e2e.scenario import Scenario, TokenScenario

# tests/e2e/src/e2e/runner.py -> tests/e2e — scenario.repo_fixture paths
# are relative to this directory (e.g. "fixtures/team-submodule").
_E2E_ROOT = Path(__file__).resolve().parents[2]
# tests/e2e -> repo root — where `make test-e2e[-smoke]` builds a fresh
# `ethos` binary before running pytest (see Makefile). One more `.parent`
# than _E2E_ROOT itself: _E2E_ROOT is already <repo>/tests/e2e.
_REPO_ROOT = _E2E_ROOT.parent.parent
_E2E_BIN_DIR = _REPO_ROOT / ".tmp" / "e2e-bin"

_CLAUDE_TIMEOUT_S = 60.0
_LOG_TAIL_LINES = 30

# Minimal PATH for the caged subprocess. It isolates HOME and the
# ANTHROPIC_* vars from whatever hooks/MCP/config the calling session
# carries — that isolation is the point. It is not used to find the
# `claude` binary: install locations vary too much across workstations
# and CI runners (npm global prefix, hostedtoolcache, homebrew, etc.) to
# enumerate. Resolve `claude` on the caller's own PATH instead — see
# _claude_bin below.
#
# _E2E_BIN_DIR comes first so a non-hermetic scenario's SessionStart hook
# runs the `ethos` binary built from the checkout under test, never
# whichever version happens to be globally installed on the developer's
# or runner's machine — a stale install there would silently exercise
# old behavior and misreport what this PR's code actually sends over the
# wire.
_CAGE_PATH = (
    "{e2e_bin}:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:{home}/.local/bin"
)


@cache
def _claude_bin() -> str:
    """Resolve the `claude` binary on the caller's PATH.

    Resolved lazily, on first invocation, rather than at import time —
    scenario discovery (``pytest --co -q``) imports this module without
    ever invoking claude, and must keep working on a machine where
    `claude` isn't installed.
    """
    path = shutil.which("claude")
    if path is None:
        raise RuntimeError(
            "runner: `claude` CLI not found on PATH — install @anthropic-ai/claude-code"
        )
    return path


def _require_e2e_bin() -> None:
    """Raise unless the checkout-built `ethos` binary is in place.

    Called only for hermetic: false scenarios, whose whole point is
    exercising the ethos binary under test. `_E2E_BIN_DIR` comes first
    on the caged PATH, but PATH lookup silently skips a missing entry —
    without this check, a run that skipped `make e2e-bin` (invoking
    `pytest` directly rather than `make test-e2e[-smoke]`) would fall
    through to whatever `ethos` happens to be globally installed and
    reintroduce the stale-binary problem this PATH ordering exists to
    prevent, with no signal that it happened.
    """
    e2e_bin = _E2E_BIN_DIR / "ethos"
    if not e2e_bin.is_file():
        raise RuntimeError(
            f"runner: {e2e_bin} not found — a hermetic: false scenario's "
            "SessionStart hook must run the ethos binary built from this "
            "checkout, not whatever's globally installed. Run `make "
            "e2e-bin` (or `make test-e2e`/`test-e2e-smoke`, which build "
            "it first) before invoking pytest directly."
        )


def run_scenario(scenario: Scenario, proxy: LiteLLMProxy) -> TokenCapture:
    """Invoke ``scenario`` against ``proxy`` and return its capture.

    Currently only ``type: token`` scenarios are supported; other types
    get their own runner + capture type when they're added.
    """
    if not isinstance(scenario, TokenScenario):
        raise ValueError(f"run_scenario: unsupported scenario type {scenario.type!r}")

    with tempfile.TemporaryDirectory(prefix=f"e2e-{scenario.id}-") as cwd:
        result = _invoke_claude(scenario, proxy, Path(cwd))

    capture_file = _latest_capture(scenario, proxy, result)
    raw_bytes = capture_file.read_bytes()
    body = _capture_body(capture_file, raw_bytes)
    return TokenCapture(scenario_id=scenario.id, raw_bytes=raw_bytes, body=body)


def _invoke_claude(
    scenario: TokenScenario, proxy: LiteLLMProxy, cwd: Path
) -> subprocess.CompletedProcess[bytes]:
    if scenario.repo_fixture:
        shutil.copytree(_E2E_ROOT / scenario.repo_fixture, cwd, dirs_exist_ok=True)

    home = os.environ.get("HOME", "")
    env = {
        "HOME": home,
        "PATH": _CAGE_PATH.format(e2e_bin=_E2E_BIN_DIR, home=home),
        "ANTHROPIC_BASE_URL": proxy.base_url,
        "ANTHROPIC_AUTH_TOKEN": proxy.auth_token,
        "ANTHROPIC_MODEL": scenario.id,
    }
    argv = [_claude_bin(), "--print"]
    if scenario.hermetic:
        argv.append("--bare")
    else:
        _require_e2e_bin()
        # cwd is a throwaway copy of repo_fixture with no .git of its own.
        # Without this override, a repo-root walk (ethos's StoreRepoRoot,
        # or anything else that climbs looking for a .git marker) would
        # keep going past the fixture and land on whichever real repo
        # happens to contain the test run's tmpdir — silently resolving
        # against the wrong tree instead of the fixture under test.
        env["ETHOS_REPO_ROOT"] = str(cwd)

    invocation = scenario.claude_invocation
    argv += [
        "--output-format",
        "json",
        "--max-turns",
        str(invocation.max_turns),
        invocation.prompt,
    ]
    return subprocess.run(
        argv,
        cwd=cwd,
        env=env,
        timeout=_CLAUDE_TIMEOUT_S,
        capture_output=True,
        check=False,  # a non-zero exit doesn't invalidate the capture; the
        # capture-file check below is the real pass/fail signal.
    )


def _latest_capture(
    scenario: TokenScenario,
    proxy: LiteLLMProxy,
    result: subprocess.CompletedProcess[bytes],
) -> Path:
    """Return the newest capture file written for ``scenario``, or raise.

    Matches on the full ``<scenario-id>-<digits>.jsonl`` filename rather
    than a ``<scenario-id>-*`` glob prefix, so scenario id
    ``empty-repo`` cannot match a capture written for
    ``empty-repo-extra``.
    """
    pattern = re.compile(rf"{re.escape(scenario.id)}-\d+\.jsonl")
    matches = sorted(
        p for p in proxy.captures_dir.iterdir() if pattern.fullmatch(p.name)
    )
    if not matches:
        raise RuntimeError(
            f"no capture file for scenario {scenario.id!r} in {proxy.captures_dir} "
            f"— claude never reached the proxy, or the model name didn't match\n"
            f"claude exited {result.returncode}\n"
            f"stdout tail:\n{_tail(result.stdout)}\n"
            f"stderr tail:\n{_tail(result.stderr)}"
        )
    return matches[-1]


def _capture_body(capture_file: Path, raw_bytes: bytes) -> Mapping[str, object]:
    """Return the Anthropic Messages body from a capture envelope, or raise."""
    envelope = json.loads(raw_bytes)
    proxy_request = envelope.get("proxy_server_request")
    if not isinstance(proxy_request, dict) or "body" not in proxy_request:
        raise RuntimeError(
            f"{capture_file}: envelope missing proxy_server_request.body"
        )
    body = proxy_request["body"]
    if not isinstance(body, dict):
        raise RuntimeError(
            f"{capture_file}: proxy_server_request.body is not an object"
        )
    return body


def _tail(data: bytes) -> str:
    lines = data.decode("utf-8", errors="replace").splitlines()
    return "\n".join(lines[-_LOG_TAIL_LINES:])
