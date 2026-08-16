"""type: token scenario assertions — payload shape, size, secret scan.

Scenarios come from ``tests/e2e/scenarios/*.yaml`` via
``conftest.py``'s ``pytest_generate_tests`` hook; this file never
changes when a scenario is added.
"""

from __future__ import annotations

import pytest

from e2e.proxy import LiteLLMProxy
from e2e.runner import run_scenario
from e2e.scenario import TokenScenario


@pytest.mark.e2e
def test_token_scenario(scenario: TokenScenario, litellm_proxy: LiteLLMProxy) -> None:
    capture = run_scenario(scenario, litellm_proxy)
    failures = capture.assert_body_shape() + capture.assert_size_within(
        scenario.max_bytes
    )
    if scenario.no_secret_patterns:
        failures += capture.assert_no_secrets()
    assert not failures, "\n".join(failures)
