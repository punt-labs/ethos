"""Live checks: use OpenCode's authenticated CLI API, never print raw config."""

import copy
import hashlib
import json
import os
import re
import shutil
import stat
import subprocess
import tempfile
import time
from pathlib import Path
from urllib.parse import urlencode

from opencode import (
    ASTRA,
    COMMANDS,
    NON_GO,
    ROSTER,
    SERVERS,
    SKILLS,
    SMOKE_STAGES,
    CheckError,
    check,
    frontmatter,
    model_ref,
    require,
    result,
    role_permissions,
    sources,
    validate_rules,
)


class API:
    def __init__(self, root):
        self.root = root.resolve()

    def run(self, args, timeout=180):
        scratch = self.root / ".tmp/opencode"
        scratch.mkdir(parents=True, exist_ok=True)
        operation = "OpenCode CLI"
        if len(args) >= 3 and args[0] == "api":
            path = args[2].partition("?")[0]
            if args[1] in {"get", "post", "put", "delete"} and re.fullmatch(r"/[A-Za-z0-9_./-]+", path):
                operation = f"{args[1].upper()} {path}"
        try:
            # 2.2.5 can exit before pipe-backed stdout drains for large catalogs.
            # Private, unlinked regular files avoid pipe backpressure/truncation.
            # Never retain or print resolved config, headers, or stderr.
            with tempfile.TemporaryFile(dir=scratch) as output, tempfile.TemporaryFile(dir=scratch) as errors:
                run = subprocess.run(["opencode", *args], cwd=self.root, stdout=output,
                                     stderr=errors, timeout=timeout, check=False)
                require(run.returncode == 0, f"{operation}: CLI exit {run.returncode}")
                output.seek(0)
                return output.read().decode()
        except FileNotFoundError as error:
            raise CheckError("opencode is missing from PATH") from error
        except subprocess.TimeoutExpired as error:
            raise CheckError(f"{operation}: CLI timeout after {timeout}s") from error

    def call(self, method, path, data=None, query=None):
        if query:
            path += "?" + urlencode(query)
        args = ["api", method, path]
        if data is not None:
            args += ["--data", json.dumps(data)]
        raw = self.run(args, timeout=900 if path.endswith("/wait") else 180)
        if not raw.strip():
            return None
        try:
            value = json.loads(raw)
        except ValueError as error:
            raise CheckError(f"{method.upper()} {path.partition('?')[0]}: malformed/truncated JSON") from error
        require(not isinstance(value, dict) or "_tag" not in value,
                f"{method.upper()} {path.partition('?')[0]}: API typed error")
        return value

    def location(self, path):
        return self.call("get", path, query={"location[directory]": str(self.root)})

    def pages(self, path, query=None):
        query = dict(query or {}, limit="20")
        items, seen = [], set()
        while True:
            page = self.call("get", path, query=query)
            items.extend(page["data"])
            cursor = page.get("cursor", {}).get("next")
            if not cursor:
                return items
            require(cursor not in seen, "OpenCode API repeated a pagination cursor")
            seen.add(cursor)
            query["cursor"] = cursor

    def create(self, title, permissions=None):
        body = {"title": title, "agent": "build", "model": model_ref(ASTRA),
                "location": {"directory": str(self.root)}}
        if permissions is not None:
            body["permissions"] = permissions
        return self.call("post", "/api/session", body)["data"]["id"]

    def prompt(self, session, text):
        self.call("post", f"/api/session/{session}/prompt", {"text": text})
        self.call("post", f"/api/experimental/session/{session}/wait")
        info = self.call("get", f"/api/session/{session}")["data"]
        require(info.get("outcome") == "succeeded" and info.get("model") == model_ref(ASTRA),
                f"session {session}: smoke parent did not succeed on Astra/high")
        messages = self.pages(f"/api/session/{session}/message")
        for message in messages:
            if message.get("type") == "assistant":
                require(message.get("model") == model_ref(ASTRA) and message.get("agent") == "build",
                        f"session {session}: parent assistant identity/model mismatch")
                require(not message.get("error"), f"session {session}: parent assistant reported an error")
        return messages


def tools(messages):
    return [part for message in messages if message.get("type") == "assistant"
            for part in message.get("content", []) if part.get("type") == "tool"]


def completed(tool):
    require(tool.get("state", {}).get("status") == "completed", "smoke tool did not complete")
    state = tool["state"]
    require(not state.get("metadata", {}).get("isError") and not state.get("metadata", {}).get("error"),
            "native tool metadata reported an error")
    if tool.get("name") == "shell":
        metadata = state.get("metadata", {})
        # Observed 2.2.5 foreground response: metadata {status: completed, exit: 0}.
        # A completed tool can also have returned a still-running background shell.
        shell_status = metadata.get("status")
        safe_status = shell_status if shell_status in {"completed", "running", "timeout", "killed"} else "missing/unknown"
        code = metadata.get("exit")
        safe_exit = str(code) if type(code) is int else "missing/invalid"
        budget = state.get("input", {}).get("timeout")
        timeout_note = f", budget {budget}ms" if type(budget) is int else ""
        require(shell_status == "completed" and type(code) is int
                and metadata["exit"] == 0 and not metadata.get("signal")
                and not metadata.get("background") and not metadata.get("timeout")
                and not state.get("input", {}).get("background"),
                f"shell check: status {safe_status}, exit {safe_exit}{timeout_note}; "
                "requires foreground zero exit without signal/background/timeout")
    for part in state.get("content", []):
        if part.get("type") == "text":
            require(not part["text"].lstrip().startswith(("Error:", "ERROR:", "✗")),
                    "health tool returned an error message")
            if tool.get("name") == "shell":
                exits = re.findall(r"(?im)^Exited with code (\d+)\s*$", part["text"])
                require(all(int(code) == 0 for code in exits), "shell check output reports failure")
            try:
                data = json.loads(part["text"])
            except ValueError:
                continue
            if isinstance(data, dict):
                require(not data.get("isError") and not data.get("error") and data.get("ok") is not False
                        and data.get("healthy") is not False,
                        "health tool returned a failure payload")
    return state


def required_calls(calls, name, matches, description):
    """Match intent before validating completion; every required attempt must pass.

    Unrelated failed tools cannot poison another operation's evidence, but a
    failed/denied required call cannot be hidden by a later successful retry.
    """
    selected = [tool for tool in calls if tool.get("name") == name
                and matches(tool.get("state", {}).get("input", {}))]
    require(selected, "smoke missing required operation: " + description)
    for tool in selected:
        completed(tool)


DISCOVERY_SKILLS = ("ethos-prfaq", "ethos-punt-release", "ethos-zspec")
DISCOVERY_COMMANDS = ("python3 scripts/opencode.py sources", "z-spec --help",
                      "z-spec doctor", "punt release --help")
DISCOVERY_ADAPTER_COMMANDS = tuple("python3 scripts/opencode.py sources " + kind
                                   for kind in ("prfaq", "release", "zspec")) + ("punt --help",)


def discovery_evidence(calls, root):
    for skill in DISCOVERY_SKILLS:
        required_calls(calls, "skill", lambda args: args.get("id") == skill, skill)
    for command in DISCOVERY_COMMANDS:
        required_calls(calls, "shell", lambda args: args.get("command") == command, command)
    # Adapters may repeat per-kind discovery/help after the full sources check.
    # Those exact harmless calls are allowed; if attempted, they must also pass.
    for command in DISCOVERY_ADAPTER_COMMANDS:
        matching = [tool for tool in calls if tool.get("name") == "shell"
                    and tool.get("state", {}).get("input", {}).get("command") == command]
        for tool in matching:
            completed(tool)
    for source in sources(root, "release") + sources(root, "prfaq"):
        required_calls(calls, "read", lambda args: (root / args.get("path", "")).resolve() == source,
                       "read maintained source " + source.name)


HEALTH_INPUTS = {
    "vox.status": {}, "beadle.get_poll_status": {}, "quarry.status": {},
    "zspec.doctor": {}, "lux.ping": {},
    "context7.resolve-library-id": {"libraryName": "Go", "query": "Go standard library testing docs"},
}
# Exact executable code binds each returned payload to its real native call. Model
# prose or arbitrary code that fabricates/rewrites the response is not evidence.
HEALTH_CODE = 'return {\n' + ',\n'.join(
    f'  "{name}": await tools.{name.split(".")[0]}[{json.dumps(name.split(".")[1])}]('
    + (json.dumps(args) if args else '') + ')'
    for name, args in HEALTH_INPUTS.items()) + '\n};'


def unwrap(value):
    """Unwrap the observed MCP result/text and JSON-string layers, fail closed."""
    for _ in range(12):
        if isinstance(value, str):
            try:
                value = json.loads(value)
                continue
            except ValueError:
                return value
        if isinstance(value, dict):
            require(not value.get("isError") and not value.get("error") and value.get("ok") is not False,
                    "health response reports failure")
            if "result" in value:
                value = value["result"]
                continue
            if value.get("type") == "text" and "text" in value:
                value = value["text"]
                continue
            if "content" in value:
                value = value["content"]
                continue
        if isinstance(value, list) and len(value) == 1:
            value = value[0]
            continue
        return value
    raise CheckError("health response exceeds wrapper nesting limit")


def rows(text):
    require(isinstance(text, str), "health response lacks required text rows")
    result = {}
    for line in text.splitlines():
        key, separator, value = line.strip().partition(":")
        if separator:
            require(key not in result, "health response repeats a status row")
            result[key] = value.strip()
    return result


def health_response(name, value):
    """Validate selected read-only capabilities, not delivery/synthesis/index quality."""
    try:
        value = unwrap(value)
        if name == "vox.status":
            require(isinstance(value, dict) and isinstance(value.get("provider_status"), dict)
                    and value["provider_status"].get("ready") is True, "provider is not ready")
            program = value.get("program")
            require(isinstance(program, dict) and program.get("mode") in
                    {"off", "generating_first", "playing_filling", "playing_rotating"}, "missing/failed program status")
            require(not any(program.get(key) for key in
                            ("error", "last_error", "playback_error", "failed_parts")), "program reports failure")
            generation = program.get("generation")
            require(isinstance(generation, dict) and type(generation.get("filling")) is bool
                    and type(generation.get("attempts")) is int and generation["attempts"] >= 0
                    and not generation.get("last_error") and program.get("failed_parts") == []
                    and "playback_error" in program and program["playback_error"] is None,
                    "missing/failed program generation/playback status")
        elif name == "zspec.doctor":
            require(isinstance(value, dict) and value.get("healthy") is True, "doctor not healthy")
        elif name == "lux.ping":
            require(isinstance(value, str) and re.fullmatch(r"pong rtt=\d+(?:\.\d+)?s", value.strip()),
                    "missing real pong/latency")
        elif name == "beadle.get_poll_status":
            fields = rows(value)
            require(fields.get("Active") in {"yes", "no"} and fields.get("Interval")
                    and re.fullmatch(r"\d+", fields.get("Unseen", "")), "missing poll status")
            require("Last error" not in fields and ("Failures" not in fields or fields["Failures"] == "0"),
                    "poller reports failures")
        elif name == "quarry.status":
            fields = rows(value)
            require(all(re.fullmatch(r"(?:\d+|\d{1,3}(?:,\d{3})+)", fields.get(key, ""))
                        for key in ("Documents", "Collections", "Chunks", "Directories"))
                    and all(fields.get(key) for key in ("Database", "Model", "Provider")),
                    "missing index/store status")
        elif name == "context7.resolve-library-id":
            require(isinstance(value, str) and "Available Libraries:" in value and
                    re.search(r"(?m)^- Title: Go\s*$", value) and
                    re.search(r"(?m)^- Context7-compatible library ID: /[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+\s*$", value),
                    "missing Go library result")
        else:
            raise CheckError("unreviewed health tool")
    except CheckError as error:
        # Payloads can contain credentials, account details and local paths.
        raise CheckError(f"{name}: {error}") from error


def codemode_health(calls):
    require(len(calls) == 1 and calls[0].get("name") == "execute", "root health must use one native execute")
    state = completed(calls[0])
    require(state.get("input", {}).get("code") == HEALTH_CODE, "root health altered the exact response-returning code")
    nested = state.get("metadata", {}).get("toolCalls")
    require(isinstance(nested, list) and len(nested) == len(HEALTH_INPUTS), "root health lacks six native nested calls")
    require({call.get("tool") for call in nested} == set(HEALTH_INPUTS), "root health nested tools differ")
    for call in nested:
        require(call.get("status") == "completed" and call.get("input", {}) == HEALTH_INPUTS[call["tool"]],
                "root health nested call failed or used different arguments")
    content = state.get("content", [])
    require(len(content) == 1 and content[0].get("type") == "text", "root health lacks native returned JSON")
    try:
        values = json.loads(content[0]["text"])
    except ValueError as error:
        raise CheckError("root health returned malformed JSON") from error
    require(isinstance(values, dict) and set(values) == set(HEALTH_INPUTS), "root health missing returned tool results")
    for name, value in values.items():
        health_response(name, value)


# Observed OpenCode 2.2.5 additions when its native browser is unavailable.
# Neither rule overlaps this project's required formal/research capabilities.
NATIVE_BROWSER_DENIALS = [
    {"action": "browser", "resource": "*", "effect": "deny"},
    {"action": "skill", "resource": "browser", "effect": "deny"},
]


def resolved_permissions(name, rules, config):
    """Verify the entire effective suffix after the last all-action reset.

    Earlier native defaults are irrelevant after a universal reset. Only the
    exact observed native browser-denial pair may follow the reviewed policy;
    arbitrary denials can disable required capabilities just as grants widen it.
    """
    validate_rules(rules)
    expected = config["permissions"] + ([] if name == "build" else role_permissions(name))
    def suffix(entries):
        resets = [i for i, rule in enumerate(entries) if rule["action"] == rule["resource"] == "*"]
        require(resets, f"{name}: missing universal permission baseline")
        return entries[resets[-1]:]
    actual, wanted = suffix(rules), suffix(expected)
    require(actual[:len(wanted)] == wanted and actual[len(wanted):] in ([], NATIVE_BROWSER_DENIALS),
             f"{name}: live effective permission structure/exceptions differ")


def runtime_config(entries):
    """Project only safe fields in memory. Never serialize merged configuration."""
    default, reasoning, fmt = None, None, {}
    for entry in entries:
        info = entry.get("info", {})
        default = info.get("model", default)
        reasoning = (info.get("providers", {}).get("bifrost", {}).get("models", {})
                     .get("openai/gpt-6-astra", {}).get("settings", {}).get("reasoningEffort", reasoning))
        setting = info.get("formatter")
        if setting is False:
            fmt = {}
        elif setting is True:
            fmt = {name: {} for name in NON_GO | {"gofmt"}}
        elif isinstance(setting, dict):
            for name, value in setting.items():
                fmt[name] = dict(fmt.get(name, {}), **value)
    return default, reasoning, fmt


def connected_servers(api, budget=180):
    deadline = time.monotonic() + budget
    while True:
        servers = {s["name"]: s["status"]["status"] for s in api.location("/api/mcp")["data"]}
        require(set(servers) == SERVERS, "live MCP roster differs (including inherited servers)")
        pending = [name for name, status in servers.items() if status == "pending"]
        for name, status in servers.items():
            safe_status = status if status in {"connected", "pending", "failed", "disabled", "needs_auth"} else "unexpected"
            require(status in {"connected", "pending"}, f"{name}: transport {safe_status}; prerequisite is not ready")
        if not pending:
            return servers
        require(time.monotonic() < deadline, "MCP startup/catalog budget exhausted while pending")
        time.sleep(2)


def doctor(root):
    config = check(root)
    api = API(root)
    version = api.call("get", "/api/info")["version"]
    require(version == "2.2.5", "live helper targets OpenCode 2.2.5; review API/built-ins before upgrading gate")
    agents = {a["id"]: a for a in api.location("/api/agent")["data"]}
    for name, selector in dict(ROSTER, build=ASTRA).items():
        require(name in agents and agents[name].get("model") == model_ref(selector),
                f"{name}: live model/variant differs; reload and verify location")
        require(agents[name]["mode"] == ("primary" if name == "build" else "subagent"),
                f"{name}: live agent mode differs")
        require(not agents[name].get("hidden") and agents[name].get("steps") is None,
                f"{name}: hidden agent or inherited step cap requires review")
        resolved_permissions(name, agents[name].get("permissions"), config)
        if name != "build":
            _, body = frontmatter(root / f".opencode/agents/{name}.md")
            require(agents[name].get("system", "").strip() == body.strip(),
                    f"{name}: live prompt differs; inspect duplicate definition sources")
    models = {(m["providerID"], m["id"]): m for m in api.location("/api/model")["data"]}
    for selector in set(ROSTER.values()):
        ref = model_ref(selector)
        model = models.get((ref["providerID"], ref["id"]))
        require(model and model.get("enabled") and ref["variant"] in {v["id"] for v in model["variants"]},
                "intended model/variant missing from live location catalog")
        variant = next(v for v in model["variants"] if v["id"] == ref["variant"])
        require(variant.get("settings", {}).get("reasoningEffort") == ref["variant"],
                "live Bifrost variant reasoning differs from its pinned effort")
        if selector == ASTRA:
            require(model.get("settings", {}).get("reasoningEffort") == "high",
                    "live catalog default Astra reasoning is not high")
    default, reasoning, fmt = runtime_config(api.location("/api/config"))
    expected = model_ref(ASTRA)
    require(default in (ASTRA.split("#")[0], {"providerID": expected["providerID"], "model": expected["id"]},
                        {"providerID": expected["providerID"], "id": expected["id"]}),
            "live default model differs from project Astra")
    require(reasoning == "high", "live default reasoning is not high")
    require(fmt.get("gofmt", {}).get("disabled") is not True, "live gofmt is disabled")
    require(all(fmt.get(name, {}).get("disabled") is True for name in NON_GO),
            "live non-Go built-in formatter is enabled")
    for name, entry in fmt.items():
        if not entry.get("disabled"):
            require(name == "gofmt" or entry.get("extensions") == [".go"],
                    "inherited non-Go custom formatter requires review")
    for endpoint, expected_ids in (("/api/skill", SKILLS), ("/api/command", COMMANDS)):
        entries = api.location(endpoint)["data"]
        # Skill IDs are path-derived; command IDs are returned as name.
        found = {entry.get("id", entry.get("name")) for entry in entries}
        require(expected_ids <= found, "live skills/commands missing; reload and verify location")
    servers = connected_servers(api)
    for name in sorted(SERVERS):
        status = servers[name]
        require(status == "connected", f"{name}: transport {status}; prerequisite is not ready")
        print(f"{name}: connected (health call not yet asserted)")
    print("Live native roster, effective permissions, model pins/default reasoning and formatter policy verified")
    return api


def tree_snapshot(root):
    """Git source membership plus on-disk type/mode/content, without following links.

    Tracked files remain included even when ignored or deleted. Non-ignored new
    files are included; generated ignored outputs use Git's existing exclusions.
    Parent entries detect a directory replaced by a symlink without reading out
    of the checkout. This is change detection, not a concurrent filesystem lock.
    """
    raw = subprocess.check_output(["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"], cwd=root)
    names = {os.fsdecode(name) for name in raw.split(b"\0") if name}
    for name in list(names):
        names.update(str(parent) for parent in Path(name).parents if str(parent) != ".")
    snapshot = {}
    for name in sorted(names, key=lambda name: (len(Path(name).parts), name)):
        if any(snapshot[str(parent)]["type"] != "directory" for parent in Path(name).parents
               if str(parent) != "."):
            snapshot[name] = {"type": "blocked-parent"}
            continue
        path = root / name
        try:
            mode = path.lstat().st_mode
        except FileNotFoundError:
            snapshot[name] = {"type": "missing"}
            continue
        entry = {"mode": stat.S_IMODE(mode)}
        if stat.S_ISLNK(mode):
            entry.update(type="symlink", target=os.readlink(path))
        elif stat.S_ISREG(mode):
            entry.update(type="file", sha256=hashlib.sha256(path.read_bytes()).hexdigest())
        elif stat.S_ISDIR(mode):
            entry.update(type="directory")
        else:
            entry.update(type="special", file_type=stat.S_IFMT(mode))
        snapshot[name] = entry
    return snapshot


def tree_digest(root):
    return hashlib.sha256(json.dumps(tree_snapshot(root), sort_keys=True).encode()).hexdigest()


def source_changes(before, after):
    return sorted(name for name in before.keys() | after.keys() if before.get(name) != after.get(name))


def formatter_patch(samples):
    """Exact sentinel-line changes leave malformed Go whitespace to the formatter."""
    lines = ["*** Begin Patch"]
    for name, text in samples.items():
        require(text.count("SMOKE_BEFORE") == 1, "formatter sample must contain one sentinel")
        line = next(line for line in text.splitlines() if "SMOKE_BEFORE" in line)
        lines.extend([f"*** Update File: {name}", "@@", "-" + line,
                      "+" + line.replace("SMOKE_BEFORE", "SMOKE_AFTER")])
    return "\n".join([*lines, "*** End Patch"])


def formatter_inputs(calls, fixture, samples):
    """Verify exact edit intent before checking bytes produced by native gofmt.

    OpenCode's GPT selection hook removes edit/write and exposes patch, including
    for Bifrost GPT IDs. Compare the entire patch to our known sentinel-only input
    instead of interpreting arbitrary patches or trusting disk bytes alone.
    """
    patches = 0
    for tool in calls:
        require(tool.get("name") in {"read", "patch"}, "formatter fixture used an unreviewed tool")
        state = completed(tool)
        args = state.get("input", {})
        if tool["name"] == "patch":
            require(args == {"patchText": formatter_patch(samples)},
                    "formatter tool requested more than the exact sentinel-only patch")
            patches += 1
        else:
            path = (fixture / args.get("path", "")).resolve()
            require(path.parent == fixture.resolve() and path.name in samples, "formatter tool path outside samples")
    require(patches == 1, "formatter must apply the sentinel-only patch exactly once")


def formatter_outputs(fixture, samples):
    for name, text in samples.items():
        expected = text.replace("SMOKE_BEFORE", "SMOKE_AFTER").encode()
        if name.endswith(".go"):
            require(shutil.which("gofmt"), "gofmt missing from PATH")
            expected = subprocess.check_output(["gofmt"], input=expected)
        require((fixture / name).read_bytes() == expected, f"formatter behavior differs: {name}")


def child_evidence(api, parent, author, task):
    children = api.pages("/api/session", {"parentID": parent})
    require(len(children) == 1, "smoke must dispatch exactly one native child")
    child = children[0]["id"]
    info = api.call("get", f"/api/session/{child}")["data"]
    messages = api.pages(f"/api/session/{child}/message")
    artifact = result(info, messages, child, parent, task, 1, author)
    calls = tools(messages)
    required_calls(calls, "read", lambda args: args.get("path") == "go.mod", "child read go.mod")
    require(not any(t.get("name") in {"edit", "write", "patch", "subagent"} for t in calls),
            "read-only child attempted mutation/delegation")
    return child, artifact


def smoke(root, stages=None):
    stages = list(SMOKE_STAGES if stages is None else stages)
    require(stages and len(set(stages)) == len(stages) and set(stages) <= set(SMOKE_STAGES),
            "smoke stages must be nonempty, known and unique")
    selected = [name for name in SMOKE_STAGES if name in stages]
    scratch = root / ".tmp/opencode"
    scratch.mkdir(parents=True, exist_ok=True)
    evidence = Path(tempfile.mkdtemp(prefix="smoke-", dir=scratch))
    # Keep sessions and fixtures; never delete user sessions or force-remove trees.
    receipt = {"status": "running", "stage": "source baseline", "sessions": [], "checks": [],
               "scope": "full" if selected == list(SMOKE_STAGES) else "targeted",
               "requested_stages": selected, "completed_stages": []}
    receipt_path = evidence / "receipt.json"

    def save():
        receipt_path.write_text(json.dumps(receipt, indent=2) + "\n")

    def new_session(client, title, permissions=None):
        session = client.create(title, permissions)
        receipt["sessions"].append(session)
        receipt["active_session"] = session
        save()
        return session

    def stage(name):
        receipt["stage"] = name
        save()

    baseline = None

    def guard_sources():
        after = tree_snapshot(root)
        (evidence / "source-after.json").write_text(json.dumps(after, indent=2) + "\n")
        changed = source_changes(baseline, after)
        receipt["source_changes"] = changed
        require(not changed, "source changed outside scratch: " + json.dumps(changed))

    save()
    print(f"Retaining smoke evidence: {evidence}")
    try:
        baseline = tree_snapshot(root)
        (evidence / "source-before.json").write_text(json.dumps(baseline, indent=2) + "\n")
        stage("live doctor")
        api = doctor(root)
        # Each role's actual model/permissions must work, not just representatives.
        for author in (ROSTER if "children" in selected else ()):
            stage("child " + author)
            task = "bootstrap-smoke-" + evidence.name + "-" + author
            parent = new_session(api, task, [{"action": "edit", "resource": "*", "effect": "deny"}])
            messages = api.prompt(parent,
                f"Dispatch exactly one {author} subagent in the foreground. No other tools or work. "
                f"Child task ID {task}, round 1, author {author}, empty write-set. Tell it to use "
                "the read tool on path go.mod, then return the exact task-result header from "
                "docs/opencode-workflow.md and a brief observed module line. Do not edit, shell, "
                "delegate, install or publish. After it returns report only completion and native child ID.")
            child, artifact = child_evidence(api, parent, author, task)
            dispatch = [t for t in tools(messages) if t.get("name") == "subagent"]
            require(len(dispatch) == 1, "parent did not use exactly one native subagent tool")
            state = completed(dispatch[0])
            require(child in json.dumps(state.get("metadata", {})) or
                    child in json.dumps(state.get("content", [])), "parent dispatch did not expose exact child ID")
            (evidence / f"{author}-result.md").write_text(artifact)
            receipt["checks"].append({"agent": author, "parent": parent, "child": child,
                                      "model": ROSTER[author], "go_mod_read": True})
            guard_sources()
            save()
        if "children" in selected:
            receipt["completed_stages"].append("children")

        if "discovery" in selected:
            stage("procedure discovery")
            for kind in ("prfaq", "release", "zspec"):
                sources(root, kind)  # Exact prerequisite diagnosis before model work.
            discovery_rules = [{"action": "*", "resource": "*", "effect": "deny"}]
            discovery_rules += [{"action": action, "resource": "*", "effect": "allow"}
                                for action in ("read", "external_directory", "skill")]
            discovery_rules += [{"action": "shell", "resource": command, "effect": "allow"}
                                for command in DISCOVERY_COMMANDS + DISCOVERY_ADAPTER_COMMANDS]
            parent = new_session(api, "bootstrap procedure discovery", discovery_rules)
            discovery = api.prompt(parent,
                "READ ONLY discovery, no release/model-check/compile/install/source writes. Load skills "
                "ethos-prfaq, ethos-punt-release and ethos-zspec. Run python3 scripts/opencode.py sources, "
                "read the returned prfaq SKILL.md, punt-kit auto SKILL.md and release.yaml. Run "
                "z-spec --help, z-spec doctor (availability only) and punt release --help. "
                "Report sources and exit results. "
                "Do not run the release playbook or repeat user discovery questions.")
            discovery_evidence(tools(discovery), root)
            guard_sources()
            receipt["checks"].append({"procedure_discovery": "passed"})
            receipt["completed_stages"].append("discovery")
            save()

        if "health" in selected:
            health_names = {name.replace(".", "_", 1) for name in HEALTH_INPUTS}
            health_names.add("context7_resolve_library_id")
            health_rules = [{"action": "*", "resource": "*", "effect": "deny"}]
            health_rules += [{"action": name, "resource": "*", "effect": "allow"} for name in sorted(health_names)]
            stage("root Code Mode MCP capabilities")
            parent = new_session(api, "bootstrap root Code Mode MCP capabilities", health_rules + [
                {"action": "execute", "resource": "*", "effect": "allow"}])
            messages = api.prompt(parent,
                "Call the native execute tool exactly once with this exact code (no changes, no other tools). "
                "The selected tools are already in your catalog. Return their real responses, not a summary. "
                "This only checks status/library resolution; no mail, speech or other actions.\n" + HEALTH_CODE)
            codemode_health(tools(messages))
            receipt["checks"].append({"root_codemode_capabilities": sorted(HEALTH_INPUTS)})
            guard_sources()
            receipt["completed_stages"].append("health")
            save()

        if "formatter" in selected:
            stage("formatter sentinel fixture")
            fixture = evidence / "fixture"
            fixture.mkdir()
            fixture_config = copy.deepcopy(check(root))
            fixture_config["instructions"] = []
            for server in fixture_config["mcp"]["servers"].values():
                if server["type"] == "local":
                    server["cwd"] = str(root)
                    server["command"][2] = str(root)
            (fixture / "opencode.json").write_text(json.dumps(fixture_config, indent=2) + "\n")
            fixture_api = API(fixture)
            samples = {"sample.go": "package smoke\nfunc f( ){ }\n// SMOKE_BEFORE\n",
                       "sample.md": "#   Title\n\nSMOKE_BEFORE   \n",
                       "sample.py": "value=  [1,2] # SMOKE_BEFORE\n",
                       "sample.tex": "\\section{A}  % SMOKE_BEFORE\n",
                       "sample.sh": "echo   SMOKE_BEFORE\n", "sample.json": '{"a":1,"b":"SMOKE_BEFORE"}\n'}
            for name, text in samples.items():
                (fixture / name).write_bytes(text.encode())
            parent = new_session(fixture_api, "bootstrap formatter fixture", [
                {"action": "*", "resource": "*", "effect": "deny"},
                {"action": "read", "resource": "*", "effect": "allow"},
                {"action": "edit", "resource": "sample.*", "effect": "allow"}])
            messages = fixture_api.prompt(parent,
                "Use ONLY native read and patch tools on ONLY " + ", ".join(samples) + ". "
                "Call patch exactly once with patchText equal to the following patch, without changes. "
                "Preserve all other bytes, including deliberately malformed Go whitespace. "
                "Do not manually format or use shell/formatter commands. This tests automatic formatting.\n"
                + formatter_patch(samples))
            formatter_inputs(tools(messages), fixture, samples)
            formatter_outputs(fixture, samples)
            guard_sources()
            receipt["checks"].append({"go_format_and_non_go_preservation": "passed"})
            receipt["completed_stages"].append("formatter")
        require(receipt["completed_stages"] == selected, "smoke did not finish every requested stage")
        receipt["status"] = "passed"
    except (CheckError, OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        receipt["status"] = "failed"
        receipt["error"] = str(error) if isinstance(error, CheckError) else type(error).__name__
        if baseline is not None:
            try:
                guard_sources()
            except CheckError as source_error:
                receipt["source_error"] = str(source_error)
            except (OSError, ValueError, subprocess.SubprocessError) as source_error:
                receipt["source_error"] = type(source_error).__name__
        raise
    finally:
        save()
    print(f"{receipt['scope'].capitalize()} smoke passed ({', '.join(selected)}): {receipt_path}")
