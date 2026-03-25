# ARK — AI Runtime Kernel
runtime that learns from execution

> ARK dynamically controls what goes into an LLM’s context — reducing tool overhead by ~99% and learning from every execution.

> A Context Operating System for AI agents that gets smarter every time it runs.


[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue?style=flat-square)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen?style=flat-square)](CONTRIBUTING.md)

---

## ARK Learns

Most AI tools treat every run as a fresh start. ARK remembers.

```
$ ark demo-learn

  RUN 1 (no history):
    1. github-search   0.552  ██████████████████████
    2. github-get      0.382  ███████████████
    3. github-list     0.378  ███████████████

  github-search → FAILED (5000ms timeout)
  github-list   → SUCCESS (120ms)

  RUN 2 (learning from Run 1):
    1. github-list     0.692  ███████████████████████████  [1 call, 100% success]
    2. github-search   0.419  ████████████████              [1 call, 0% success]
    3. github-get      0.382  ███████████████

  RUN 3 (compounding knowledge):
    1. github-list     0.954  ██████████████████████████████████████  [2 calls, 100%]
    2. github-get      0.647  █████████████████████████
    3. github-search   0.419  ████████████████

  github-list:   0.378 → 0.954  (+152.7%)
  github-search: 0.552 → 0.419  (-24.1%)

  ✅ PROVEN: ARK promotes tools that work, demotes tools that fail.
```

ARK updates its decisions after every run — successful tools rise, failing tools fall. This persists across restarts.

This behavior is deterministic and reproducible — not heuristic caching.

## The Problem

MCP tools waste **30% of your context window** before your agent does any work.

Connect 7 MCP servers (GitHub, Slack, Jira, Gmail, Drive, Calendar, Postgres) and **60,000+ tokens are gone** — consumed by tool schemas your agent won't use in the current task. That's context you can't spend on reasoning, memory, or conversation.


## Why not just load all tools?

Because context is the bottleneck.

Every token spent on unused tool schemas is a token not available for reasoning. ARK treats context as a constrained resource and allocates it dynamically per task.

## What ARK Does

ARK manages what your LLM sees. ARK is a runtime that solves three core problems:

**1. Context Efficiency** — loads only 3-5 tools per task instead of all 140.

```
  Raw MCP:  60,468 tokens  (30.2% of context)
  ARK:     ~80 tokens      (0.05% of context)
  Savings:  99.9%
```

**2. Adaptive Execution** — when a tool fails, ARK observes the error type and reacts:

```
  Tool not found    → load more tools
  Tool misunderstood → upgrade to full schema
  Tool crashed      → swap to alternative
  Nothing relevant  → broaden search to other servers
```

**3. Online Learning (persists across runs)** — every execution updates a weighted scoring model:


Every tool is ranked using a weighted score based on runtime signals:
```
  score = (relevance × 0.45)
        + (success_rate × 0.30)
        - (latency × 0.10)
        - (token_cost × 0.05)
        + (confidence × 0.10)
        + memory_bonus
```

Scores and query patterns persist to disk. Run 2 is smarter than Run 1.

## Quick Start

```bash
git clone https://github.com/atripati/ark.git
cd ark

go run ./cmd/ark bench        # see context savings (99.9% reduction)
go run ./cmd/ark demo         # see failure → adapt → recover
go run ./cmd/ark demo-learn   # see ranking improve across 3 runs
go run ./cmd/ark init         # create an agent.yaml config
```

No API keys needed for any demo. Zero external dependencies.

## Run a Real Agent

```bash
# With Anthropic
export ANTHROPIC_API_KEY=sk-ant-...
go run ./cmd/ark run agent.yaml --task "list my github repos"

# With OpenAI
export OPENAI_API_KEY=sk-...
# edit agent.yaml: provider: openai, name: gpt-4o
go run ./cmd/ark run agent.yaml --task "list my github repos"

# With Ollama (free, local)
# edit agent.yaml: provider: ollama, name: llama3
go run ./cmd/ark run agent.yaml --task "list my github repos"
```

## Safety

ARK is safe by default. Dangerous operations require explicit opt-in.

```bash
ark run agent.yaml --task "list repos"          # ✅ reads work
ark run agent.yaml --task "create issue"        # ❌ blocked
ark run agent.yaml --task "create issue" --allow-write  # ✅ opt-in
ark run agent.yaml --task "create issue" --dry-run      # ✅ simulate
```

Additional protections: domain allowlist (only `api.github.com` by default), output sanitization (4000 char cap), full audit traces for every context decision.

## Architecture

```
ark/
├── cmd/ark/                    CLI (run, bench, demo, demo-learn, init)
├── pkg/
│   ├── config/                 YAML config parser + validation
│   │   ├── config.go
│   │   └── config_test.go      14 tests
│   ├── context/                Context engine + ranker + tracer + memory
│   │   ├── manager.go          Budget allocation, compression, eviction
│   │   ├── engine.go           Dynamic engine, tool ranker, context memory
│   │   ├── manager_test.go     7 tests
│   │   └── engine_test.go      12 tests
│   ├── models/                 LLM providers (Anthropic, OpenAI, Ollama)
│   │   └── providers.go        Raw HTTP, retry + backoff, no SDKs
│   ├── runtime/                Agent execution loop
│   │   └── agent.go            task → plan → execute → observe → adapt
│   ├── store/                  Persistent learning
│   │   └── store.go            Channel worker, buffered writes, decay
│   └── tools/                  Real tool execution
│       ├── http.go             HTTP executor, domain allowlist, safety
│       └── github.go           6 GitHub API tools
├── LICENSE                     Apache 2.0
├── NOTICE                      Attribution
└── README.md
```

## How the Scoring Works

Every tool gets a composite score from 6 signals:

| Signal | Weight | What it measures |
|--------|--------|-----------------|
| Relevance | 45% | How well the tool matches the current query |
| Success rate | 30% | Historical success/failure ratio |
| Latency | -10% | Penalty for slow tools |
| Token cost | -5% | Penalty for expensive schemas |
| Confidence | 10% | How much data we have (Bayesian) |
| Memory bonus | varies | Did this tool work for a similar query before? |

Tools with 0% success rate rank last. Tools on a 3+ failure streak get halved scores. Tools that worked for similar queries get a memory bonus. All of this persists across restarts.

## Roadmap

### v0.5 — Learning Runtime ✅ (current)
- [x] Context engine with budget allocation + compression
- [x] Dynamic context: load → observe → expand → retry
- [x] Weighted tool scoring (6 signals)
- [x] Persistent learning (tool stats + query patterns)
- [x] Adaptive execution (error-driven strategy switching)
- [x] Full audit tracer
- [x] 3 LLM providers (Anthropic, OpenAI, Ollama)
- [x] 6 real GitHub API tools
- [x] Safety: domain allowlist, write protection, dry-run
- [x] 33 tests, 5 CLI commands

### v0.6 — Real Connectors
- [ ] MCP server connector (connect to live MCP servers)
- [ ] Slack tools
- [ ] Custom HTTP tool registration via agent.yaml

### v0.7 — Production Storage
- [ ] SQLite backend (replace JSON file store)
- [ ] Multi-agent shared memory
- [ ] Query pattern clustering (semantic, not keyword)

### v1.0 — Production Runtime
- [ ] `ark run` with streaming output
- [ ] Hot-reload agent configs
- [ ] Plugin system for custom tools
- [ ] OpenTelemetry trace export

## Contributing

ARK is designed to be the foundational runtime for AI agents. There's a lot to build.

**Good first issues:**
- Add tiktoken-based token counting
- Write MCP server connector
- Add Slack tool set
- Add `--trace=summary` mode
- SQLite store backend

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup instructions.

## Why "ARK"

**A**I **R**untime **K**ernel. A vessel built to carry what matters through turbulent waters. AI agent development is a flood of accidental complexity. ARK carries your agent logic safely above it.

## License

Apache 2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).

Copyright 2026 Abhishek Tripathi and ARK Contributors.