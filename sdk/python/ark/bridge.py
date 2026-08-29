"""Go<->Python transport. Default: invoke the local ark-bridge binary as a subprocess.

The transport is an IMPLEMENTATION DETAIL: the public ARK API does not depend on this
being a subprocess, so it can later be replaced (e.g. a local service) without changing
the Python API. Any object with a ``call(request: dict) -> dict`` method works.
"""
from __future__ import annotations

import json
import os
import shutil
import subprocess

from .errors import ArkBridgeError


def _bundled_binary() -> "str | None":
    """The bridge shipped inside the installed wheel (ark/_bridge/ark-bridge[.exe])."""
    name = "ark-bridge.exe" if os.name == "nt" else "ark-bridge"
    p = os.path.join(os.path.dirname(os.path.abspath(__file__)), "_bridge", name)
    return p if os.path.isfile(p) else None


def _ensure_executable(path: str) -> None:
    """Wheels don't always preserve the +x bit for package data; restore it if missing."""
    if os.name == "nt":
        return
    try:
        mode = os.stat(path).st_mode
        if not (mode & 0o111):
            os.chmod(path, mode | 0o111)
    except OSError:
        pass


def _find_binary() -> str:
    # 1. explicit override — for developers/debugging (not needed by an installed package)
    b = os.environ.get("ARK_BRIDGE_BIN")
    if b and os.path.exists(b):
        return b
    # 2. the bundled bridge inside the installed wheel — the normal path, zero setup
    bundled = _bundled_binary()
    if bundled:
        _ensure_executable(bundled)
        return bundled
    # 3. a bridge on PATH, then a source-checkout build (developer convenience)
    p = shutil.which("ark-bridge")
    if p:
        return p
    for guess in (os.path.expanduser("~/ark/ark-bridge-bin"),
                  os.path.expanduser("~/ark/ark-bridge-test-bin")):
        if os.path.exists(guess):
            return guess
    raise ArkBridgeError(
        "ark-bridge binary not found. An installed `ark-runtime` wheel bundles it "
        "automatically; if you are running from a source checkout, build it with "
        "`go build -o ark-bridge-bin ./cmd/ark-bridge` and set ARK_BRIDGE_BIN."
    )


class SubprocessBridge:
    def __init__(self, binary: str | None = None, timeout: int = 120):
        self._bin = binary or _find_binary()
        self._timeout = timeout

    def call(self, request: dict) -> dict:
        try:
            p = subprocess.run([self._bin], input=json.dumps(request).encode(),
                               capture_output=True, timeout=self._timeout)
        except (OSError, subprocess.TimeoutExpired) as e:
            raise ArkBridgeError(f"bridge invocation failed: {e}") from e
        out = (p.stdout or b"").decode().strip()
        if not out:
            err = (p.stderr or b"").decode()[:300]
            raise ArkBridgeError(f"bridge produced no output (exit {p.returncode}): {err}")
        data = _parse_json(out)
        if data is None:
            raise ArkBridgeError(f"bridge returned non-JSON: {out[:300]}")
        if isinstance(data, dict) and data.get("error"):
            raise ArkBridgeError(data["error"])
        return data


def _parse_json(out: str):
    """The bridge emits a single JSON object (possibly multi-line/indented). Parse the
    whole payload; tolerate any surrounding text by falling back to the outermost braces."""
    try:
        return json.loads(out)
    except json.JSONDecodeError:
        i, j = out.find("{"), out.rfind("}")
        if 0 <= i < j:
            try:
                return json.loads(out[i:j + 1])
            except json.JSONDecodeError:
                return None
        return None
