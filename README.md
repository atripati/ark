# ARK — AI Runtime Kernel

> ARK dynamically controls what goes into an LLM’s context — reducing tool overhead by ~99%, learning from every execution, and attributing cost per decision.

> An economically rational runtime for AI agents. Context optimization. Adaptive execution. Decision-level cost attribution.


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
  score = (relevance × 0.40)
        + (success_rate × 0.30)
        - (latency × 0.05)
        - (cost × 0.15)          ← real dollar cost, not token estimate
        + (confidence × 0.10)
        + memory_bonus
```

Scores and query patterns persist to disk. Run 2 is smarter than Run 1.

**4. Decision-Level Cost Attribution** — every step has a price tag:

```
  💰 Cost Report: ark-run
  Model: GPT-4o Mini
  Total Cost: $0.000493
    Input:  $0.000209 (1395 tokens)
    Output: $0.000284 (473 tokens)

  Decision Cost Graph:
    Step 1 [tool_call: github_list_repos]  $0.000054  (in:303 out:15 tokens)
    Step 2 [complete]                      $0.000439  (in:1092 out:458 tokens)
```

Cost flows back into ranking: expensive tools get demoted, cheap reliable tools get promoted. ARK doesn’t just track cost — it acts on cost.

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

## Stress Tested

ARK was stress tested with 30 sequential and parallel runs. Results:

```
Sequential (20 runs):  20/20 completed, 0 crashes, 0 hallucinated data
Parallel (10 runs):    10/10 completed, 0 crashes, 0 state corruption

Failures handled correctly:
  401 (no auth)     → LLM retried with user param → succeeded
  Tool hallucinated → "github_get_repos" rejected, valid tools listed → LLM self-corrected
  Timeout           → clean termination with structured error
```

**Zero hallucinated answers across all 30 runs.** Every answer was grounded in real API data.

## Production Guarantees

| Guarantee | How |
|-----------|-----|
| No hallucination when tools available | Grounding gate blocks ungrounded answers |
| No invalid tool calls | RequiredParams validated before execution |
| No runaway loops | MaxSteps=5, TotalTimeout=120s, per-tool retry budget |
| No silent failures | Structured error taxonomy (auth/404/429/timeout/params) |
| No state corruption | Deep-copy persistence, snapshot semantics, race-detector clean |
| Deterministic ranking | Sorted IDs, stable sort with tiebreaker |
| Observable | RuntimeMetrics, TraceJSON export, per-tool latency P50 |
| Cost-aware | Per-decision cost graph, budget enforcement, cost-weighted ranking |

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
│   │   ├── agent.go            ReAct loop, grounding gate, metrics, trace export
│   │   └── agent_test.go       7 tests
│   ├── cost/                   Decision-level cost attribution
│   │   ├── cost.go             Tracker, pricing, budget, attribution, aggregation
│   │   └── cost_test.go        14 tests
│   ├── store/                  Persistent learning
│   │   ├── store.go            Channel worker, snapshot semantics, decay
│   │   └── store_test.go       5 tests
│   └── tools/                  Real tool execution
│       ├── http.go             Router, param validation, safety layer
│       ├── http_test.go        8 tests
│       └── github.go           6 GitHub API tools with RequiredParams
├── LICENSE                     Apache 2.0
├── NOTICE                      Attribution
└── README.md

67 tests | Race detector clean | 30-run stress test passed
```

## How the Scoring Works

Every tool gets a composite score from 6 signals:

| Signal | Weight | What it measures |
|--------|--------|-----------------|
| Relevance | 40% | How well the tool matches the current query |
| Success rate | 30% | Historical success/failure ratio |
| Latency | -5% | Penalty for slow tools |
| **Cost** | **-15%** | **Real dollar cost per call (not token estimate)** |
| Confidence | 10% | How much data we have (Bayesian) |
| Memory bonus | varies | Did this tool work for a similar query before? |

Cost is a first-class decision signal. A tool that costs 10x more but has the same success rate will be ranked lower. This makes ARK economically rational — it optimizes for value, not just accuracy.

Tools with 0% success rate rank last. Tools on a 3+ failure streak get halved scores. All of this persists across restarts.

## Roadmap

### v0.5 — Learning Runtime ✅
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

### v0.6 — Production Hardening ✅
- [x] Grounding gate (no hallucination when tools available)
- [x] Parameter validation (RequiredParams on all tools)
- [x] Error taxonomy (auth/404/429/timeout/params → distinct strategies)
- [x] Monotonic learning (scores never regress on success)
- [x] Deterministic ranking (sorted IDs, stable sort)
- [x] Safe persistence (deep-copy, snapshot semantics)
- [x] Runtime metrics + TraceJSON export
- [x] Execution bounds (MaxSteps, TotalTimeout, per-tool retry budget)
- [x] 53 tests, race detector clean, 30-run stress test

### v0.7 — Cost Attribution ✓ (current)
- [x] Decision-level cost graph (per-step, per-tool, per-action)
- [x] Provider-aware pricing (Anthropic, OpenAI, Ollama)
- [x] Accurate input/output token split (verified with OpenAI)
- [x] Cost-aware ranking (15% weight, expensive tools demoted)
- [x] Budget enforcement (max_cost_per_task stops execution)
- [x] Cost attribution (user_id, feature_id, session_id)
- [x] Cross-task aggregation (cost by feature, by user, by tool)
- [x] Human-readable trace (real dollars + score impact)
- [x] 67 tests, race detector clean

### v0.8 — Real Connectors (next)
- [ ] MCP server connector (connect to live MCP servers)
- [ ] Web search tool (Brave/SerpAPI)
- [ ] File system tools (read/write local files)
- [ ] Custom HTTP tool registration via agent.yaml

### v0.9 — Production Storage
- [ ] SQLite backend (replace JSON file store)
- [ ] Multi-agent shared memory
- [ ] Query pattern clustering (semantic, not keyword)

### v1.0 — Production Runtime
- [ ] `ark run` with streaming output
- [ ] Hot-reload agent configs
- [ ] Plugin system for custom tools
- [ ] OpenTelemetry trace export
- [ ] `go get github.com/atripati/ark` library mode

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
