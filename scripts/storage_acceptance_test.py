"""Tests of the independent acceptance oracle, not product acceptance substitutes."""

import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

import storage_acceptance as gate


class BoundaryTests(unittest.TestCase):
    def setUp(self):
        scratch = gate.ROOT / ".tmp/opencode/storage-acceptance/unit"
        scratch.mkdir(parents=True, exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(dir=scratch)
        self.addCleanup(self.temp.cleanup)
        self.repo = Path(self.temp.name)

    def test_allowlist_does_not_legalize_runtime_under_record_tree(self):
        mid = "m-2026-11-04-001"
        for name in (
            f"missions/{mid}/contract.yaml", f"missions/{mid}/corrections.yaml",
            f"missions/{mid}/delegations/d-001/record.yaml", "CLAUDE.md", ".vendored-manifest",
        ):
            self.assertTrue(gate.blame_path_allowed(name), name)
        for name in (
            f"missions/{mid}/reflections.yaml", f"missions/{mid}/reflections.local.yaml",
            f"missions/{mid}/delegations/d-001/prompt.md", f"missions/{mid}/scratch/result.yaml",
            f"missions/{mid}/contract.yaml.lock", f"missions/{mid}/.ethos-tmp-contract.yaml",
            f"missions/{mid}/log-session-0001-0002.jsonl", "identities/test.ext/ns.local.yaml",
            "sessions/2026-11-04-test/audit-0001-0002.jsonl",
        ):
            self.assertFalse(gate.blame_path_allowed(name), name)

    def test_ignored_trace_is_still_rejected(self):
        gate.write(self.repo / ".gitignore", ".punt-labs/ethos/\n")
        gate.write(self.repo / gate.RECORD / "missions/m-2026-11-04-001/reflections.yaml", "private")
        with self.assertRaisesRegex(gate.Failure, "forbidden source-tree"):
            gate.check_boundary(self.repo)

    def test_empty_scratch_and_local_source_symlink_rejected(self):
        (self.repo / gate.RECORD / "scratch").mkdir(parents=True)
        with self.assertRaisesRegex(gate.Failure, "empty record directory"):
            gate.check_boundary(self.repo)
        (self.repo / gate.RECORD / "scratch").rmdir()
        target = gate.write(self.repo / gate.LOCAL / "private", "private")
        (self.repo / gate.RECORD / "CLAUDE.md").symlink_to(target)
        with self.assertRaisesRegex(gate.Failure, "non-regular record"):
            gate.check_boundary(self.repo)

    def test_storage_root_symlinks_and_files_rejected(self):
        for relative in (Path(".punt-labs"), gate.RECORD, Path(".punt-labs/local"), gate.LOCAL):
            with self.subTest(relative=relative):
                path = self.repo / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.symlink_to(self.repo / "missing-target")
                try:
                    with self.assertRaisesRegex(gate.Failure, "symlink storage root"):
                        gate.check_boundary(self.repo)
                finally:
                    path.unlink()
                gate.write(path, "not a directory")
                try:
                    with self.assertRaisesRegex(gate.Failure, "non-directory storage root"):
                        gate.check_boundary(self.repo)
                finally:
                    path.unlink()

    def test_inventory_does_not_swallow_walk_errors(self):
        def failed_walk(root, **kwargs):
            kwargs["onerror"](PermissionError("unreadable fixture directory"))
            return iter(())

        with patch.object(gate.os, "walk", failed_walk):
            with self.assertRaisesRegex(PermissionError, "unreadable fixture"):
                gate.inventory(self.repo)

    def test_local_runtime_is_allowed_but_not_elsewhere(self):
        gate.write(self.repo / gate.LOCAL / "scratch/x.lock", "local")
        gate.check_boundary(self.repo)
        gate.write(self.repo / "host/.ethos-tmp-orphan", "outside")
        with self.assertRaisesRegex(gate.Failure, "outside canonical local"):
            gate.check_boundary(self.repo)

    def test_full_mode_does_not_allow_live_raw_or_scratch(self):
        record = self.repo / gate.RECORD
        gate.write(record / "sessions/2026-11-04-test/audit-0001-0002.jsonl", "published")
        gate.check_boundary(self.repo, full=True)
        gate.write(record / "sessions/test.audit.jsonl", "live")
        with self.assertRaisesRegex(gate.Failure, "forbidden source-tree"):
            gate.check_boundary(self.repo, full=True)

    def test_inventory_records_ignored_files_hash_modes_and_symlinks(self):
        path = gate.write(self.repo / "hidden/.ignored", "contents")
        path.chmod(0o600)
        (self.repo / "link").symlink_to("hidden/.ignored")
        data = gate.inventory(self.repo)
        self.assertEqual(data["hidden/.ignored"]["sha256"], gate.digest(b"contents"))
        self.assertEqual(data["hidden/.ignored"]["mode"], 0o600)
        # A symlink's own mode is the platform's: Linux reports 0o777, macOS the
        # umask-derived mode. The inventory must record what lstat says.
        link_mode = gate.stat.S_IMODE((self.repo / "link").lstat().st_mode)
        self.assertEqual(data["link"], {"kind": "symlink", "mode": link_mode, "target": "hidden/.ignored"})

    def test_source_section_cannot_borrow_next_sources_association(self):
        text = ("sources:\n    - path: ambiguous.txt\n      candidates: []\n      unknown: ambiguous\n"
                "    - path: known.txt\n      mission: m-example\n      delegation: d-example\n")
        self.assertNotIn("mission:", gate.source_section(text, "ambiguous.txt"))
        self.assertIn("mission: m-example", gate.source_section(text, "known.txt"))
        for malformed in ("", text + text):
            with self.assertRaisesRegex(gate.Failure, "exactly one source"):
                gate.source_section(malformed, "known.txt")

    def test_missing_executable_is_failure_not_skip(self):
        runner = gate.Runner(self.repo / "commands")
        with self.assertRaisesRegex(gate.Failure, "did not complete"):
            runner.run([str(self.repo / "missing-binary")], self.repo, {})
        self.assertTrue((self.repo / "commands/0001.error.txt").is_file())

    def test_unexpected_success_is_failure_with_real_exit_evidence(self):
        runner = gate.Runner(self.repo / "commands")
        with self.assertRaisesRegex(gate.Failure, "expected refusal, exit 0"):
            runner.run([sys.executable, "-B", "-c", "print('actually ran')"], self.repo, {}, ok=False)
        self.assertEqual(json.loads((self.repo / "commands/0001.exit.json").read_text()), {"exit": 0})
        self.assertEqual((self.repo / "commands/0001.stdout").read_text(), "actually ran\n")

    @unittest.skipIf(gate.os.name == "nt", "POSIX signal return codes")
    def test_signal_is_not_an_expected_refusal(self):
        runner = gate.Runner(self.repo / "commands")
        with self.assertRaisesRegex(gate.Failure, "killed by signal"):
            runner.run([sys.executable, "-B", "-c", "import os, signal; os.kill(os.getpid(), signal.SIGTERM)"],
                       self.repo, {}, ok=False)
        self.assertLess(json.loads((self.repo / "commands/0001.exit.json").read_text())["exit"], 0)

    def test_timeout_retains_partial_output_and_is_not_refusal(self):
        runner = gate.Runner(self.repo / "commands")
        error = gate.subprocess.TimeoutExpired(["fixture"], 1, output=b"partial output", stderr=b"partial error")
        with patch.object(gate.subprocess, "run", side_effect=error):
            with self.assertRaisesRegex(gate.Failure, "timed out"):
                runner.run(["fixture"], self.repo, {}, ok=False)
        self.assertEqual((self.repo / "commands/0001.stdout").read_bytes(), b"partial output")
        self.assertEqual((self.repo / "commands/0001.stderr").read_bytes(), b"partial error")
        self.assertEqual(json.loads((self.repo / "commands/0001.exit.json").read_text()),
                         {"exit": None, "timed_out": True})

    def test_clean_environment_has_no_inherited_session_or_hook_configuration(self):
        env = gate.clean_env(self.repo, self.repo, "/usr/bin:/bin")
        self.assertNotIn("MISSION_ID", env)
        self.assertNotIn("GIT_INDEX_FILE", env)
        self.assertNotIn("ETHOS_REPO_ROOT", env)
        self.assertEqual(env["GIT_CONFIG_GLOBAL"], gate.os.devnull)


if __name__ == "__main__":
    unittest.main()
