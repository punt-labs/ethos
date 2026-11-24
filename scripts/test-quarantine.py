"""List or run the tracked Go test quarantines."""

import argparse
import re
import subprocess
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path


def read_inventory(path):
    today = datetime.now(timezone.utc).date()
    seen = set()
    for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        if not line.strip() or line.startswith("#"):
            continue
        fields = line.split("\t")
        if len(fields) != 6:
            raise ValueError(f"{path}:{number}: expected six tab-separated fields")
        package, test, issue, owner, expires, reason = fields
        where = f"{path}:{number}"
        if not re.fullmatch(r"\./(?:[A-Za-z0-9_-]+/)*[A-Za-z0-9_-]+", package):
            raise ValueError(f"{where}: expected one relative Go package")
        if not re.fullmatch(r"TestQuarantined_[A-Za-z0-9_]+", test):
            raise ValueError(f"{where}: expected a TestQuarantined_… Go test name")
        if (package, test) in seen:
            raise ValueError(f"{where}: duplicate test {package} {test}")
        seen.add((package, test))
        if not re.fullmatch(r"PL-[1-9][0-9]*", issue):
            raise ValueError(f"{where}: a Punt Labs Linear issue is required")
        if not owner.strip() or not reason.strip():
            raise ValueError(f"{where}: a named owner and reason are required")
        try:
            expiry = datetime.strptime(expires, "%Y-%m-%d").date()
        except ValueError as exc:
            raise ValueError(f"{where}: expiry must be YYYY-MM-DD (UTC)") from exc
        if expires != expiry.isoformat() or not today <= expiry <= today + timedelta(days=14):
            raise ValueError(f"{where}: expiry must be unexpired and within 14 days (UTC)")
        yield fields


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("list", "run"))
    parser.add_argument("inventory", type=Path)
    args = parser.parse_args()
    try:
        records = list(read_inventory(args.inventory))
        if not records:
            print("No quarantined tests.")
        for package, test, issue, owner, expires, reason in records:
            print(f"{package} {test}: {issue}, {owner}, expires {expires}: {reason}", flush=True)
            if args.mode == "run":
                command = ["go", "test", "-race", "-count=1", "-tags", "quarantine"]
                pattern = f"^{test}$"
                listed = subprocess.check_output([*command, "-list", pattern, package], text=True)
                if test not in listed.splitlines():
                    raise ValueError(f"{package} did not select {test} with -tags quarantine")
                subprocess.run([*command, "-run", pattern, "-v", package], check=True)
    except (OSError, ValueError, subprocess.SubprocessError) as exc:
        print(exc, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
