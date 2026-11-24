"""Opt this checkout into development gates without redirecting Git hooks."""

import os
import re
import stat
import subprocess
import sys
from pathlib import Path
from tempfile import NamedTemporaryFile

MARKER = "ethos-dev-hooks"
BEGIN = b"# BEGIN Ethos development gate\n"
END = b"# END Ethos development gate\n"
LEGACY_SEAL_IDENT = "hooks/pre-commit.sh — Seal pending live audit lines".encode()
SHELL = re.compile(rb"#!\s*(?:\S*/)?(?:env\s+(?:-S\s+)?)?(?:ba|da|k|z|mk|a)?sh(?:[ \t].*)?")
PUSH_GATE = """    _ethos_dev_refs=$(mktemp) || exit 1
    if ! cat > "$_ethos_dev_refs" ||
       ! .githooks/pre-push "$@" < "$_ethos_dev_refs" ||
       ! exec < "$_ethos_dev_refs"; then
        rm -f "$_ethos_dev_refs"
        exit 1
    fi
    rm -f "$_ethos_dev_refs" || exit 1
    unset _ethos_dev_refs
"""


def git_path(*args):
    return Path(subprocess.check_output(["git", "rev-parse", *args], text=True).removesuffix("\n"))


def prefix(name):
    gate = "    .githooks/pre-commit || exit $?\n" if name == "pre-commit" else PUSH_GATE
    return BEGIN + (
        f'if [ -f .githooks/{name} ]; then\n'
        '    _ethos_dev_dir=$(git rev-parse --absolute-git-dir) || exit 1\n'
        f'    if [ -f "$_ethos_dev_dir/{MARKER}" ]; then\n'
        f'{gate}'
        '    fi\n'
        '    unset _ethos_dev_dir\n'
        'fi\n'
    ).encode() + END


def edit(path, gate):
    try:
        mode = path.lstat().st_mode
    except FileNotFoundError:
        return b"#!/bin/sh\n" + gate, 0o755
    if not stat.S_ISREG(mode) or not os.access(path, os.X_OK):
        raise ValueError(f"{path}: expected an executable regular hook; chain the gate manually")
    first, _, rest = path.read_bytes().partition(b"\n")
    if not SHELL.fullmatch(first):
        raise ValueError(f"{path}: expected a shell hook; chain the gate manually")
    if path.name == "pre-commit" and LEGACY_SEAL_IDENT in rest.partition(b"\n")[0]:
        raise ValueError(f"{path}: legacy standalone audit seal; run ethos enable before opting in")
    if rest.startswith(gate):
        return None
    if BEGIN in rest or END in rest:
        raise ValueError(f"{path}: existing development gate differs; review it manually")
    return first + b"\n" + gate + rest, stat.S_IMODE(mode)


def write_hook(path, body, mode):
    with NamedTemporaryFile(dir=path.parent, delete=False) as tmp:
        tmp_path = Path(tmp.name)
        try:
            tmp.write(body)
            tmp.close()
            tmp_path.chmod(mode)
            tmp_path.replace(path)
        finally:
            tmp_path.unlink(missing_ok=True)


def main():
    try:
        if git_path("--show-toplevel").resolve() != Path.cwd():
            raise ValueError("run make hooks at the checkout root")
        common = git_path("--git-common-dir").resolve()
        hooks = common / "hooks"
        if hooks.is_symlink() or git_path("--git-path", "hooks").resolve() != hooks:
            raise ValueError("Git is not using the repository-local hooks directory; chain the gates manually")
        marker = git_path("--absolute-git-dir") / MARKER
        edits = [(hooks / name, edit(hooks / name, prefix(name))) for name in ("pre-commit", "pre-push")]
        hooks.mkdir(exist_ok=True)
        for path, change in edits:
            if change is not None:
                write_hook(path, *change)
        try:
            with marker.open("xb"):
                pass
        except FileExistsError:
            if not marker.is_file() or marker.is_symlink():
                raise
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(exc, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
