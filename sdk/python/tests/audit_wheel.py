"""Check a built wheel. Usage: python tests/audit_wheel.py <wheel>

Fails if the wheel has anything besides the SDK, the bundled bridge, the license and
metadata, or if any file (including the go binary) contains a secret value. Never prints a
secret. Stdlib only so it runs anywhere in CI.
"""
import re
import sys
import zipfile

# stuff that must never end up in the wheel
FORBIDDEN = [
    ".env", "agent.yaml", "ark-memory", "ark-router", "ark-governor", "ark-credential",
    "/.git", ".venv", "credblock", "/tests/", "/examples/", "benchmark", "research",
    ".jsonl", "build_wheel.sh",
]
# real secret values, not env var names like OPENAI_API_KEY
SECRET = re.compile(
    rb"sk-proj-[A-Za-z0-9]{6,}|sk-ant-[A-Za-z0-9]{6,}|ghp_[A-Za-z0-9]{20,}|"
    rb"xoxb-[A-Za-z0-9-]{10,}|-----BEGIN [A-Z ]*PRIVATE KEY-----|AKIA[0-9A-Z]{16}"
)


def main(path):
    fails = []
    with zipfile.ZipFile(path) as z:
        names = z.namelist()

        # everything should live under ark/ (or the dist-info/data dirs)
        for n in names:
            low = n.lower()
            core = n.split(".data/purelib/", 1)[-1]  # drop the platform-wheel data prefix
            if not (core.startswith("ark/") or ".dist-info/" in n or ".data/" in n):
                fails.append(f"unexpected entry outside ark/: {n}")
            for bad in FORBIDDEN:
                if bad in low:
                    fails.append(f"forbidden path fragment {bad!r}: {n}")

        # license must ship
        if not any(re.search(r"\.dist-info/licenses/LICENSE$", n) for n in names):
            fails.append("LICENSE missing from dist-info/licenses/")

        # scan every file, including the go binary
        for n in names:
            if n.endswith("/"):
                continue
            data = z.read(n)
            m = SECRET.search(data)
            if m:
                fails.append(f"secret-like VALUE in {n} (pattern {m.group()[:6].decode(errors='replace')}...)")

    if fails:
        print("WHEEL AUDIT FAILED:")
        for f in fails:
            print("  -", f)
        return 1
    print(f"WHEEL AUDIT OK: {path}")
    print(f"  {len(names)} entries; only ark/ payload + bundled bridge + LICENSE + metadata; no secrets")
    return 0


if __name__ == "__main__":
    if len(sys.argv) != 2:
        print("usage: python tests/audit_wheel.py <wheel>")
        sys.exit(2)
    sys.exit(main(sys.argv[1]))
