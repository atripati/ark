"""Basic ARK SDK example: run a task through the Go ARK runtime, inspect the RunResult."""
from ark import ARK

ark = ARK()  # supervision off by default

result = ark.run(task="find the top Python web frameworks on GitHub")

print("success:    ", result.success)
print("total_cost: ", result.total_cost)
print("total_tokens:", result.total_tokens)
print("cost_by_model:", result.cost_by_model)
print("cost_by_tool: ", result.cost_by_tool)
print("providers:   ", result.providers)
print("decisions:")
for d in result.decisions:
    print(f"  {d.id}  {d.action:9} model={d.model:12} ${d.cost.total_cost:.6f}  routing={d.routing_reason}")
