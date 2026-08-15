"""L4 capture logger: dumps every proxied request body to disk.

Registered on the generated litellm.yaml via
``litellm_settings.callbacks: e2e.custom_callbacks.token_capture``.
LiteLLM's ``async_post_call_success_hook`` fires after every successful
proxy request and receives the full request dict, including
``proxy_server_request`` — the raw JSON body the client (Claude Code)
sent. This hook writes that body verbatim to a per-capture jsonl file
in ``TOKEN_CAPTURE_DIR`` so ``e2e.runner`` and ``cmd/e2e-attribute``
can read it back.
"""

from __future__ import annotations

import json
import logging
import os
import time
from pathlib import Path
from typing import Any  # litellm's CustomLogger base ships no stubs (PY-TS-9)

from litellm.integrations.custom_logger import CustomLogger

logger = logging.getLogger(__name__)


class TokenCaptureLogger(CustomLogger):
    """Writes each successful proxy request's body to TOKEN_CAPTURE_DIR."""

    # CustomLogger is a third-party base class whose own __init__ sets up
    # its internal state; overriding via __new__ (PY-CC-1) would bypass
    # that contract, so this subclass keeps __init__ deliberately.
    def __init__(self) -> None:
        super().__init__()
        self._capture_dir = Path(os.getenv("TOKEN_CAPTURE_DIR", "captures"))
        self._capture_dir.mkdir(parents=True, exist_ok=True)

    async def async_post_call_success_hook(
        self, data: Any, user_api_key_dict: Any, response: Any
    ) -> Any:
        # litellm.yaml keys each model_list entry by scenario id (see
        # e2e.scenario.Scenario.litellm_model_entry), so the model name
        # here IS the scenario id — filenames carry it so e2e.runner can
        # find a scenario's capture without parsing every file in the dir.
        model = data.get("model")
        if not model:
            logger.error(
                "e2e-capture: request has no model, refusing to write "
                "(request keys: %s)",
                sorted(data.keys()),
            )
            return response

        capture = {
            "timestamp_ns": time.time_ns(),
            "model": model,
            "proxy_server_request": data.get("proxy_server_request", {}),
        }
        capture_file = self._capture_dir / f"{model}-{time.time_ns()}.jsonl"
        try:
            capture_file.write_text(json.dumps(capture, default=str) + "\n")
        except OSError:
            logger.exception("e2e-capture: failed writing %s", capture_file)
            raise
        logger.info("e2e-capture: wrote %s", capture_file)
        return response


token_capture = TokenCaptureLogger()
