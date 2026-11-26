"""Compare two Ethos refs on three paired Homebrew Tap snapshot replays.

Run on Linux with Go, Git and Homebrew on PATH. The Tap ref must have
the formula-style-handoff policy and any provisioned runtime it needs.
Use --tap-ref to pin the fixture and --out to retain evidence at a fresh path.
For a policy with runtime bundles, pass --runtime-archive for each installed
<sha256>.tar.gz archive to copy and verify in every trial home.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import statistics
import subprocess
import tempfile
import time


def commit(repo, ref):
    return subprocess.check_output(
        ["git", "-C", repo, "rev-parse", "--verify", "--end-of-options", f"{ref}^{{commit}}"],
        text=True,
    ).strip()


def build(repo, out, label, revision):
    tree = out / f"{label}-src"
    binary = out / f"ethos-{label}"
    subprocess.run(["git", "-C", repo, "worktree", "add", "--detach", tree, revision], check=True)
    try:
        subprocess.run(["go", "build", "-o", binary, "./cmd/ethos"], cwd=tree, check=True)
    finally:
        subprocess.run(["git", "-C", repo, "worktree", "remove", tree], check=True)
    return binary


def seed_runtimes(home, archives):
    if not archives:
        return
    store = home / ".punt-labs/ethos/runtimes"
    store.mkdir(parents=True, mode=0o700)
    for archive in archives:
        match = re.fullmatch(r"([0-9a-f]{64})\.tar\.gz", archive.name)
        if not match:
            raise ValueError(f"Expected an installed <sha256>.tar.gz runtime archive: {archive}")
        digest = hashlib.sha256()
        fd = os.open(archive, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(fd, "rb") as src:
            if not stat.S_ISREG(os.fstat(src.fileno()).st_mode):
                raise ValueError(f"Runtime archive is not a regular file: {archive}")
            target = store / archive.name
            with target.open("xb") as dst:
                for chunk in iter(lambda: src.read(1 << 20), b""):
                    digest.update(chunk)
                    dst.write(chunk)
        if digest.hexdigest() != match[1]:
            target.unlink()
            raise ValueError(f"Runtime archive digest does not match its SHA-256 filename: {archive}")


def replay(out, binary, tap, tap_revision, label, trial, archives):
    case = out / f"{label}-{trial}"
    checkout, home = case / "tap", case / "home"
    home.mkdir(parents=True)
    seed_runtimes(home, archives)
    env = {
        "HOME": str(home),
        "PATH": os.environ["PATH"],
        "GIT_CONFIG_GLOBAL": os.devnull,
        "GIT_CONFIG_SYSTEM": os.devnull,
    }
    subprocess.run(
        ["git", "clone", "-q", "--shared", "--no-checkout", tap, checkout], env=env, check=True,
    )
    subprocess.run(
        ["git", "-C", checkout, "switch", "-q", "--detach", tap_revision], env=env, check=True,
    )
    stop = dict(session_id="performance", cwd=str(checkout), agent_id="worker", agent_type="implementer")
    write = dict(stop, tool_name="Write", tool_input={"file_path": str(checkout / "Formula/ethos.rb")})

    def hook(name, payload, evidence):
        start = time.monotonic()
        result = subprocess.run(
            [binary, "hook", name], cwd=checkout, env=env, input=json.dumps(payload),
            text=True, capture_output=True, timeout=90,
        )
        wall = time.monotonic() - start
        (case / f"{evidence}.stdout").write_text(result.stdout, encoding="utf-8")
        (case / f"{evidence}.stderr").write_text(result.stderr, encoding="utf-8")
        result.check_returncode()
        return result.stdout, wall

    hook("audit-log", write, "audit-log")
    audit = checkout / ".punt-labs/local/ethos/sessions/performance.audit.jsonl"
    if not any(
        row.get("tool") == "Write" and row.get("agent_id") == stop["agent_id"]
        for row in map(json.loads, audit.read_text(encoding="utf-8").splitlines())
    ):
        raise RuntimeError(f"Missing attributed Write: {audit}")

    diag = home / ".punt-labs/ethos/logs/ethos.log"
    walls = []
    with diag.open(encoding="utf-8") as stream:
        stream.seek(0, os.SEEK_END)
        for phase, expected in (("miss", ["miss", "launched", "completed"]), ("hit", ["reused"])):
            output, wall = hook("subagent-stop", stop, phase)
            events = re.findall(r"check=formula-style-handoff outcome=(\w+)", stream.read())
            print(f"{label} {trial} {phase} wall={wall:.3f}s {' -> '.join(events)}", flush=True)
            if events != expected or output.strip():
                raise RuntimeError(f"Expected {expected}, got {events} and {output!r}; see {case}")
            walls.append(wall)
    return walls


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("baseline", help="Baseline Ethos Git ref")
    parser.add_argument("candidate", help="Candidate Ethos Git ref")
    parser.add_argument("homebrew_tap", type=Path, help="Local Homebrew Tap Git checkout")
    parser.add_argument("--tap-ref", default="HEAD", help="Homebrew Tap Git ref (default: HEAD)")
    parser.add_argument("--out", type=Path, help="Fresh evidence directory (default: under .tmp/opencode)")
    parser.add_argument(
        "--runtime-archive", action="append", type=Path, default=[],
        help="Copy a verified installed <sha256>.tar.gz to each fresh trial home (repeatable)",
    )
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[1]
    tap = args.homebrew_tap.resolve()
    tap_revision = commit(tap, args.tap_ref)
    revisions = {label: commit(repo, getattr(args, label)) for label in ("baseline", "candidate")}
    if args.out is None:
        evidence = repo / ".tmp/opencode"
        evidence.mkdir(parents=True, exist_ok=True)
        out = Path(tempfile.mkdtemp(prefix="bench-handoff-brew-", dir=evidence))
    else:
        out = args.out.resolve()
        out.mkdir(parents=True)
    print(f"Evidence: {out}\nHomebrew Tap: {tap_revision}", flush=True)
    binaries = {}
    for label, revision in revisions.items():
        print(f"{label}: {revision}", flush=True)
        binaries[label] = build(repo, out, label, revision)
    samples = {label: [] for label in binaries}
    for trial in range(1, 4):
        for label, binary in binaries.items():
            samples[label].append(replay(out, binary, tap, tap_revision, label, trial, args.runtime_archive))
    for label, pairs in samples.items():
        misses, hits = zip(*pairs)
        print(f"{label}: median miss {statistics.median(misses):.3f}s; "
              f"median confirmed hit {statistics.median(hits):.3f}s")


if __name__ == "__main__":
    main()
