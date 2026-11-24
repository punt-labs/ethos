import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]


def run_make(home, target, *options):
    env = os.environ.copy()
    for name in ("MAKEFLAGS", "MAKEOVERRIDES", "MFLAGS", "GNUMAKEFLAGS", "PLUGIN_CACHE"):
        env.pop(name, None)
    return subprocess.run(
        ["make", "--no-print-directory", "-o", "install", *options,
         f"HOME={home}", f"GOBIN={Path(home) / 'bin'}", target],
        cwd=ROOT,
        env={**env, "HOME": str(home)},
        capture_output=True,
        text=True,
        check=False,
    )


class PluginCacheTests(unittest.TestCase):
    def test_bad_registry_is_only_reported_by_cache_targets(self):
        with tempfile.TemporaryDirectory() as home:
            cache = Path(home) / ".claude/plugins/cache/punt-labs"
            prod = cache / "ethos/5.2.1"
            prod.mkdir(parents=True)
            (cache / "ethos-dev/5.2.1").mkdir(parents=True)
            (cache.parents[1] / "installed_plugins.json").write_text("{", encoding="utf-8")

            for target in ("help", "build"):
                with self.subTest(target=target):
                    result = run_make(home, target, "-n")
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertNotIn("Traceback", result.stderr)
                    self.assertNotIn("invalid Claude plugin registry", result.stderr)

            result = run_make(home, "dev")
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("invalid Claude plugin registry", result.stderr)
            self.assertNotIn("Traceback", result.stderr)
            self.assertFalse(prod.is_symlink())

    def test_inherited_make_overrides_cannot_redirect_cache_access(self):
        with tempfile.TemporaryDirectory() as home, tempfile.TemporaryDirectory() as outer:
            version = Path(home) / ".claude/plugins/cache/punt-labs/ethos/5.2.1"
            version.mkdir(parents=True)
            outer_cache = Path(outer) / ".claude/plugins/cache/punt-labs/ethos"
            outer_cache.mkdir(parents=True)
            with patch.dict(os.environ, {
                "MAKEFLAGS": f" -- HOME={outer} PLUGIN_CACHE={outer_cache}",
            }):
                result = run_make(home, "dev")
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertTrue(version.is_symlink())
            self.assertEqual(list(outer_cache.iterdir()), [])

    def test_dev_registration_must_apply_to_the_checkout(self):
        for scope, correct_project, expected in (
            ("project", False, "ethos"),
            ("local", False, "ethos"),
            ("local", True, "ethos-dev"),
            ("project", True, "ethos-dev"),
            ("user", False, "ethos-dev"),
        ):
            with self.subTest(scope=scope, correct_project=correct_project), tempfile.TemporaryDirectory() as home:
                cache = Path(home) / ".claude/plugins/cache/punt-labs"
                (cache / "ethos/5.2.1").mkdir(parents=True)
                dev = cache / "ethos-dev/5.2.1"
                dev.mkdir(parents=True)
                record = {"scope": scope, "installPath": str(dev)}
                if scope != "user":
                    record["projectPath"] = str(ROOT if correct_project else Path(home) / "other-project")
                (cache.parents[1] / "installed_plugins.json").write_text(
                    json.dumps({"version": 2, "plugins": {"ethos-dev@punt-labs": [record]}}),
                    encoding="utf-8",
                )
                result = run_make(home, "dev")
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertTrue((cache / expected / "5.2.1").is_symlink())

    def test_dev_uses_registered_install_path_before_latest_cache_version(self):
        for has_path in (True, False):
            with self.subTest(has_path=has_path), tempfile.TemporaryDirectory() as home:
                cache = Path(home) / ".claude/plugins/cache/punt-labs/ethos-dev"
                older = cache / "5.2.0"
                newer = cache / "5.2.1"
                older.mkdir(parents=True)
                newer.mkdir()
                record = {"scope": "user"}
                if has_path:
                    record["installPath"] = str(older)
                (cache.parents[2] / "installed_plugins.json").write_text(
                    json.dumps({"plugins": {"ethos-dev@punt-labs": [record]}}),
                    encoding="utf-8",
                )

                expected, untouched = (older, newer) if has_path else (newer, older)
                result = run_make(home, "dev")
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertTrue(expected.is_symlink(), result.stdout)
                self.assertEqual(expected.resolve(), ROOT / "plugin")
                self.assertFalse(untouched.is_symlink())

    def test_undev_restores_links_after_cache_selection_changes(self):
        for link_both in (False, True):
            with self.subTest(link_both=link_both), tempfile.TemporaryDirectory() as home:
                cache = Path(home) / ".claude/plugins/cache/punt-labs"
                prod = cache / "ethos/5.2.1"
                prod.mkdir(parents=True)
                (prod / "original").touch()
                result = run_make(home, "dev")
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertTrue(prod.is_symlink())

                (cache / "ethos/5.2.2").mkdir()
                dev = cache / "ethos-dev/5.2.1"
                dev.mkdir(parents=True)
                (dev / "original").touch()
                registry = cache.parents[1] / "installed_plugins.json"
                registry.write_text(
                    json.dumps({"plugins": {"ethos-dev@punt-labs": [{"scope": "user"}]}}),
                    encoding="utf-8",
                )
                if link_both:
                    result = run_make(home, "dev")
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertTrue(dev.is_symlink())
                    registry.write_text("{", encoding="utf-8")

                foreign_link = cache / "ethos/5.2.3"
                foreign_link.symlink_to(Path(home) / "another-checkout/plugin")
                (cache / "ethos/5.2.3.bak").mkdir()
                result = run_make(home, "undev")
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertFalse(prod.is_symlink(), result.stdout)
                self.assertFalse(dev.is_symlink(), result.stdout)
                self.assertTrue((prod / "original").is_file())
                self.assertTrue((dev / "original").is_file())
                self.assertTrue(foreign_link.is_symlink())

    def test_dev_and_undev_restore_either_cache_layout(self):
        for name, other in (
            ("ethos", "backup"),
            ("ethos-dev", None),
            ("ethos-dev", "installed"),
            ("ethos", "uninstalled"),
        ):
            with self.subTest(name=name, other=other), tempfile.TemporaryDirectory() as home:
                cache = Path(home) / ".claude/plugins/cache/punt-labs"
                installed = cache / name / "5.2.1"
                installed.mkdir(parents=True)
                marker = installed / "installed"
                marker.touch()
                other_name = "ethos-dev" if name == "ethos" else "ethos"
                other_path = cache / other_name / "5.2.1"
                if other in ("installed", "uninstalled"):
                    other_path.mkdir(parents=True)
                    registered = [installed, other_path] if other == "installed" else [installed]
                    registry = {f"{path.parent.name}@punt-labs": [{"scope": "user", "installPath": str(path)}]
                                for path in registered}
                    (cache.parents[1] / "installed_plugins.json").write_text(
                        json.dumps({"version": 2, "plugins": registry}), encoding="utf-8"
                    )
                elif other == "backup":
                    (cache / "ethos-dev/5.2.0.bak").mkdir(parents=True)
                backup = installed.with_name(installed.name + ".bak")

                def run(target):
                    result = run_make(home, target)
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

                run("dev")
                self.assertTrue(installed.is_symlink())
                self.assertEqual(installed.resolve(), ROOT / "plugin")
                self.assertTrue((backup / marker.name).is_file())
                run("undev")
                self.assertFalse(installed.is_symlink())
                self.assertTrue(marker.is_file())
                self.assertFalse(backup.exists())
                if other in ("installed", "uninstalled"):
                    self.assertFalse(other_path.is_symlink())
                    self.assertFalse(other_path.with_name(other_path.name + ".bak").exists())


if __name__ == "__main__":
    unittest.main()
