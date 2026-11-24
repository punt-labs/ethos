import json
from pathlib import Path
import subprocess
import sys


def resolves_to(path, target):
    try:
        return path.resolve() == target
    except (OSError, RuntimeError, ValueError):
        return False


def dev_registrations(registry, checkout):
    try:
        state = json.loads(registry.read_text(encoding="utf-8"))
        records = state["plugins"].get("ethos-dev@punt-labs", [])
        if not isinstance(records, list) or not all(isinstance(row, dict) for row in records):
            raise ValueError("expected a list of install records")
    except FileNotFoundError:
        return None
    except (AttributeError, KeyError, TypeError, ValueError) as exc:
        raise ValueError(f"invalid Claude plugin registry {registry}: {exc}") from exc
    applicable = []
    for record in records:
        scope = record.get("scope")
        if scope == "user":
            applicable.append(record)
        elif scope in ("project", "local"):
            project = record.get("projectPath")
            if isinstance(project, str) and Path(project).is_absolute() and resolves_to(Path(project), checkout):
                applicable.append(record)
    return applicable


def cache_versions(cache):
    if not cache.is_dir():
        return []
    return [
        entry for entry in cache.iterdir()
        if not entry.name.endswith(".bak") and (entry.is_dir() or entry.is_symlink())
    ]


def plugin_caches(state, override):
    if override:
        return [Path(override)]
    root = state / "cache/punt-labs"
    return [root / "ethos-dev", root / "ethos"]


def missing_cache(caches):
    return ValueError(f"no plugin cache found at {', '.join(str(cache) for cache in caches)}")


def registered_version(cache, install_paths):
    versions = {entry.name: entry for entry in cache_versions(cache) if entry.is_dir()}
    if versions:
        root = cache.resolve()
        for install_path in install_paths:
            if not isinstance(install_path, str):
                continue
            path = Path(install_path)
            if path.is_absolute() and path.name in versions and resolves_to(path.parent, root):
                return versions[path.name]
    raise ValueError(f"no registered ethos-dev installPath points to a cached version under {cache}")


def select_version(state, checkout, override):
    caches = plugin_caches(state, override)
    if not override:
        registrations = dev_registrations(state / "installed_plugins.json", checkout)
        if registrations == []:
            caches = caches[1:]
        elif registrations:
            paths = [record.get("installPath") for record in registrations]
            paths = [path for path in paths if path not in (None, "")]
            if paths:
                return registered_version(caches[0], paths)
    for cache in caches:
        versions = cache_versions(cache)
        if versions:
            names = "\n".join(entry.name for entry in versions)
            latest = subprocess.check_output(["sort", "-V"], input=names, text=True).splitlines()[-1]
            return cache / latest
    raise missing_cache(caches)


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
    backup = backup_path(link)
    if not backup.is_dir():
        raise ValueError(
            f"no backup at {backup}; leaving the symlink in place — reinstall with "
            f"'claude plugin install {link.parent.name}@punt-labs' to repopulate the cache"
        )
    link.unlink()
    backup.rename(link)
    print(f"restored {link}")


def restore_caches(caches, checkout):
    versions = [entry for cache in caches for entry in cache_versions(cache)]
    if not versions:
        raise missing_cache(caches)
    targets = {checkout, checkout / "plugin"}
    linked = [entry for entry in versions if entry.is_symlink() and entry.readlink() in targets]
    if not linked:
        print("not in dev mode")
        return
    errors = []
    for link in linked:
        try:
            restore_cache(link)
        except (OSError, ValueError) as exc:
            errors.append(str(exc))
    if errors:
        raise ValueError("\n".join(errors))


def main(action, home, checkout, override):
    try:
        repo = Path(checkout).resolve()
        state = Path(home) / ".claude/plugins"
        if action == "dev":
            link_cache(select_version(state, repo, override), repo / "plugin")
        else:
            restore_caches(plugin_caches(state, override), repo)
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(*sys.argv[1:]))
