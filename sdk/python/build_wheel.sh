#!/usr/bin/env bash
# Build one platform-specific ark-runtime wheel: compile the Go bridge, drop it into the
# package as data, then build the wheel. Run from a CLEAN checkout of committed source.
#
# Usage:
#   ./build_wheel.sh                      # current host platform
#   GOOS=linux  GOARCH=amd64 ./build_wheel.sh   # cross-compile a Linux x86_64 wheel
#   GOOS=linux  GOARCH=arm64 ./build_wheel.sh
#   GOOS=darwin GOARCH=amd64 ./build_wheel.sh
#   GOOS=darwin GOARCH=arm64 ./build_wheel.sh
#   GOOS=windows GOARCH=amd64 ./build_wheel.sh  # produces ark-bridge.exe
#
# The wheel's platform TAG is derived from the Python building it. To produce a wheel for a
# platform other than the host, either run this on that platform (CI matrix) or pass
# --plat-name to `python -m build` (the Go binary itself cross-compiles cleanly via GOOS/GOARCH).
# The five release targets are: macosx arm64, macosx x86_64, linux x86_64, linux arm64,
# windows x86_64. Only build/publish the targets you have actually produced and tested.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"        # sdk/python
REPO="$(cd "$HERE/../.." && pwd)"            # repo root (has go.mod)
PY="${PYTHON:-python3}"

GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"
BIN_NAME="ark-bridge"
[ "$GOOS" = "windows" ] && BIN_NAME="ark-bridge.exe"
OUT="$HERE/ark/_bridge/$BIN_NAME"

echo ">> building bridge for $GOOS/$GOARCH -> ark/_bridge/$BIN_NAME"
( cd "$REPO" && CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -trimpath -ldflags="-s -w" -o "$OUT" ./cmd/ark-bridge )
chmod +x "$OUT" 2>/dev/null || true

# On macOS, the wheel advertises ARK_MACOS_MIN (default 12.0, see setup.py). Verify the
# binary's real Mach-O floor is not NEWER than that, so we never advertise support we lack.
if [ "$GOOS" = "darwin" ] && command -v otool >/dev/null 2>&1; then
  MINOS=$(otool -l "$OUT" | awk '/LC_BUILD_VERSION/{f=1} f&&/minos/{print $2; exit}')
  WANT="${ARK_MACOS_MIN:-12.0}"
  echo ">> bridge minos=$MINOS ; wheel will advertise macOS $WANT"
  # numeric compare major.minor; fail if binary needs newer than advertised
  awk -v b="$MINOS" -v w="$WANT" 'BEGIN{split(b,B,".");split(w,W,".");
    if (B[1]>W[1] || (B[1]==W[1] && B[2]>W[2])) {exit 1}}' \
    || { echo "!! binary requires macOS $MINOS but wheel would advertise $WANT — set ARK_MACOS_MIN=$MINOS"; exit 1; }
fi

echo ">> building wheel"
( cd "$HERE" && rm -rf build dist *.egg-info && "$PY" -m build --wheel )

echo ">> done. wheel(s):"
ls -1 "$HERE/dist/"*.whl
