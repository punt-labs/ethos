import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]


class PluginCacheTests(unittest.TestCase):
    def test_dev_and_undev_restore_either_cache_layout(self):
        for name, both in (("ethos", False), ("ethos-dev", False), ("ethos-dev", True)):
            with self.subTest(name=name, both=both), tempfile.TemporaryDirectory() as home:
                cache = Path(home) / ".claude/plugins/cache/punt-labs"
                installed = cache / name / "5.2.1"
                installed.mkdir(parents=True)
                marker = installed / "installed"
                marker.touch()
                if both:
                    (cache / "ethos/5.2.1").mkdir(parents=True)
                elif name == "ethos":
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
                if both:
                    self.assertFalse((cache / "ethos/5.2.1").is_symlink())
                    self.assertFalse((cache / "ethos/5.2.1.bak").exists())


if __name__ == "__main__":
    unittest.main()
