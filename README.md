# ARK — AI Runtime Kernel

**Cut agent costs by 80–90%. Make every step verifiable. Ship agents that don't hallucinate.**

ARK doesn't let the model control the system. The runtime does.

It decides which tools run, which model handles each step, how much each decision costs, and whether the output is valid — before anything reaches the user. The model's job is reduced to what it's good at: language. Everything else is governed.

```
┌─ ARK Agent: Task "ark-run"
│  find the top 5 most popular JavaScript backend frameworks on GitHub
│
├─ Task type: ranking
├─ Context: loaded 3 tools (93 tokens) [strategy: minimal]
├─ Step 1: TOOL_CALL — github_search_repos
│  ↳ ✓ Verified (confidence: 88%)
├─ Step 2: ✓ Reasoning verified (confidence: 87%)
├─ Step 2: COMPLETE — NestJS (75K★), Express (69K★), Socket.IO (63K★)...
│
└─ Done: 2 steps, 1406 tokens, 6.5s | Cost: $0.005

  💰 Cost: $0.005 per task (not $0.05)
  🧠 Routing: gpt-4o-mini → tool call, gpt-4o → reasoning
  🔍 Governor: verified both steps, variable confidence
  📊 Learning: 258 observations, 99% success rate
```

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue?style=flat-square)](LICENSE)
[![Tests](https://img.shields.io/badge/Tests-156%20passing-brightgreen?style=flat-square)]()
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen?style=flat-square)](CONTRIBUTING.md)

---

## Why ARK Exists

Current agent frameworks have a fundamental design flaw: **they let the model make infrastructure decisions.**

The model picks which tools to call. The model decides if the output is good enough. The model controls retry logic. This is like letting a database query decide its own execution plan.

ARK inverts this. The runtime makes every infrastructure decision. The model only does language work.

| What other frameworks do | What ARK does |
|--------------------------|---------------|
| Dump all 140 tool schemas into prompt | Load 3 relevant tools per task (99.9% context reduction) |
| Use one model for every step | Route each step: cheap model for tool calls, strong for reasoning |
| No cost visibility until the bill | Per-decision cost graph — every step has a dollar amount |
| Trust model output blindly | Cognitive governor verifies every output with calibrated confidence |
| Every run starts from zero | Bayesian learning persists across runs — Run 2 is smarter than Run 1 |
| Forward raw queries to APIs | Query intelligence: noise stripping, language detection, semantic scoring |

---

## What Makes ARK Different

### 1. Cognitive Governor

The governor is the core of ARK. It sits between every model call and the user, enforcing trust.

```
Task → Classify → Predict failure → Select model → Execute → Verify → Learn → Output
         ↑                                                       │
         └───────────── Registry feeds back ─────────────────────┘
```

Every output gets a calibrated confidence score — not a flat number, but a signal computed from model history, tool track record, response quality, and grounding:

```
├─ Step 1: TOOL_CALL — github_search_repos
│  ↳ ✓ Verified (confidence: 88%)     ← model proven on this tool
├─ Step 2: ✓ Reasoning verified (confidence: 87%)  ← grounded in tool data
```

**Confidence is variable, not decorative:**
- 85-88% → grounded reasoning with proven model+tool combo
- 75% → pure reasoning without tool data
- 50% → ungrounded (model answered without calling tools)
- Below 60% → forces strong model on next step automatically

The governor also:
- **Predicts failures** before execution — skips models with bad track records
- **Injects experience** into prompts ("Previous attempts with this tool had failures. Be more careful.")
- **Tracks per-task-type performance** — learns that gpt-4o-mini handles retrieval but struggles with ranking
- **Records task-type observations** — the registry knows performance per domain, not just per model

### 2. Per-Step Model Routing

ARK doesn't use one model for everything. Each step gets the right model:

```
🧠 Model Routing:
  Step 1 [tool_call] gpt-4o-mini  (tool calls are simple, using fast model to save cost)
  Step 2 [complete]  gpt-4o       (final reasoning benefits from strong model)

  Fast model: 1 step | Strong model: 1 step
```

The router learns from failures. If the fast model fails on a step type, ARK promotes it to the strong model next time. Learning persists across restarts.

### 3. Search Intelligence (7-Phase Pipeline)

Most agent frameworks send the user's raw query to an API and hope for the best. ARK owns the entire retrieval pipeline:

```
"find the top 5 most popular JavaScript backend frameworks on GitHub"

Phase 1: Query Intelligence
  → Strip noise: "javascript frameworks"
  → Detect language: JavaScript
  → Add ecosystem hint: +nodejs
  → Skip API language filter for JS (TypeScript repos also needed)

Phase 2: Retrieval
  → GitHub API: sort=stars, order=desc, per_page=30

Phase 3: Language Filter
  → Accept: JavaScript + TypeScript (NestJS, Fastify are TS)
  → Reject: Java, Python, etc.

Phase 4: Junk Filter
  → Remove: awesome-lists, tutorials, cheatsheets, interview prep

Phase 5: Semantic Scoring (3-tier)
  → "web framework" in description  → 2.0× boost (Express, Fastify)
  → No framework signal             → 0.3× penalty (unknown relevance)
  → Anti-signal (ORM, CSS, testing) → 0.01× buried (Mocha, MUI)

Phase 6: Diversity Guard
  → Max 2 repos per owner (prevents Django/Django-channels clustering)

Phase 7: Simplify
  → Essential fields only → LLM explains, never selects
```

The LLM never decides what's relevant. The runtime ranks. The LLM explains.

### 4. Decision-Level Cost Attribution

Every step has a price tag. Cost feeds back into ranking.

```
💰 Cost Report: ark-run
  Total Cost: $0.004840
    Input:  $0.002750 (1100 tokens)
    Output: $0.002090 (209 tokens)

  Decision Cost Graph:
    Step 1 [tool_call: github_search_repos]  $0.000990
    Step 2 [complete]                        $0.003850
```

### 5. Task Classification

ARK classifies every task before execution and adapts its behavior:

```
├─ Task type: ranking          ← detected from "top", "most popular"
```

| Task Type | Behavior |
|-----------|----------|
| ranking | Strong model for reasoning, search tool preferred |
| retrieval | Cheap model sufficient, list tool preferred |
| coding | Strong model, code-specific verification |
| multi_step | High effort, full verification pipeline |
| summarization | Medium effort, grounded check |

### 6. Adaptive Learning

ARK remembers across runs. Tool scores evolve based on real outcomes.

```
RUN 1: github_list_repos = 0.55   (no history)
RUN 2: github_list_repos = 0.69   (1 success)
RUN 3: github_list_repos = 0.95   (2 successes, compounding)

RUN 1: github_search = 0.55       (no history)
RUN 2: github_search = 0.42       (1 failure, demoted)
```

Learning is bounded — history can't dominate. Confidence capped at 0.80. New tools get exploration bonuses. Intent-matching boosts the right tool for the right query.

---

## By the Numbers

| Metric | Raw MCP | ARK | Improvement |
|--------|---------|-----|-------------|
| Context per task | 60,468 tokens | ~93 tokens | **99.9% reduction** |
| Cost per task | ~$0.05 | ~$0.005 | **10× cheaper** |
| Tools loaded | All 140 | 3 relevant | **97% fewer** |
| Steps to answer | 1 (expensive) | 2 (cheap + strong) | **Right model per step** |
| Verification | None | Every output | **Variable confidence** |
| Learning | None | Persistent | **Run 2 > Run 1** |

---

## Quick Start

```bash
git clone https://github.com/atripati/ark.git
cd ark

# No API keys needed for demos
go run ./cmd/ark bench        # see context savings (99.9% reduction)
go run ./cmd/ark demo         # see failure → adapt → recover
go run ./cmd/ark demo-learn   # see ranking improve across 3 runs

# With OpenAI (~$0.005 per task)
export OPENAI_API_KEY=sk-...
export GITHUB_TOKEN=ghp_...
go run ./cmd/ark run agent.yaml --task "find the top 3 most popular Python web frameworks on GitHub"
```

## Configuration

```yaml
name: my-agent
version: "0.1"

model:
  provider: openai           # openai | anthropic | ollama
  name: gpt-4o
  max_tokens: 4096
  strategy: cost_optimized   # single | cost_optimized | quality_first
  fast_model: gpt-4o-mini
  strong_model: gpt-4o

context:
  total_tokens: 200000
  strategy: adaptive
  tool_budget: 10%
  memory_budget: 10%
  conversation_budget: 35%
  max_steps: 5
  timeout_seconds: 120

memory:
  backend: file
  path: "./ark-memory.json"
```

## Connect Any API

```yaml
tools:
  - name: get_weather
    type: http
    method: GET
    uri: "https://api.openweathermap.org/data/2.5/weather?q={city}&appid=${OPENWEATHER_KEY}"
    description: "get current weather for a city"
    params:
      - city

  - name: slack_post
    type: http
    method: POST
    uri: "https://slack.com/api/chat.postMessage"
    description: "post a message to a Slack channel"
    params: [channel, text]
    headers:
      Authorization: "Bearer ${SLACK_TOKEN}"
    write: true   # requires --allow-write
```

ARK handles domain allowlisting, parameter validation, cost tracking, and learning for custom tools automatically.

## Built-in Tools

| Category | Tools | Auth |
|----------|-------|------|
| GitHub | list_repos, get_repo, list_issues, create_issue, list_pulls, get_user, **search_repos** | GITHUB_TOKEN (optional) |
| Web Search | web_search, web_search_news | BRAVE_API_KEY |
| File System | file_read, file_write, file_list | None |
| Custom HTTP | Any REST API via agent.yaml | Defined in config |

**12 tools** across 4 categories. All ranked, learned, and cost-tracked automatically.

## Safety

Safe by default. Dangerous operations require explicit opt-in.

```bash
ark run agent.yaml --task "list repos"          # ✅ reads work
ark run agent.yaml --task "create issue"        # ❌ blocked
ark run agent.yaml --task "create issue" --allow-write  # ✅ opt-in
ark run agent.yaml --task "create issue" --dry-run      # ✅ simulate
```

## Architecture

```
ark/
├── cmd/ark/                    CLI (run, bench, demo, init)
├── pkg/
│   ├── config/                 YAML config parser + validation (14 tests)
│   ├── context/                Context engine + adaptive ranker + memory
│   │   ├── manager.go          Budget allocation, compression, eviction
│   │   └── engine.go           Tool ranking, intent matching, scoring
│   ├── governor/               Cognitive supervisor
│   │   ├── registry.go         Model capability registry (Bayesian learning)
│   │   ├── verifier.go         Output verification (variable confidence)
│   │   ├── intelligence.go     Task classification, failure prediction, effort allocation
│   │   └── adapter.go          Runtime bridge
│   ├── models/                 LLM providers (Anthropic, OpenAI, Ollama)
│   ├── router/                 Per-step model routing (persistent learning)
│   ├── runtime/                Agent execution loop
│   │   └── agent.go            Governor integration, tool output trimming, diversity
│   ├── cost/                   Decision-level cost attribution
│   ├── store/                  Persistent learning (JSON, decay, snapshots)
│   └── tools/                  Tool implementations
│       ├── github.go           7 GitHub tools + search intelligence pipeline
│       ├── websearch.go        Brave Search (web + news)
│       ├── filesystem.go       File system (read/write/list)
│       └── custom.go           Custom HTTP tool engine

156 tests | Race detector clean | 12 tools | Per-step model routing
Cognitive governor | Variable confidence | Task classification
```

## How the Scoring Works

Every tool gets a composite score from weighted signals:

| Signal | Weight | What it measures |
|--------|--------|-----------------|
| Relevance | 50% | Keyword match + intent boost |
| Success rate | 20% | Historical success/failure ratio |
| Confidence | 5% | Data volume (capped at 0.80 to prevent bias) |
| Cost | -10% | Real dollar cost per call |
| Latency | -5% | Penalty for slow tools |
| Memory bonus | up to 10% | Similar query succeeded before |

New tools get an exploration bonus (+0.15) so they can compete with established tools. Intent keywords ("top", "popular", "best") boost search tools by +0.40.

## Production Guarantees

| Guarantee | How |
|-----------|-----|
| No hallucination when tools available | Governor blocks ungrounded responses |
| Variable confidence | 88% grounded, 75% pure reasoning, 50% ungrounded |
| No invalid tool calls | RequiredParams validated before execution |
| No runaway loops | MaxSteps=5, TotalTimeout=120s, per-tool retry budget |
| Cost-aware | Per-decision cost graph, budget enforcement |
| Self-improving | Bayesian learning, persistent across restarts |
| Failure prediction | Governor predicts failures before execution |
| Task-aware routing | Ranking tasks force strong model |
| Diversity enforcement | Max 2 repos per owner, junk filtering |
| Semantic scoring | 3-tier relevance (boost / penalize / bury) |

## Stress Tested

```
Sequential (20 runs):  20/20 completed, 0 crashes, 0 hallucinated data
Parallel (10 runs):    10/10 completed, 0 crashes, 0 state corruption

Failures handled correctly:
  401 (no auth)     → LLM retried with user param → succeeded
  Tool hallucinated → rejected, valid tools listed → LLM self-corrected
  Timeout           → clean termination with structured error
```

## Roadmap

### v1.0 — Cognitive Governor ✅ (current)
- [x] Cognitive governor (verifier + registry + intelligence layer)
- [x] Task classification (ranking, retrieval, coding, multi_step, reasoning)
- [x] Variable confidence (model history + tool track record + response quality)
- [x] Failure prediction (predict → avoid before execution)
- [x] Experience-aware prompting (inject failure history into prompts)
- [x] Confidence-driven routing (low confidence → force strong model)
- [x] Search intelligence (noise stripping, language detection, semantic scoring)
- [x] Diversity enforcement (max 2 per owner, junk filtering)
- [x] Context-aware learning (per-task-type performance tracking)
- [x] Intent-aware tool selection (search vs list based on query signals)
- [x] Scoring rebalance (relevance dominates, history capped)
- [x] Tool output trimming (50-70% token reduction)
- [x] 156 tests, race detector clean, 12 tools

### v1.1 — MCP Protocol (next)
- [ ] MCP server connector (stdio/SSE)
- [ ] Auto-discover tools from MCP servers
- [ ] ARK manages context for any MCP-connected tool

### v1.2 — Production Storage
- [ ] SQLite backend
- [ ] Multi-agent shared memory
- [ ] Semantic query clustering

### v1.3 — Production Runtime
- [ ] Streaming output
- [ ] Hot-reload agent configs
- [ ] Plugin system
- [ ] OpenTelemetry export
- [ ] `go get github.com/atripati/ark` library mode

## Contributing

ARK is designed to be the foundational runtime for AI agents.

**Good first issues:**
- Add tiktoken-based token counting
- Write MCP server connector
- Add Slack tool set
- SQLite store backend
- Add more language detection (Rust, Swift, Kotlin)

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup instructions.

## Why "ARK"

**A**I **R**untime **K**ernel.

Not a framework — frameworks give you scaffolding and hope you fill it in.
Not a wrapper — wrappers add a layer and call it abstraction.

A kernel. The lowest layer that governs how intelligence is allocated, how decisions are verified, and how money is spent. Every tool call, every model selection, every output flows through ARK before it reaches the user.

The model is the CPU. ARK is the operating system.

## License

Apache 2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).

Copyright 2026 Abhishek Tripathi and ARK Contributors.