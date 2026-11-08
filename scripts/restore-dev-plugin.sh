#!/usr/bin/env bash
set -euo pipefail

# Restore dev plugin state on main after a release tag.
#
# Usage:
#   scripts/restore-dev-plugin.sh [release-prep-commit]
#
# CONTRACT: stage the manifest and dev commands only; do not commit. The release
# engine re-stamps the version, then makes one hook-checked restore commit.
# Without an argument, use the most recent first-parent snapshot with a dev
# plugin name. Release PRs are squash-merged, so their prep subject is not kept.
# An explicit release-prep commit still selects its first parent for compatibility.

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# Do not refresh/write the index during a preflight that may refuse below.
# Unrelated untracked/ignored runtime files are allowed; selected paths are
# checked separately once the complete restoration set is known.
status="$(git --no-optional-locks -C "$REPO_ROOT" status --porcelain -uno)"
if [[ -n "$status" ]]; then
  echo "Error: repository has uncommitted changes. Commit or stash before running $(basename "$0")." >&2
  exit 1
fi

if [[ $# -gt 1 ]]; then
  echo "Usage: $(basename "$0") [release-prep-commit]" >&2
  exit 1
fi

python3 - "$REPO_ROOT" "${1:-}" <<'PY'
import json
import os
import stat
import subprocess
import sys

root, prep = sys.argv[1:]
manifest = "plugin/.claude-plugin/plugin.json"


def git(*args):
    return subprocess.check_output(["git", "-C", root, *args])


if prep:
    commit = git("rev-parse", "--verify", "--end-of-options", prep + "^{commit}").decode().strip()
    commits = [git("rev-parse", "--verify", commit + "^1").decode().strip()]
else:
    # Do not path-limit this walk: a command-only commit is a newer dev snapshot
    # even if it did not modify the manifest.
    commits = git("rev-list", "--first-parent", "HEAD").decode().splitlines()

source = None
for commit in commits:
    if not git("ls-tree", "--name-only", commit, "--", manifest):
        continue
    data = json.loads(git("show", commit + ":" + manifest))
    name = data["name"]
    if not isinstance(name, str) or not name or name == "-dev":
        sys.exit(f"Error: invalid plugin name in {commit}:{manifest}")
    if name.endswith("-dev"):
        source = commit
        break

if source is None:
    sys.exit("Error: could not find a dev plugin snapshot in first-parent history; "
             "fetch the development history or pass a release-prep commit")

# Restore only dev commands, not production commands that may have changed
# since release. Include current tracked dev paths so the snapshot is exact.
historical = git("ls-tree", "-rz", "--name-only", source, "--", "plugin/commands/")
current = git("ls-files", "-z", "--", "plugin/commands/")
dev_paths = sorted({p for p in (historical + current).split(b"\0") if p.endswith(b"-dev.md")})
paths = [os.fsencode(manifest), *dev_paths]
tracked = set(git("ls-files", "-z", "--", manifest, "plugin/commands/").split(b"\0"))

# git restore --worktree can overwrite untracked AND ignored data. Inspect every
# selected path before the single restore, not one path followed by one restore.
# Walk ancestors with lstat so symlinks (including dangling ones) are obstructions,
# never routes into another directory. A missing ancestor means a missing leaf.
# Keep Git -z names as bytes, including whitespace and non-UTF-8 names.
# Like the tracked-dirty guard, this preflight requires a quiescent checkout;
# it does not serialize concurrent worktree writers with Git's later restore.
for path in paths:
    components = path.split(b"/")
    absolute = os.fsencode(root)
    for i, component in enumerate(components):
        absolute = os.path.join(absolute, component)
        try:
            mode = os.lstat(absolute).st_mode
        except FileNotFoundError:
            break
        if i < len(components) - 1:
            if not stat.S_ISDIR(mode):
                sys.exit(f"Error: restore collision at ancestor {b'/'.join(components[:i+1])!r} "
                         f"of {path!r}; preserve or move it before retrying")
        elif path not in tracked or not (stat.S_ISREG(mode) or stat.S_ISLNK(mode)):
            sys.exit(f"Error: restore collision at {path!r}; "
                     "preserve or move it before retrying")

print(f"Staging dev state from {source[:12]}", flush=True)
subprocess.run(["git", "--literal-pathspecs", "-C", root, "restore", "--source=" + source,
                "--staged", "--worktree", "--", *paths], check=True)
PY
