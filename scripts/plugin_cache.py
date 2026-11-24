import json
from pathlib import Path
import subprocess
import sys


def dev_is_registered(registry, checkout):
    try:
        state = json.loads(registry.read_text(encoding="utf-8"))
        records = state["plugins"].get("ethos-dev@punt-labs", [])
        if not isinstance(records, list) or not all(isinstance(row, dict) for row in records):
            raise ValueError("expected a list of install records")
    except FileNotFoundError:
        return True
    except (AttributeError, KeyError, TypeError, ValueError) as exc:
        raise ValueError(f"invalid Claude plugin registry {registry}: {exc}") from exc
    for record in records:
        scope = record.get("scope")
        if scope == "user":
            return True
        if scope == "project":
            project = record.get("projectPath")
            if isinstance(project, str) and Path(project).is_absolute() and Path(project).resolve() == checkout:
                return True
    return False


def cache_versions(cache):
    if not cache.is_dir():
        return []
    return [
        entry for entry in cache.iterdir()
        if not entry.name.endswith(".bak") and (entry.is_dir() or entry.is_symlink())
    ]


def select_version(state, checkout, override):
    root = state / "cache/punt-labs"
    if override:
        caches = [Path(override)]
    else:
        caches = [root / "ethos"]
        if dev_is_registered(state / "installed_plugins.json", checkout):
            caches.insert(0, root / "ethos-dev")
    for cache in caches:
        versions = cache_versions(cache)
        if versions:
            names = "\n".join(entry.name for entry in versions)
            latest = subprocess.check_output(["sort", "-V"], input=names, text=True).splitlines()[-1]
            return cache / latest
    raise ValueError(f"no plugin cache found at {', '.join(str(cache) for cache in caches)}")


def backup_path(link):
    return link.with_name(link.name + ".bak")


def link_cache(link, target):
    backup = backup_path(link)
    if link.is_symlink():
        current = link.readlink()
        if current == target:
            print(f"plugin cache already symlinked → {target}")
            return
        print(f"retargeting stale symlink ({current} → {target})")
        link.unlink()
    else:
        if backup.exists() or backup.is_symlink():
            raise ValueError(f"{backup} already exists; refusing to overwrite")
        link.rename(backup)
        print(f"original cached at {backup}")
    link.symlink_to(target, target_is_directory=True)
    print(f"symlinked {link} → {target}")


def restore_cache(link):
    if not link.is_symlink():
        print("not in dev mode")
        return
    backup = backup_path(link)
    if not backup.is_dir():
        raise ValueError(
            f"no backup at {backup}; leaving the symlink in place — reinstall with "
            f"'claude plugin install {link.parent.name}@punt-labs' to repopulate the cache"
        )
    link.unlink()
    backup.rename(link)
    print(f"restored {link}")


def main(action, home, checkout, override):
    try:
        repo = Path(checkout).resolve()
        link = select_version(Path(home) / ".claude/plugins", repo, override)
        if action == "dev":
            link_cache(link, repo / "plugin")
        else:
            restore_cache(link)
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(*sys.argv[1:]))
