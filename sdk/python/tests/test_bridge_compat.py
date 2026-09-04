"""Bridge<->SDK compatibility handshake: a stale/incompatible bridge must fail LOUDLY, never
silently downgrade supervision guarantees."""
import os

import pytest

from ark import ARK
from ark.errors import ArkBridgeError
from ark.session import check_bridge_compat, PROTOCOL_VERSION, REQUIRED_CAPABILITIES


def _good_reply():
    return {"protocol_version": PROTOCOL_VERSION, "capabilities": sorted(REQUIRED_CAPABILITIES) + ["audit"]}


def test_accepts_compatible_bridge():
    check_bridge_compat(_good_reply())  # must not raise


def test_rejects_no_capabilities():
    with pytest.raises(ArkBridgeError):
        check_bridge_compat({"run_id": "x"})  # an OLD bridge: no protocol/capabilities at all


def test_rejects_incompatible_protocol():
    r = _good_reply()
    r["protocol_version"] = PROTOCOL_VERSION + 1
    with pytest.raises(ArkBridgeError):
        check_bridge_compat(r)


def test_rejects_missing_required_capability():
    r = _good_reply()
    r["capabilities"] = [c for c in r["capabilities"] if c != "trusted_provider"]
    with pytest.raises(ArkBridgeError) as e:
        check_bridge_compat(r)
    assert "trusted_provider" in str(e.value)


def test_live_bridge_advertises_required_capabilities():
    info = ARK().bridge_info()  # the ACTUAL bridge under test (conftest built it)
    assert info.get("protocol_version") == PROTOCOL_VERSION
    assert REQUIRED_CAPABILITIES <= set(info.get("capabilities") or [])


# The real trap: a stale bridge binary resolved without ARK_BRIDGE_BIN. If a pre-hardening
# binary exists on this machine, prove opening a trace against it fails loudly.
def test_stale_bridge_binary_is_rejected():
    stale = os.path.expanduser("~/ark/ark-bridge-bin")
    if not os.path.exists(stale):
        pytest.skip("no stale bridge binary present to test against")
    # is it actually stale? (a hardened bridge would advertise capabilities)
    import subprocess, json
    out = subprocess.run([stale], input=b'{"kind":"hello"}', capture_output=True, timeout=30).stdout
    try:
        caps = set((json.loads(out or b"{}") or {}).get("capabilities") or [])
    except Exception:
        caps = set()
    if REQUIRED_CAPABILITIES <= caps:
        pytest.skip("the binary at ~/ark/ark-bridge-bin is already hardened; nothing to test")
    old = os.environ.get("ARK_BRIDGE_BIN")
    os.environ["ARK_BRIDGE_BIN"] = stale
    try:
        with pytest.raises(ArkBridgeError):
            with ARK(supervision="experimental").trace("t"):
                pass
    finally:
        if old is None:
            os.environ.pop("ARK_BRIDGE_BIN", None)
        else:
            os.environ["ARK_BRIDGE_BIN"] = old
