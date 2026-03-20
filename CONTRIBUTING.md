# Contributing to ARK

Thanks for your interest in contributing to ARK! We're building the foundational runtime for AI agents, and every contribution moves the ecosystem forward.

## Quick Start

```bash
git clone https://github.com/atripati/ark.git
cd ark
go test ./...
go run ./cmd/ark bench
```

## Project Structure

```
ark/
├── cmd/ark/          # CLI entry point
├── pkg/
│   ├── context/      # Context manager (v0.1 focus)
│   ├── memory/       # Shared memory graph (v0.2)
│   ├── runtime/      # Agent execution loop (v0.3)
│   ├── tracer/       # Built-in observability (v0.3)
│   └── models/       # Model provider abstraction (v0.4)
├── internal/config/  # YAML config parsing
├── examples/         # Example agents
└── docs/             # Documentation
```

## Good First Issues

If you're new to the project, these are great starting points:

### Easy
- **Better token counting**: Replace `EstimateTokens()` with tiktoken-based counting
- **Add more tests**: Edge cases for context eviction, concurrent access
- **YAML config parser**: Parse `agent.yaml` into Go structs

### Medium  
- **MCP client**: Connect to a real MCP server and register its tools
- **SQLite memory**: Implement the memory graph backend
- **OpenTelemetry export**: Export traces in OTLP format

### Hard
- **Agent execution loop**: The core runtime that runs tool → think → act cycles
- **Model router**: Abstract multiple LLM providers behind a common interface
- **Shared memory graph**: Multi-agent knowledge sharing with permissions

## Code Style

- Follow standard Go conventions (`gofmt`, `golint`)
- Every exported function needs a doc comment
- Tests are required for new features
- Benchmark critical paths

## Pull Request Process

1. Fork the repo and create a feature branch
2. Write tests for your changes
3. Run `go test ./...` and ensure everything passes
4. Submit a PR with a clear description of what and why

## Architecture Principles

1. **Single binary**: ARK should always be deployable as one binary with zero dependencies
2. **Composable packages**: Each package in `pkg/` should work standalone
3. **Observability by default**: Every action should be traceable without extra setup
4. **Model agnostic**: Never hard-code assumptions about a specific LLM provider
5. **Performance matters**: Context management is on the hot path — benchmark everything

## License

By contributing, you agree that your contributions will be licensed under the Apache 2.0 License.
