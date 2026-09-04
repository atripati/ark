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


_NO_BRIDGE_MSG = (
    "ark-bridge binary not found. An installed `ark-agent-runtime` wheel bundles a matching "
    "bridge automatically. From a source checkout, build one and point ARK_BRIDGE_BIN at it: "
    "`go build -o ark-bridge-bin ./cmd/ark-bridge && export ARK_BRIDGE_BIN=$PWD/ark-bridge-bin`."
)


def _resolve_binary(override: "str | None", bundled: "str | None", path_bridge: "str | None") -> "str | None":
    """Pure binary-resolution priority (kept separate so it is directly testable):

      1. explicit ARK_BRIDGE_BIN override (developer/operator intent — validated by the handshake)
      2. the bridge bundled inside the installed wheel (the release-correct path)
      3. an `ark-bridge` deliberately installed on PATH (development/ops convenience)
      else: None (caller raises)

    Invariant: an accidental stale developer binary is NEVER auto-preferred over the bundled
    release bridge — the bundled bridge outranks PATH, and there are no machine-specific ~/… guesses.
    """
    if override:
        return override
    if bundled:
        return bundled
    if path_bridge:
        return path_bridge
    return None


def _find_binary() -> str:
    override = os.environ.get("ARK_BRIDGE_BIN") or None
    if override and not os.path.exists(override):
        # an explicit override that does not exist is a loud error — never silently fall back to a
        # different (possibly stale) bridge than the operator asked for.
        raise ArkBridgeError(f"ARK_BRIDGE_BIN points to a missing file: {override}")
    bundled = _bundled_binary()
    if bundled:
        _ensure_executable(bundled)
    chosen = _resolve_binary(override, bundled, shutil.which("ark-bridge"))
    if chosen is None:
        raise ArkBridgeError(_NO_BRIDGE_MSG)
    return chosen


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
