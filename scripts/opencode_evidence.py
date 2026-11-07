"""Read-only, stdout-only evidence inspection; never run or import evidence.

Paths are scoped to this script's checkout, not the caller's cwd. External
access is hash/stat only, for the two exact installed tool files bound in r351.
No output files, subprocesses, network, glob expansion or configurable roots.
Descriptor-relative no-follow opens fail closed on unsupported platforms.
These observations are not a snapshot lock or proof of historical immutability.
"""

import argparse
import hashlib
import json
import math
import os
import stat
import sys
from collections import Counter
from contextlib import contextmanager
from pathlib import Path

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[1]
# Exact operator-authorized files, as recorded in archival-kernel-bindings-r351.
# Components keep this explicitly workstation-specific exception separate from
# portable workspace paths. No home discovery, prefix permission or link resolve.
TOOLS = {
    "go": Path("/", "home", "jfreeman", "go", "pkg", "mod",
               "golang.org", "toolchain@v0.0.1-go1.26.5.linux-amd64", "bin", "go"),
    "gcc": Path("/usr/bin/x86_64-linux-gnu-gcc-13"),
}
MAX_BYTES = 256 * 1024 * 1024
MAX_JSON_BYTES = 8 * 1024 * 1024
MAX_LINE_BYTES = 1024 * 1024
MAX_EVENTS = 1_000_000
MAX_FILES = 10_000
MAX_DEPTH = 64
TERMINALS = ("pass", "fail", "skip")


class EvidenceError(Exception):
    """A diagnostic that contains no evidence contents."""


def require(ok, message):
    if not ok:
        raise EvidenceError(message)


def safe_parts(path):
    require(".." not in path.parts, "path traversal refused")
    require(not any(".env" in part.lower() for part in path.parts),
            "credential-name path component refused")


def workspace_path(value):
    path = Path(value)
    safe_parts(path)
    path = path if path.is_absolute() else ROOT / path
    require(path.is_relative_to(ROOT), "path is outside the workspace")
    return path


def stamp(info):
    return (info.st_dev, info.st_ino, info.st_mode, info.st_size,
            info.st_mtime_ns, info.st_ctime_ns)


@contextmanager
def opened(path, directory=False):
    """Walk from / with pinned directory descriptors; never follow any link."""
    safe_parts(path)
    require(path.is_absolute(), "internal path must be absolute")
    require(os.name == "posix" and hasattr(os, "O_NOFOLLOW") and
            hasattr(os, "O_DIRECTORY") and os.open in os.supports_dir_fd and
            os.stat in os.supports_dir_fd and os.scandir in os.supports_fd,
            "no-follow descriptor reads unsupported on this platform")
    flags = os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK
    parent = os.open("/", flags | os.O_DIRECTORY)
    handle = None
    try:
        for part in path.parts[1:-1]:
            child = os.open(part, flags | os.O_DIRECTORY, dir_fd=parent)
            os.close(parent)
            parent = child
        name = path.name
        handle = os.open(name, flags | (os.O_DIRECTORY if directory else 0), dir_fd=parent)
        before = os.fstat(handle)
        require((stat.S_ISDIR if directory else stat.S_ISREG)(before.st_mode),
                "expected a regular directory" if directory else "expected a regular file")
        yield handle, before
        require(stamp(before) == stamp(os.fstat(handle)) ==
                stamp(os.stat(name, dir_fd=parent, follow_symlinks=False)),
                "evidence changed during inspection")
    finally:
        if handle is not None:
            os.close(handle)
        os.close(parent)


def fingerprint(path):
    with opened(path) as (handle, info):
        require(info.st_size <= MAX_BYTES, "file exceeds byte limit")
        digest = hashlib.sha256()
        size = 0
        while chunk := os.read(handle, 1024 * 1024):
            size += len(chunk)
            require(size <= MAX_BYTES, "file exceeds byte limit")
            digest.update(chunk)
        require(size == info.st_size, "file size changed during inspection")
        result = {"path": str(path), "sha256": digest.hexdigest(), "size": size,
                  "device": info.st_dev, "inode": info.st_ino}
    return result


def directory_names(handle, limit):
    """Read at most limit + 1 entries from a pinned directory, then sort."""
    names = []
    with os.scandir(handle) as scanner:
        for entry in scanner:
            require(len(names) < limit, "inventory exceeds entry limit")
            names.append(entry.name)
    return sorted(names)


def inventory(path):
    """Sorted regular-file inventory, including hidden files; no ignored paths."""
    files = []
    entries = 0

    def visit(directory, depth=0):
        nonlocal entries
        require(depth <= MAX_DEPTH, "inventory exceeds depth limit")
        with opened(directory, directory=True) as (handle, _):
            names = directory_names(handle, MAX_FILES - entries)
            # Reserve these siblings before descending, so retained names across
            # the traversal never exceed the same global entry budget.
            entries += len(names)
            for name in names:
                child = directory / name
                safe_parts(child)
                info = os.stat(name, dir_fd=handle, follow_symlinks=False)
                if stat.S_ISDIR(info.st_mode):
                    visit(child, depth + 1)
                else:
                    require(stat.S_ISREG(info.st_mode), "inventory contains a link or special file")
                    record = fingerprint(child)
                    record["path"] = child.relative_to(path).as_posix()
                    files.append(record)
            require(names == directory_names(handle, len(names)), "directory changed during inspection")

    visit(path)
    return {"path": str(path), "files": sorted(files, key=lambda item: item["path"])}


def strict_json(text):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, "duplicate JSON key")
            result[key] = value
        return result

    def number(value):
        result = float(value)
        require(math.isfinite(result), "nonfinite JSON number")
        return result

    def constant(_):
        raise EvidenceError("nonfinite JSON constant")

    return json.loads(text, object_pairs_hook=unique, parse_float=number, parse_constant=constant)


def read_json(path):
    with opened(path) as (handle, info):
        require(info.st_size <= MAX_JSON_BYTES, "JSON exceeds byte limit")
        with os.fdopen(os.dup(handle), "rb") as stream:
            data = stream.read(MAX_JSON_BYTES + 1)
        require(len(data) <= MAX_JSON_BYTES, "JSON exceeds byte limit")
        value = strict_json(data.decode("utf-8"))
    return {"path": str(path), "value": value}


def recount(path):
    """Recount one Go -json invocation; failed test outcomes are not parse errors."""
    tests, packages = {}, {}
    test_states, package_states = {}, {}
    uncollected = {}
    actions = Counter()
    test_actions = Counter({action: 0 for action in ("run", *TERMINALS)})
    anomalies = []
    events = 0

    def anomaly(kind, package, test, action, state):
        anomalies.append({"kind": kind, "package": package, "test": test,
                          "event": events, "action": action, "state": state})

    def transition(state, action, start):
        if state == "not_started" and action == start:
            return "running"
        if state == "running":
            if action in TERMINALS:
                return "terminal"
            if action in ("output", "bench"):
                return state
            if start == "run" and action == "pause":
                return "paused"
        if start == "run" and state == "paused":
            if action == "cont":
                return "running"
            if action in ("output", "bench"):
                return state
        return None

    with opened(path) as (handle, info):
        require(info.st_size <= MAX_BYTES, "log exceeds byte limit")
        with os.fdopen(os.dup(handle), "rb") as stream:
            total = 0
            while line := stream.readline(MAX_LINE_BYTES + 1):
                events += 1
                total += len(line)
                require(events <= MAX_EVENTS and total <= MAX_BYTES, "log exceeds recount limit")
                require(len(line) <= MAX_LINE_BYTES, f"event {events}: line exceeds byte limit")
                try:
                    event = strict_json(line.decode("utf-8"))
                except (ValueError, UnicodeError, EvidenceError, RecursionError):
                    raise EvidenceError(f"event {events}: malformed JSON") from None
                require(isinstance(event, dict), f"event {events}: JSON event must be an object")
                action, package, test = event.get("Action"), event.get("Package"), event.get("Test", "")
                require(isinstance(action, str) and action in
                        {"start", "run", "pause", "cont", "output", "bench", *TERMINALS},
                        f"event {events}: unsupported Go test action")
                require(isinstance(package, str) and package and isinstance(test, str),
                        f"event {events}: invalid package/test name")
                require(action != "output" or isinstance(event.get("Output"), str),
                        f"event {events}: output must be a string")
                allowed_scope = action != "start" if test else action in {"start", "output", *TERMINALS}
                require(allowed_scope,
                        f"event {events}: action has wrong package/test scope")
                actions[action] += 1
                if test:
                    test_actions[action] += 1
                key = (package, test)
                counts = (tests if test else packages).setdefault(key, Counter())
                start = "run" if test else "start"
                if action in TERMINALS and not counts[start]:
                    anomalies.append({"kind": "terminal_before_start", "package": package, "test": test})
                counts[action] += 1
                states = test_states if test else package_states
                state = states.get(key, "not_started")
                following = transition(state, action, start)
                if following is None:
                    kind = ("event_after_terminal" if state == "terminal" else
                            "event_before_start" if state == "not_started" else "invalid_transition")
                    anomaly(kind, package, test, action, state)
                if test:
                    pending = uncollected.setdefault(package, set())
                    if key not in test_states:
                        pending.add(test)
                    package_state = package_states.get((package, ""), "not_started")
                    if package_state != "running":
                        anomaly("test_outside_active_package", package, test, action, package_state)
                        following = None
                    if following == "terminal":
                        pending.discard(test)
                elif action in TERMINALS and uncollected.get(package):
                    anomaly("package_terminal_with_uncollected_tests", package, test, action, state)
                # A terminal closes the observed node even when its transition
                # is invalid. Later events cannot resurrect or repair that node.
                if action in TERMINALS:
                    states[key] = "terminal"
                else:
                    states[key] = following if following is not None else state
    require(events, "empty Go test log")

    def nodes(table, action):
        return [{"package": package, "test": test} for (package, test), counts in sorted(table.items())
                if counts[action]]

    for table, start in ((tests, "run"), (packages, "start")):
        for (package, test), counts in sorted(table.items()):
            terminal_count = sum(counts[action] for action in TERMINALS)
            for bad, kind in ((counts[start] == 0, "missing_start"),
                              (counts[start] > 1, "duplicate_start"),
                              (terminal_count == 0, "missing_terminal"),
                              (terminal_count > 1, "duplicate_terminal")):
                if bad:
                    anomalies.append({"kind": kind, "package": package, "test": test})
            if test and (package, "") not in packages:
                anomalies.append({"kind": "missing_package", "package": package, "test": test})
    runs = nodes(tests, "run")
    return {"path": str(path), "json_events": events, "actions": dict(sorted(actions.items())),
            "test_actions": dict(sorted(test_actions.items())), "test_nodes": len(runs),
            "top_level_tests": sum("/" not in item["test"] for item in runs),
            "inventory": {action: nodes(tests, action) for action in ("run", *TERMINALS)},
            "packages": {action: nodes(packages, action) for action in ("start", *TERMINALS)},
            "anomalies": anomalies}


class Parser(argparse.ArgumentParser):
    def error(self, message):
        raise EvidenceError("invalid arguments; use --help")


def main(argv=None):
    parser = Parser(description=__doc__, allow_abbrev=False)
    sub = parser.add_subparsers(dest="command", required=True, parser_class=Parser)
    for command in ("hash", "inventory", "go-test", "json", "tool"):
        child = sub.add_parser(command, allow_abbrev=False)
        child.add_argument("paths", nargs="+" if command == "hash" else None,
                           choices=tuple(TOOLS) if command == "tool" else None)
    try:
        args = parser.parse_args(argv)
        if args.command == "tool":
            report = fingerprint(TOOLS[args.paths])
        elif args.command == "hash":
            require(len(args.paths) <= MAX_FILES, "hash request exceeds file limit")
            report = {"files": [fingerprint(workspace_path(value)) for value in args.paths]}
        else:
            operation = {"inventory": inventory, "go-test": recount, "json": read_json}[args.command]
            report = operation(workspace_path(args.paths))
        ok = not (args.command == "go-test" and report["anomalies"])
        print(json.dumps({"ok": ok, **report}, indent=2, allow_nan=False))
        return 0 if ok else 1
    except (EvidenceError, OSError, ValueError, RecursionError) as error:
        message = str(error) if isinstance(error, EvidenceError) else type(error).__name__
        print(json.dumps({"ok": False, "error": message}, sort_keys=True))
        return 1


if __name__ == "__main__":
    sys.exit(main())
