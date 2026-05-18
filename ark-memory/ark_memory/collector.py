"""
Auto-Learning Collector — The bridge between ARK Runtime and ARK Memory.

ARK Runtime (Go) emits structured JSON events during execution.
This collector watches those events and automatically records them
as operational experience. No manual calls needed.

Usage:

    from ark_memory import Agent, Experience
    from ark_memory.collector import Collector

    agent = Agent("my-agent")
    exp = Experience(agent)
    collector = Collector(exp)

    # Ingest from Runtime's event log
    collector.ingest_file("ark-execution-log.jsonl")

    # Or ingest from Runtime's existing files
    collector.ingest_governor_registry("ark-governor-registry.json")
    collector.ingest_memory_file("ark-memory.json")
    collector.ingest_router_learning("ark-router-learning.json")

    # Now the experience engine has real data from actual executions
    print(exp.execution_context("coding task"))

Runtime Event Format (JSONL — one JSON object per line):

    {"event":"tool_call","tool":"github_search_repos","query":"python frameworks","success":true,"duration_ms":2500,"tokens":1100,"cost":0.001,"timestamp":1778804251}
    {"event":"tool_call","tool":"web_search","query":"news","success":false,"error":"API key missing","timestamp":1778804255}
    {"event":"step_complete","task":"find frameworks","step":1,"model":"gpt-4o-mini","action":"tool_call","tokens":335,"cost":0.001,"timestamp":1778804253}
    {"event":"task_complete","task":"find frameworks","steps":2,"total_cost":0.004,"total_tokens":1287,"duration_ms":4600,"success":true,"model":"gpt-4o","timestamp":1778804256}
    {"event":"verification","task":"write CSV reader","level":"executed","score":0.95,"compiled":true,"tests_passed":true,"timestamp":1778804260}
"""

import json
import os
import time
from pathlib import Path
from typing import Optional

from ark_memory.experience import Experience


class Collector:
    """
    Automatic learning collector. Ingests execution events from
    ARK Runtime and records them as structured experience.

    Supports three ingestion modes:
    1. JSONL event log (real-time or batch)
    2. Governor registry JSON (existing Runtime data)
    3. Memory/router JSON files (existing Runtime data)
    """

    def __init__(self, experience: Experience):
        self.exp = experience
        self._last_position: dict = {}  # tracks file read positions for incremental ingestion

    # ═══════════════════════════════════════════════════════════
    # Mode 1: JSONL Event Log Ingestion
    # ═══════════════════════════════════════════════════════════

    def ingest_file(self, path: str, incremental: bool = True) -> int:
        """
        Ingest events from a JSONL file. Each line is one JSON event.

        Args:
            path: Path to the JSONL event log file.
            incremental: If True, only reads new lines since last call.

        Returns:
            Number of events ingested.
        """
        if not os.path.exists(path):
            return 0

        start_pos = 0
        if incremental and path in self._last_position:
            start_pos = self._last_position[path]

        count = 0
        with open(path, "r") as f:
            f.seek(start_pos)
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    event = json.loads(line)
                    self._process_event(event)
                    count += 1
                except (json.JSONDecodeError, KeyError):
                    continue

            self._last_position[path] = f.tell()

        return count

    def _process_event(self, event: dict):
        """Route an event to the appropriate experience recorder."""
        event_type = event.get("event", "")

        if event_type == "tool_call":
            self._process_tool_call(event)
        elif event_type == "step_complete":
            self._process_step(event)
        elif event_type == "task_complete":
            self._process_task(event)
        elif event_type == "verification":
            self._process_verification(event)
        elif event_type == "strategy":
            self._process_strategy(event)

    def _process_tool_call(self, event: dict):
        tool = event.get("tool", "unknown")
        query = event.get("query", "")
        success = event.get("success", True)

        if success:
            self.exp.tool_succeeded(
                tool_name=tool,
                query=query,
                duration_ms=event.get("duration_ms", 0),
                tokens_used=event.get("tokens", 0),
                result_quality=event.get("quality", 1.0),
                metadata={"cost": event.get("cost", 0)},
            )
        else:
            self.exp.tool_failed(
                tool_name=tool,
                query=query,
                error=event.get("error", ""),
                recoverable=event.get("recoverable", True),
            )

    def _process_step(self, event: dict):
        model = event.get("model", "")
        action = event.get("action", "")
        task = event.get("task", "")

        if action in ("complete", "tool_call"):
            self.exp.prompt_worked(
                task_description=f"{task} (step {event.get('step', 0)})",
                model=model,
                quality_score=event.get("quality", 0.8),
                tokens_used=event.get("tokens", 0),
                task_type=action,
            )

    def _process_task(self, event: dict):
        self.exp.execution_recorded(
            task=event.get("task", "unknown"),
            steps=event.get("steps", 0),
            total_cost=event.get("total_cost", 0),
            total_tokens=event.get("total_tokens", 0),
            duration_ms=event.get("duration_ms", 0),
            success=event.get("success", True),
            model=event.get("model", ""),
        )

    def _process_verification(self, event: dict):
        task = event.get("task", "")
        level = event.get("level", "")
        score = event.get("score", 0)

        if score >= 0.8:
            self.exp.prompt_worked(
                task_description=f"{task} (verification: {level})",
                quality_score=score,
                task_type="verification",
                metadata={
                    "compiled": event.get("compiled", False),
                    "tests_passed": event.get("tests_passed", False),
                },
            )
        else:
            self.exp.prompt_failed(
                task_description=f"{task} (verification: {level})",
                error=f"verification score {score:.0%}",
                task_type="verification",
            )

    def _process_strategy(self, event: dict):
        self.exp.strategy_learned(
            task_type=event.get("task_type", "general"),
            strategy=event.get("strategy", ""),
            improvement=event.get("improvement", ""),
        )

    # ═══════════════════════════════════════════════════════════
    # Mode 2: Ingest from existing Runtime files
    # ═══════════════════════════════════════════════════════════

    def ingest_governor_registry(self, path: str) -> int:
        """
        Ingest from ARK Runtime's governor registry JSON.
        Extracts model performance stats and tool success rates.
        """
        if not os.path.exists(path):
            return 0

        with open(path, "r") as f:
            data = json.load(f)

        count = 0
        for model_name, profile in data.items():
            if not isinstance(profile, dict):
                continue

            # Record model stats
            calls = profile.get("total_calls", 0)
            successes = profile.get("successes", 0)
            failures = profile.get("failures", 0)

            if calls > 0:
                success_rate = successes / calls
                self.exp.prompt_worked(
                    task_description=f"model {model_name}: {calls} calls, {success_rate:.0%} success",
                    model=model_name,
                    quality_score=success_rate,
                    metadata={
                        "total_calls": calls,
                        "successes": successes,
                        "failures": failures,
                        "source": "governor_registry",
                    },
                )
                count += 1

            # Record per-tool stats
            tool_stats = profile.get("tool_stats", {})
            for tool_name, stats in tool_stats.items():
                if not isinstance(stats, dict):
                    continue
                tool_calls = stats.get("calls", 0)
                tool_successes = stats.get("successes", 0)
                tool_success_rate = tool_successes / tool_calls if tool_calls > 0 else 0

                if tool_successes > 0:
                    self.exp.tool_succeeded(
                        tool_name=tool_name,
                        query=f"aggregated: {tool_calls} calls, {tool_success_rate:.0%} success",
                        duration_ms=stats.get("avg_latency_ms", 0),
                        result_quality=tool_success_rate,
                        metadata={"source": "governor_registry", "total_calls": tool_calls},
                    )
                    count += 1

        return count

    def ingest_memory_file(self, path: str) -> int:
        """
        Ingest from ARK Runtime's ark-memory.json.
        Extracts tool usage stats and learned patterns.
        """
        if not os.path.exists(path):
            return 0

        with open(path, "r") as f:
            data = json.load(f)

        count = 0

        # Tool stats
        tool_stats = data.get("tool_stats", {})
        for tool_name, stats in tool_stats.items():
            if not isinstance(stats, dict):
                continue
            calls = stats.get("calls", 0)
            successes = stats.get("successes", 0)

            if successes > 0:
                self.exp.tool_succeeded(
                    tool_name=tool_name,
                    query=f"historical: {calls} total calls",
                    result_quality=successes / calls if calls > 0 else 0,
                    metadata={"source": "ark_memory_json", "total_calls": calls},
                )
                count += 1

        # Patterns
        patterns = data.get("patterns", [])
        if isinstance(patterns, list):
            for pattern in patterns:
                if isinstance(pattern, dict):
                    query = pattern.get("query", "")
                    tool = pattern.get("tool", "")
                    if query and tool:
                        self.exp.strategy_learned(
                            task_type="tool_selection",
                            strategy=f"for queries like '{query}', use {tool}",
                            metadata={"source": "ark_memory_json"},
                        )
                        count += 1

        return count

    def ingest_router_learning(self, path: str) -> int:
        """
        Ingest from ARK Runtime's router learning JSON.
        Extracts learned routing rules.
        """
        if not os.path.exists(path):
            return 0

        with open(path, "r") as f:
            data = json.load(f)

        count = 0

        if isinstance(data, list):
            for rule in data:
                if not isinstance(rule, dict):
                    continue
                step_type = rule.get("step_type", "")
                model = rule.get("model", "")
                reason = rule.get("reason", "")

                if step_type and model:
                    self.exp.strategy_learned(
                        task_type="model_routing",
                        strategy=f"route {step_type} to {model}",
                        improvement=reason,
                        metadata={"source": "router_learning"},
                    )
                    count += 1

        return count

    def ingest_all(
        self,
        governor_path: str = "ark-governor-registry.json",
        memory_path: str = "ark-memory.json",
        router_path: str = "ark-router-learning.json",
        event_log_path: str = "ark-events.jsonl",
    ) -> dict:
        """
        Ingest from all available Runtime files at once.
        Call this once at startup to bootstrap experience from existing data.
        """
        results = {
            "governor": self.ingest_governor_registry(governor_path),
            "memory": self.ingest_memory_file(memory_path),
            "router": self.ingest_router_learning(router_path),
            "events": self.ingest_file(event_log_path),
        }
        results["total"] = sum(results.values())
        return results