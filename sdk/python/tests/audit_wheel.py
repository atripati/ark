"""Cross-platform wheel content + secret audit. Usage: python tests/audit_wheel.py <wheel>

Exits non-zero (and explains) if the wheel contains anything beyond the SDK, the bundled
bridge, the license, and package metadata — or if any file (including the Go binary) contains
a secret-VALUE pattern. Never prints a secret. Pure stdlib so it runs on every CI platform.
"""
import re
import sys
import zipfile

# forbidden path fragments (case-insensitive) that must never appear in the distribution
FORBIDDEN = [
    ".env", "agent.yaml", "ark-memory", "ark-router", "ark-governor", "ark-credential",
    "/.git", ".venv", "credblock", "/tests/", "/examples/", "benchmark", "research",
    ".jsonl", "build_wheel.sh",
]
# actual secret-VALUE signatures (not env-var NAMES like OPENAI_API_KEY, which are fine)
SECRET = re.compile(
    rb"sk-proj-[A-Za-z0-9]{6,}|sk-ant-[A-Za-z0-9]{6,}|ghp_[A-Za-z0-9]{20,}|"
    rb"xoxb-[A-Za-z0-9-]{10,}|-----BEGIN [A-Z ]*PRIVATE KEY-----|AKIA[0-9A-Z]{16}"
)


def main(path):
    fails = []
    with zipfile.ZipFile(path) as z:
        names = z.namelist()

        # 1. structure: every payload entry is under an ark/ package or the dist-info/data dir
        for n in names:
            low = n.lower()
            core = n.split(".data/purelib/", 1)[-1]  # normalize the platform-wheel data dir
            if not (core.startswith("ark/") or ".dist-info/" in n or ".data/" in n):
                fails.append(f"unexpected entry outside ark/: {n}")
            for bad in FORBIDDEN:
                if bad in low:
                    fails.append(f"forbidden path fragment {bad!r}: {n}")

        # 2. license must ship
        if not any(re.search(r"\.dist-info/licenses/LICENSE$", n) for n in names):
            fails.append("LICENSE missing from dist-info/licenses/")

        # 3. secret-value scan over every file, including the compiled bridge
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
