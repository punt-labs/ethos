"""Select Go tests by their top-level test kind."""

import os
import re
import shlex
import sys
from pathlib import Path


def main() -> None:
    kind, *go_args = sys.argv[1:]
    script = Path(__file__).resolve()
    if kind == "exec":
        run_package(script.parent.parent, *go_args)
        return
    runner = shlex.join(["python3", str(script), "exec", kind])
    os.execvp("go", ["go", "test", f"-exec={runner}", *go_args])


def run_package(root: Path, kind: str, *test_args: str) -> None:
    registry = root / "tests" / "go-subprocess-tests.txt"
    entries = [
        line
        for line in registry.read_text(encoding="utf-8").splitlines()
        if line and not line.startswith("#")
    ]
    if not entries or len(entries) != len(set(entries)) or not all(
        re.fullmatch(r"[\w./-]+ Test[A-Za-z0-9_]+", entry) for entry in entries
    ):
        sys.exit(f"{registry}: expected unique package and top-level Go test pairs")
    package = Path.cwd().relative_to(root).as_posix()
    tests = [name for path, name in map(str.split, entries) if path == package]
    flag = {"subprocess": "-test.run", "unit": "-test.skip"}[kind]
    pattern = "^(" + "|".join(tests) + ")$"
    os.execv(test_args[0], [*test_args, flag, pattern])


if __name__ == "__main__":
    main()
