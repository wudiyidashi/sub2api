#!/bin/sh
set -eu
driver_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec python3 - "$driver_root" "${1:-}" <<'PY'
import hashlib
import json
import pathlib
import subprocess
import sys

root = pathlib.Path(sys.argv[1])
lock = json.loads((root / "upstream-lock.json").read_text(encoding="utf-8"))
source = sys.argv[2] or lock["repository"]
upstream = root / "upstream"
patch = root / lock["patches"][0]
if hashlib.sha256(patch.read_bytes()).hexdigest() != lock["patch_sha256"]:
    raise SystemExit("CPR patch checksum does not match upstream-lock.json")

def git(*args, capture=False):
    result = subprocess.run(["git", "-C", str(upstream), *args], check=True,
                            stdout=subprocess.PIPE if capture else None)
    return result.stdout.decode("utf-8").strip() if capture else ""

if not (upstream / ".git").exists():
    subprocess.run(["git", "clone", "--no-checkout", "--", source, str(upstream)], check=True)
    git("checkout", "--detach", lock["commit"])
if git("rev-parse", "HEAD", capture=True) != lock["commit"]:
    raise SystemExit("Existing CPR checkout differs from the pinned commit; preserve it before preparing a separate checkout")
if git("ls-files", "--others", "--exclude-standard", capture=True):
    raise SystemExit("CPR checkout contains untracked files; preserve them before preparing the dependency")
diff = git("diff", "--no-ext-diff", "--full-index", capture=True)
if diff:
    if diff != patch.read_text(encoding="utf-8").strip():
        raise SystemExit("CPR checkout has changes beyond the maintained patch")
    git("apply", "--reverse", "--check", str(patch))
else:
    git("apply", "--check", str(patch))
    git("apply", str(patch))
print(f"CPR {lock['commit']} prepared with {lock['patches'][0]}")
PY
