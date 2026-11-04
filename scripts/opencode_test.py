"""Offline regression tests; fixtures live only under .tmp/opencode."""

import copy
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import opencode as oc
import opencode_live as live


class ConfigTests(unittest.TestCase):
    def setUp(self):
        scratch = oc.ROOT / ".tmp/opencode"
        scratch.mkdir(parents=True, exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(dir=scratch, prefix="offline-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for name in ("opencode.json", ".opencode", "docs", "scripts"):
            source = oc.ROOT / name
            if source.is_dir():
                if name == "docs":
                    (self.root / name).mkdir()
                    for doc in ("opencode-workflow.md", "opencode-setup.md"):
                        shutil.copyfile(source / doc, self.root / name / doc)
                elif name == "scripts":
                    (self.root / name).mkdir()
                    for script in source.glob("opencode*.py"):
                        shutil.copyfile(script, self.root / name / script.name)
                else:
                    shutil.copytree(source, self.root / name)
            else:
                shutil.copyfile(source, self.root / name)

    def mutate_config(self, change):
        path = self.root / "opencode.json"
        config = json.loads(path.read_text())
        change(config)
        path.write_text(json.dumps(config))

    def mutate_agent(self, change, name="rsc"):
        path = self.root / f".opencode/agents/{name}.md"
        info, body = oc.frontmatter(path)
        change(info)
        path.write_text("---\n" + json.dumps(info) + "\n---\n" + body)

    def rejected(self):
        with self.assertRaises(oc.CheckError):
            oc.check(self.root)

    def test_real_configuration_and_all_helper_syntax(self):
        oc.check(self.root)

    def test_wrong_mode_model_and_obsolete_identity(self):
        for key, value in (("mode", "primary"), ("model", "bifrost/openai/gpt-6-astra#low"), ("name", "rsc")):
            with self.subTest(key=key):
                path = self.root / ".opencode/agents/rsc.md"
                original = path.read_text()
                self.mutate_agent(lambda a, key=key, value=value: a.update({key: value}))
                self.rejected()
                path.write_text(original)

    def test_review_shell_and_nested_delegation_regressions(self):
        for action in ("shell", "subagent", "edit"):
            with self.subTest(action=action):
                path = self.root / ".opencode/agents/rsc.md"
                original = path.read_text()
                self.mutate_agent(lambda a, action=action: a["permissions"].append(
                    {"action": action, "resource": "*", "effect": "allow"}))
                self.rejected()
                path.write_text(original)

    def test_narrow_grants_and_missing_secret_denials(self):
        for name, rule in (("rsc", {"action": "subagent", "resource": "qa", "effect": "allow"}),
                           ("qa", {"action": "edit", "resource": "only-one.go", "effect": "allow"}),
                           ("rsc", {"action": "shell", "resource": "touch one.go", "effect": "allow"}),
                           ("bwk", {"action": "read", "resource": "private/.env.key", "effect": "allow"})):
            path = self.root / f".opencode/agents/{name}.md"
            original = path.read_text()
            self.mutate_agent(lambda a, rule=rule: a["permissions"].append(rule), name)
            self.rejected()
            path.write_text(original)
        for name in oc.ROSTER:
            if name == "bwk":
                continue
            path = self.root / f".opencode/agents/{name}.md"
            original = path.read_text()
            self.mutate_agent(lambda a: a.update(permissions=[r for r in a["permissions"]
                              if r["action"] != "read" or r["effect"] != "deny"]), name)
            self.rejected()
            path.write_text(original)

    def test_role_capabilities_are_usable_but_not_publication_grants(self):
        config = oc.check(self.root)
        for name in ("jms", "jra", "researcher", "qa"):
            rules = config["permissions"] + oc.role_permissions(name)
            if name in ("jms", "jra"):
                allowed = ("execute", "zspec_check", "zspec_model_check", "zspec_doctor")
                denied = ("zspec_test", "quarry_ingest", "beadle_send_email", "context7_query-docs")
            elif name == "researcher":
                allowed = ("execute", "quarry_find", "quarry_show", "quarry_status",
                           "context7_resolve-library-id", "context7_query-docs")
                denied = ("quarry_remember", "quarry_ingest", "quarry_use", "zspec_model_check", "beadle_send_email")
            else:
                allowed, denied = (), ("execute", "beadle_send_email")
                for command in ("make build", "make test", "make check", "./ethos version",
                                "./ethos identity schema --json", "./ethos mission show m-test"):
                    self.assertEqual(oc.effect(rules, "shell", command), "allow")
                for command in ("make install", "./ethos enable", "./ethos mission close m-test", "make build X=1"):
                    self.assertEqual(oc.effect(rules, "shell", command), "deny")
            for action in allowed:
                self.assertEqual(oc.effect(rules, action, "*"), "allow", (name, action))
            for action in denied:
                self.assertEqual(oc.effect(rules, action, "*"), "deny", (name, action))
            self.assertEqual(oc.effect(rules, "edit", "docs/spec-test.tex"),
                             "allow" if name == "jms" else "deny")
            self.assertEqual(oc.effect(rules, "shell", "git push origin topic"), "deny")

    def test_qodo_adapter_is_required_and_review_cannot_bypass_router(self):
        command = self.root / ".opencode/commands/review.md"
        original = command.read_text()
        command.write_text(original.replace("ethos-qodo-review", "qodo-review"))
        self.rejected()
        command.write_text(original)
        (self.root / ".opencode/skills/ethos-qodo-review/SKILL.md").unlink()
        self.rejected()

    def test_non_go_formatting_and_duplicate_sources(self):
        self.mutate_config(lambda c: c["formatter"].update(latexindent={}))
        self.rejected()
        shutil.copyfile(oc.ROOT / "opencode.json", self.root / "opencode.json")
        (self.root / ".opencode/opencode.json").write_text("{}")
        self.rejected()

    def test_secret_and_wrong_remote_auth_are_rejected(self):
        self.mutate_config(lambda c: c["mcp"]["servers"]["context7"]["headers"].update(
            Authorization="Bearer " + "synthetic" * 4))
        self.rejected()

    def test_root_secret_read_protection_cannot_disappear(self):
        self.mutate_config(lambda c: c.update(permissions=[r for r in c["permissions"] if r["action"] != "read"]))
        self.rejected()

    def test_literal_secret_in_adapter_not_just_config(self):
        path = self.root / ".opencode/skills/ethos-prfaq/SKILL.md"
        path.write_text(path.read_text() + "\nCredential: sk-" + "x" * 32 + "\n")
        self.rejected()

    def test_missing_command_target_and_skill_duplicate(self):
        path = self.root / ".opencode/commands/prfaq.md"
        path.write_text(path.read_text().replace("Load ethos-prfaq", "Load ethos-missing"))
        self.rejected()
        shutil.copyfile(oc.ROOT / ".opencode/commands/prfaq.md", path)
        destination = self.root / ".opencode/skills/another/ethos-prfaq"
        shutil.copytree(self.root / ".opencode/skills/ethos-prfaq", destination)
        self.rejected()

    def test_missing_default_reasoning_and_secret_edit_rule(self):
        self.mutate_config(lambda c: c["providers"]["bifrost"]["models"]["openai/gpt-6-astra"].update(settings={}))
        self.rejected()
        shutil.copyfile(oc.ROOT / "opencode.json", self.root / "opencode.json")
        self.mutate_config(lambda c: c["permissions"].append(
            {"action": "edit", "resource": ".envrc.local", "effect": "allow"}))
        self.rejected()

    def test_bifrost_routing_and_model_only_catalog_are_required(self):
        original = (self.root / "opencode.json").read_text()
        mutations = [lambda c: c.update(model="openai/gpt-6-astra"),
                     lambda c: c["providers"]["bifrost"].update(settings={"baseURL": "https://example.invalid"}),
                     lambda c: c["providers"]["bifrost"]["models"].pop("openai/gpt-6.1-sol"),
                     lambda c: c["providers"]["bifrost"]["models"]["openai/gpt-6.1-sol"].update(variants=[])]
        for mutation in mutations:
            with self.subTest(mutation=mutation):
                self.mutate_config(mutation)
                self.rejected()
                (self.root / "opencode.json").write_text(original)
        self.assertEqual(oc.model_ref(oc.ROSTER["bwk"]),
                         {"providerID": "bifrost", "id": "openai/gpt-6.1-sol", "variant": "high"})
        self.assertEqual(oc.model_ref(oc.ROSTER["researcher"]),
                         {"providerID": "bifrost", "id": "openai/gpt-5.6-terra", "variant": "medium"})
        self.assertEqual(oc.model_ref(oc.ASTRA.split("#")[0]),
                         {"providerID": "bifrost", "id": "openai/gpt-6-astra"})

    def test_duplicate_json_key(self):
        with self.assertRaises(oc.CheckError):
            oc.load_json('{"model":1,"model":2}')

    def test_json_command_alias_cannot_drop_its_release_adapter(self):
        self.mutate_config(lambda c: c["commands"]["punt:auto"].update(template="No workflow"))
        self.rejected()

    def test_output_cannot_escape_or_overwrite(self):
        with self.assertRaises(oc.CheckError):
            oc.local_output(self.root / "source.md", self.root)
        allowed = self.root / ".tmp/opencode/result.md"
        self.assertEqual(oc.local_output(allowed, self.root), allowed)
        allowed.write_text("existing")
        with self.assertRaises(oc.CheckError):
            oc.local_output(allowed, self.root)

    def test_installed_prfaq_fallback_also_works_for_full_discovery(self):
        workspace = self.root / "workspace"
        home = self.root / "home"
        installed = home / ".agents/skills/prfaq/SKILL.md"
        installed.parent.mkdir(parents=True)
        installed.write_text("fixture")
        for relative in ("z-spec/plugin/commands/code2model.md", "z-spec/plugin/commands/check.md",
                         "punt-kit/plugin/skills/auto/SKILL.md", "punt-kit/plugin/playbooks/release.yaml"):
            path = workspace / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("fixture")
        with patch.object(oc, "canonical_checkout", return_value=workspace / "ethos"), \
                patch.object(Path, "home", return_value=home):
            self.assertEqual(oc.sources(self.root, "prfaq"), [installed])
            self.assertEqual(oc.sources(self.root, None)[0], installed)

    def test_worktree_aware_maintained_release_sources(self):
        main = self.root / "workspace/ethos"
        main.mkdir(parents=True)
        def git(*args):
            return subprocess.run(["git", *args], cwd=main, check=True, capture_output=True, text=True)
        git("init", "--initial-branch=main")
        # No hooks, user config, installation, or user records in this owned fixture.
        git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
            "-c", f"core.hooksPath={os.devnull}", "-c", "commit.gpgsign=false",
            "commit", "--allow-empty", "-m", "fixture")
        linked = main / ".worktrees/linked"
        git("worktree", "add", "--detach", str(linked))
        release = main.parent / "punt-kit/plugin"
        for relative in ("skills/auto/SKILL.md", "playbooks/release.yaml"):
            path = release / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("fixture")
        self.assertEqual(oc.canonical_checkout(linked), main)
        paths = oc.sources(linked, "release")
        self.assertEqual(paths, [release / "skills/auto/SKILL.md", release / "playbooks/release.yaml"])
        paths[1].unlink()
        with self.assertRaisesRegex(oc.CheckError, "release.yaml"):
            oc.sources(linked, "release")


class ResultTests(unittest.TestCase):
    def setUp(self):
        self.info = {"id": "ses_child", "parentID": "ses_parent", "agent": "rsc",
                     "outcome": "succeeded", "model": oc.model_ref(oc.ROSTER["rsc"])}
        self.header = (f"{oc.START}\ntask: ethos-t1uy\nround: 1\nauthor: rsc\n{oc.END}\n")
        self.message = {"id": "msg_final", "type": "assistant", "agent": "rsc",
                        "model": self.info["model"], "time": {"completed": 20},
                        "content": [{"type": "text", "text": self.header + "Checks: go test (exit 0).\n"}]}

    def extract(self, info=None, messages=None):
        return oc.result(self.info if info is None else info,
                         [self.message] if messages is None else messages,
                         "ses_child", "ses_parent", "ethos-t1uy", 1, "rsc")

    def test_newest_completed_not_array_order_and_no_gstack_restriction(self):
        old = copy.deepcopy(self.message)
        old.update(id="msg_old", time={"completed": 10})
        old["content"][0]["text"] = "older result without header"
        pending = copy.deepcopy(self.message)
        pending.update(id="msg_pending", time={"created": 30})
        pending["content"][0]["text"] = "incomplete"
        self.assertTrue(self.extract(messages=[self.message, pending, old]).startswith(self.header))

    def test_wrong_identity_parent_model_and_failed_session(self):
        for key, value in (("id", "ses_other"), ("parentID", "ses_other"), ("agent", "bwk"),
                           ("model", oc.model_ref("bifrost/openai/gpt-6-astra#low")), ("outcome", "failed")):
            with self.subTest(key=key), self.assertRaises(oc.CheckError):
                self.extract(info=dict(self.info, **{key: value}))

    def test_latest_wrong_header_incomplete_and_duplicate(self):
        for text in (self.header.replace("ethos-t1uy", "other"),
                     self.header.replace("round: 1", "round: 2"),
                     self.header + self.header, "", self.header):
            changed = copy.deepcopy(self.message)
            changed["content"][0]["text"] = text
            with self.subTest(text=text), self.assertRaises(oc.CheckError):
                self.extract(messages=[changed])

    def test_assistant_identity_is_checked_independently(self):
        changed = copy.deepcopy(self.message)
        changed["agent"] = "bwk"
        with self.assertRaises(oc.CheckError):
            self.extract(messages=[changed])

    def test_tool_only_messages_cannot_hide_wrong_identity_or_model(self):
        for key, value in (("agent", "bwk"), ("model", oc.model_ref("bifrost/openai/gpt-6-astra#low"))):
            changed = copy.deepcopy(self.message)
            changed.update(id="msg_tools", content=[{"type": "tool", "name": "read"}],
                           time={"completed": 10})
            changed[key] = value
            with self.subTest(key=key), self.assertRaises(oc.CheckError):
                self.extract(messages=[changed, self.message])

    def test_newer_empty_or_tool_only_answer_does_not_reuse_old_result(self):
        for content in ([], [{"type": "text", "text": ""}], [{"type": "tool", "name": "read"}]):
            changed = dict(self.message, id="msg_new", time={"completed": 30}, content=content)
            with self.subTest(content=content), self.assertRaises(oc.CheckError):
                self.extract(messages=[self.message, changed])

    def test_nonproject_author_works_with_explicit_model_not_a_handle_prefix(self):
        info = dict(self.info, agent="team/custom-reviewer")
        message = copy.deepcopy(self.message)
        message["agent"] = info["agent"]
        message["content"][0]["text"] = message["content"][0]["text"].replace("author: rsc", "author: " + info["agent"])
        args = (info, [message], "ses_child", "ses_parent", "ethos-t1uy", 1, info["agent"])
        self.assertIn("author: team/custom-reviewer", oc.result(*args, expected_model=oc.ASTRA))
        with self.assertRaisesRegex(oc.CheckError, "explicit --model"):
            oc.result(*args)

    def test_explicit_model_must_not_override_pinned_project_author(self):
        args = (self.info, [self.message], "ses_child", "ses_parent", "ethos-t1uy", 1, "rsc")
        self.assertTrue(oc.result(*args, expected_model=oc.ROSTER["rsc"]).startswith(self.header))
        other = "bifrost/openai/gpt-6-astra#low"
        info = dict(self.info, model=oc.model_ref(other))
        message = dict(self.message, model=info["model"])
        with self.assertRaisesRegex(oc.CheckError, "cannot override"):
            oc.result(info, [message], *args[2:], expected_model=other)

    def test_nonproject_author_rejects_missing_or_empty_variant(self):
        selector = oc.ASTRA.split("#")[0]
        info = dict(self.info, agent="team/custom-reviewer", model=oc.model_ref(selector))
        message = copy.deepcopy(self.message)
        message.update(agent=info["agent"], model=info["model"])
        message["content"][0]["text"] = message["content"][0]["text"].replace("author: rsc", "author: " + info["agent"])
        for invalid in (selector, selector + "#"):
            with self.subTest(selector=invalid), self.assertRaisesRegex(oc.CheckError, "variant"):
                oc.result(info, [message], "ses_child", "ses_parent", "ethos-t1uy", 1,
                          info["agent"], expected_model=invalid)


class LiveHelperTests(unittest.TestCase):
    @staticmethod
    def read_tool(path):
        lines = (oc.ROOT / path).read_text().splitlines()
        text = f"Read file {path}, lines 1-{len(lines)}\n"
        text += "\n".join(f"{number}: {line}" for number, line in enumerate(lines, 1))
        return {"type": "tool", "name": "read", "state": {
            "status": "completed", "input": {"path": str(path)},
            "content": [{"type": "text", "text": text}], "metadata": {"truncated": False}}}

    @staticmethod
    def invalid_read_contents():
        return (None, [], [{"type": "text", "text": ""}], [{"type": "text", "text": " \n"}],
                [{"type": "text", "text": "Read file go.mod, lines 1-1\n1: unrelated source"}],
                [{"type": "text", "text": "Read completed successfully"}])

    def test_child_parent_dispatch_rejects_extra_tools(self):
        dispatch = {"name": "subagent", "state": {"status": "completed", "metadata": {"sessionID": "ses_child"}}}
        live.dispatch_evidence([dispatch], "ses_child")
        for name in ("execute", "read", "shell", "subagent"):
            extra = {"name": name, "state": {"status": "completed"}}
            for calls in ([dispatch, extra], [extra, dispatch]):
                with self.subTest(name=name, calls=calls), self.assertRaisesRegex(oc.CheckError, "no other tools"):
                    live.dispatch_evidence(calls, "ses_child")
        for calls in ([], [{"name": "read"}], [dict(dispatch, state={"status": "error"})]):
            with self.subTest(calls=calls), self.assertRaises(oc.CheckError):
                live.dispatch_evidence(calls, "ses_child")
        with self.assertRaisesRegex(oc.CheckError, "exact child ID"):
            live.dispatch_evidence([dispatch], "ses_other")
        for metadata in ({}, {"sessionID": "ses_other"}, {"sessionID": "ses_child_suffix"},
                         {"sessionID": "prefix_ses_child"}, {"sessionID": None}, {"sessionID": 123},
                         {"sessionID": True}, {"sessionID": ["ses_child"]}, {"sessionID": {"id": "ses_child"}}):
            changed = copy.deepcopy(dispatch)
            changed["state"].update(metadata=dict(metadata, note="ses_child"),
                                    content=[{"type": "text", "text": "ses_child"}])
            with self.subTest(metadata=metadata), self.assertRaisesRegex(oc.CheckError, "exact child ID"):
                live.dispatch_evidence([changed], "ses_child")

    def test_child_evidence_requires_read_content_and_rejects_non_read_tools(self):
        info = {"id": "ses_child", "parentID": "ses_parent", "agent": "rsc",
                "outcome": "succeeded", "model": oc.model_ref(oc.ROSTER["rsc"])}
        artifact = f"{oc.START}\ntask: ethos-t1uy\nround: 1\nauthor: rsc\n{oc.END}\nObserved module.\n"
        read = self.read_tool("go.mod")
        message = {"id": "msg_final", "type": "assistant", "agent": "rsc", "model": info["model"],
                   "time": {"completed": 20}, "content": [read, {"type": "text", "text": artifact}]}
        api = live.API(oc.ROOT)
        with patch.object(api, "call", return_value={"data": info}), \
                patch.object(api, "pages", side_effect=lambda path, *args: [info] if path == "/api/session" else [message]):
            self.assertEqual(live.child_evidence(api, "ses_parent", "rsc", "ethos-t1uy"), ("ses_child", artifact))
            for content in self.invalid_read_contents():
                bad = copy.deepcopy(read)
                if content is None:
                    del bad["state"]["content"]
                else:
                    bad["state"]["content"] = content
                for attempts in ([bad], [bad, read], [read, bad]):
                    message["content"] = [*attempts, {"type": "text", "text": artifact}]
                    with self.subTest(content=content, attempts=len(attempts)), \
                            self.assertRaisesRegex(oc.CheckError, "returned text"):
                        live.child_evidence(api, "ses_parent", "rsc", "ethos-t1uy")
            for name in ("shell", "execute", "edit", "write", "patch", "subagent", "skill"):
                extra = {"type": "tool", "name": name, "state": {"status": "completed"}}
                message["content"] = [read, extra, {"type": "text", "text": artifact}]
                with self.subTest(name=name), self.assertRaisesRegex(oc.CheckError, "other than read"):
                    live.child_evidence(api, "ses_parent", "rsc", "ethos-t1uy")
            workflow_read = {"type": "tool", "name": "read", "state": {"status": "completed",
                             "input": {"path": "docs/opencode-workflow.md"}}}
            message["content"] = [read, workflow_read, {"type": "text", "text": artifact}]
            self.assertEqual(live.child_evidence(api, "ses_parent", "rsc", "ethos-t1uy"), ("ses_child", artifact))
            for status in ("error", "running"):
                workflow_read["state"]["status"] = status
                with self.subTest(status=status), self.assertRaisesRegex(oc.CheckError, "did not complete"):
                    live.child_evidence(api, "ses_parent", "rsc", "ethos-t1uy")

    @staticmethod
    def health_values():
        return {
            "vox.status": {"result": json.dumps({"provider_status": {"ready": True},
                                               "program": {"mode": "off", "failed_parts": [],
                                                           "generation": {"filling": False, "attempts": 0},
                                                           "playback_error": None}})},
            "beadle.get_poll_status": "   Interval:    disabled\n   Active:      no\n   Unseen:      0",
            "quarry.status": {"result": "▶  quarry status\n   Documents: 0\n   Collections: 0\n"
                              "   Chunks: 0\n   Directories: 0\n   Database: fixture\n"
                              "   Model: fixture\n   Provider: CPUExecutionProvider"},
            "zspec.doctor": {"result": '{"healthy":true}'},
            "lux.ping": {"result": "pong rtt=0.035s"},
            "context7.resolve-library-id": "Available Libraries:\n- Title: Go\n"
                                           "- Context7-compatible library ID: /golang/go\n"
                                           "- Description: Go standard library documentation",
        }

    def test_each_health_contract_and_wrapped_failures(self):
        values = self.health_values()
        for name, value in values.items():
            live.health_response(name, value)
            live.health_response(name, {"result": json.dumps({"result": value})})
            live.health_response(name, {"content": [{"type": "text", "text": json.dumps(value)}]})
            for malformed in (None, "", {}, {"result": "completed"}, {"result": {"isError": True}}):
                with self.subTest(name=name, malformed=malformed), self.assertRaises(oc.CheckError):
                    live.health_response(name, malformed)
        invalid = {
            "vox.status": [{"provider_status": {"ready": False}, "program": {"mode": "off"}},
                           {"provider_status": {"ready": True}, "program": {"error": "sensitive"}},
                           {"provider_status": {"ready": True}},
                           {"provider_status": {"ready": True}, "program": {"mode": "off", "playback_error": "sensitive"}}],
            "beadle.get_poll_status": [values["beadle.get_poll_status"] + "\n   Failures: 1",
                                      values["beadle.get_poll_status"] + "\n   Last error: sensitive"],
            "quarry.status": ["▶ quarry status", "Documents: unknown\nChunks: 0"],
            "zspec.doctor": [{"healthy": False}, {"healthy": "true"}],
            "lux.ping": ["ping", "pong", "pong rtt=NaNs"],
            "context7.resolve-library-id": ["No results found", "Available Libraries:\n- Title: Go"],
        }
        for name, failures in invalid.items():
            for value in failures:
                with self.subTest(name=name, value=value), self.assertRaises(oc.CheckError) as caught:
                    live.health_response(name, {"result": json.dumps(value)})
                self.assertNotIn("sensitive", str(caught.exception))
        program_error = json.loads(values["vox.status"]["result"])
        program_error["program"]["generation"]["last_error"] = "sensitive"
        with self.assertRaises(oc.CheckError):
            live.health_response("vox.status", {"result": json.dumps(program_error)})

    def codemode_tool(self):
        return {"name": "execute", "state": {
            "status": "completed", "input": {"code": live.HEALTH_CODE},
            "metadata": {"toolCalls": [{"tool": name, "status": "completed", "input": args}
                                       for name, args in live.HEALTH_INPUTS.items()]},
            "content": [{"type": "text", "text": json.dumps(self.health_values())}]}}

    def test_codemode_requires_real_nested_calls_and_unmodified_result_code(self):
        tool = self.codemode_tool()
        live.codemode_health([tool])
        mutations = [lambda s: s["metadata"].update(toolCalls=[]),
                     lambda s: s["metadata"].update(error=True),
                     lambda s: s["metadata"]["toolCalls"][0].update(status="error"),
                     lambda s: s["metadata"]["toolCalls"].append({"tool": "beadle.send_email", "status": "completed"}),
                     lambda s: s["input"].update(code='return {"zspec.doctor":{"healthy":true}};'),
                     lambda s: s["content"][0].update(text='{"zspec.doctor":{"healthy":true}}'),
                     lambda s: s["metadata"]["toolCalls"][-1].update(input={"libraryName": "Other"})]
        for mutation in mutations:
            changed = copy.deepcopy(tool)
            mutation(changed["state"])
            with self.subTest(mutation=mutation), self.assertRaises(oc.CheckError):
                live.codemode_health([changed])

    def test_formatter_rejects_manual_format_even_if_disk_looks_correct(self):
        temp = tempfile.TemporaryDirectory(dir=oc.ROOT / ".tmp/opencode", prefix="formatter-")
        self.addCleanup(temp.cleanup)
        fixture = Path(temp.name)
        samples = {"sample.go": "package smoke\nfunc f( ){ }\n// SMOKE_BEFORE\n"}
        sentinel_patch = ("*** Begin Patch\n*** Update File: sample.go\n@@\n"
                          "-// SMOKE_BEFORE\n+// SMOKE_AFTER\n*** End Patch")
        tool = {"name": "patch", "state": {"status": "completed", "input": {"patchText": sentinel_patch}}}
        live.formatter_inputs([tool], fixture, samples)
        changed = copy.deepcopy(tool)
        changed["state"]["input"]["patchText"] = (
            "*** Begin Patch\n*** Update File: sample.go\n@@\n package smoke\n"
            "-func f( ){ }\n+\n+func f() {}\n+\n-// SMOKE_BEFORE\n+// SMOKE_AFTER\n*** End Patch")
        # A no-op formatter after this model-supplied edit yields seemingly
        # correct disk bytes. The input evidence must still reject the pass.
        (fixture / "sample.go").write_text("package smoke\n\nfunc f() {}\n\n// SMOKE_AFTER\n")
        self.assertEqual((fixture / "sample.go").read_text(), "package smoke\n\nfunc f() {}\n\n// SMOKE_AFTER\n")
        with self.assertRaises(oc.CheckError):
            live.formatter_inputs([changed], fixture, samples)
        changed = copy.deepcopy(tool)
        changed["name"] = "write"
        with self.assertRaises(oc.CheckError):
            live.formatter_inputs([changed], fixture, samples)
        with self.assertRaises(oc.CheckError):
            live.formatter_inputs([tool, tool], fixture, samples)
        changed = copy.deepcopy(tool)
        changed["state"]["input"]["patchText"] = sentinel_patch.replace("sample.go", "other.go")
        with self.assertRaises(oc.CheckError):
            live.formatter_inputs([changed], fixture, samples)

    def test_formatter_rejects_newline_only_mutations_in_raw_bytes(self):
        with tempfile.TemporaryDirectory(dir=oc.ROOT / ".tmp/opencode", prefix="formatter-") as directory:
            fixture = Path(directory)
            for original, mutation in (("# SMOKE_BEFORE\n", b"# SMOKE_AFTER\r\n"),
                                       ("# SMOKE_BEFORE\r\n", b"# SMOKE_AFTER\n")):
                samples = {"sample.md": original}
                (fixture / "sample.md").write_bytes(original.replace("BEFORE", "AFTER").encode())
                live.formatter_outputs(fixture, samples)
                (fixture / "sample.md").write_bytes(mutation)
                with self.assertRaisesRegex(oc.CheckError, "sample.md"):
                    live.formatter_outputs(fixture, samples)

    def test_gofmt_expected_input_and_output_are_binary(self):
        with tempfile.TemporaryDirectory(dir=oc.ROOT / ".tmp/opencode", prefix="formatter-") as directory:
            fixture = Path(directory)
            samples = {"sample.go": "package smoke\r\n// SMOKE_BEFORE\r\n"}
            expected = b"package smoke\n\n// SMOKE_AFTER\n"
            (fixture / "sample.go").write_bytes(expected)
            with patch("shutil.which", return_value="gofmt"), \
                    patch("subprocess.check_output", return_value=expected) as run:
                live.formatter_outputs(fixture, samples)
            run.assert_called_once_with(["gofmt"], input=b"package smoke\r\n// SMOKE_AFTER\r\n")

    def test_required_evidence_matches_arguments_before_completion_and_checks_every_attempt(self):
        failed = {"name": "shell", "state": {"status": "error", "input": {"command": "unrelated"}}}
        good = {"name": "shell", "state": {"status": "completed", "input": {"command": "required"},
                                            "metadata": {"status": "completed", "exit": 0}}}
        def matches(args):
            return args.get("command") == "required"
        live.required_calls([failed, good], "shell", matches, "required")
        with self.assertRaisesRegex(oc.CheckError, "missing required"):
            live.required_calls([failed], "shell", matches, "required")
        bad = copy.deepcopy(good)
        for state in (dict(bad["state"], status="error"),
                      dict(bad["state"], metadata={"status": "completed", "exit": 1})):
            bad["state"] = state
            for calls in ([bad, good], [good, bad]):
                with self.subTest(calls=calls), self.assertRaises(oc.CheckError):
                    live.required_calls(calls, "shell", matches, "required")

    def test_discovery_requires_zspec_help_and_all_required_operations(self):
        temp = tempfile.TemporaryDirectory(dir=oc.ROOT / ".tmp/opencode", prefix="round10-discovery-")
        self.addCleanup(temp.cleanup)
        paths = [Path(temp.name) / name for name in ("SKILL.md", "release.yaml")]
        for path in paths:
            path.write_text(f"# Maintained procedure {path.name}\n\nRead this actual source.\n")
        calls = [{"name": "skill", "state": {"status": "completed", "input": {"id": skill}}}
                 for skill in live.DISCOVERY_SKILLS]
        calls += [{"name": "shell", "state": {"status": "completed", "input": {"command": command},
                                              "metadata": {"status": "completed", "exit": 0}}}
                  for command in live.DISCOVERY_COMMANDS]
        calls += [self.read_tool(path) for path in paths]
        self.assertIn("z-spec --help", live.DISCOVERY_COMMANDS)
        with patch.object(live, "sources", return_value=paths):
            live.discovery_evidence(calls, oc.ROOT)
            for index in range(len(calls) - len(paths), len(calls)):
                for content in self.invalid_read_contents():
                    changed = copy.deepcopy(calls)
                    if content is None:
                        del changed[index]["state"]["content"]
                    else:
                        changed[index]["state"]["content"] = content
                    for attempts in (changed, changed + calls, calls + changed):
                        with self.subTest(read=index, content=content, attempts=len(attempts)), \
                                self.assertRaisesRegex(oc.CheckError, "returned text"):
                            live.discovery_evidence(attempts, oc.ROOT)
                changed = copy.deepcopy(calls)
                changed[index]["state"]["content"] = calls[-1 if index == len(calls) - 2 else -2]["state"]["content"]
                with self.subTest(wrong_file=index), self.assertRaisesRegex(oc.CheckError, "returned text"):
                    live.discovery_evidence(changed, oc.ROOT)
            unrelated = {"name": "shell", "state": {"status": "error", "input": {"command": "unrelated"}}}
            live.discovery_evidence([unrelated, *calls], oc.ROOT)
            for command in live.DISCOVERY_ADAPTER_COMMANDS:
                attempt = {"name": "shell", "state": {"status": "completed", "input": {"command": command},
                                                      "metadata": {"status": "completed", "exit": 0}}}
                live.discovery_evidence([attempt, *calls], oc.ROOT)
                attempt["state"]["status"] = "error"
                with self.subTest(adapter_command=command), self.assertRaises(oc.CheckError):
                    live.discovery_evidence([attempt, *calls], oc.ROOT)
            for index in range(len(calls)):
                with self.subTest(missing=index), self.assertRaises(oc.CheckError):
                    live.discovery_evidence(calls[:index] + calls[index + 1:], oc.ROOT)
                changed = copy.deepcopy(calls)
                changed[index]["state"]["status"] = "error"
                with self.subTest(failed=index), self.assertRaises(oc.CheckError):
                    live.discovery_evidence(changed + calls, oc.ROOT)

    def test_required_read_compares_complete_bounded_source_lines(self):
        read = self.read_tool("go.mod")
        def matches(args):
            return args.get("path") == "go.mod"
        text = read["state"]["content"][0]["text"]
        # The real retained researcher read requested offset 1, limit 10.
        read["state"]["input"].update(offset=1, limit=10)
        read["state"]["content"][0]["text"] = "\n".join(text.splitlines()[:11])
        live.required_read([read], matches, oc.ROOT / "go.mod", "child read go.mod")
        for bad_text in (text.splitlines()[0], "\n".join(text.splitlines()[:2]),
                         text.replace("10: ", "10: unrelated "),
                         "\n".join(text.splitlines()[:11]) + " unrelated suffix"):
            changed = copy.deepcopy(read)
            changed["state"]["content"][0]["text"] = bad_text
            with self.subTest(text=bad_text), self.assertRaisesRegex(oc.CheckError, "returned text"):
                live.required_read([changed], matches, oc.ROOT / "go.mod", "child read go.mod")
        # Generic completion must still accept a legitimate empty read.
        live.completed({"name": "read", "state": {"status": "completed", "content": []}})

    def test_smoke_cli_passes_explicit_stage_selection(self):
        with patch("sys.argv", ["opencode.py", "smoke", "--stage", "discovery", "--stage", "health"]), \
                patch.object(live, "smoke") as smoke:
            oc.main()
        smoke.assert_called_once_with(oc.ROOT, ["discovery", "health"])

    def test_api_failures_keep_safe_operation_exit_and_timeout(self):
        api = live.API(oc.ROOT)
        with patch("subprocess.run", return_value=subprocess.CompletedProcess([], 7)), \
                self.assertRaisesRegex(oc.CheckError, r"GET /api/model: CLI exit 7"):
            api.call("get", "/api/model")
        with patch("subprocess.run", side_effect=subprocess.TimeoutExpired("sensitive", 180, stderr="secret")):
            with self.assertRaisesRegex(oc.CheckError, r"GET /api/model: CLI timeout after 180s") as caught:
                api.call("get", "/api/model")
            self.assertNotIn("secret", str(caught.exception))

    def test_shell_requires_actual_foreground_zero_exit_evidence(self):
        valid = {"status": "completed", "metadata": {"status": "completed", "exit": 0}}
        live.completed({"name": "shell", "state": valid})
        invalid = [{}, {"exit": 0}, {"status": "completed"},
                    {"status": "running", "exit": 0}, {"status": "timeout", "exit": 0},
                    {"status": "completed", "exit": 0, "signal": "SIGTERM"},
                    {"status": "completed", "exit": 0, "background": True},
                    {"status": "completed", "exit": 0, "timeout": True},
                    {"status": "completed", "exit": False},
                    {"status": "completed", "exit": -9}]
        for metadata in invalid:
            with self.subTest(metadata=metadata), self.assertRaises(oc.CheckError):
                live.completed({"name": "shell", "state": dict(valid, metadata=metadata)})
        with self.assertRaises(oc.CheckError):
            live.completed({"name": "shell", "state": dict(valid, input={"background": True})})

    def test_live_permission_exceptions_cannot_override_reviewed_policy(self):
        config = oc.check()
        native = [{"action": "browser", "resource": "*", "effect": "deny"},
                  {"action": "skill", "resource": "browser", "effect": "deny"}]
        for name in (*oc.ROSTER, "build"):
            rules = config["permissions"] + ([] if name == "build" else oc.role_permissions(name))
            live.resolved_permissions(name, rules, config)
            live.resolved_permissions(name, rules + native, config)
            for extra in ({"action": "subagent", "resource": "qa", "effect": "allow"},
                          {"action": "read", "resource": "private/.env.key", "effect": "allow"},
                          {"action": "execute", "resource": "*", "effect": "deny"},
                          {"action": "execute*", "resource": "*", "effect": "deny"},
                          {"action": "zspec_model_check", "resource": "*", "effect": "deny"},
                          {"action": "context7_query-docs", "resource": "*", "effect": "deny"},
                          {"action": "read", "resource": "go.mod", "effect": "deny"},
                          {"action": "unknown", "resource": "*", "effect": "deny"},
                          {"action": "browser*", "resource": "*", "effect": "deny"},
                          {"action": "skill", "resource": "browser*", "effect": "deny"}):
                for suffix in ([], native):
                    with self.subTest(name=name, extra=extra, suffix=suffix), self.assertRaises(oc.CheckError):
                        live.resolved_permissions(name, rules + suffix + [extra], config)
            for suffix in (native[:1], native[1:], list(reversed(native)), native + native):
                with self.subTest(name=name, suffix=suffix), self.assertRaises(oc.CheckError):
                    live.resolved_permissions(name, rules + suffix, config)

    def test_live_permission_required_grants_cannot_be_narrowed(self):
        config = oc.check()
        for name, action in (("jms", "execute"), ("jra", "zspec_model_check"),
                             ("researcher", "context7_query-docs")):
            rules = config["permissions"] + oc.role_permissions(name)
            for narrowed in ("deny", "ask", "resource"):
                changed = copy.deepcopy(rules)
                grant = next(rule for rule in changed if rule["action"] == action)
                if narrowed == "resource":
                    grant["resource"] = "only-one-input"
                else:
                    grant["effect"] = narrowed
                with self.subTest(name=name, narrowed=narrowed), self.assertRaises(oc.CheckError):
                    live.resolved_permissions(name, changed, config)

    def test_pending_connections_wait_but_failures_do_not_become_empty_success(self):
        api = live.API(oc.ROOT)
        def payload(status):
            return {"data": [{"name": name, "status": {"status": status}} for name in oc.SERVERS]}
        with patch.object(api, "location", side_effect=[payload("pending"), payload("connected")]), \
                patch("time.sleep") as wait:
            self.assertEqual(set(live.connected_servers(api)), oc.SERVERS)
            wait.assert_called_once_with(2)
        with patch.object(api, "location", return_value=payload("failed")), \
                self.assertRaisesRegex(oc.CheckError, "transport failed"):
            live.connected_servers(api)
        with patch.object(api, "location", return_value=payload("pending")), \
                self.assertRaisesRegex(oc.CheckError, "budget exhausted"):
            live.connected_servers(api, budget=0)
        with patch.object(api, "location", return_value=payload("sensitive")), \
                self.assertRaises(oc.CheckError) as caught:
            live.connected_servers(api)
        self.assertNotIn("sensitive", str(caught.exception))

    def test_large_stdout_uses_private_regular_file_not_a_pipe(self):
        api = live.API(oc.ROOT)
        payload = "x" * 600000
        def run(args, **kwargs):
            self.assertNotIn("capture_output", kwargs)
            self.assertTrue(os.path.isfile(f"/proc/self/fd/{kwargs['stdout'].fileno()}")
                            if Path("/proc/self/fd").exists() else kwargs["stdout"].seekable())
            kwargs["stdout"].write(payload.encode())
            return subprocess.CompletedProcess(args, 0)
        with patch("subprocess.run", side_effect=run):
            self.assertEqual(api.run(["api", "get", "/api/model"]), payload)

    def test_api_is_explicitly_location_scoped_and_never_renders_error_body(self):
        api = live.API(oc.ROOT)
        with patch.object(api, "run", return_value='{"data":[]}') as run:
            self.assertEqual(api.location("/api/mcp"), {"data": []})
            self.assertIn("location%5Bdirectory%5D=", run.call_args.args[0][2])
        with patch.object(api, "run", return_value="sensitive invalid payload"), \
                self.assertRaisesRegex(oc.CheckError, "malformed/truncated"):
            api.call("get", "/api/config")

    def test_api_pagination_and_loop_rejection(self):
        api = live.API(oc.ROOT)
        with patch.object(api, "call", side_effect=[
            {"data": [1], "cursor": {"next": "page2"}}, {"data": [2], "cursor": {"next": None}}
        ]):
            self.assertEqual(api.pages("/api/session"), [1, 2])
        with patch.object(api, "call", return_value={"data": [], "cursor": {"next": "same"}}), \
                self.assertRaisesRegex(oc.CheckError, "repeated"):
            api.pages("/api/session")

    def test_native_tool_failure_is_not_success(self):
        for state in ({"status": "error"}, {"status": "running"},
                      {"status": "completed", "content": [{"type": "text", "text": '{"ok":false}'}]},
                      {"status": "completed", "content": [{"type": "text", "text": '{"healthy":false}'}]},
                      {"status": "completed", "content": [{"type": "text", "text": 'Error: failed'}]}):
            with self.subTest(state=state), self.assertRaises(oc.CheckError):
                live.completed({"state": state})

    def test_completed_shell_with_failed_exit_is_rejected(self):
        for state in ({"status": "completed", "metadata": {"exit": 1}},
                      {"status": "completed", "content": [{"type": "text", "text": "Exited with code 1\n"}]}):
            with self.subTest(state=state), self.assertRaises(oc.CheckError):
                live.completed({"name": "shell", "state": state})

    def test_runtime_config_detects_inherited_formatter(self):
        _, reasoning, fmt = live.runtime_config([
            {"info": {"formatter": {"custom-md": {"extensions": [".md"]}}}},
            {"info": oc.check()}
        ])
        self.assertEqual(reasoning, "high")
        self.assertEqual(fmt["custom-md"]["extensions"], [".md"])
        self.assertTrue(fmt["latexindent"]["disabled"])

    def test_live_gofmt_requires_reviewed_command_and_extensions(self):
        expected = oc.check()["formatter"]
        live.resolved_formatters(expected, expected)
        for override in ({"command": ["other", "$FILE"]}, {"extensions": [".md"]},
                         {"command": None}, {"extensions": None}, {"disabled": True}):
            _, _, fmt = live.runtime_config([
                {"info": {"formatter": expected}}, {"info": {"formatter": {"gofmt": override}}}
            ])
            with self.subTest(override=override), self.assertRaisesRegex(oc.CheckError, "live gofmt"):
                live.resolved_formatters(fmt, expected)
        fmt = copy.deepcopy(expected)
        del fmt["gofmt"]
        with self.assertRaisesRegex(oc.CheckError, "command/extensions"):
            live.resolved_formatters(fmt, expected)

    def test_direct_provider_reasoning_cannot_mask_missing_bifrost_default(self):
        _, reasoning, _ = live.runtime_config([
            {"info": {"providers": {"openai": {"models": {"gpt-6-astra": {
                "settings": {"reasoningEffort": "high"}}}}}}}
        ])
        self.assertIsNone(reasoning)


class SourceTests(unittest.TestCase):
    def setUp(self):
        scratch = oc.ROOT / ".tmp/opencode"
        scratch.mkdir(parents=True, exist_ok=True)
        self.temp = tempfile.TemporaryDirectory(dir=scratch, prefix="source-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.git("init", "--initial-branch=main")
        (self.root / ".gitignore").write_text("coverage.out\n.cache/\n.tmp/\n")
        (self.root / "tracked.go").write_text("package fixture\n")
        self.git("add", ".gitignore", "tracked.go")

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.root, check=True, capture_output=True)

    def test_ignored_outputs_do_not_change_snapshot_but_tracked_ignored_files_do(self):
        before = live.tree_digest(self.root)
        (self.root / "coverage.out").write_text("generated")
        (self.root / ".cache").mkdir()
        (self.root / ".cache/output").write_text("generated")
        self.assertEqual(before, live.tree_digest(self.root))
        self.git("add", "-f", "coverage.out")
        before = live.tree_digest(self.root)
        (self.root / "coverage.out").write_text("tracked output was changed")
        self.assertNotEqual(before, live.tree_digest(self.root))

    def test_add_delete_content_mode_and_symlink_target(self):
        original = self.root / "tracked.go"
        original_mode = original.stat().st_mode & 0o777
        before = live.tree_digest(self.root)
        source = self.root / "new.go"
        source.write_text("package new\n")
        self.assertNotEqual(before, live.tree_digest(self.root))
        source.unlink()
        original.chmod(0o755)
        self.assertNotEqual(before, live.tree_digest(self.root))
        original.chmod(original_mode)
        self.assertEqual(before, live.tree_digest(self.root))
        original.write_text("package changed\n")
        self.assertNotEqual(before, live.tree_digest(self.root))
        original.write_text("package fixture\n")
        original.unlink()
        self.assertNotEqual(before, live.tree_digest(self.root))
        original.symlink_to("target-a")
        link_digest = live.tree_digest(self.root)
        original.unlink()
        original.symlink_to("target-b")
        self.assertNotEqual(link_digest, live.tree_digest(self.root))
        directory = self.root / "dir-link"
        (self.root / ".cache").mkdir()
        directory.symlink_to(".cache", target_is_directory=True)
        before = live.tree_digest(self.root)
        directory.unlink()
        directory.symlink_to(".", target_is_directory=True)
        self.assertNotEqual(before, live.tree_digest(self.root))

    def test_source_guard_reports_relative_changes_and_metadata(self):
        before = live.tree_snapshot(self.root)
        (self.root / "tracked.go").chmod(0o755)
        after = live.tree_snapshot(self.root)
        self.assertEqual(live.source_changes(before, after), ["tracked.go"])
        self.assertIn("mode", before["tracked.go"])

    def test_symlink_parent_is_recorded_without_reading_external_source(self):
        directory = self.root / "source"
        directory.mkdir()
        (directory / "file.go").write_text("package old\n")
        self.git("add", "source/file.go")
        before = live.tree_digest(self.root)
        (directory / "file.go").unlink()
        directory.rmdir()
        directory.symlink_to("missing-external", target_is_directory=True)
        after = live.tree_snapshot(self.root)
        self.assertEqual(after["source"]["type"], "symlink")
        self.assertNotEqual(before, live.tree_digest(self.root))

    def test_failed_smoke_receipt_retains_stage_and_safe_diagnostic(self):
        # Stop before any paid session can be created; exercise real receipt I/O.
        with patch.object(live, "doctor", side_effect=oc.CheckError("GET /api/model: CLI exit 7")), \
                self.assertRaises(oc.CheckError):
            live.smoke(self.root)
        receipt_path, = (self.root / ".tmp/opencode").glob("smoke-*/receipt.json")
        receipt = json.loads(receipt_path.read_text())
        self.assertEqual(receipt["status"], "failed")
        self.assertEqual(receipt["stage"], "live doctor")
        self.assertEqual(receipt["error"], "GET /api/model: CLI exit 7")
        self.assertEqual(receipt["source_changes"], [])
        self.assertEqual(receipt["scope"], "full")
        self.assertEqual(receipt["requested_stages"], list(oc.SMOKE_STAGES))
        self.assertEqual(receipt["completed_stages"], [])
        before = json.loads((receipt_path.parent / "source-before.json").read_text())
        self.assertIn("mode", before["tracked.go"])

    def test_targeted_smoke_receipt_does_not_claim_other_stages(self):
        api = live.API(self.root)
        tool = LiveHelperTests().codemode_tool()
        with patch.object(live, "doctor", return_value=api), \
                patch.object(api, "create", return_value="ses_fixture"), \
                patch.object(api, "prompt", return_value=[{"type": "assistant", "content": [dict(tool, type="tool")]}]), \
                patch.object(live, "child_evidence", side_effect=AssertionError("unexpected child turn")), \
                patch.object(live, "sources", side_effect=AssertionError("unexpected discovery")), \
                patch.object(live, "formatter_inputs", side_effect=AssertionError("unexpected formatter")):
            live.smoke(self.root, ["health"])
        receipt_path, = (self.root / ".tmp/opencode").glob("smoke-*/receipt.json")
        receipt = json.loads(receipt_path.read_text())
        self.assertEqual(receipt["status"], "passed")
        self.assertEqual(receipt["scope"], "targeted")
        self.assertEqual(receipt["requested_stages"], ["health"])
        self.assertEqual(receipt["completed_stages"], ["health"])
        self.assertEqual(receipt["source_changes"], [])
        self.assertEqual(len(receipt["checks"]), 1)

    def test_invalid_stage_selection_stops_before_live_work(self):
        with patch.object(live, "doctor", side_effect=AssertionError("unexpected live check")):
            for stages in ([], ["unknown"], ["health", "health"]):
                with self.subTest(stages=stages), self.assertRaises(oc.CheckError):
                    live.smoke(self.root, stages)


if __name__ == "__main__":
    unittest.main()
