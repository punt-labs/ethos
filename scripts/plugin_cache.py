import json
from pathlib import Path
import subprocess
import sys


def dev_registration(registry, checkout):
    try:
        state = json.loads(registry.read_text(encoding="utf-8"))
        records = state["plugins"].get("ethos-dev@punt-labs", [])
        if not isinstance(records, list) or not all(isinstance(row, dict) for row in records):
            raise ValueError("expected a list of install records")
    except FileNotFoundError:
        return {}
    except (AttributeError, KeyError, TypeError, ValueError) as exc:
        raise ValueError(f"invalid Claude plugin registry {registry}: {exc}") from exc
    for record in records:
        scope = record.get("scope")
        if scope == "user":
            return record
        if scope in ("project", "local"):
            project = record.get("projectPath")
            if isinstance(project, str) and Path(project).is_absolute() and Path(project).resolve() == checkout:
                return record
    return None


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


def select_version(state, checkout, override):
    caches = plugin_caches(state, override)
    if not override:
        registration = dev_registration(state / "installed_plugins.json", checkout)
        if registration is None:
            caches = caches[1:]
        else:
            install_path = registration.get("installPath")
            if install_path not in (None, ""):
                if not isinstance(install_path, str) or Path(install_path) not in cache_versions(caches[0]):
                    raise ValueError(f"registered ethos-dev installPath is not a cached version: {install_path!r}")
                return Path(install_path)
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
