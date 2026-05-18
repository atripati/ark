# ARK — AI Runtime Kernel

**Cut agent costs by 80–90%. Make every step verifiable. Ship agents that don't hallucinate.**

ARK doesn't let the model control the system. The runtime does.

It decides which tools run, which model handles each step, how much each decision costs, and whether the output is valid — before anything reaches the user. The model's job is reduced to what it's good at: language. Everything else is governed.

```
┌─ ARK Agent: Task "ark-run"
│  write a function in Go that reads CSV
│
├─ Task type: coding
├─ Context: loaded 2 tools (42 tokens) [strategy: minimal]
├─ Step 1: ✓ Reasoning verified (confidence: 70%)
│  🧪 Verification: tested (score: 100%)
│  ✅ Compiled
│  ✅ Executed
│  ✅ Tests passed
│  ✅ Lint clean
├─ Step 1: COMPLETE — func readCSV(filePath string) ([][]string, error)
│
└─ Done: 1 step, 637 tokens, 5.6s | Cost: $0.002

════════════════════════════════════════════════════════════
  🧠 ARK Memory — Learning from this execution
════════════════════════════════════════════════════════════

  📥 Ingested 2 new events
  📊 Total experience: 20 memories
  🚀 Context for next run:
     Tool experience: github_search_repos — 100% success (2 uses)
     Past: 'find Python frameworks' succeeded in 2 steps, $0.005
     Past: 'write CSV reader' succeeded in 1 step, $0.002
```

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go)](https://go.dev)
[![Python](https://img.shields.io/badge/Python-3.9+-3776AB?style=flat-square&logo=python)](https://python.org)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue?style=flat-square)](LICENSE)
[![Tests](https://img.shields.io/badge/Tests-210%2B%20passing-brightgreen?style=flat-square)]()
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

### 4. Code Verification Engine

ARK doesn't trust generated code. It compiles, runs, and tests it before delivering.

```
├─ Step 1: ✓ Reasoning verified (confidence: 70%)
│  🧪 Verification: tested (score: 100%)
│  ✅ Compiled         ← go build passed
│  ✅ Executed          ← go run passed
│  ✅ Tests passed      ← auto-generated tests passed
│  ✅ Lint clean        ← go vet passed
│  ✔ code_extraction (1 code block(s) found)
│  ✔ structural_lint (0 issues)
│  ✔ constraints (0 violations)
│  ✔ compilation (compiled successfully)
│  ✔ execution (ran without error)
│  ✔ tests (auto-generated tests passed)
│  ✔ lint (0 warnings)
```

The verification pipeline:

| Phase | What it does |
|-------|-------------|
| Extract | Pull code blocks, auto-detect language |
| Auto-Fix | Fix common model errors (orphan braces, missing error handling) |
| Structural Lint | Check braces, parens, completeness, placeholders |
| Constraint Check | Over-commenting, filler comments, unused imports |
| Compile | `go build` / `python -m py_compile` / `node --check` |
| Execute | Run with 10s timeout |
| Auto-Test | Generate smoke tests for functions, run `go test` |
| Lint | `go vet` for static analysis |

If code fails verification, ARK self-corrects: feeds the compiler error back to the model, forces the strong model, and retries. If it still fails after 2 attempts, ARK refuses to deliver broken code.

### 5. ARK Memory — Persistent Agent Experience

Every AI agent has amnesia. ARK Memory fixes it.

```python
from ark_memory import Agent, Experience

agent = Agent("my-agent")
exp = Experience(agent)

# Agent learns from every execution automatically
exp.tool_succeeded("github_search_repos", "python frameworks", duration_ms=2500)
exp.tool_failed("web_search", "latest news", error="API key missing")
exp.strategy_learned("coding", "strip test instructions", improvement="eliminated import conflicts")

# Next run — agent queries its own experience
best = exp.best_tool_for("search repositories")
# → github_search_repos: 100% success, avg 2500ms

context = exp.execution_context("coding task")
# → Learned strategies, tool performance, failures to avoid
```

ARK Memory is a separate Python package (`pip install ark-memory`) with:
- SQLite persistence — survives crashes, zero config
- Semantic search via cosine similarity on embeddings
- Time decay with configurable half-life
- Namespace isolation (per agent, per user, per session)
- Anti-redundancy deduplication
- Auto-learning collector that ingests Runtime events

### 6. Unified Execution — One Command, Both Layers

```bash
./ark-run.sh "write a function in Go that reads CSV"
```

Runtime executes the task → emits events → Memory ingests them automatically. Each run makes the next one smarter.

```
════════════════════════════════════════════════════════════
  🧠 ARK Memory — Learning from this execution
════════════════════════════════════════════════════════════

  📥 Ingested 2 new events
  📊 Total experience: 20 memories
     Tool successes: 7
     Executions: 10

  🚀 Context for next run:
  Tool experience:
    - github_search_repos: 100% success (2 uses)
  Past execution history:
    - Task 'write CSV reader' succeeded in 1 step, $0.002
    - Task 'find Python frameworks' succeeded in 2 steps, $0.005

════════════════════════════════════════════════════════════
  Every run makes the next one smarter.
════════════════════════════════════════════════════════════
```

### 7. Decision-Level Cost Attribution

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

# Run Runtime only
go run ./cmd/ark run agent.yaml --task "find the top 3 Python web frameworks on GitHub"

# Run Runtime + Memory together (one command)
chmod +x ark-run.sh
./ark-run.sh "find the top 3 Python web frameworks on GitHub"

# Install ARK Memory separately
cd ark-memory
pip install -e .
pytest tests/ -v   # 55+ tests
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
├── cmd/ark/                    CLI entry point
│   └── main.go                 Config, agent setup, event emitter init
├── ark-memory/                 Persistent agent memory (Python)
│   ├── ark_memory/
│   │   ├── agent.py            Agent class (remember/recall/context/forget)
│   │   ├── store.py            SQLite persistence, vector search, multi-signal ranking
│   │   ├── embeddings.py       Local hash-based + optional OpenAI embeddings
│   │   ├── experience.py       Experience engine (tool tracking, strategy learning)
│   │   ├── collector.py        Auto-learning collector (ingests Runtime events)
│   │   └── types.py            Memory, RecallResult, MemoryConfig
│   └── tests/                  55+ tests
├── ark-run.sh                  Unified run script (Runtime + Memory)
├── pkg/
│   ├── config/                 YAML config + validation
│   ├── context/                Context engine (99.9% reduction)
│   │   ├── engine.go           Tool ranking (6 signals, Bayesian confidence)
│   │   └── manager.go          Context window management
│   ├── governor/               Cognitive supervisor
│   │   ├── registry.go         Model capability registry (Bayesian learning)
│   │   ├── verifier.go         Output verification (variable confidence)
│   │   ├── intelligence.go     Task classification, failure prediction
│   │   └── adapter.go          Runtime bridge
│   ├── models/                 LLM providers (Anthropic, OpenAI, Ollama)
│   ├── router/                 Per-step model routing (persistent learning)
│   ├── runtime/                Agent execution loop
│   │   ├── agent.go            Execution loop, governor integration
│   │   ├── verify.go           Code verification (compile, test, lint)
│   │   ├── quality.go          Quality layer (auto-fix, prompt optimization)
│   │   └── events.go           Event emitter (JSONL bridge to Memory)
│   ├── cost/                   Decision-level cost attribution
│   ├── store/                  Persistent learning (JSON, decay, snapshots)
│   └── tools/                  Tool implementations
│       ├── github.go           7 GitHub tools + search intelligence pipeline
│       ├── websearch.go        Brave Search (web + news)
│       ├── filesystem.go       File system (read/write/list)
│       └── custom.go           Custom HTTP tool engine

Go Runtime:  35 files | 156+ tests | Race detector clean | 12 tools
Python Memory: 9 files | 55+ tests | Zero config | 4,800 events/sec
Total: 44 files | 210+ tests | One command runs both layers
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
| Code is verified before delivery | Compile → execute → test → lint pipeline |
| Never delivers broken code as success | Hard fail enforcement after max retries |
| Auto-fixes common model errors | Orphan braces, missing error handling, indentation |
| Variable confidence | 88% grounded, 75% pure reasoning, 50% ungrounded |
| No invalid tool calls | RequiredParams validated before execution |
| No runaway loops | MaxSteps=5, TotalTimeout=120s, per-tool retry budget, max 2 self-corrections |
| Cost-aware | Per-decision cost graph, budget enforcement |
| Self-improving | Bayesian learning + experience memory, persistent across restarts |
| Failure prediction | Governor predicts failures before execution |
| Task-aware routing | Ranking tasks force strong model, tool calls use cheap model |
| Diversity enforcement | Max 2 repos per owner, junk filtering |
| Semantic scoring | 3-tier relevance (boost / penalize / bury) |
| Experience accumulation | Every run feeds into the next via ARK Memory |

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

### v1.0 — Cognitive Governor ✅
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

### v1.1 — Code Verification + ARK Memory ✅ (current)
- [x] Code verification engine (compile, execute, test, lint)
- [x] Auto-fix: orphan braces, missing error handling, space/tab indentation
- [x] Task decomposition: strip test instructions from coding tasks
- [x] Self-correction: 2 retries with strong model, history reset
- [x] Hard fail enforcement: never deliver broken code as success
- [x] Quality layer: prompt optimization, response cleaning
- [x] ARK Memory: persistent semantic memory (Python, SQLite, zero config)
- [x] Experience engine: tool tracking, strategy learning, model performance
- [x] Auto-learning collector: ingests Runtime events automatically
- [x] Event bridge: Go Runtime emits JSONL → Python Memory ingests
- [x] Unified execution: `ark-run.sh` runs both layers with one command
- [x] 55+ Python tests, stress tested at 4,800 events/sec
- [x] 210+ total tests across Runtime + Memory

### v1.2 — Adaptive Execution (next)
- [ ] Multi-step adaptive chains (fail → recall experience → adapt → succeed)
- [ ] Experience-aware routing (Memory informs Runtime decisions)
- [ ] MCP server connector (stdio/SSE)
- [ ] Auto-discover tools from MCP servers

### v1.3 — Production Runtime
- [ ] Streaming output
- [ ] Hot-reload agent configs
- [ ] Plugin system
- [ ] OpenTelemetry export
- [ ] `go get github.com/atripati/ark` library mode
- [ ] DSL prototype: 10 lines replaces 1000 lines of Python+SQL+VectorDB

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