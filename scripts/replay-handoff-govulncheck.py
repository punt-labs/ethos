"""Linux amd64: replay a built Ethos hook with a preinstalled govulncheck.

Usage: python3 scripts/replay-handoff-govulncheck.py OUT ETHOS GOVULNCHECK
Use a fresh OUT under .tmp/opencode/ and a current local Go toolchain.
Fixture preparation and refresh-data can use the network; neither gate can.
"""

import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys


def replay(out, ethos, scanner):
    out.mkdir()
    home = out / "home"
    home.mkdir()
    env = dict(os.environ, HOME=str(home), GIT_CONFIG_GLOBAL="/dev/null",
               GIT_CONFIG_SYSTEM="/dev/null", GOTOOLCHAIN="local")

    def run(cwd, *args, payload=None):
        result = subprocess.run(
            args, cwd=cwd, env=env, text=True, capture_output=True, timeout=240,
            input=json.dumps(payload) if payload is not None else None,
        )
        print(f"$ (cd {cwd} && {' '.join(map(str, args))}) -> {result.returncode}", flush=True)
        if result.stdout.strip():
            print(result.stdout.strip(), flush=True)
        if result.stderr.strip():
            print(result.stderr.strip(), flush=True)
        result.check_returncode()
        return result.stdout

    runtime = out / "bundle"
    (runtime / "bin").mkdir(parents=True)
    (runtime / "data").mkdir()
    (runtime / "data" / "bundle-marker").write_text("from runtime archive\n")
    shutil.copy2(scanner, runtime / "bin" / "govulncheck")
    goroot = Path(run(out, "go", "env", "GOROOT").strip())
    archive = out / "govulncheck-runtime.tar.gz"
    run(out, "tar", "-czf", archive, "-C", runtime, "bin", "data",
        "-C", goroot.parent, goroot.name)
    checksum = hashlib.sha256()
    with archive.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1 << 20), b""):
            checksum.update(chunk)
    digest = checksum.hexdigest()

    def fixture(case, version, terminal):
        source = out / (case + "-source")
        source.mkdir()
        repo = home / case
        run(source, "git", "init", "-q")
        checks = source / ".punt-labs/ethos/checks"
        (checks / "snapshots").mkdir(parents=True)
        (source / ".punt-labs/ethos.yaml").write_text("checks: project\n")
        (checks / "project.yaml").write_text("""name: project
checks:
  - name: database-offline
    stage: handoff
    files: all
    run: [sh, gate.sh]
""")
        (checks / "snapshots/project.yaml").write_text(f"""checks:
  database-offline:
    profile: linux-worktree-v1
    epoch: govulncheck-replay-v1
    sources: {{mode: worktree}}
    runtime:
      platforms:
        linux-amd64:
          bundle: govulncheck-runtime.tar.gz
          sha256: "{digest}"
          path: ["{{runtime}}/{goroot.name}/bin", "{{runtime}}/bin"]
          env:
            CGO_ENABLED: "0"
            GOFLAGS: "-mod=vendor"
            GOTOOLCHAIN: local
            GOCACHE: "{{scratch}}/go-build"
            GOPROXY: "off"
            GOSUMDB: "off"
    data:
      - name: govulndb
        url: https://vuln.go.dev
        mount: govulndb
        max_age_hours: 24
""")
        (source / "gate.sh").write_text('''set -eu
test "$(cat "$ETHOS_HANDOFF_RUNTIME/data/bundle-marker")" = 'from runtime archive'
if touch "$ETHOS_HANDOFF_RUNTIME/data/govulndb/write-probe" 2>/dev/null; then exit 92; fi
exec govulncheck -db "file://$ETHOS_HANDOFF_RUNTIME/data/govulndb" ./...
''')
        (source / "go.mod").write_text(f"""module example.com/handoff-data-replay

go 1.26

require golang.org/x/text {version}
""")
        (source / "main.go").write_text('''package main

import "golang.org/x/text/language"

func main() {
    _, _, _ = language.ParseAcceptLanguage("en")
}
''')
        run(source, "go", "mod", "tidy")
        run(source, "go", "mod", "vendor")
        run(source, "git", "add", ".punt-labs", "gate.sh", "go.mod", "go.sum", "main.go", "vendor")
        run(source, "git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com",
            "commit", "-qm", "vendored offline govulncheck fixture")
        run(out, "git", "clone", "-q", str(source), str(repo))
        run(repo, ethos, "handoff", "fetch-runtime", "--file", str(archive))
        assert run(repo, ethos, "handoff", "refresh-data").strip() == "govulndb"
        fields = dict(session_id=case, agent_id="worker", agent_type="implementer", cwd=str(repo))
        run(repo, ethos, "hook", "audit-log", payload=dict(
            fields, tool_name="Write", tool_input={"file_path": str(repo / "main.go")},
        ))
        audit = repo / ".punt-labs/local/ethos/sessions" / (case + ".audit.jsonl")
        rows = [json.loads(line) for line in audit.read_text().splitlines()]
        assert any(row.get("tool") == "Write" and row.get("agent_id") == fields["agent_id"]
                   for row in rows)
        print(f"Write/stop agent_id=worker matches ({audit})", flush=True)
        diag = home / ".punt-labs/ethos/logs/ethos.log"
        offset = diag.stat().st_size
        feedback = run(repo, ethos, "hook", "subagent-stop", payload=fields)
        events = re.findall(r"check=database-offline outcome=(\w+)", diag.read_bytes()[offset:].decode())
        print(case, version, " -> ".join(events), flush=True)
        assert events == ["miss", "launched", terminal], events
        if terminal == "completed":
            assert not feedback.strip(), feedback
        else:
            failure = json.loads(feedback)
            assert failure["decision"] == "block", failure
            assert "GO-2022-1059" in failure["reason"], failure
            assert "exit status 3" in failure["reason"], failure

    fixture("clean", "v0.42.0", "completed")
    fixture("vulnerable", "v0.3.7", "failed")
    print("PASS: the real hook scanned vendored Go modules using the read-only file DB offline")


if __name__ == "__main__":
    replay(*(Path(arg).resolve() for arg in sys.argv[1:]))
