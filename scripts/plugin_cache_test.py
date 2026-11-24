import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]


class PluginCacheTests(unittest.TestCase):
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
                    registry = {f"{path.parent.name}@punt-labs": [{"installPath": str(path)}]
                                for path in registered}
                    (cache.parents[1] / "installed_plugins.json").write_text(
                        json.dumps({"version": 2, "plugins": registry}), encoding="utf-8"
                    )
                elif other == "backup":
                    (cache / "ethos-dev/5.2.0.bak").mkdir(parents=True)
                backup = installed.with_name(installed.name + ".bak")

                def run(target):
                    result = subprocess.run(
                        ["make", "--no-print-directory", "-o", "install", target],
                        cwd=ROOT,
                        env={**os.environ, "HOME": home},
                        capture_output=True,
                        text=True,
                        check=False,
                    )
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
