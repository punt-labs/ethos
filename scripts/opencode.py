"""Small native OpenCode checks and result/source helpers (Python stdlib only)."""

import argparse
import ast
import json
import re
import subprocess
import sys
from pathlib import Path

# Direct CLI invocations must not leave __pycache__ in the source tree.
sys.dont_write_bytecode = True

ROOT = Path(__file__).resolve().parents[1]
ASTRA = "bifrost/openai/gpt-6-astra#high"
ROSTER = dict.fromkeys(
    ("rsc", "djb", "jms", "jra", "ghr", "code-reviewer",
     "silent-failure-hunter", "invariant-completeness-reviewer"), ASTRA
)
ROSTER.update(bwk="bifrost/openai/gpt-6.1-sol#high", qa="bifrost/openai/gpt-6.1-sol#high",
              researcher="bifrost/openai/gpt-5.6-terra#medium")
# Documented V2.2.5 built-ins, not a claim about future OpenCode releases.
NON_GO = {"mix", "oxfmt", "prettier", "biome", "zig", "clang-format", "ktlint", "ruff", "air", "uv", "rubocop",
          "standardrb", "htmlbeautifier", "dart", "ocamlformat", "terraform", "latexindent", "gleam",
          "shfmt", "nixfmt", "rustfmt", "pint", "ormolu", "cljfmt", "dfmt"}
SKILLS = {"ethos-prfaq", "ethos-zspec", "ethos-quarry-recall",
          "ethos-quarry-capture", "ethos-collaboration", "ethos-punt-release",
          "ethos-opencode-check", "ethos-reference", "ethos-qodo-review"}
COMMANDS = {"punt:auto", "opencode-check", "prfaq", "zspec", "review"}
SERVERS = {"vox", "beadle", "quarry", "zspec", "lux", "context7"}
SMOKE_STAGES = ("children", "discovery", "health", "formatter")
START, END = "--- task-result ---", "--- end-task-result ---"


class CheckError(Exception):
    """A safe, actionable diagnostic; never include resolved config values."""


def require(ok, message):
    if not ok:
        raise CheckError(message)


def load_json(text):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, "duplicate JSON key")
            result[key] = value
        return result
    return json.loads(text, object_pairs_hook=unique)


def frontmatter(path):
    """Agents use JSON flow-style YAML; adapters use only scalar YAML fields."""
    text = path.read_text()
    parts = text.split("---\n", 2)
    require(len(parts) == 3 and parts[0] == "", f"{path.name}: missing frontmatter")
    raw, body = parts[1:]
    if raw.lstrip().startswith("{"):
        info = load_json(raw)
    else:
        info = {}
        for line in raw.splitlines():
            key, sep, value = line.partition(": ")
            require(sep and key not in info, f"{path.name}: unsupported/duplicate field")
            info[key] = value.strip()
    require(body.strip(), f"{path.name}: empty prompt")
    return info, body


def effect(rules, action, resource):
    """Offline probes for this repo's simple wildcard policies, not a sandbox."""
    def matches(value, pattern):
        # Native V2 treats everything except * and ? literally (not fnmatch []).
        expression = re.escape(pattern).replace(r"\*", ".*").replace(r"\?", ".")
        return re.fullmatch(expression, value, re.DOTALL) is not None

    answer = "ask"
    for rule in rules:
        pattern = rule["resource"]
        matched = matches(resource, pattern)
        if action == "shell" and pattern.endswith(" *"):
            matched |= resource == pattern[:-2]
        if matches(action, rule["action"]) and matched:
            answer = rule["effect"]
    return answer


def validate_rules(rules):
    require(isinstance(rules, list), "permissions must be a native V2 array")
    for rule in rules:
        require(isinstance(rule, dict), "invalid permission rule")
        require(set(rule) == {"action", "resource", "effect"}, "invalid permission keys")
        require(all(isinstance(v, str) for v in rule.values()), "invalid permission value")
        require(rule["effect"] in {"allow", "deny", "ask"}, "invalid permission effect")
        require(rule["action"] not in {"bash", "task", "doom_loop"}, "obsolete permission action")


def permission(action, resource="*", effect="allow"):
    return {"action": action, "resource": resource, "effect": effect}


def root_permissions():
    return [permission("*"), permission("read", "*.env*", "ask"),
            permission("read", "*.envrc*", "ask"), permission("edit", "*.env*", "deny"),
            permission("edit", "*.envrc*", "deny"), permission("shell", "sudo *", "ask"),
            permission("shell", "su *", "ask"), permission("shell", "direnv allow *", "ask")]


def role_permissions(name):
    """Reviewed exception lists, not sampled paths that narrow grants can evade.

    Shell allows inspected commands with host authority; it is not containment.
    New exceptions require deliberate review and an update to this contract.
    """
    if name == "bwk":
        return [permission("subagent", effect="deny"),
                *[permission("shell", command, "deny") for command in
                  ("git add *", "git commit *", "git push *", "gh *", "punt release *")]]
    rules = [permission("*", effect="deny")]
    rules += [permission(action) for action in ("read", "glob", "grep", "external_directory")]
    if name == "jms":
        rules += [permission("edit", path) for path in ("docs/spec-*.tex", ".tmp/opencode/*")]
    skills = {"ghr": ("ethos-prfaq", "ethos-punt-release"), "jms": ("ethos-zspec",),
              "jra": ("ethos-zspec",), "researcher": ("ethos-quarry-recall",)}
    rules += [permission("skill", skill) for skill in skills.get(name, ())]
    # Native MCP permission names preserve hyphens (core/src/tool/mcp.ts).
    mcp_actions = {
        "jms": ("zspec_check", "zspec_model_check", "zspec_doctor"),
        "jra": ("zspec_check", "zspec_model_check", "zspec_doctor"),
        "researcher": ("quarry_find", "quarry_show", "quarry_status",
                       "context7_resolve-library-id", "context7_query-docs"),
    }
    if name in mcp_actions:
        rules += [permission(action) for action in ("execute", *mcp_actions[name])]
    commands = {
        "rsc": ("git status *", "git diff *", "git show *", "git log *", "go test *", "go vet *", "make check-opencode"),
        "djb": ("git diff *", "git show *", "go test *", "go vet *", "make check-opencode"),
        "code-reviewer": ("git diff *", "git show *", "go test *", "go vet *", "make check-opencode"),
        "silent-failure-hunter": ("git diff *", "git show *", "go test *", "make check-opencode"),
        "invariant-completeness-reviewer": ("git diff *", "git show *", "go test *", "make check-opencode"),
        "ghr": ("git diff *", "git show *", "python3 scripts/opencode.py sources *"),
        "jms": ("git diff *", "fuzz *", "z-spec doctor", "z-spec --help", "python3 scripts/opencode.py sources zspec"),
        "jra": ("git diff *", "fuzz *", "z-spec doctor", "z-spec --help", "make fuzz-check",
                "python3 scripts/opencode.py sources zspec"),
        "qa": ("git status *", "git diff *", "git rev-parse HEAD",
               "python3 -B scripts/opencode_evidence.py hash *",
               "python3 -B scripts/opencode_evidence.py inventory *",
               "python3 -B scripts/opencode_evidence.py go-test *",
               "python3 -B scripts/opencode_evidence.py json *",
               "python3 -B scripts/opencode_evidence.py tool go",
               "python3 -B scripts/opencode_evidence.py tool gcc",
               "go test *", "go vet *", "make check-opencode", "make fuzz-check",
               "make build", "make test", "make check", "make lint", "./ethos version", "./ethos --help",
               "./ethos whoami *", "./ethos show *", "./ethos list *", "./ethos identity schema *",
               "./ethos role schema *", "./ethos team schema *", "./ethos mission show *",
               "./ethos mission list *", "./ethos mission lint *", "./ethos audit show *", "./ethos adr list *",
               "python3 -B -m unittest discover -s scripts -p opencode_test.py"),
        "researcher": ("git log *", "git show *"),
    }
    if name == "researcher":
        rules += [permission(action) for action in ("webfetch", "websearch")]
    rules += [permission("shell", command) for command in commands[name]]
    return rules + [permission("read", "*.env*", "deny"), permission("read", "*.envrc*", "deny"),
                    permission("subagent", effect="deny")]


def check(root=ROOT):
    config = load_json((root / "opencode.json").read_text())
    require(set(config) == {"$schema", "model", "default_agent", "providers", "instructions",
                            "agents", "commands", "permissions", "lsp", "formatter", "mcp"},
            "unexpected/missing root config keys")
    require(config["$schema"] == "https://opencode.ai/config.json", "missing published schema")
    require(config["model"] == ASTRA.split("#")[0] and config["default_agent"] == "build",
            "default leader must be Bifrost Astra/build")
    # Sol limits are conservative project budgets, not claimed gateway maxima.
    # Credentials, endpoint and Responses package come from the existing provider.
    require(config["providers"] == {"bifrost": {"models": {
        "openai/gpt-6-astra": {"settings": {"reasoningEffort": "high"}},
        "openai/gpt-6.1-sol": {"name": "openai/gpt-6.1-sol",
                              "limit": {"context": 200000, "output": 32000},
                              "variants": [{"id": "high", "settings": {"reasoningEffort": "high"}}]}}}},
        "Bifrost model-only overrides/default reasoning differ")
    require(config["agents"] == {"build": {"model": ASTRA}}, "unexpected agent override")
    require(config["instructions"] == ["docs/opencode-workflow.md"], "keep native instructions focused")
    require((root / config["instructions"][0]).is_file(), "missing workflow instructions")
    require(not list((root / ".opencode").glob("opencode.json*")), "duplicate project config source")
    require(config["lsp"] == {"gopls": {"command": ["gopls"], "extensions": [".go"]}},
            "Go LSP configuration differs")
    fmt = config["formatter"]
    require(set(fmt) == NON_GO | {"gofmt"}, "formatter roster differs from reviewed V2 built-ins")
    require(fmt["gofmt"] == {"command": ["gofmt", "-w", "$FILE"], "extensions": [".go"]},
            "only Go formatting is approved")
    require(all(fmt[n] == {"disabled": True} for n in NON_GO), "non-Go formatter enabled")
    validate_rules(config["permissions"])
    require(config["permissions"] == root_permissions(), "root permission structure/exceptions differ")
    for path in (".envrc", ".envrc.local", ".env", "a/.env.prod"):
        require(effect(config["permissions"], "edit", path) == "deny", "environment edits unprotected")
        require(effect(config["permissions"], "read", path) != "allow", "secret reads unprotected")
    for action, target in (("edit", "docs/a.md"), ("shell", "make check"),
                           ("shell", "git push origin topic"), ("beadle_send_email", "*")):
        require(effect(config["permissions"], action, target) == "allow", "leader workflow is gated")

    mcp = config["mcp"]
    require(set(mcp) == {"timeout", "servers"} and set(mcp["servers"]) == SERVERS,
            "MCP must use native six-server roster")
    require(mcp["timeout"] == {"startup": 120000, "catalog": 60000, "execution": 3600000},
            "MCP budgets differ")
    for name, binary, verb in (("vox", "vox", "mcp"), ("beadle", "beadle-email", "serve"),
                               ("quarry", "quarry", "mcp"), ("zspec", "z-spec", "mcp")):
        require(mcp["servers"][name] == {"type": "local", "cwd": ".",
                                        "command": ["direnv", "exec", ".", binary, verb]},
                f"{name}: unexpected local transport")
    require(mcp["servers"]["lux"] == {"type": "remote", "url": "http://127.0.0.1:8430/mcp"},
            "Lux endpoint differs")
    require(mcp["servers"]["context7"] == {"type": "remote", "url": "https://mcp.context7.com/mcp",
            "oauth": False, "headers": {"Authorization": "Bearer {env:CONTEXT7_API_KEY}"}},
            "Context7 must use the environment-reference Bearer header")

    files = list((root / ".opencode/agents").glob("*.md"))
    require({p.stem for p in files} == set(ROSTER), "specialist roster differs")
    for path in files:
        info, body = frontmatter(path)
        require(set(info) == {"description", "mode", "model", "permissions"},
                f"{path.stem}: use compact native V2 agent fields, no name/variant/steps")
        require(info["mode"] == "subagent" and info["model"] == ROSTER[path.stem],
                f"{path.stem}: wrong mode/model/variant")
        require(info["description"], f"{path.stem}: missing description")
        validate_rules(info["permissions"])
        require(info["permissions"] == role_permissions(path.stem),
                f"{path.stem}: permission structure/exceptions differ from reviewed role")
        rules = config["permissions"] + info["permissions"]
        require(effect(rules, "subagent", "bwk") == "deny", f"{path.stem}: nested delegation allowed")
        require("task-result" in body and "docs/opencode-workflow.md" in body,
                f"{path.stem}: missing result protocol")
        if path.stem != "bwk":
            for command in ("git push origin main", "gh release create v1", "punt release 1.0.0",
                            "make --eval 'x: ; true' x", "python3 -c 'print(1)'"):
                require(effect(rules, "shell", command) == "deny", f"{path.stem}: overly broad shell")
            require(effect(rules, "edit", "internal/a.go") == "deny", f"{path.stem}: source edits allowed")
            require(effect(rules, "edit", "docs/spec-test.tex") ==
                    ("allow" if path.stem == "jms" else "deny"), f"{path.stem}: spec edit policy differs")

    skill_files = list((root / ".opencode/skills").rglob("SKILL.md"))
    require({p.parent.name for p in skill_files} == SKILLS and len(skill_files) == len(SKILLS),
            "missing/duplicate adapter sources")
    for path in skill_files:
        info, body = frontmatter(path)
        require(set(info) == {"name", "description"} and info["name"] == path.parent.name,
                f"{path.parent.name}: invalid skill metadata")
        require(len(body.split()) <= 400, "adapter too large")
    command_files = list((root / ".opencode/commands").glob("*.md"))
    require(set(config["commands"]) == {"punt:auto"}, "release command alias differs")
    require({p.stem for p in command_files} == COMMANDS - set(config["commands"]), "command roster differs")
    commands = []
    for path in command_files:
        info, body = frontmatter(path)
        commands.append((path.name, info, body))
    for name, entry in config["commands"].items():
        info = dict(entry)
        body = info.pop("template")
        commands.append((name, info, body))
    for name, info, body in commands:
        require(set(info) == {"description", "agent", "model"} and info["agent"] == "build"
                and info["model"] == ASTRA, "command must pin build/Astra high")
        require(isinstance(body, str) and body.strip() and info["description"], "empty command prompt/description")
        adapter = {"punt:auto": "ethos-punt-release", "opencode-check.md": "ethos-opencode-check",
                   "prfaq.md": "ethos-prfaq", "zspec.md": "ethos-zspec", "review.md": "ethos-qodo-review"}[name]
        require(adapter in body, f"{name}: missing maintained workflow adapter")
        require("!`" not in body, "command shell interpolation bypasses tool permissions")
        for skill in re.findall(r"ethos-[a-z-]+", body):
            require(skill in SKILLS, f"{name}: missing skill path")

    scan = [root / "opencode.json", *list((root / ".opencode").rglob("*.md")),
            *list((root / "scripts").glob("opencode*.py")),
            root / "docs/opencode-workflow.md", root / "docs/opencode-setup.md"]
    for path in scan:
        text = path.read_text()
        require(not re.search(r"(?:sk-[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|"
                              r"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----)", text),
                f"{path.name}: possible literal credential")
        require(not re.search(r"Bearer (?!\{env:)[A-Za-z0-9_./+-]{12,}", text),
                f"{path.name}: literal Bearer credential")
        require(not re.search(r"/(?:home|Users)/[A-Za-z0-9]", text), f"{path.name}: nonportable home path")
        if path.suffix == ".py":
            ast.parse(text, filename=str(path))
            require(all(line.rstrip() == line for line in text.splitlines()),
                    f"{path.name}: trailing whitespace")
    return config


def canonical_checkout(root):
    raw = subprocess.check_output(["git", "rev-parse", "--git-common-dir"], cwd=root, text=True).strip()
    common = (root / raw).resolve()
    # The common dir, not the linked checkout's parent, anchors sibling discovery.
    require(common.name == ".git", "canonical discovery requires a non-bare checkout with .git common-dir")
    return common.parent


def sources(root, kind):
    workspace = canonical_checkout(root).parent
    paths = {
        "prfaq": [workspace / "prfaq/plugin/skills/prfaq/SKILL.md"],
        "zspec": [workspace / "z-spec/plugin/commands/code2model.md",
                  workspace / "z-spec/plugin/commands/check.md"],
        "release": [workspace / "punt-kit/plugin/skills/auto/SKILL.md",
                    workspace / "punt-kit/plugin/playbooks/release.yaml"],
    }
    if kind in (None, "prfaq") and not paths["prfaq"][0].is_file():
        for directory in (".config/opencode/skills", ".agents/skills", ".claude/skills"):
            candidate = Path.home() / directory / "prfaq/SKILL.md"
            if candidate.is_file():
                paths["prfaq"] = [candidate]
                break
    selected = paths[kind] if kind else [path for group in paths.values() for path in group]
    for path in selected:
        require(path.is_file(), f"missing maintained procedure: {path}")
    return selected


def model_ref(selector):
    base, separator, variant = selector.partition("#")
    provider, model = base.split("/", 1)
    require(provider and model and (not separator or variant), "invalid model selector")
    return dict(providerID=provider, id=model, **({"variant": variant} if separator else {}))


def result(info, messages, child, parent, task, round_number, author, expected_model=None):
    require(info.get("id") == child and info.get("parentID") == parent and info.get("agent") == author,
            "result session/parent/author identity mismatch")
    require(info.get("outcome") == "succeeded", "result session did not succeed")
    require(round_number > 0 and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._:/-]*", task),
            "invalid task ID or round")
    require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._/-]*", author), "invalid result author ID")
    pinned_model = ROSTER.get(author)
    require(not pinned_model or expected_model in (None, pinned_model),
            "explicit --model cannot override a configured author's pinned model")
    expected_model = pinned_model or expected_model
    require(expected_model, "unconfigured author requires an explicit --model selector")
    require(pinned_model or expected_model.partition("#")[2],
            "unconfigured author requires an explicit --model provider/model#variant")
    require(info.get("model") == model_ref(expected_model), "result model mismatch")
    candidates = []
    for message in messages:
        if message.get("type") != "assistant":
            continue
        require(not message.get("error"), "assistant reported an error")
        require(message.get("agent") == author and message.get("model") == info["model"],
                "assistant identity/model mismatch")
        text = "\n".join(p["text"] for p in message.get("content", []) if p.get("type") == "text")
        completed = message.get("time", {}).get("completed")
        if completed is not None:
            candidates.append((completed, message["id"], text))
    require(candidates, "no completed assistant result")
    latest = max(candidates)[2]
    header = f"{START}\ntask: {task}\nround: {round_number}\nauthor: {author}\n{END}\n"
    require(latest.startswith(header) and latest.splitlines().count(START) == 1
            and latest.splitlines().count(END) == 1, "latest completed artifact has wrong/duplicate header")
    require(latest[len(header):].strip(), "empty result body")
    return latest


def local_output(path, root=ROOT):
    path = path.resolve()
    require(any(path.is_relative_to(root / base) for base in (".tmp/opencode", ".punt-labs/local/ethos")),
            "output must remain in approved local scratch/state")
    require(not path.exists(), "refusing to overwrite an existing result")
    path.parent.mkdir(parents=True, exist_ok=True)
    return path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    for name in ("check", "doctor"):
        sub.add_parser(name)
    smoke_parser = sub.add_parser("smoke")
    smoke_parser.add_argument("--stage", action="append", choices=SMOKE_STAGES,
                              help="Run only selected stages (repeatable); default: all stages")
    source = sub.add_parser("sources")
    source.add_argument("kind", nargs="?", choices=("prfaq", "zspec", "release"))
    extract = sub.add_parser("extract")
    for name in ("child", "parent", "task"):
        extract.add_argument(name)
    extract.add_argument("round", type=int)
    extract.add_argument("author")
    extract.add_argument("output", type=Path)
    extract.add_argument("--model", help="Expected provider/model#variant; required for non-project agents")
    args = parser.parse_args()
    if args.command == "sources":
        print("\n".join(map(str, sources(Path.cwd(), args.kind))))
    elif args.command == "check":
        check()
        print("OpenCode offline configuration and Python syntax/whitespace checks passed")
    else:
        # A script entry point and imported module must share exception identity.
        sys.modules.setdefault("opencode", sys.modules[__name__])
        from opencode_live import API, doctor, smoke
        if args.command == "extract":
            output = local_output(args.output)
            api = API(ROOT)
            info = api.call("get", f"/api/session/{args.child}")["data"]
            text = result(info, api.pages(f"/api/session/{args.child}/message"), args.child,
                          args.parent, args.task, args.round, args.author, args.model)
            with output.open("x") as handle:
                handle.write(text)
            print(f"validated {args.task} round {args.round} author {args.author} -> {output}")
        elif args.command == "doctor":
            doctor(ROOT)
        else:
            smoke(ROOT, args.stage)


if __name__ == "__main__":
    try:
        main()
    except (CheckError, OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        # Do not render subprocess stdout/stderr or arbitrary resolved values.
        message = str(error) if isinstance(error, CheckError) else type(error).__name__
        print(f"OpenCode check failed: {message}", file=sys.stderr)
        sys.exit(1)
