"""Framework integrations for ARK.

Each integration is a THIN adapter that maps a framework's native lifecycle onto the generic
`ark.trace()` session primitives (`run.record(...)`, `run.check(...)`). Adapters never
reimplement routing, supervision, retries, pricing, or telemetry — those live in Go behind
the session. Integrations import their framework lazily, so `import ark` never requires it.
"""
