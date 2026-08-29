"""Observe-only external-agent integration.

This is an ARBITRARY Python agent — its own model, its own tools, its own loop. ARK does
not run it and knows nothing about its framework. We simply attach ARK around the runtime
with `ark.trace(...)` and REPORT each decision; ARK returns the canonical RunResult.

Run:  python examples/external_observe.py
"""
import ark


# ---- a made-up "external agent" ARK has never heard of ---------------------------
class MyStep:
    def __init__(self, kind, model, tool, in_tok, out_tok, ms, text=None):
        self.kind, self.model, self.tool = kind, model, tool
        self.in_tok, self.out_tok, self.ms, self.text = in_tok, out_tok, ms, text

    def execute(self):
        # your framework really calls your model / tools here; we just return metadata
        return self


class MyAgent:
    def plan(self, task):
        return [
            MyStep("tool_call", "gpt-4o-mini", "github_search_repos", 449, 16, 620),
            MyStep("complete", "gpt-4o", None, 882, 186, 1400,
                   text="1. Django  2. Flask  3. FastAPI"),
        ]


def main():
    agent = MyAgent()
    with ark.trace(task="find the top Python web frameworks on GitHub") as run:
        for step in agent.plan(run.task):
            out = step.execute()                     # you run it, under your control
            run.record(                              # you report what happened
                action=out.kind, model=out.model, tool=out.tool,
                input_tokens=out.in_tok, output_tokens=out.out_tok,
                latency_ms=out.ms, outcome="success",
            )
    result = run.result                              # same schema as ARK.run

    print("run_id:      ", result.run_id)
    print("success:     ", result.success)
    print("total_cost:  ", result.total_cost, "(derived by ARK from reported tokens+model)")
    print("total_tokens:", result.total_tokens)
    print("by_model:    ", result.cost_by_model)
    print("decisions:")
    for d in result.decisions:
        print(f"  {d.id}  {d.action:<9} model={d.model} tool={d.tool} cost={d.cost.total_cost:.6f}")
    print("\nprovenance (what ARK derived vs what you reported):")
    for did, pv in sorted(run.provenance["decisions"].items()):
        print(f"  {did}: reported={pv['reported']}  derived={pv['derived']}")


if __name__ == "__main__":
    main()
