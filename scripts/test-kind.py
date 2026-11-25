"""Select Go tests by their top-level test kind."""

import os
import re
import subprocess
import sys
from pathlib import Path


def main() -> None:
    kind, *go_args = sys.argv[1:]
    script = Path(__file__).resolve()
    if kind == "exec":
        run_package(script.parent.parent, *go_args)
        return
    os.environ["ETHOS_TEST_KIND_SCRIPT"] = str(script)
    runner = (
        "python3 -c 'import os, runpy; "
        'runpy.run_path(os.environ.pop("ETHOS_TEST_KIND_SCRIPT"), run_name="__main__")'
        f"' exec {kind}"
    )
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
    args = list(test_args)
    if kind == "subprocess" and not test_flag(args, "run"):
        set_test_flag(args, "run", exact_names(tests) or "^$")
    else:
        if kind == "subprocess" and tests:
            names = subprocess.check_output([args[0], "-test.list=."], text=True)
            selected = set(tests)
            tests = [
                name
                for name in names.splitlines()
                if re.fullmatch(r"(Test|Example|Fuzz)\w*", name) and name not in selected
            ]
            pattern = exact_names(tests)
        elif kind == "subprocess":
            pattern = "."
        else:
            pattern = exact_names(tests)
        if pattern:
            skip = test_flag(args, "skip")
            if skip:
                pattern += "|" + skip
            set_test_flag(args, "skip", pattern)
    os.execv(args[0], args)


def test_flag(args: list[str], name: str) -> str:
    i = test_flag_index(args, name)
    return args[i].partition("=")[2] if i else ""


def set_test_flag(args: list[str], name: str, value: str) -> None:
    flag = f"-test.{name}={value}"
    i = test_flag_index(args, name)
    if i:
        args[i] = flag
    else:
        args.insert(1, flag)


def test_flag_index(args: list[str], name: str) -> int:
    prefix = f"-test.{name}="
    return next((i for i in range(len(args) - 1, 0, -1) if args[i].startswith(prefix)), 0)


def exact_names(names: list[str]) -> str:
    return "^(" + "|".join(names) + ")$" if names else ""


if __name__ == "__main__":
    main()
