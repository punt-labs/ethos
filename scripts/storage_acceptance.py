#!/usr/bin/env python3
"""Offline, black-box DES-079 storage gate; no product layout classifier.

Run `make check-storage`, or rerun a release artifact with
`python3 -B scripts/storage_acceptance.py --binary /absolute/path/to/ethos`.
Every case is required. Evidence and disposable repositories are retained under
.tmp/opencode/storage-acceptance/<unique-run>/. This is a final-placement gate,
NOT syscall-level proof of transient placement or crash durability.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import queue
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import threading
import time
import traceback
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
RECORD = Path(".punt-labs/ethos")
LOCAL = Path(".punt-labs/local/ethos")
MISSION = r"m-\d{4}-\d{2}-\d{2}-\d+"


class Failure(Exception):
    """An acceptance assertion, never a skippable prerequisite."""


def require(condition: Any, message: str) -> None:
    if not condition:
        raise Failure(message)


def digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def write(path: Path, data: str | bytes) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(data.encode() if isinstance(data, str) else data)
    return path


def save_json(path: Path, value: Any) -> None:
    write(path, json.dumps(value, indent=2, sort_keys=True) + "\n")


def inventory(root: Path) -> dict[str, Any]:
    """Include ignored entries, directories, symlinks and runtime debris."""
    result: dict[str, Any] = {}
    if not root.exists():
        return result
    def walk_error(error: OSError) -> None:
        raise error

    for directory, dirs, files in os.walk(root, followlinks=False, onerror=walk_error):
        # Git's own database is not product storage. The index is captured apart.
        if Path(directory) == root and ".git" in dirs:
            dirs.remove(".git")
        for name in sorted(dirs + files):
            path = Path(directory) / name
            info = path.lstat()
            entry: dict[str, Any] = {"mode": stat.S_IMODE(info.st_mode)}
            if path.is_symlink():
                entry.update(kind="symlink", target=os.readlink(path))
            elif path.is_dir():
                entry.update(kind="directory")
            elif path.is_file():
                entry.update(kind="file", sha256=digest(path.read_bytes()), size=info.st_size)
            else:
                entry.update(kind="special")
            result[path.relative_to(root).as_posix()] = entry
    return result


def blame_path_allowed(relative: str) -> bool:
    """Acceptance allowlist, deliberately independent of internal/layout.

    No catch-all mission-directory or recognized-local-file exception. A lock,
    temporary or prompt is forbidden here even if the product recognizes it.
    """
    if relative in {"enabled", "CLAUDE.md", "ETHOS-SETUP.md", ".vendored-manifest", "missions.jsonl"}:
        return True
    patterns = (
        r"(?:identities|archetypes)/[a-z0-9-]+\.yaml",
        r"(?:personalities|writing-styles|talents)/[a-z0-9-]+\.md",
        rf"missions/{MISSION}/(?:contract|results|corrections)\.yaml",
        rf"missions/{MISSION}/delegations/[a-zA-Z0-9-]+/record\.yaml",
        r"adrs/[a-zA-Z0-9-]+\.yaml",
        r"provenance/[a-f0-9]{64}\.yaml",
    )
    return any(re.fullmatch(pattern, relative) for pattern in patterns)


def check_boundary(repo: Path, full: bool = False) -> None:
    for relative in (Path(".punt-labs"), RECORD, Path(".punt-labs/local"), LOCAL):
        path = repo / relative
        require(not path.is_symlink(), f"symlink storage root: {relative}")
        require(not path.exists() or path.is_dir(), f"non-directory storage root: {relative}")
    data = inventory(repo / RECORD)
    allowed_files = set()
    for name, entry in data.items():
        if entry["kind"] == "directory":
            continue
        require(entry["kind"] == "file", f"non-regular record entry: {name}")
        allowed = blame_path_allowed(name)
        if full:
            allowed = allowed or bool(re.fullmatch(
                rf"missions/{MISSION}/(?:reflections\.yaml|delegations/[a-zA-Z0-9-]+/prompt\.md|log-.+\.jsonl)", name
            ) or re.fullmatch(r"sessions/[a-zA-Z0-9-]+/audit-.+\.jsonl", name))
        require(allowed, f"forbidden source-tree entry: {name}")
        allowed_files.add(name)
    # Empty runtime/scratch directories also violate placement.
    for name, entry in data.items():
        if entry["kind"] == "directory":
            require(any(p.startswith(name + "/") for p in allowed_files),
                    f"unexpected empty record directory: {name}")
    for name, entry in inventory(repo).items():
        parts = Path(name).parts
        if parts[:3] == (".punt-labs", "local", "ethos") or name == ".git":
            continue
        require(not any(".ethos-tmp" in p or p in {"scratch", "quarantine"} or p.endswith(".lock")
                        for p in parts), f"runtime debris outside canonical local zone: {name}")


def clean_env(home: Path, tmp: Path, path: str) -> dict[str, str]:
    return {
        "HOME": str(home), "USER": "storage-fixture", "PATH": path,
        "TMPDIR": str(tmp), "LANG": "C", "LC_ALL": "C", "TZ": "UTC",
        "GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_SYSTEM": os.devnull,
        "GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0",
        "GIT_OPTIONAL_LOCKS": "0",
        "GIT_AUTHOR_DATE": "2026-11-04T12:00:00Z",
        "GIT_COMMITTER_DATE": "2026-11-04T12:00:00Z",
        "GIT_ALLOW_PROTOCOL": "file", "GIT_TRACE": "0", "GIT_TRACE2": "0",
        "ETHOS_TEST_NOT_UNDER_CLAUDE_CODE": "1",
    }


class Runner:
    def __init__(self, directory: Path):
        self.directory = directory
        self.sequence = 0

    def run(self, args: list[str], cwd: Path, env: dict[str, str],
            data: str | None = None, ok: bool = True, timeout: int = 60) -> subprocess.CompletedProcess[bytes]:
        self.sequence += 1
        base = self.directory / f"{self.sequence:04d}"
        save_json(base.with_suffix(".command.json"), {"argv": args, "cwd": str(cwd), "stdin": data})
        try:
            result = subprocess.run(args, cwd=cwd, env=env,
                                    input=None if data is None else data.encode(),
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, check=False)
        except subprocess.TimeoutExpired as error:
            write(base.with_suffix(".stdout"), error.stdout or b"")
            write(base.with_suffix(".stderr"), error.stderr or b"")
            save_json(base.with_suffix(".exit.json"), {"exit": None, "timed_out": True})
            write(base.with_suffix(".error.txt"), str(error))
            raise Failure(f"command timed out: {args}; evidence {base}") from error
        except OSError as error:
            write(base.with_suffix(".error.txt"), str(error))
            raise Failure(f"command did not complete: {args}; evidence {base}") from error
        write(base.with_suffix(".stdout"), result.stdout)
        write(base.with_suffix(".stderr"), result.stderr)
        save_json(base.with_suffix(".exit.json"), {"exit": result.returncode})
        require(result.returncode >= 0, f"command killed by signal {-result.returncode}: {args}; evidence {base}")
        require((result.returncode == 0) == ok,
                f"expected {'success' if ok else 'refusal'}, exit {result.returncode}: {args}; "
                f"{result.stderr.decode(errors='replace')[-2000:]}; evidence {base}")
        return result


class Fixture:
    def __init__(self, run: Runner, binary: Path):
        self.run = run
        self.root = run.directory
        self.repo = self.root / "repo"
        self.home = self.root / "home"
        self.tmp = self.root / "tmp"
        for path in (self.repo, self.home / ".punt-labs/ethos", self.tmp, self.root / "bin"):
            path.mkdir(parents=True)
        (self.root / "bin/ethos").symlink_to(binary)
        # Only the executable PATH is inherited, never hook/session/config env.
        self.env = clean_env(self.home, self.tmp, str(self.root / "bin") + os.pathsep + os.environ.get("PATH", os.defpath))
        self.env["GIT_CEILING_DIRECTORIES"] = str(self.root)
        self.binary = binary
        self.git("init", "-b", "main")
        self.git("config", "user.name", "Storage Fixture")
        self.git("config", "user.email", "fixture@example.invalid")
        self.git("config", "commit.gpgsign", "false")
        self.git("config", "core.autocrlf", "false")
        self.git("config", "core.hooksPath", str(self.repo / ".git/hooks"))
        write(self.repo / "source.txt", "base\n")
        self.seed()
        self.git("add", "source.txt", str(RECORD))
        self.git("commit", "-m", "fixture base")

    def git(self, *args: str, cwd: Path | None = None, env: dict[str, str] | None = None,
            ok: bool = True) -> bytes:
        return self.run.run(["git", *args], cwd or self.repo, env or self.env, ok=ok).stdout

    def cli(self, *args: str, cwd: Path | None = None, env: dict[str, str] | None = None,
            ok: bool = True) -> bytes:
        return self.run.run([str(self.binary), *args], cwd or self.repo, env or self.env, ok=ok).stdout

    def value(self, *args: str, **kwargs: Any) -> Any:
        return json.loads(self.cli(*args, "--json", **kwargs))

    def wrapper(self, name: str, payload: dict[str, Any], cwd: Path | None = None,
                extra: dict[str, str] | None = None) -> bytes:
        script = ROOT / "plugin/hooks" / name
        require(script.is_file(), f"missing shipped wrapper {script}")
        return self.run.run(["bash", str(script)], cwd or self.repo, self.env | (extra or {}),
                            data=json.dumps(payload)).stdout

    def seed(self) -> None:
        for kind, slug in (("personalities", "fixture"), ("writing-styles", "fixture"), ("talents", "fixture")):
            write(self.repo / RECORD / kind / (slug + ".md"), "# Fixture\n\nAcceptance content.\n")
        for handle in ("claude", "bwk", "djb", "rsc"):
            write(self.repo / RECORD / "identities" / (handle + ".yaml"),
                  f"name: {handle}\nhandle: {handle}\nkind: agent\npersonality: fixture\n"
                  "writing_style: fixture\ntalents: [fixture]\n")
        write(self.repo / RECORD / "archetypes/implement.yaml", "name: implement\ndescription: Acceptance\n")

    def mode(self, mode: str | None) -> None:
        write(self.repo / ".punt-labs/ethos.yaml", "resolution: repo-only\n" + ("" if mode is None else f"record: {mode}\n"))

    def snapshot(self, name: str, repo: Path | None = None) -> None:
        repo = repo or self.repo
        dest = self.root / "snapshots" / name
        save_json(dest / "inventory.json", inventory(repo))
        write(dest / "ls-files-stage.z", self.git("ls-files", "--stage", "-z", cwd=repo))
        write(dest / "cached.diff", self.git("diff", "--cached", "--binary", cwd=repo))
        index = Path(self.git("rev-parse", "--path-format=absolute", "--git-path", "index", cwd=repo).decode().strip())
        write(dest / "index", index.read_bytes())

    def create(self, label: str, rounds: int = 2, cwd: Path | None = None) -> str:
        contract = contract_body(label, rounds)
        path = write(self.root / "inputs" / (label + ".yaml"), contract)
        value = self.value("mission", "create", "--file", str(path), cwd=cwd)
        mid = value["mission_id"]
        require(re.fullmatch(MISSION, mid), f"invalid returned mission ID {mid!r}")
        return mid

    def submit(self, method: str, mid: str, body: str, cwd: Path | None = None,
               ok: bool = True, env: dict[str, str] | None = None) -> bytes:
        path = write(self.root / "inputs" / f"{method}-{self.run.sequence}.yaml", body)
        return self.cli("mission", method, mid, "--file", str(path), "--json", cwd=cwd, ok=ok, env=env)

    def audit(self, marker: str, session: str = "acceptance-session", cwd: Path | None = None) -> None:
        self.wrapper("audit-log.sh", {"session_id": session, "tool_name": "Bash",
                                     "tool_input": {"command": "printf " + marker}}, cwd,
                     {"DELEGATION_ID": "acceptance-audit"})
        files = list((self.repo / LOCAL).rglob("*.jsonl"))
        if cwd is not None and cwd != self.repo and contains(cwd / LOCAL, marker):
            raise Failure(f"audit appended in checkout {cwd / LOCAL}, not canonical store {self.repo / LOCAL}")
        require(any(marker.encode() in p.read_bytes() for p in files), "wrapper silently failed to append audit")

    def delegate(self, mid: str, agent: str, cwd: Path | None = None) -> str:
        out = self.wrapper("pre-tool-use.sh", {"session_id": "acceptance-dispatch", "tool_name": "Agent",
                           "tool_input": {"subagent_type": agent, "prompt": "retained-prompt-" + agent +
                                          " password=acceptance-synthetic-credential"}},
                           cwd, {"MISSION_ID": mid})
        value = json.loads(out)
        extra = value["hookSpecificOutput"]["additionalEnv"]
        did = extra["DELEGATION_ID"]
        artifact = Path(extra["MISSION_ARTIFACTS_DIR"])
        require(artifact.is_relative_to(self.repo / LOCAL), f"nonlocal artifact destination: {artifact}")
        require((self.repo / RECORD / "missions" / mid / "delegations" / did / "record.yaml").is_file(),
                "delegation record absent from shared authority")
        require(any(b"retained-prompt-" in p.read_bytes() for p in (self.repo / LOCAL).rglob("prompt.md")),
                "delegation prompt not retained locally")
        return did


def contract_body(label: str, rounds: int = 2) -> str:
    return ("leader: claude\nworker: bwk\nevaluator:\n  handle: djb\n"
            f"write_set: [work/{label}/]\nsuccess_criteria: [Acceptance evidence]\n"
            f"budget:\n  rounds: {rounds}\n  reflection_after_each: true\ncontext: Acceptance {label}\n")


def reflection(mid: str, recommendation: str, round_number: int = 1) -> str:
    return (f"mission: {mid}\nround: {round_number}\nauthor: claude\nconverging: true\n"
            f"signals: [private-reflection-signal]\nrecommendation: {recommendation}\nreason: Acceptance rationale\n")


def result_body(mid: str, round_number: int = 1) -> str:
    return (f"mission: {mid}\nround: {round_number}\nauthor: bwk\nverdict: pass\nconfidence: 0.9\n"
            "evidence:\n  - name: isolated fixture\n    status: pass\n")


def correction(mid: str) -> str:
    return f"mission: {mid}\nround: 0\nkind: decision\nauthor: claude\ncorrected: shared-correction-marker\n"


def contains(root: Path, marker: str) -> bool:
    return root.exists() and any(marker.encode() in p.read_bytes() for p in root.rglob("*") if p.is_file())


def source_section(manifest: str, path: str) -> str:
    """Inspect the emitted source shape, without a product or YAML dependency."""
    parts = manifest.split("- path: " + path + "\n")
    require(len(parts) == 2, f"manifest did not identify exactly one source: {path}")
    return re.split(r"\n    - path: |\n- path: ", parts[1], maxsplit=1)[0]


def default_seal(f: Fixture) -> None:
    f.mode(None)
    foreign = f.repo / ".git/hooks/pre-commit"
    write(foreign, "#!/bin/sh\nprintf 'foreign-hook-ran\\n' >> \"$HOME/foreign-hook\"\n")
    foreign.chmod(0o755)
    f.cli("enable")
    first = inventory(f.repo / RECORD)
    require(b"hook pre-commit" in foreign.read_bytes(), "enable did not install the pre-commit shim")
    write(f.repo / "source.txt", "staged-A\n")
    f.git("add", "source.txt")
    write(f.repo / "source.txt", "working-B\n")
    f.audit("default-first")
    index = f.repo / ".git/index"
    original = index.read_bytes()
    f.snapshot("before-seal")
    f.cli("audit", "seal", "--dry-run", "--verbose")
    f.cli("audit", "seal", "--verbose")
    f.cli("audit", "seal", "--verbose")
    require(index.read_bytes() == original, "default seal changed raw index bytes")
    require(f.git("show", ":source.txt") == b"staged-A\n", "seal replaced staged source")
    lock = write(index.with_suffix(".lock"), "held-index-lock\n")
    try:
        f.audit("default-held-lock")
        f.cli("audit", "seal", "--verbose")
        require(lock.read_bytes() == b"held-index-lock\n", "seal damaged another writer's index lock")
        require(index.read_bytes() == original, "locked-index seal changed index")
    finally:
        lock.unlink()
    require(contains(f.repo / LOCAL / "trace", "default-first"), "no local sealed audit chunk")
    f.audit("ordinary-commit-hook")
    f.git("commit", "-m", "ordinary source commit")
    require((f.home / "foreign-hook").read_text().count("foreign-hook-ran") == 1, "foreign hook did not run exactly once")
    require(contains(f.repo / LOCAL / "trace", "ordinary-commit-hook"), "real commit did not archive through installed hook")
    require(f.git("show", "HEAD:source.txt") == b"staged-A\n", "ordinary commit did not preserve staged A")
    require((f.repo / "source.txt").read_bytes() == b"working-B\n", "ordinary commit changed working B")
    tree = f.git("ls-tree", "-r", "--name-only", "HEAD").decode()
    require("sessions/" not in tree and ".punt-labs/local/" not in tree, "default commit published trace/local data")
    query = f.cli("audit", "show", "--delegation", "acceptance-audit", "--format", "json")
    require(b"default-first" in query and b"ordinary-commit-hook" in query, "audit reader lost archived entries")
    check_boundary(f.repo)
    f.snapshot("after-commit")
    # Do the real source commit before the separate idempotence assertion so a
    # re-enable defect cannot conceal whether the installed hook actually ran.
    f.cli("enable")
    require(first == inventory(f.repo / RECORD), "enable is not idempotent")


def modes(f: Fixture) -> None:
    f.mode("blame")
    f.cli("enable")
    f.audit("private-before-full")
    f.cli("audit", "seal")
    f.mode("full")
    f.cli("audit", "seal")
    require(not contains(f.repo / RECORD, "private-before-full"), "mode switch promoted private history")
    f.audit("eligible-full")
    f.cli("audit", "seal")
    require(contains(f.repo / RECORD / "sessions", "eligible-full"), "full mode did not publish eligible session audit")
    require(contains(f.repo / LOCAL / "trace", "eligible-full"), "full mode lost local archive")
    f.mode("blame")
    f.audit("private-after-full")
    f.cli("audit", "seal")
    require(not contains(f.repo / RECORD, "private-after-full"), "switch back retained full write policy")
    require(contains(f.repo / RECORD, "eligible-full"), "switch back deleted published history")
    check_boundary(f.repo, full=True)
    for invalid in ("invalid", "[full]", "{mode: full}", "[full", "null", "42", "true"):
        f.mode(invalid)
        before = inventory(f.repo / RECORD)
        f.cli("audit", "seal", ok=False)
        f.cli("mission", "list", "--json", ok=False)
        require(before == inventory(f.repo / RECORD), "invalid policy changed shared data")
    f.mode("blame")
    f.snapshot("modes")


def audit_recovery(f: Fixture) -> None:
    f.mode("blame")
    f.cli("enable")
    f.audit("recoverable-audit-marker")
    f.cli("audit", "seal")
    chunks = list((f.repo / LOCAL / "trace/sessions").rglob("audit-*.jsonl"))
    require(len(chunks) == 1, "fixture did not produce exactly one sealed audit chunk")
    chunk = chunks[0]
    original = chunk.read_bytes()
    corrupt = b"{intentionally corrupt acceptance chunk\n"
    write(chunk, corrupt)
    index = f.repo / ".git/index"
    # Archival failure is advisory ONLY in default-blame commit context.
    write(f.repo / "source.txt", "source-despite-old-trace-corruption\n")
    f.git("add", "source.txt")
    f.git("commit", "-m", "source remains available")
    require(f.git("show", "HEAD:source.txt") == b"source-despite-old-trace-corruption\n",
            "historical trace corruption blocked unrelated source commit")
    before = index.read_bytes()
    # Recovery is automatic: the user runs nothing. The next entry points (a
    # session start, then another real commit through the installed hook) must
    # repair the corrupt chunk without touching the index.
    f.wrapper("session-start.sh", {"session_id": "acceptance-recovery"})
    f.wrapper("session-start.sh", {"session_id": "acceptance-recovery"})
    require(index.read_bytes() == before, "automatic recovery changed index")
    require(any(p.read_bytes() == corrupt for p in (f.repo / LOCAL).rglob("*") if p.is_file()),
            "automatic recovery discarded original corrupt bytes")
    write(f.repo / "source.txt", "source-after-automatic-recovery\n")
    f.git("add", "source.txt")
    f.git("commit", "-m", "commit after automatic recovery")
    # Reader must return recovered content or name the loss; an empty success
    # without diagnostic evidence is not recovery.
    query = f.cli("audit", "show", "--delegation", "acceptance-audit").decode()
    diagnostic = (f.root / f"{f.run.sequence:04d}.stderr").read_text()
    require("recoverable-audit-marker" in query or "gap" in diagnostic.lower(),
            "quarantine produced neither recovery nor explicit gap evidence")
    require(original != corrupt, "invalid corruption fixture")
    check_boundary(f.repo)
    f.snapshot("audit-recovery")


def lifecycle(f: Fixture) -> None:
    f.mode(None)
    for recommendation in ("continue", "advance", "pivot", "stop", "escalate"):
        mid = f.create(recommendation)
        initial = f.value("mission", "show", mid)
        require(initial["round_decisions"] == {"version": 1, "entries": []}, "new mission lacks initialized authority")
        states = f.value("mission", "reflections", mid)
        require(states[0]["decision_state"] == "absent" and states[0]["detail_state"] == "absent",
                "new mission did not report non-submission distinctly")
        f.cli("mission", "advance", mid, ok=False)
        if recommendation == "continue":
            contract = f.repo / RECORD / "missions" / mid / "contract.yaml"
            valid = contract.read_text()
            for malformed in ("null", "{version: 99, entries: []}", "{version: 1, entries: null}"):
                bad = re.sub(r"(?ms)^round_decisions:.*?(?=^\S|\Z)", "round_decisions: " + malformed + "\n", valid)
                require(bad != valid, "authority corruption fixture did not replace decision block")
                write(contract, bad)
                try:
                    f.cli("mission", "show", mid, "--json", ok=False)
                    f.cli("mission", "advance", mid, ok=False)
                    require(contract.read_text() == bad, "invalid authority was silently repaired")
                finally:
                    write(contract, valid)
        for bad in (reflection(mid, recommendation) + "unknown_field: true\n",
                    reflection(mid, recommendation) + "---\n{}\n",
                    reflection("m-2000-01-01-999", recommendation),
                    reflection(mid, recommendation, 2), "mission: [malformed\n"):
            before = inventory(f.repo / RECORD)
            f.submit("reflect", mid, bad, ok=False)
            require(before == inventory(f.repo / RECORD), "rejected reflection changed shared authority")
        f.submit("result", mid, result_body(mid))
        f.submit("reflect", mid, reflection(mid, recommendation))
        # A retained exact retry is permitted; a different second submission is not.
        f.submit("reflect", mid, reflection(mid, recommendation))
        f.submit("reflect", mid, reflection(mid, recommendation).replace("Acceptance rationale", "Changed rationale"), ok=False)
        now = f.value("mission", "show", mid)
        decisions = now["round_decisions"]["entries"]
        require(len(decisions) == 1 and decisions[0]["recommendation"] == recommendation, "lost/duplicate compact decision")
        require(now["evaluator"] == initial["evaluator"] and now["budget"] == initial["budget"], "reflection changed frozen authority")
        permitted = recommendation not in {"stop", "escalate"}
        # Ordinary staging of shared authority, then an independent machine
        # with no archive. Exercise gates while the clone's mission is OPEN.
        f.git("add", str(RECORD))
        f.git("commit", "-m", "handoff " + recommendation)
        clone = f.root / ("handoff-" + recommendation)
        f.git("clone", "--no-local", str(f.repo), str(clone))
        home = f.root / ("home-" + recommendation)
        home.mkdir()
        clone_env = f.env | {"HOME": str(home)}
        require(not (clone / LOCAL).exists(), "handoff clone inherited local archive")
        remote = f.value("mission", "show", mid, cwd=clone, env=clone_env)
        require(remote == now, "handoff changed mission authority")
        states = f.value("mission", "reflections", mid, cwd=clone, env=clone_env)
        require(states[0]["decision_state"] == "present" and states[0]["detail_state"] == "unavailable_here",
                "handoff erased submitted decision when prose unavailable")
        f.submit("reflect", mid, reflection(mid, recommendation), cwd=clone, env=clone_env, ok=False)
        f.cli("mission", "advance", mid, "--json", cwd=clone, env=clone_env, ok=permitted)
        f.cli("mission", "advance", mid, "--json", ok=permitted)
        round_number = 2 if permitted else 1
        if permitted:
            f.submit("result", mid, result_body(mid, 2))
            f.submit("reflect", mid, reflection(mid, "continue", 2))
            f.cli("mission", "advance", mid, ok=False)  # exhausted budget
        f.cli("mission", "close", mid)
        closed = f.value("mission", "show", mid)
        f.submit("correct", mid, correction(mid))
        annotated = f.value("mission", "show", mid)
        require({k: v for k, v in closed.items() if k != "corrections"} ==
                {k: v for k, v in annotated.items() if k != "corrections"},
                "correction changed lifecycle/result state")
        require(len(annotated["corrections"]) == 1 and annotated["corrections"][0]["corrected"] == "shared-correction-marker",
                "shared correction reader lost accepted annotation")
        require((f.repo / RECORD / "missions" / mid / "corrections.yaml").is_file(), "no shared correction authority")
        require(len(f.value("mission", "results", mid)["results"]) == round_number, "accepted results not retained")
    check_boundary(f.repo)
    f.snapshot("lifecycle")


def required_event_failure(f: Fixture) -> None:
    f.mode("blame")
    blocked_audit = f.repo / LOCAL / "sessions/failed-audit.audit.jsonl"
    blocked_audit.mkdir(parents=True)
    try:
        notice = f.wrapper("audit-log.sh", {"session_id": "failed-audit", "tool_name": "Read",
                                           "tool_input": {"file_path": "source.txt"}})
        require("audit" in json.loads(notice).get("systemMessage", "").lower(),
                "advisory audit write failure was silently swallowed")
    finally:
        blocked_audit.rmdir()
    for method in ("reflect", "result"):
        mid = f.create("rollback-" + method)
        streams = list((f.repo / LOCAL / "missions" / mid).glob("*.log.jsonl"))
        require(len(streams) == 1, "create did not establish one canonical local mission stream")
        live = streams[0]
        backup = live.with_suffix(".saved")
        live.rename(backup)
        live.mkdir()  # Fail the event stream, not intent staging or permissions.
        before = inventory(f.repo / RECORD)
        body = reflection(mid, "continue") if method == "reflect" else result_body(mid)
        try:
            f.submit(method, mid, body, ok=False)
            require(before == inventory(f.repo / RECORD), f"failed required {method} event installed shared authority")
        finally:
            live.rmdir()
            backup.rename(live)
        f.submit(method, mid, body)
        if method == "reflect":
            require(len(f.value("mission", "show", mid)["round_decisions"]["entries"]) == 1, "retry duplicated reflection authority")
        else:
            require(len(f.value("mission", "results", mid)["results"]) == 1, "retry duplicated result")
    check_boundary(f.repo)
    f.snapshot("rollback-retry")


def worktree_retention(f: Fixture) -> None:
    f.mode("blame")
    f.cli("enable")
    f.git("add", ".gitignore", "CLAUDE.md", str(RECORD), ".punt-labs/ethos.yaml")
    f.git("commit", "-m", "enabled fixture")
    linked = f.root / "linked"
    f.git("worktree", "add", "-b", "retention", str(linked))
    mid = f.create("linked", cwd=linked)
    f.delegate(mid, "bwk", cwd=linked)
    f.submit("reflect", mid, reflection(mid, "continue"), cwd=linked)
    f.audit("retained-sealed", "linked-session", cwd=linked)
    f.cli("audit", "seal", cwd=linked)
    f.audit("retained-unsealed-tail", "linked-session", cwd=linked)
    shared = inventory(f.repo / LOCAL)
    require(contains(f.repo / LOCAL, "retained-unsealed-tail"), "tail not owned by S")
    local_linked = linked / LOCAL
    require(not local_linked.exists() or not inventory(local_linked), "linked checkout owns runtime state")
    f.git("worktree", "remove", str(linked))  # no SessionEnd or courtesy flush
    require(shared == inventory(f.repo / LOCAL), "worktree removal changed retained evidence")
    query = f.cli("audit", "show", "--delegation", "acceptance-audit")
    require(b"retained-sealed" in query and b"retained-unsealed-tail" in query, "surviving checkout cannot read union")
    reads = f.value("mission", "reflections", mid)
    require(reads[0]["detail_state"] == "present", "surviving reflection unavailable")
    check_boundary(f.repo)
    f.snapshot("surviving-store")


def full_export(f: Fixture) -> None:
    f.mode("blame")
    mid = f.create("export")
    other = f.create("unrelated")
    f.delegate(mid, "bwk")
    f.submit("reflect", mid, reflection(mid, "continue"))
    f.submit("result", mid, result_body(mid))
    f.audit("excluded-session-audit")
    write(f.repo / LOCAL / "scratch/agent-artifact.txt", "excluded-agent-artifact")
    write(f.repo / LOCAL / "quarantine/raw.corrupt", "excluded-quarantine")
    before = (f.repo / ".git/index").read_bytes()
    dest = f.repo / "exported"
    f.cli("mission", "export", mid, "--full", "--dir", str(dest))
    require((f.repo / ".git/index").read_bytes() == before, "full export staged files")
    require(contains(dest, "retained-prompt-") and contains(dest, "private-reflection-signal"), "full export omitted private mission detail")
    require(any(p.name.startswith("log-") and p.suffix == ".jsonl" for p in dest.rglob("*")), "full export omitted locally sealed unsealed mission tail")
    require(not any(p.name.endswith((".log.jsonl", ".audit.jsonl")) for p in dest.rglob("*")), "export copied live files as files")
    for marker in (other, "excluded-session-audit", "excluded-agent-artifact", "excluded-quarantine"):
        require(not contains(dest, marker), f"full export leaked {marker}")
    require(not contains(dest, "acceptance-synthetic-credential"), "full export leaked synthetic credential")
    f.git("add", "exported")
    require(b"exported/" in f.git("diff", "--cached", "--name-only"), "full export requires force-add")
    check_boundary(f.repo)
    f.snapshot("full-export")


def publish_squash(f: Fixture) -> None:
    f.mode("blame")
    f.cli("enable")
    f.git("add", ".gitignore", "CLAUDE.md", str(RECORD), ".punt-labs/ethos.yaml")
    f.git("commit", "-m", "enabled fixture")
    base = f.git("rev-parse", "HEAD").decode().strip()
    linked = f.root / "feature"
    f.git("worktree", "add", "-b", "publication", str(linked))
    mids = [f.create(label, cwd=linked) for label in ("publish-one", "publish-two")]
    pairs = [(mids[0], f.delegate(mids[0], "bwk", linked)),
             (mids[0], f.delegate(mids[0], "rsc", linked)),
             (mids[1], f.delegate(mids[1], "bwk", linked))]
    for number, (mid, did) in enumerate(pairs):
        path = f"known-{number}.txt"
        write(linked / path, f"known-source-{number}\n")
        f.git("add", path, cwd=linked)
        f.git("commit", "-m", f"source {number}\n\nMission: {mid}\nDelegation: {did}", cwd=linked)
    write(linked / "ambiguous.txt", "ambiguous-source\n")
    f.git("add", "ambiguous.txt", cwd=linked)
    f.git("commit", "-m", f"ambiguous\n\nMission: {mids[0]}\nMission: {mids[1]}\n"
          f"Delegation: {pairs[0][1]}\nDelegation: {pairs[2][1]}", cwd=linked)
    frozen = {}
    for mid in mids:
        f.submit("result", mid, result_body(mid), cwd=linked)
        f.submit("reflect", mid, reflection(mid, "continue"), cwd=linked)
        f.cli("mission", "advance", mid, cwd=linked)
        f.submit("result", mid, result_body(mid, 2), cwd=linked)
        f.cli("mission", "close", mid, cwd=linked)
        f.submit("correct", mid, correction(mid), cwd=linked)
        frozen[mid] = f.value("mission", "show", mid, cwd=linked)
    adr = f.value("adr", "create", "--title", "Acceptance ADR", "--decision", "share-adr-marker",
                  "--author", "claude", "--mission-id", mids[0], cwd=linked)["id"]
    # Use a genuinely alternate committing index, outside both source trees.
    normal = Path(f.git("rev-parse", "--path-format=absolute", "--git-path", "index", cwd=linked).decode().strip())
    original = normal.read_bytes()
    alternate = write(f.root / "alternate-index", original)
    env = f.env | {"GIT_INDEX_FILE": str(alternate)}
    write(linked / "unrelated.txt", "unrelated-staged-A\n")
    f.git("add", "unrelated.txt", cwd=linked, env=env)
    write(linked / "unrelated.txt", "unrelated-working-B\n")
    lock = write(Path(str(alternate) + ".lock"), "held-publication-lock\n")
    starting = alternate.read_bytes()
    try:
        f.cli("mission", "publish", *mids, "--base", base, "--json", cwd=linked, env=env, ok=False)
        require(alternate.read_bytes() == starting and lock.read_text() == "held-publication-lock\n", "failed publication changed held actual index")
    finally:
        lock.unlink()
    receipt = f.value("mission", "publish", *mids, "--base", base, cwd=linked, env=env)["receipt"]
    require(receipt["index_installed"], "publisher did not install actual index")
    require(normal.read_bytes() == original, "publication changed ordinary index instead of alternate")
    require(f.git("show", ":unrelated.txt", cwd=linked, env=env) == b"unrelated-staged-A\n", "publication replaced unrelated staged blob")
    manifest = receipt["manifest"]
    f.cli("provenance", "verify-index", manifest, cwd=linked, env=env)
    required = [str(RECORD / "missions.jsonl"), str(RECORD / "adrs" / (adr + ".yaml"))]
    for mid in mids:
        required += [str(RECORD / "missions" / mid / leaf) for leaf in ("contract.yaml", "results.yaml", "corrections.yaml")]
    required += [str(RECORD / "missions" / mid / "delegations" / did / "record.yaml") for mid, did in pairs]
    for path in required:
        require(f.git("show", ":" + path, cwd=linked, env=env), f"required publication missing: {path}")
    # Missing inventory must fail in the REAL candidate index, not be repaired.
    saved = alternate.read_bytes()
    f.git("update-index", "--force-remove", required[2], cwd=linked, env=env)
    f.cli("provenance", "verify-index", manifest, cwd=linked, env=env, ok=False)
    write(alternate, saved)
    f.git("commit", "-m", "selected publication", cwd=linked, env=env)
    message = f.value("provenance", "message", "HEAD", cwd=linked)
    body = write(f.root / "squash-message", "Acceptance squash\n\n" + message["body"])
    # The checkout's alternate-index commit left its ordinary index stale. Remove
    # the fixture checkout only after aligning that index to the committed tree.
    f.git("read-tree", "HEAD", cwd=linked)
    (linked / "unrelated.txt").write_text("unrelated-staged-A\n")
    f.git("worktree", "remove", str(linked))
    # The integration checkout is independent of S's live, untracked authority.
    # This models the server's squash without hand-copying selected records or
    # relying on hooks that do not run on a remote squash server.
    integration = f.root / "integration"
    f.git("clone", "--no-local", str(f.repo), str(integration))
    f.git("config", "user.name", "Storage Integrator", cwd=integration)
    f.git("config", "user.email", "integrator@example.invalid", cwd=integration)
    f.git("config", "commit.gpgsign", "false", cwd=integration)
    f.git("merge", "--squash", "origin/publication", cwd=integration)
    f.cli("provenance", "verify-index", manifest, cwd=integration)
    f.git("commit", "-F", str(body), cwd=integration)
    squash = f.git("rev-parse", "HEAD", cwd=integration).decode().strip()
    f.cli("provenance", "verify", squash, cwd=integration)
    f.git("branch", "-D", "publication")
    clone = f.root / "clone"
    f.git("clone", "--no-local", "--single-branch", str(integration), str(clone))
    home = f.root / "clone-home"
    home.mkdir()
    clone_env = f.env | {"HOME": str(home)}
    require(not (clone / LOCAL).exists(), "fresh clone inherited local history")
    f.git("cat-file", "-e", message["head"] + "^{commit}", cwd=clone, env=clone_env, ok=False)
    f.cli("provenance", "verify", "HEAD", cwd=clone, env=clone_env)
    tree = f.git("ls-tree", "-r", "--name-only", "HEAD", cwd=clone, env=clone_env).decode().splitlines()
    require(set(required).issubset(tree), "actual squash lost required blame inventory")
    for mid in mids:
        require(f.value("mission", "show", mid, cwd=clone, env=clone_env) == frozen[mid], "clone changed frozen mission/results")
        states = f.value("mission", "reflections", mid, cwd=clone, env=clone_env)
        require(states[0]["decision_state"] == "present" and states[0]["detail_state"] == "unavailable_here", "clone confused unavailable prose with non-submission")
        f.submit("reflect", mid, reflection(mid, "continue"), cwd=clone, env=clone_env, ok=False)
        outcome = f.value("mission", "results", mid, cwd=clone, env=clone_env)
        require(len(outcome["results"]) == 2 and outcome["corrections"][0]["corrected"] == "shared-correction-marker",
                "clone outcome reader lost all-results/correction authority")
    require(b"share-adr-marker" in f.cli("adr", "show", adr, "--json", cwd=clone, env=clone_env), "clone reader lost ADR")
    text = f.git("show", "HEAD:" + manifest, cwd=clone, env=clone_env).decode()
    for number, (mid, did) in enumerate(pairs):
        section = source_section(text, f"known-{number}.txt")
        require("mission: " + mid in section and "delegation: " + did in section and "unknown:" not in section,
                f"known source lost its association tuple: known-{number}.txt")
    section = source_section(text, "ambiguous.txt")
    require("unknown:" in section and "candidates: []" in section,
            "ambiguous parallel trailers selected an arbitrary first agent")
    check_boundary(clone)
    f.snapshot("actual-squash-clone", clone)
    check_boundary(f.repo)


class MCP:
    """One long-lived stdio transport; bounded reads, no startup polling."""
    def __init__(self, f: Fixture):
        self.f = f
        self.stderr = (f.root / "mcp.stderr").open("wb")
        self.process = subprocess.Popen([str(f.binary), "serve"], cwd=f.repo, env=f.env,
                                        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=self.stderr)
        self.messages: queue.Queue[bytes] = queue.Queue()
        self.reader = threading.Thread(target=self.read, daemon=True)
        self.reader.start()
        self.sequence = 0

    def read(self) -> None:
        assert self.process.stdout is not None
        for line in self.process.stdout:
            self.messages.put(line)
        self.messages.put(b"")

    def send(self, value: dict[str, Any]) -> None:
        assert self.process.stdin is not None
        self.process.stdin.write(json.dumps(value).encode() + b"\n")
        self.process.stdin.flush()

    def call(self, method: str, params: dict[str, Any]) -> Any:
        self.sequence += 1
        request = {"jsonrpc": "2.0", "id": self.sequence, "method": method, "params": params}
        save_json(self.f.root / f"mcp-{self.sequence}.request.json", request)
        self.send(request)
        deadline = time.monotonic() + 30
        while True:
            try:
                line = self.messages.get(timeout=max(0.001, deadline - time.monotonic()))
            except queue.Empty as error:
                raise Failure("MCP response timeout") from error
            require(line, "MCP transport closed before response")
            response = json.loads(line)
            save_json(self.f.root / f"mcp-{self.sequence}.response.json", response)
            if response.get("id") == self.sequence:
                require("error" not in response, f"MCP protocol error: {response}")
                return response["result"]
            require(time.monotonic() < deadline, "MCP response deadline exceeded")

    def close(self) -> None:
        assert self.process.stdin is not None
        self.process.stdin.close()
        try:
            code = self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.terminate()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait()
            code = self.process.returncode
        self.reader.join(timeout=5)
        assert self.process.stdout is not None
        self.process.stdout.close()
        self.stderr.close()
        save_json(self.f.root / "mcp.exit.json", {"exit": code})
        require(code == 0, f"MCP shutdown exit {code}")


def mcp_modes(f: Fixture) -> None:
    f.mode("blame")
    client = MCP(f)
    try:
        client.call("initialize", {"protocolVersion": "2024-11-05", "capabilities": {},
                                   "clientInfo": {"name": "storage-acceptance", "version": "1"}})
        client.send({"jsonrpc": "2.0", "method": "notifications/initialized"})
        response = client.call("tools/call", {"name": "mission", "arguments": {
            "method": "create", "contract": contract_body("mcp-modes", 3)}})
        require(not response.get("isError"), f"MCP create failed: {response}")
        text = "\n".join(c.get("text", "") for c in response.get("content", []))
        ids = set(re.findall(MISSION, text))
        require(len(ids) == 1, f"MCP create did not identify exactly one mission: {text}")
        mid = ids.pop()
        for round_number, (mode, label) in enumerate((("blame", "mcp-private"), ("full", "mcp-shared"), ("blame", "mcp-private-again")), 1):
            f.mode(mode)
            response = client.call("tools/call", {"name": "mission", "arguments": {
                "method": "reflect", "mission_id": mid,
                "reflection": reflection(mid, "continue", round_number).replace("private-reflection-signal", label + "-signal")}})
            require(not response.get("isError"), f"MCP reflect failed: {response}")
            published = f.repo / RECORD / "missions" / mid / "reflections.yaml"
            contents = published.read_text() if published.exists() else ""
            require((label + "-signal" in contents) == (mode == "full"), f"long-lived MCP retained stale {mode} policy")
            require("mcp-private-signal" not in contents and "mcp-private-again-signal" not in contents,
                    "collection rewrite promoted private reflection history")
            if round_number >= 2:
                require("mcp-shared-signal" in contents, "mode switch lost already-published reflection")
            if round_number < 3:
                response = client.call("tools/call", {"name": "mission", "arguments": {"method": "advance", "mission_id": mid}})
                require(not response.get("isError"), f"MCP advance failed: {response}")
        f.mode("invalid")
        response = client.call("tools/call", {"name": "mission", "arguments": {"method": "list"}})
        require(response.get("isError"), "MCP silently accepted invalid policy")
    finally:
        client.close()
    f.mode("blame")
    check_boundary(f.repo, full=True)
    f.snapshot("mcp-modes")


CORPUS = ROOT / "internal/autorepair/testdata/corpus"
UPGRADE_MARKER = "ethos: upgraded storage"
TOKEN = re.compile(rb"gh[pousr]_[A-Za-z0-9]{36,}")
USER_PATH = re.compile(rb"/(?:Users|home)/[A-Za-z][A-Za-z0-9._-]*")


def restore_corpus_repo(f: Fixture, snapshot: Path, repo: Path, env: dict[str, str]) -> None:
    """Rebuild a corpus snapshot's repository: files, commit, index and the old release's hooks."""
    shutil.copytree(snapshot / "repo", repo, symlinks=True)
    run = lambda *args: f.run.run(["git", *args], repo, env)  # noqa: E731
    run("init", "-b", "main")
    run("config", "user.name", "Storage Fixture")
    run("config", "user.email", "fixture@example.invalid")
    run("config", "commit.gpgsign", "false")
    run("config", "core.hooksPath", str(repo / ".git/hooks"))
    # The bundle is the snapshot's whole history; fetch it, point main at it and
    # load its tree into the index so the working files are tracked as shipped.
    run("fetch", str(snapshot / "git/repo.bundle"), "refs/heads/main")
    run("update-ref", "refs/heads/main", "FETCH_HEAD")
    run("read-tree", "HEAD")
    for hook in sorted((snapshot / "git/hooks").glob("*")):
        target = repo / ".git/hooks" / hook.name
        shutil.copy2(hook, target)
        target.chmod(0o755)


def scan_for_leaks(scratch: Path, home: Path, repo: Path) -> None:
    """No credential-shaped token and no machine path other than the scratch tree itself."""
    scratch_bytes = str(scratch).encode()
    for root in (repo, home):
        for path in sorted(p for p in root.rglob("*") if p.is_file() and not p.is_symlink()):
            data = path.read_bytes()
            require(not TOKEN.search(data), f"credential-shaped token survives upgrade in {path}")
            leaked = USER_PATH.search(data.replace(scratch_bytes, b""))
            require(not leaked, f"machine path {leaked.group(0).decode() if leaked else ''} in {path}")


def upgrade(f: Fixture) -> None:
    """Every released layout, upgraded by this binary with no user action."""
    versions = sorted(p for p in CORPUS.iterdir() if p.is_dir())
    require(len(versions) >= 9, f"legacy corpus has {len(versions)} snapshots, expected at least 9")
    for snapshot in versions:
        label = snapshot.name
        scratch = f.root / "upgrade" / label
        home, tmp, bindir, repo = scratch / "home", scratch / "tmp", scratch / "bin", scratch / "repo"
        for path in (tmp, bindir):
            path.mkdir(parents=True)
        shutil.copytree(snapshot / "home", home, symlinks=True)
        (bindir / "ethos").symlink_to(f.binary)
        env = clean_env(home, tmp, str(bindir) + os.pathsep + os.environ.get("PATH", os.defpath))
        env["GIT_CEILING_DIRECTORIES"] = str(scratch)
        restore_corpus_repo(f, snapshot, repo, env)

        def entry(*args: str, data: str | None = None) -> str:
            result = f.run.run(list(args), repo, env, data=data)
            return (result.stdout + result.stderr).decode(errors="replace")

        start = entry(str(f.binary), "hook", "session-start",
                      data=json.dumps({"session_id": "upgrade-" + label, "source": "startup", "cwd": str(repo)}))
        write(repo / "upgrade-new-file.txt", "new work after upgrade\n")
        entry("git", "add", "upgrade-new-file.txt")
        commit = entry("git", "commit", "-m", "work after upgrade")
        notices = [line for line in (start + commit).splitlines() if UPGRADE_MARKER in line]
        require(len(notices) <= 1, f"{label}: {len(notices)} upgrade lines across session start and commit: {notices}")
        write(repo / "upgrade-new-file.txt", "second change\n")
        entry("git", "add", "upgrade-new-file.txt")
        again = entry("git", "commit", "-m", "second work after upgrade")
        chatter = [line for line in again.splitlines() if "ethos" in line.lower()]
        require(not chatter, f"{label}: second commit printed ethos output: {chatter}")
        local = entry("git", "ls-files", ".punt-labs/local")
        require(not local.strip(), f"{label}: tracked machine-local files after upgrade: {local!r}")
        # doctor refuses to run with the test-harness override set, so it gets the
        # environment a user's shell has.
        doctor_env = {k: v for k, v in env.items() if k != "ETHOS_TEST_NOT_UNDER_CLAUDE_CODE"}
        doctor_env["ETHOS_SESSION"] = "upgrade-" + label
        doctor = (f.run.run([str(f.binary), "doctor"], repo, doctor_env).stdout).decode(errors="replace")
        require(re.search(r"^\s*Storage\b", doctor, re.MULTILINE), f"{label}: ethos doctor has no Storage row:\n{doctor}")
        scan_for_leaks(scratch, home, repo)


CASES = (default_seal, modes, audit_recovery, lifecycle, required_event_failure, worktree_retention,
         full_export, publish_squash, mcp_modes, upgrade)


def source_evidence(runner: Runner, env: dict[str, str]) -> dict[str, Any]:
    head = runner.run(["git", "rev-parse", "HEAD", "HEAD^{tree}"], ROOT, env).stdout.decode().splitlines()
    # Product/build inputs, including concurrent workers' NEW files. Go tests
    # are not build inputs to the CLI; edits to them cannot alter this binary.
    paths = [ROOT / "go.mod", ROOT / "go.sum", ROOT / "Makefile", Path(__file__)]
    for directory in ("cmd", "internal", "plugin"):
        paths += [p for p in (ROOT / directory).rglob("*") if p.is_file() and not p.name.endswith("_test.go")]
    hashes = {p.relative_to(ROOT).as_posix(): digest(p.read_bytes()) for p in sorted(paths)}
    return {"head": head[0], "head_tree": head[1], "input_sha256": hashes,
            "input_tree_sha256": digest(json.dumps(hashes, sort_keys=True).encode())}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, help="existing artifact; do not build or install")
    args = parser.parse_args()
    parent = ROOT / ".tmp/opencode/storage-acceptance"
    parent.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="run-", dir=parent)).resolve()
    report: dict[str, Any] = {"schema_version": 1, "status": "fail", "cases": [],
        "required_cases": [case.__name__ for case in CASES],
        "coverage_limits": ["Final walks do not prove transient placement; syscall/fault acceptance is separate.",
                            "No crash/fsync/EXDEV injection, competing-index writer, live UI or external-consumer checks.",
                            "The upgrade case replays the committed legacy corpus; it does not cover layouts absent from it."]}
    runner = Runner(directory / "setup")
    tmp = directory / "build-tmp"
    tmp.mkdir()
    env = clean_env(Path.home(), tmp, os.environ.get("PATH", os.defpath))
    try:
        for tool in ("git", "bash"):
            require(shutil.which(tool, path=env["PATH"]), f"missing required executable: {tool}")
        report["source_before"] = source_evidence(runner, env)
        if args.binary:
            binary = args.binary.resolve()
        else:
            require(shutil.which("go", path=env["PATH"]), "missing required Go compiler")
            binary = directory / "bin/ethos"
            binary.parent.mkdir()
            build_env = env | {"GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local",
                               "GOENV": "off", "GOWORK": "off", "CGO_ENABLED": "0"}
            runner.run(["go", "build", "-o", str(binary), "./cmd/ethos"], ROOT, build_env, timeout=180)
        require(binary.is_file() and os.access(binary, os.X_OK), f"missing executable binary: {binary}")
        report["binary"] = {"path": str(binary), "sha256": digest(binary.read_bytes()),
                            "built_here": args.binary is None}
        report["source_after_build"] = source_evidence(runner, env)
        require(report["source_before"] == report["source_after_build"], "product inputs changed during build; rerun at a coherent revision")
        runner.run([str(binary), "version"], ROOT, env)
        for case in CASES:
            start = time.monotonic()
            item: dict[str, Any] = {"name": case.__name__, "status": "fail"}
            case_runner = Runner(directory / case.__name__)
            fixture = None
            try:
                fixture = Fixture(case_runner, binary)
                case(fixture)
                item["status"] = "pass"
            except Exception as error:  # Preserve driver defects as failures too.
                item["error"] = str(error)
                write(case_runner.directory / "failure.txt", traceback.format_exc())
            finally:
                if fixture is not None:
                    save_json(case_runner.directory / "final-inventory.json", inventory(fixture.repo))
                item["seconds"] = round(time.monotonic() - start, 3)
                report["cases"].append(item)
                save_json(case_runner.directory / "result.json", item)
                save_json(directory / "results.json", report)
            print(f"{item['status'].upper()} {case.__name__}" + (": " + item["error"] if "error" in item else ""))
        report["source_after_cases"] = source_evidence(runner, env)
        require(digest(binary.read_bytes()) == report["binary"]["sha256"], "binary changed during acceptance")
        before_hooks = {p: h for p, h in report["source_after_build"]["input_sha256"].items() if p.startswith("plugin/")}
        after_hooks = {p: h for p, h in report["source_after_cases"]["input_sha256"].items() if p.startswith("plugin/")}
        require(before_hooks == after_hooks, "shipped wrappers changed during acceptance; rerun with coherent inputs")
        require(len(report["cases"]) == len(CASES) and all(c["status"] == "pass" for c in report["cases"]), "required storage acceptance case(s) failed")
        report["status"] = "pass"
    except (Failure, OSError, ValueError) as error:
        report["error"] = str(error)
        print(f"FAIL storage acceptance: {error}", file=sys.stderr)
    finally:
        save_json(directory / "results.json", report)
        print(f"Evidence: {directory / 'results.json'}")
    return 0 if report["status"] == "pass" else 1


if __name__ == "__main__":
    sys.exit(main())
