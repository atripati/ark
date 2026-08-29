"""Packaging shim: metadata lives in pyproject.toml; this only customizes the wheel tag.

The package bundles a standalone Go bridge binary — native to the platform, but usable from
ANY Python 3 (it is a subprocess, not a C extension). So the wheel must be platform-specific
yet ABI-independent: `py3-none-<platform>` (e.g. py3-none-macosx_15_0_arm64), not cp3x-*.
"""
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
        return "py3", "none", plat  # any Python 3, no ABI lock, this platform only


setup(cmdclass={"bdist_wheel": bdist_wheel})
