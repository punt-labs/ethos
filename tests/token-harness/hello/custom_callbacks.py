"""L6 hello-world capture logger.

Registered on the LiteLLM proxy via
`litellm_settings.callbacks: custom_callbacks.token_capture`
in `litellm.yaml`. The `async_post_call_success_hook` fires after
every successful proxy request and receives the full request dict,
including `proxy_server_request` — the raw JSON body the client
(Claude Code, in the L6 use case) sent. This hook writes that body
verbatim to a per-capture jsonl file in `TOKEN_CAPTURE_DIR` so
downstream tooling (attribution parser + tokenizer, planned as
Phase 6) can slice it by ethos-injected section and tokenize.

Environment:
- `TOKEN_CAPTURE_DIR` (default `captures`) — where captures land.
  The `run.sh` script exports it to `.tmp/token-captures/hello`.
"""

import json
import os
import time
from pathlib import Path

from litellm.integrations.custom_logger import CustomLogger


CAPTURE_DIR = Path(os.getenv("TOKEN_CAPTURE_DIR", "captures"))
CAPTURE_DIR.mkdir(parents=True, exist_ok=True)


class TokenCaptureLogger(CustomLogger):
    async def async_post_call_success_hook(self, data, user_api_key_dict, response):
        capture = {
            "timestamp_ns": time.time_ns(),
            "model": data.get("model"),
            "proxy_server_request": data.get("proxy_server_request", {}),
        }
        capture_file = CAPTURE_DIR / f"capture-{time.time_ns()}.jsonl"
        capture_file.write_text(json.dumps(capture, indent=2, default=str) + "\n")
        print(f"[capture-logger] wrote {capture_file}", flush=True)
        return response


token_capture = TokenCaptureLogger()
