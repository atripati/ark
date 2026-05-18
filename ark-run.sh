#!/bin/bash
# ═══════════════════════════════════════════════════════════
# ARK — Unified Run
# Runs Runtime + Memory together. One command.
#
# Usage:
#   ./ark-run.sh "find the top 3 Python web frameworks"
#   ./ark-run.sh "write a function in Go that reads CSV"
# ═══════════════════════════════════════════════════════════

TASK="$1"

if [ -z "$TASK" ]; then
    echo "Usage: ./ark-run.sh \"your task here\""
    exit 1
fi

# Step 1: Run ARK Runtime (Go) — emits events to ark-events.jsonl
go run ./cmd/ark run agent.yaml --task "$TASK"

# Step 2: Feed events into ARK Memory (Python) — automatic learning
echo ""
echo "════════════════════════════════════════════════════════════"
echo "  🧠 ARK Memory — Learning from this execution"
echo "════════════════════════════════════════════════════════════"
echo ""

cd ark-memory
python3 -c "
from ark_memory import Agent, Experience, Collector

agent = Agent('ark-runtime', db_path='../ark-experience.db')
exp = Experience(agent)
collector = Collector(exp)

# Ingest new events from this run
count = collector.ingest_file('../ark-events.jsonl', incremental=True)
print(f'  📥 Ingested {count} new events')
print()

# Show what ARK knows now
s = exp.summary()
total = sum(s.values())
print(f'  📊 Total experience: {total} memories')
print(f'     Tool successes: {s[\"tool_successes\"]}')
print(f'     Tool failures: {s[\"tool_failures\"]}')
print(f'     Strategies: {s[\"strategies_learned\"]}')
print(f'     Executions: {s[\"executions_recorded\"]}')
print()

# Show execution context for next run
ctx = exp.execution_context('$TASK')
if ctx:
    print('  🚀 Context for next run:')
    for line in ctx.split('\n'):
        print(f'  {line}')
else:
    print('  First run — no prior experience yet.')

print()
print('════════════════════════════════════════════════════════════')
print('  Every run makes the next one smarter.')
print('════════════════════════════════════════════════════════════')
"
cd ..