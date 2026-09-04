"""Bridge binary discovery: a stale developer binary must never be auto-preferred over the
bundled release bridge; an explicit override wins but is still handshake-validated."""
import os

import pytest

from ark import bridge as B
from ark.errors import ArkBridgeError


# ---- pure resolution order ----

def test_resolve_prefers_override():
    assert B._resolve_binary("/x/override", "/x/bundled", "/x/path") == "/x/override"


def test_resolve_bundled_beats_path():
    # the key invariant: the bundled (release-correct) bridge outranks whatever is on PATH.
    assert B._resolve_binary(None, "/x/bundled", "/x/path-stale") == "/x/bundled"


def test_resolve_path_only_when_no_override_or_bundled():
    assert B._resolve_binary(None, None, "/x/path") == "/x/path"


def test_resolve_none_when_nothing():
    assert B._resolve_binary(None, None, None) is None


def test_no_machine_specific_home_guesses():
    # the old ~/ark/ark-bridge-* fallbacks are gone (they could pick up ANY user's stale binary).
    import inspect
    src = inspect.getsource(B._find_binary) + inspect.getsource(B._resolve_binary)
    assert "~/ark" not in src and "ark-bridge-bin" not in src


# ---- _find_binary integration (monkeypatched) ----

def test_bundled_wins_over_path(monkeypatch, tmp_path):
    bundled = tmp_path / "ark-bridge"
    bundled.write_text("#!/bin/sh\n")
    monkeypatch.delenv("ARK_BRIDGE_BIN", raising=False)
    monkeypatch.setattr(B, "_bundled_binary", lambda: str(bundled))
    monkeypatch.setattr(B.shutil, "which", lambda name: "/usr/local/bin/ark-bridge-STALE")
    assert B._find_binary() == str(bundled)


def test_explicit_override_wins(monkeypatch, tmp_path):
    ovr = tmp_path / "my-bridge"
    ovr.write_text("#!/bin/sh\n")
    monkeypatch.setenv("ARK_BRIDGE_BIN", str(ovr))
    monkeypatch.setattr(B, "_bundled_binary", lambda: "/x/bundled")
    assert B._find_binary() == str(ovr)


def test_missing_override_fails_loud(monkeypatch, tmp_path):
    monkeypatch.setenv("ARK_BRIDGE_BIN", str(tmp_path / "does-not-exist"))
    monkeypatch.setattr(B, "_bundled_binary", lambda: "/x/bundled")
    with pytest.raises(ArkBridgeError):
        B._find_binary()  # must NOT silently fall back to bundled


def test_no_bridge_anywhere_errors(monkeypatch):
    monkeypatch.delenv("ARK_BRIDGE_BIN", raising=False)
    monkeypatch.setattr(B, "_bundled_binary", lambda: None)
    monkeypatch.setattr(B.shutil, "which", lambda name: None)
    with pytest.raises(ArkBridgeError):
        B._find_binary()
