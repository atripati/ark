"""SDK error types. Runtime/bridge failures map to ArkBridgeError; using experimental
supervision without opting in raises ArkSupervisionDisabled."""


class ArkError(Exception):
    """Base class for all ARK SDK errors."""


class ArkBridgeError(ArkError):
    """The Go bridge/runtime failed, was unreachable, or returned an error."""


class ArkSupervisionDisabled(ArkError):
    """Constrained supervision was used without opting in (it is experimental/off by default)."""


class ArkSupervisionError(ArkError):
    """A supervision check was misconfigured or its evidence was malformed. ARK fails CLOSED:
    an unknown constraint, a missing scope, or malformed/typo'd evidence is refused here rather
    than silently treated as an authorization to execute."""


class ArkProviderError(ArkError):
    """An evidence provider was requested but not registered, or was not callable. A provider that
    RAISES at resolve time does not raise this — it fails closed to a REQUIRE_EVIDENCE verdict."""
