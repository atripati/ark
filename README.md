# ARK — AI Runtime Kernel
🚨 AI agents are wasting ~30% of their context before doing any work.
They load ALL tools upfront.
ARK fixes this.

→ Loads only relevant tools  
→ Reduces context by ~99%  
→ Adapts when tools fail  

This gives your model its reasoning space back.


> A Context Operating System for AI agents. We virtualize context for LLMs.

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue?style=flat-square)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen?style=flat-square)](CONTRIBUTING.md)

---

## The Problem

MCP tools waste **30% of your context window** before your agent does any work. Every tool schema gets dumped into the prompt upfront — 140 tools × ~430 tokens each = 60,000 tokens gone. That's context you can't use for reasoning, memory, or conversation.

Current solutions (LangChain, RAG pipelines, vector DBs) are stitched-together plumbing. You spend months on infrastructure instead of building your agent.

## What ARK Does

ARK is a single runtime that manages what your LLM sees. It loads only the tools relevant to the current task, learns which tools work best, and adapts context in real-time when things fail.

```
┌─────────────────────────────────────────────────┐
│               Your Agent Logic                   │
├─────────────────────────────────────────────────┤
│                    A R K                         │
│                                                  │
│  ┌─────────────┐  ┌──────────┐  ┌────────────┐ │
│  │   Context    │  │  Tool    │  │  Context   │ │
│  │   Engine     │  │  Ranker  │  │  Memory    │ │
│  │ load→adapt→  │  │ weighted │  │ learns     │ │
│  │ retry        │  │ scoring  │  │ over time  │ │
│  └─────────────┘  └──────────┘  └────────────┘ │
│  ┌─────────────┐  ┌──────────────────────────┐  │
│  │   Tracer    │  │   Agent Runtime Loop     │  │
│  │ full audit  │  │   task→plan→execute→     │  │
│  │ trail       │  │   observe→adapt          │  │
│  └─────────────┘  └──────────────────────────┘  │
├─────────────────────────────────────────────────┤
│   Claude • GPT • Gemini • Llama • Ollama         │
└─────────────────────────────────────────────────┘
```

## Benchmark: ~99% reduction in tool context overhead

```
$ go run ./cmd/ark bench

  ❌ RAW MCP (load everything upfront):
     140 tools → 60,468 tokens → 30.2% of context GONE
     ██████████████████████████████
     Before your agent does ANY actual work.

  ✅ ARK (load only what's relevant):

  ┌─ Task: "Create a GitHub PR"
  │  Loaded: 5/140 tools (in 63µs)
  │  ARK tokens:     97     (compressed summaries)
  │  Raw would cost: 2,160  (full schemas for same 5 tools)
  │  All-tools cost: 60,468 (what raw MCP actually loads)
  └─ Saved: 60,371 tokens vs raw MCP

  📊 RESULTS
  ┌──────────────────────────────────────────────────┐
  │  Metric              Raw MCP         ARK         │
  ├──────────────────────────────────────────────────┤
  │  Tools loaded        ALL 140         ~4/task     │
  │  Tokens consumed     60,468          80          │
  │  Context window      30.2%           0.05%       │
  │  Tokens freed        —               +60,388     │
  │  Reduction           —               99.9%       │
  └──────────────────────────────────────────────────┘
```
🧠 MCP gives your model less room to think.  
⚡ ARK gives it that space back.

## Dynamic Context: Observe → Adapt → Recover

ARK doesn't just load tools statically. It watches execution results and adapts in real-time.

```
$ go run ./cmd/ark demo

  ── Scenario 2: Failure → Adapt → Retry ──

  ┌─ ARK Agent: Task "retry-demo"
  │  search jira issues assigned to me
  │
  ├─ Context: loaded 3 tools [strategy: minimal]
  ├─ Step 1: TOOL_CALL — jira_search_issues
  │  ↳ Failed, adapting: swapped → 7 tools
  ├─ Step 2: TOOL_CALL — jira_list_issues
  │  ↳ Result: [{"key":"ARK-101"}, {"key":"ARK-102"}...]
  ├─ Step 3: COMPLETE
  └─ Done: 3 steps, success

  Trace:
  ├─ [tool_ranking] Ranked 6 candidates
  │  jira-3(0.30 [r=0.45 s=0.50 p=medium])
  │  jira-2(0.27 [r=0.38 s=0.50 p=medium])
  ├─ [execution_result] success=false, error=tool_failed
  │  Jira API returned 503: service temporarily unavailable
  ├─ [context_adapted] strategy=swapped, tools=7
  ├─ [execution_result] success=true
  └─ [trace_complete] Completed in 2 attempt(s)
```

The tool failed. ARK evicted it, swapped in an alternative, retried, and succeeded. The trace shows exactly what happened and why.

## Intelligent Tool Scoring

Every tool gets a real, weighted score — not a flat number:

```
score = (relevance × 0.45)      How well does this tool match the query?
      + (success_rate × 0.30)    How often has it worked before?
      - (latency × 0.10)         How slow is it?
      - (token_cost × 0.05)      How expensive is it?
      + (confidence × 0.10)      How much data do we have?
      + memory_bonus              Did it work for similar queries before?
```

Tools with 0% success rate get ranked last. Tools on a 3+ failure streak get halved. Tools that worked for similar queries get a memory bonus. ARK learns what works.

## Quick Start

```bash
git clone https://github.com/atripati/ark.git
cd ark

go test ./...             # 19 passing tests
go run ./cmd/ark bench    # See the token savings
go run ./cmd/ark demo     # See dynamic context in action
go run ./cmd/ark init     # Create an agent.yaml template
```

## Using the Context Engine in Your Code

```go
package main

import (
    ctx "github.com/atripati/ark/pkg/context"
)

func main() {
    ////// Create manager with 200k token budget
    mgr := ctx.NewManager(ctx.DefaultBudget(200000))

    //// Register tools (lazy — no context consumed yet)
    mgr.RegisterTool("gh-pr", "create_pr",
        "Create a pull request on GitHub", fullSchema)

    ///////// Create the dynamic engine
    engine := ctx.NewEngine(mgr, ctx.DefaultEngineConfig())

    //////// Prepare context for a task (loads minimal relevant tools)
    plan := engine.PrepareContext("task-1", "create a github PR")

    // ////... execute with your LLM ...

    /////// If it fails, adapt and retry
    result := ctx.ExecutionResult{Success: false, ErrorType: ctx.ErrToolFailed}
    newPlan := engine.AdaptContext(plan, result)
    // ///ARK swaps failed tools, loads alternatives, retries

    ////// Full decision trace  gyus
    fmt.Println(engine.TracerRef().PrintTrace(plan.TraceID))
}
```

## Architecture

```
ark/
├── cmd/ark/                  CLI (bench, demo, init)
├── pkg/
│   ├── context/
│   │   ├── manager.go        Context manager + budget allocation
│   │   ├── engine.go         Dynamic engine + tool ranker + tracer + memory
│   │   ├── manager_test.go   7 tests
│   │   └── engine_test.go    12 tests
│   └── runtime/
│       └── agent.go          Agent execution loop + mock executor
├── NOTICE                    Attribution requirements
├── LICENSE                   Apache 2.0
├── CONTRIBUTING.md           Contributor guide
└── README.md
```

## Who is this for?

- Developers building AI agents
- Teams struggling with tool bloat (MCP, LangChain, etc.)
- Anyone hitting context limits or high token costs

## Roadmap

### v0.3 — Context Decision Engine ✅ (current)
- [x] Token budget allocation with per-category limits
- [x] Adaptive tool loading by relevance
- [x] Schema compression (full → summary)
- [x] Priority-based eviction
- [x] Dynamic context engine (load → observe → expand → retry)
- [x] Weighted tool scoring (relevance, success, latency, cost, confidence)
- [x] Context memory (learns what works for similar queries)
- [x] Confidence prediction (high/medium/low per tool)
- [x] Failure streak detection
- [x] Full audit tracer with decision traces
- [x] Agent execution loop with mock executor
- [x] CLI: bench, demo, init

### v0.4 — Production Ready
- [ ] Real MCP server connector
- [ ] Tiktoken-accurate token counting
- [ ] YAML config parser for agent.yaml
- [ ] `ark run` with live LLM execution
- [ ] SQLite-backed persistent memory
- [ ] OpenTelemetry trace export

### v0.5 — Multi-Agent
- [ ] Shared memory graph between agents
- [ ] Multi-step context evolution (GitHub → Logs → DB → Slack)
- [ ] Model router (Anthropic, OpenAI, Ollama) with automatic fallback
- [ ] Cost-optimized model routing

### v1.0 — Production
- [ ] Hot-reload agent configs
- [ ] Plugin system for custom tools
- [ ] Distributed agent communication
- [ ] Summary trace mode (`--trace=summary`)

## Contributing

ARK is designed to be the foundational runtime for AI agents — there's a lot to build.

**Good first issues:**
- Add tiktoken-based token counting (replace character estimation)
- Write MCP server connector (connect to real MCP servers)
- Add YAML config parser for agent.yaml
- Add OpenTelemetry trace export
- Add `--trace=summary` mode for cleaner output

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup instructions.

## Why "ARK"?

**A**I **R**untime **K**ernel. A vessel built to carry what matters through turbulent waters. AI agent development is a flood of accidental complexity — MCP bloat, scattered memory, invisible failures. ARK carries your agent logic safely above it.

## License

Apache 2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).

You are free to use, modify, and distribute ARK. If you redistribute modified versions:
1. **Keep the notice**: Retain the copyright notice, LICENSE, and NOTICE files
2. **State changes**: Mark modified files with a notice that you changed them
3. **No trademark use**: Do not use "ARK" or contributor names to endorse derivative products without permission

Copyright 2026 Abhishek Tripathi and ARK Contributors.


⭐ If this resonates, star the repo.