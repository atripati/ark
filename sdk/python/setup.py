"""Packaging shim: metadata lives in pyproject.toml; this only customizes the wheel tag.

The package bundles a standalone Go bridge binary — native to the platform, but usable from
ANY Python 3 (it is a subprocess, not a C extension). So the wheel must be platform-specific
yet ABI-independent: `py3-none-<platform>` (e.g. py3-none-macosx_12_0_arm64), not cp3x-*.

The macOS floor in the tag must match the bundled binary's ACTUAL minimum OS (its Mach-O
`minos`), not the macOS version the building Python happened to be compiled against
(homebrew Python reports 15.0). A Go 1.26 darwin/arm64 binary has minos 12.0 (Go dropped
macOS 11), so the honest floor is 12.0 — this covers macOS 12–15, versus the needlessly
narrow 15.0 that sysconfig would otherwise stamp. Override with ARK_MACOS_MIN only when the
bundled binary is actually built for a different floor (e.g. an older Go for 11.0).
"""
import os

from setuptools import setup

try:  # setuptools >= 70 vendors bdist_wheel
    from setuptools.command.bdist_wheel import bdist_wheel as _bdist_wheel
except ImportError:  # older: fall back to the wheel package
    from wheel.bdist_wheel import bdist_wheel as _bdist_wheel


class bdist_wheel(_bdist_wheel):
    def finalize_options(self):
        super().finalize_options()
        self.root_is_pure = False  # contains a native binary -> platform-specific wheel

    def get_tag(self):
        _python, _abi, plat = super().get_tag()
        # Explicit override for platforms where the building Python's tag is wrong for a
        # bundled standalone binary. Linux uses this: a CGO_ENABLED=0 bridge is fully static
        # (no libc), so it is honestly manylinux2014-compatible even though the build host
        # reports a bare `linux_x86_64`. CI sets ARK_WHEEL_PLAT=manylinux2014_{x86_64,aarch64}.
        forced = os.environ.get("ARK_WHEEL_PLAT")
        if forced:
            return "py3", "none", forced
        # macOS: pin the version component to the bundled binary's real floor (must be <= the
        # binary's minos, verified at build time). Default 12.0 = the Go 1.26 macOS floor.
        target = os.environ.get("ARK_MACOS_MIN", "12.0")
        if plat.startswith("macosx_"):
            arch = plat.rsplit("_", 1)[-1]
            major, _, minor = target.partition(".")
            plat = f"macosx_{major}_{minor or '0'}_{arch}"
        return "py3", "none", plat  # any Python 3, no ABI lock, this platform only


setup(cmdclass={"bdist_wheel": bdist_wheel})
