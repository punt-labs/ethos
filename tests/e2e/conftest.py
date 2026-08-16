"""E2E scenario collection: file-drop discovery + the shared proxy fixture.

Dropping a new ``tests/e2e/scenarios/*.yaml`` file is picked up by
``pytest_generate_tests`` below with zero edits here.
"""

from __future__ import annotations

from collections.abc import Iterator
from pathlib import Path

import pytest

from e2e.proxy import LiteLLMProxy
from e2e.redact import redact_tree
from e2e.scenario import ScenarioRegistry

_SCENARIOS_DIR = Path(__file__).parent / "scenarios"
# tests/e2e/conftest.py -> repo root, for a fixed .tmp/e2e workdir (never a
# system tmpdir) so CI can upload it by a stable glob.
_REPO_ROOT = Path(__file__).resolve().parents[2]
_WORKDIR = _REPO_ROOT / ".tmp" / "e2e"


def pytest_generate_tests(metafunc: pytest.Metafunc) -> None:
    """Parametrize ``test_<type>_scenario(scenario, ...)`` over its yaml files.

    The test function name determines which scenario ``type`` it
    collects: ``test_token_scenario`` gets every ``type: token``
    scenario, ``test_hallucination_scenario`` would get every
    ``type: hallucination`` scenario, and so on.
    """
    if "scenario" not in metafunc.fixturenames:
        return
    kind = metafunc.function.__name__.removeprefix("test_").removesuffix("_scenario")
    registry = ScenarioRegistry.discover(_SCENARIOS_DIR)
    scenarios = registry.by_type(kind)
    if not scenarios:
        raise pytest.UsageError(
            f"no scenarios of type {kind!r} found under {_SCENARIOS_DIR} "
            f"— {metafunc.function.__name__} would silently collect zero tests"
        )
    params = [
        pytest.param(s, id=s.id, marks=[pytest.mark.smoke] if s.smoke else [])
        for s in scenarios
    ]
    metafunc.parametrize("scenario", params)


@pytest.fixture(scope="session")
def scenario_registry() -> ScenarioRegistry:
    return ScenarioRegistry.discover(_SCENARIOS_DIR)


@pytest.fixture(scope="session")
def litellm_proxy(scenario_registry: ScenarioRegistry) -> Iterator[LiteLLMProxy]:
    """One proxy for the whole session; every scenario shares it."""
    proxy = LiteLLMProxy.start(scenario_registry, _WORKDIR)
    yield proxy
    proxy.stop()


def pytest_sessionfinish(session: pytest.Session, exitstatus: int) -> None:
    """Redact secret-shaped bytes from every capture and proxy-log file.

    Belt-and-braces with .github/workflows/test.yml's own ``e2e.redact``
    step: this hook covers a normal (even failing) pytest exit; the
    workflow step covers a hard-killed pytest (timeout, OOM, SIGKILL)
    that never reaches this hook at all.
    """
    redact_tree(_WORKDIR)
    redact_tree(LiteLLMProxy.captures_dir_for(_WORKDIR))
