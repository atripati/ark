"""Home of the bundled ARK bridge binary.

At build time the platform-native `ark-bridge` (or `ark-bridge.exe`) executable is compiled
from the Go source (cmd/ark-bridge) and placed in this directory, then shipped as package
data inside the wheel. `ark.bridge._find_binary` locates it here, so an installed package
needs no Go toolchain and no ARK_BRIDGE_BIN. The binary itself is a build artifact and is not
committed to source control.
"""
