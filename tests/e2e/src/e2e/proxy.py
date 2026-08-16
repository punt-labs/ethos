"""LiteLLMProxy: spawns and tears down the local mock-upstream proxy.

One proxy runs per pytest session (see conftest.py's ``litellm_proxy``
fixture) — every scenario in the run shares it, keyed by scenario id
in the generated litellm.yaml.
"""

from __future__ import annotations

import os
import shutil
import signal
import socket
import subprocess
import time
from pathlib import Path
from typing import Self

import yaml

from e2e.scenario import ScenarioRegistry

_MASTER_KEY = "sk-litellm-test-e2e"
# LiteLLM's callback loader (litellm.proxy.types_utils.utils.get_instance_fn)
# resolves a dotted callback path as a *file* relative to --config's
# directory whenever a config file is given — it never falls back to a
# normal package import. So the callback module has to be copied next to
# the generated litellm.yaml; "e2e.custom_callbacks" (a real installed
# package) is not reachable this way.
_CALLBACK_MODULE_NAME = "custom_callbacks"
_CALLBACK_PATH = f"{_CALLBACK_MODULE_NAME}.token_capture"
# litellm's proxy import graph (fastapi, pydantic, litellm-enterprise) is
# heavy enough that a cold start can take 20s+; a short timeout here reads
# as a false "proxy never came up" when it's actually still importing.
_STARTUP_TIMEOUT_S = 45.0
_POLL_INTERVAL_S = 0.5
_STOP_TIMEOUT_S = 5.0
_LOG_TAIL_LINES = 30


class LiteLLMProxy:
    """A running ``litellm --config ...`` subprocess."""

    __slots__ = ("_process", "_port", "_log_path", "_captures_dir")

    _process: subprocess.Popen[bytes]
    _port: int
    _log_path: Path
    _captures_dir: Path

    def __new__(
        cls,
        process: subprocess.Popen[bytes],
        port: int,
        log_path: Path,
        captures_dir: Path,
    ) -> Self:
        self = super().__new__(cls)
        self._process = process
        self._port = port
        self._log_path = log_path
        self._captures_dir = captures_dir
        return self

    @classmethod
    def start(cls, registry: ScenarioRegistry, workdir: Path) -> Self:
        """Generate litellm.yaml from ``registry`` and spawn the proxy.

        Raises RuntimeError with the captured log tail if the proxy
        never accepts a connection within the startup timeout.
        """
        workdir.mkdir(parents=True, exist_ok=True)
        config_path = workdir / "litellm.yaml"
        log_path = workdir / "litellm.log"
        captures_dir = cls.captures_dir_for(workdir)
        # Cleared on every start — a capture left over from a prior local run
        # would otherwise let e2e.runner._latest_capture pick up stale data
        # and pass a scenario that never actually reached the proxy.
        if captures_dir.exists():
            shutil.rmtree(captures_dir)
        captures_dir.mkdir(parents=True, exist_ok=True)
        config_path.write_text(yaml.safe_dump(cls._config(registry)))
        shutil.copy(
            Path(__file__).with_name(f"{_CALLBACK_MODULE_NAME}.py"),
            workdir / f"{_CALLBACK_MODULE_NAME}.py",
        )

        port = cls._ephemeral_port()
        env = os.environ | {"TOKEN_CAPTURE_DIR": str(captures_dir)}
        with log_path.open("wb") as log_file:
            process = subprocess.Popen(
                [
                    "litellm",
                    "--config",
                    str(config_path),
                    "--port",
                    str(port),
                    "--host",
                    "127.0.0.1",
                ],
                stdout=log_file,
                stderr=subprocess.STDOUT,
                env=env,
            )

        self = cls(process, port, log_path, captures_dir)
        self._wait_until_listening()
        return self

    @staticmethod
    def captures_dir_for(workdir: Path) -> Path:
        """The capture directory ``e2e.custom_callbacks`` writes into for ``workdir``.

        Sibling to workdir (e.g. .tmp/e2e -> .tmp/e2e-captures) so CI can
        upload both with one .tmp/e2e-* glob (see .github/workflows/test.yml).
        Exposed as the single derivation both ``start`` and
        ``conftest.pytest_sessionfinish`` use, so the two can't disagree
        about where captures land.
        """
        return workdir.parent / f"{workdir.name}-captures"

    @staticmethod
    def _config(registry: ScenarioRegistry) -> dict[str, object]:
        return {
            "model_list": list(registry.litellm_model_list()),
            "litellm_settings": {
                "callbacks": _CALLBACK_PATH,
                "drop_params": True,
            },
            "general_settings": {"master_key": _MASTER_KEY},
        }

    @staticmethod
    def _ephemeral_port() -> int:
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
            s.bind(("127.0.0.1", 0))
            port = s.getsockname()[1]
        return int(port)

    def _wait_until_listening(self) -> None:
        deadline = time.monotonic() + _STARTUP_TIMEOUT_S
        while time.monotonic() < deadline:
            if self._process.poll() is not None:
                raise RuntimeError(
                    f"litellm proxy exited early (code {self._process.returncode}); "
                    f"log tail:\n{self._log_tail()}"
                )
            try:
                with socket.create_connection(("127.0.0.1", self._port), timeout=0.5):
                    return
            except OSError:
                time.sleep(_POLL_INTERVAL_S)
        raise RuntimeError(
            f"litellm proxy on 127.0.0.1:{self._port} did not start within "
            f"{_STARTUP_TIMEOUT_S}s; log tail:\n{self._log_tail()}"
        )

    def _log_tail(self) -> str:
        lines = self._log_path.read_text(errors="replace").splitlines()
        if not lines:
            return "(log file is empty — check dependency install)"
        return "\n".join(lines[-_LOG_TAIL_LINES:])

    @property
    def base_url(self) -> str:
        return f"http://127.0.0.1:{self._port}"

    @property
    def auth_token(self) -> str:
        return _MASTER_KEY

    @property
    def captures_dir(self) -> Path:
        return self._captures_dir

    def stop(self) -> None:
        """SIGTERM, wait, then SIGKILL on timeout. Idempotent."""
        if self._process.poll() is not None:
            return
        self._process.send_signal(signal.SIGTERM)
        try:
            self._process.wait(timeout=_STOP_TIMEOUT_S)
        except subprocess.TimeoutExpired:
            self._process.kill()
            self._process.wait(timeout=_STOP_TIMEOUT_S)
