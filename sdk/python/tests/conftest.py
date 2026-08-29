import os
import pathlib
import subprocess
import sys

import pytest

SDK = pathlib.Path(__file__).resolve().parents[1]   # ~/ark/sdk/python
REPO = pathlib.Path(__file__).resolve().parents[3]  # ~/ark
sys.path.insert(0, str(SDK))                        # make `ark` importable


@pytest.fixture(scope="session", autouse=True)
def _build_bridge():
    binpath = REPO / "ark-bridge-test-bin"
    subprocess.run(["go", "build", "-o", str(binpath), "./cmd/ark-bridge"],
                   cwd=str(REPO), check=True)
    os.environ["ARK_BRIDGE_BIN"] = str(binpath)
    yield
