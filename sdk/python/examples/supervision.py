"""Experimental constrained-supervision example: proposed -> REJECT -> retry -> ALLOW,
using only generic runtime evidence. ARK validates/gates; the agent re-authors the action."""
from ark import ARK

ark = ARK(supervision="experimental")  # explicit opt-in

# generic priced options; the user asked for the 2nd-cheapest ("requested_rank": 2)
evidence = {"requested_rank": 2, "evidence_complete": True,
            "options": [{"id": "A", "price": 163, "is_direct": True}, {"id": "B", "price": 290}]}

# 1) the agent proposes A (rank-1) -> REJECT, with a runtime-derived suggestion
r1 = ark.supervise(constraint="rank", proposed={"option": "A"}, evidence=evidence, retry_count=0)
print("proposed A ->", r1.verdict, "| ARK suggests (evidence, not authored):", r1.suggested_from_evidence)

# 2) the AGENT re-proposes B (rank-2) -> ALLOW  (ARK never created this action itself)
r2 = ark.supervise(constraint="rank", proposed={"option": "B"}, evidence=evidence, retry_count=1)
print("re-proposed B ->", r2.verdict, "| executed:", r2.record.executed)
