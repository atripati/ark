"""
Experience Engine — Operational memory for AI agents.

This is NOT general-purpose memory. This is structured execution experience
that makes agents learn from every run.

    from ark_memory import Agent
    from ark_memory.experience import Experience

    agent = Agent("my-agent")
    exp = Experience(agent)

    # Record what happened during execution
    exp.tool_succeeded("github_search_repos", "python web frameworks",
                       duration_ms=2500, tokens_used=1100)

    exp.tool_failed("web_search", "latest news",
                    error="API key missing", recoverable=True)

    exp.prompt_worked("write a Go function", model="gpt-4o",
                      quality_score=0.95, tokens_used=571)

    exp.strategy_learned("coding", "strip test instructions from first generation",
                         improvement="eliminated import conflicts")

    # Next run — agent queries its own experience
    tool_advice = exp.best_tool_for("search GitHub repositories")
    # → "github_search_repos: 92% success, avg 2.5s, 1100 tokens"

    failures = exp.common_failures("web_search")
    # → ["API key missing (3 times)", "rate limited (1 time)"]

    strategy = exp.what_worked_for("coding tasks with tests")
    # → "strip test instructions from first generation — eliminated import conflicts"

    # Get full execution context for LLM prompt injection
    context = exp.execution_context("coding task in Go")
    # → structured summary of relevant past experience
"""

import time
from typing import List, Optional, Dict, Any

from ark_memory.agent import Agent
from ark_memory.types import RecallResult


class Experience:
    """
    Structured operational memory for AI agents.

    Built on top of Agent memory but with purpose-built methods
    for recording and querying execution experience.

    Namespaces:
      - tool_success: successful tool executions
      - tool_failure: failed tool executions with error details
      - prompt_success: prompts that produced good results
      - prompt_failure: prompts that failed or produced bad results
      - strategy: learned strategies and patterns
      - execution: full execution traces
    """

    # Namespace constants
    NS_TOOL_SUCCESS = "tool_success"
    NS_TOOL_FAILURE = "tool_failure"
    NS_PROMPT_SUCCESS = "prompt_success"
    NS_PROMPT_FAILURE = "prompt_failure"
    NS_STRATEGY = "strategy"
    NS_EXECUTION = "execution"

    def __init__(self, agent: Agent):
        self.agent = agent

    # ═══════════════════════════════════════════════════════════
    # Recording Experience
    # ═══════════════════════════════════════════════════════════

    def tool_succeeded(
        self,
        tool_name: str,
        query: str,
        duration_ms: float = 0,
        tokens_used: int = 0,
        result_quality: float = 1.0,
        metadata: Optional[Dict[str, Any]] = None,
    ):
        """Record a successful tool execution."""
        meta = {
            "tool": tool_name,
            "duration_ms": duration_ms,
            "tokens_used": tokens_used,
            "result_quality": result_quality,
            "timestamp": time.time(),
            **(metadata or {}),
        }

        content = f"{tool_name} succeeded for '{query}'"
        if duration_ms > 0:
            content += f" in {duration_ms:.0f}ms"
        if tokens_used > 0:
            content += f" using {tokens_used} tokens"

        self.agent.remember(
            content=content,
            namespace=self.NS_TOOL_SUCCESS,
            importance=result_quality,
            metadata=meta,
            skip_dedup=True,
        )

    def tool_failed(
        self,
        tool_name: str,
        query: str,
        error: str = "",
        recoverable: bool = True,
        metadata: Optional[Dict[str, Any]] = None,
    ):
        """Record a failed tool execution."""
        meta = {
            "tool": tool_name,
            "error": error,
            "recoverable": recoverable,
            "timestamp": time.time(),
            **(metadata or {}),
        }

        content = f"{tool_name} failed for '{query}'"
        if error:
            content += f": {error}"

        # Failures are more important — agent should remember what went wrong
        self.agent.remember(
            content=content,
            namespace=self.NS_TOOL_FAILURE,
            importance=2.0 if not recoverable else 1.5,
            metadata=meta,
            skip_dedup=True,
        )

    def prompt_worked(
        self,
        task_description: str,
        model: str = "",
        quality_score: float = 1.0,
        tokens_used: int = 0,
        task_type: str = "",
        metadata: Optional[Dict[str, Any]] = None,
    ):
        """Record a prompt/task that produced good results."""
        meta = {
            "model": model,
            "quality_score": quality_score,
            "tokens_used": tokens_used,
            "task_type": task_type,
            "timestamp": time.time(),
            **(metadata or {}),
        }

        content = f"'{task_description}' succeeded"
        if model:
            content += f" with {model}"
        if quality_score > 0:
            content += f" (quality: {quality_score:.0%})"

        self.agent.remember(
            content=content,
            namespace=self.NS_PROMPT_SUCCESS,
            importance=quality_score,
            metadata=meta,
            skip_dedup=True,
        )

    def prompt_failed(
        self,
        task_description: str,
        model: str = "",
        error: str = "",
        task_type: str = "",
        metadata: Optional[Dict[str, Any]] = None,
    ):
        """Record a prompt/task that failed."""
        meta = {
            "model": model,
            "error": error,
            "task_type": task_type,
            "timestamp": time.time(),
            **(metadata or {}),
        }

        content = f"'{task_description}' failed"
        if model:
            content += f" with {model}"
        if error:
            content += f": {error}"

        self.agent.remember(
            content=content,
            namespace=self.NS_PROMPT_FAILURE,
            importance=2.0,
            metadata=meta,
            skip_dedup=True,
        )

    def strategy_learned(
        self,
        task_type: str,
        strategy: str,
        improvement: str = "",
        metadata: Optional[Dict[str, Any]] = None,
    ):
        """Record a strategy or pattern that was discovered."""
        meta = {
            "task_type": task_type,
            "improvement": improvement,
            "timestamp": time.time(),
            **(metadata or {}),
        }

        content = f"For {task_type}: {strategy}"
        if improvement:
            content += f" — {improvement}"

        # Strategies are high importance — hard-won knowledge
        self.agent.remember(
            content=content,
            namespace=self.NS_STRATEGY,
            importance=3.0,
            metadata=meta,
            skip_dedup=True,
        )

    def execution_recorded(
        self,
        task: str,
        steps: int,
        total_cost: float = 0,
        total_tokens: int = 0,
        duration_ms: float = 0,
        success: bool = True,
        model: str = "",
        metadata: Optional[Dict[str, Any]] = None,
    ):
        """Record a complete execution trace summary."""
        meta = {
            "steps": steps,
            "total_cost": total_cost,
            "total_tokens": total_tokens,
            "duration_ms": duration_ms,
            "success": success,
            "model": model,
            "timestamp": time.time(),
            **(metadata or {}),
        }

        status = "succeeded" if success else "failed"
        content = f"Task '{task}' {status} in {steps} steps"
        if total_cost > 0:
            content += f", cost ${total_cost:.4f}"
        if duration_ms > 0:
            content += f", {duration_ms:.0f}ms"

        self.agent.remember(
            content=content,
            namespace=self.NS_EXECUTION,
            importance=1.5 if success else 2.5,
            metadata=meta,
            skip_dedup=True,
        )

    # ═══════════════════════════════════════════════════════════
    # Querying Experience
    # ═══════════════════════════════════════════════════════════

    def best_tool_for(self, task: str, limit: int = 3) -> List[Dict[str, Any]]:
        """
        Find the best tool for a task based on past experience.
        Returns tool recommendations with success rates and performance data.
        """
        successes = self.agent.recall(task, namespace=self.NS_TOOL_SUCCESS, limit=limit * 2)
        failures = self.agent.recall(task, namespace=self.NS_TOOL_FAILURE, limit=limit * 2)

        # Aggregate by tool name
        tool_stats: Dict[str, Dict] = {}

        for r in successes:
            tool = r.memory.metadata.get("tool", "unknown")
            if tool not in tool_stats:
                tool_stats[tool] = {"successes": 0, "failures": 0, "total_ms": 0, "total_tokens": 0}
            tool_stats[tool]["successes"] += 1
            tool_stats[tool]["total_ms"] += r.memory.metadata.get("duration_ms", 0)
            tool_stats[tool]["total_tokens"] += r.memory.metadata.get("tokens_used", 0)

        for r in failures:
            tool = r.memory.metadata.get("tool", "unknown")
            if tool not in tool_stats:
                tool_stats[tool] = {"successes": 0, "failures": 0, "total_ms": 0, "total_tokens": 0}
            tool_stats[tool]["failures"] += 1

        # Rank by success rate
        recommendations = []
        for tool, stats in tool_stats.items():
            total = stats["successes"] + stats["failures"]
            success_rate = stats["successes"] / total if total > 0 else 0
            avg_ms = stats["total_ms"] / stats["successes"] if stats["successes"] > 0 else 0
            avg_tokens = stats["total_tokens"] / stats["successes"] if stats["successes"] > 0 else 0

            recommendations.append({
                "tool": tool,
                "success_rate": success_rate,
                "total_uses": total,
                "avg_duration_ms": avg_ms,
                "avg_tokens": avg_tokens,
            })

        recommendations.sort(key=lambda x: (x["success_rate"], x["total_uses"]), reverse=True)
        return recommendations[:limit]

    def common_failures(self, tool_name: str, limit: int = 5) -> List[Dict[str, Any]]:
        """Get common failure patterns for a specific tool."""
        failures = self.agent.recall(
            tool_name,
            namespace=self.NS_TOOL_FAILURE,
            limit=limit,
        )

        patterns = []
        for r in failures:
            patterns.append({
                "error": r.memory.metadata.get("error", "unknown"),
                "recoverable": r.memory.metadata.get("recoverable", True),
                "age_hours": r.memory.age_hours(),
            })

        return patterns

    def what_worked_for(self, task_type: str, limit: int = 5) -> List[str]:
        """Get strategies that worked for a specific task type."""
        strategies = self.agent.recall(
            task_type,
            namespace=self.NS_STRATEGY,
            limit=limit,
        )

        return [r.content for r in strategies]

    def model_performance(self, model: str = "", limit: int = 10) -> Dict[str, Any]:
        """Get performance stats for a model across tasks."""
        query = model if model else "model performance"

        successes = self.agent.recall(query, namespace=self.NS_PROMPT_SUCCESS, limit=limit)
        failures = self.agent.recall(query, namespace=self.NS_PROMPT_FAILURE, limit=limit)

        success_count = len(successes)
        failure_count = len(failures)
        total = success_count + failure_count

        avg_quality = 0
        avg_tokens = 0
        if successes:
            avg_quality = sum(r.memory.metadata.get("quality_score", 0) for r in successes) / len(successes)
            avg_tokens = sum(r.memory.metadata.get("tokens_used", 0) for r in successes) / len(successes)

        return {
            "model": model,
            "total_tasks": total,
            "success_rate": success_count / total if total > 0 else 0,
            "avg_quality": avg_quality,
            "avg_tokens": avg_tokens,
        }

    def execution_context(self, task: str, limit: int = 5) -> str:
        """
        Build a context string from past experience, ready to inject
        into an LLM system prompt.

        This is the key integration point: ARK Runtime can call this
        before every execution to give the model knowledge of what
        has worked and failed before.
        """
        parts = []

        # What strategies work for this type of task
        strategies = self.agent.recall(task, namespace=self.NS_STRATEGY, limit=3)
        if strategies:
            parts.append("Learned strategies:")
            for r in strategies:
                parts.append(f"  - {r.content}")

        # What tools work best
        tool_recs = self.best_tool_for(task, limit=3)
        if tool_recs:
            parts.append("Tool experience:")
            for rec in tool_recs:
                rate = rec["success_rate"]
                parts.append(f"  - {rec['tool']}: {rate:.0%} success ({rec['total_uses']} uses)")

        # What has failed recently
        failures = self.agent.recall(task, namespace=self.NS_TOOL_FAILURE, limit=2)
        if failures:
            parts.append("Recent failures to avoid:")
            for r in failures:
                error = r.memory.metadata.get("error", "")
                parts.append(f"  - {r.content}")

        # Past execution stats
        executions = self.agent.recall(task, namespace=self.NS_EXECUTION, limit=2)
        if executions:
            parts.append("Past execution history:")
            for r in executions:
                parts.append(f"  - {r.content}")

        if not parts:
            return ""

        return "\n".join(parts)

    def summary(self) -> Dict[str, Any]:
        """Get a summary of all accumulated experience."""
        return {
            "tool_successes": self.agent.count(namespace=self.NS_TOOL_SUCCESS),
            "tool_failures": self.agent.count(namespace=self.NS_TOOL_FAILURE),
            "prompt_successes": self.agent.count(namespace=self.NS_PROMPT_SUCCESS),
            "prompt_failures": self.agent.count(namespace=self.NS_PROMPT_FAILURE),
            "strategies_learned": self.agent.count(namespace=self.NS_STRATEGY),
            "executions_recorded": self.agent.count(namespace=self.NS_EXECUTION),
        }