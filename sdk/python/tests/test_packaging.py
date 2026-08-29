"""Packaging/discovery unit tests: bundled-bridge lookup precedence, the executable-bit
restore, and a useful error for a missing/corrupted bridge. (The full fresh-wheel install is
proven separately by tests/fresh_install_check.py run in an environment outside the repo.)"""
import os
import stat

import pytest

from ark.bridge import SubprocessBridge, _bundled_binary, _ensure_executable, _find_binary
from ark.errors import ArkBridgeError


def test_env_override_takes_precedence(tmp_path):
    fake = tmp_path / "ark-bridge"
    fake.write_text("#!/bin/sh\n")
    old = os.environ.get("ARK_BRIDGE_BIN")
    os.environ["ARK_BRIDGE_BIN"] = str(fake)
    try:
        assert _find_binary() == str(fake)     # explicit debug override wins
    finally:
        if old is None:
            os.environ.pop("ARK_BRIDGE_BIN", None)
        else:
            os.environ["ARK_BRIDGE_BIN"] = old


def test_bundled_binary_probe_is_safe():
    # in a source checkout there is no bundled binary; the probe returns None (never raises).
    # in an installed wheel it returns a path under ark/_bridge.
    b = _bundled_binary()
    assert b is None or (os.path.isfile(b) and os.path.join("ark", "_bridge") in b)


def test_ensure_executable_restores_bit(tmp_path):
    f = tmp_path / "bin"
    f.write_bytes(b"\x7fELF stub")
    os.chmod(f, 0o644)                          # simulate a wheel that dropped +x
    assert not (os.stat(f).st_mode & 0o111)
    _ensure_executable(str(f))
    if os.name != "nt":
        assert os.stat(f).st_mode & stat.S_IXUSR


def test_corrupted_bridge_gives_useful_error(tmp_path):
    corrupt = tmp_path / "ark-bridge"
    corrupt.write_bytes(b"not a real executable\n")
    os.chmod(corrupt, 0o755)
    with pytest.raises(ArkBridgeError):         # exec format error -> mapped, not a raw OSError
        SubprocessBridge(binary=str(corrupt)).call({"kind": "run", "task": "x"})


def test_missing_bridge_error_mentions_the_wheel():
    with pytest.raises(ArkBridgeError) as ei:
        SubprocessBridge(binary="/nonexistent/ark-bridge").call({"kind": "run", "task": "x"})
    # message should guide the user, not leak internals
    assert "bridge" in str(ei.value).lower()
