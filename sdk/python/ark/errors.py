"""SDK error types. Runtime/bridge failures map to ArkBridgeError; using experimental
supervision without opting in raises ArkSupervisionDisabled."""


class ArkError(Exception):
    """Base class for all ARK SDK errors."""


class ArkBridgeError(ArkError):
    """The Go bridge/runtime failed, was unreachable, or returned an error."""


class ArkSupervisionDisabled(ArkError):
    """Constrained supervision was used without opting in (it is experimental/off by default)."""
