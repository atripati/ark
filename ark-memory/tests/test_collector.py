"""Tests for ARK Collector — auto-learning bridge."""

import json
import os
import tempfile
import pytest

from ark_memory import Agent, Experience
from ark_memory.collector import Collector


class TestCollector:
    def _make(self):
        tmp_db = tempfile.mktemp(suffix=".db")
        agent = Agent("test-agent", db_path=tmp_db)
        exp = Experience(agent)
        collector = Collector(exp)
        return collector, exp, tmp_db

    def test_ingest_jsonl(self):
        collector, exp, tmp_db = self._make()
        tmp_log = tempfile.mktemp(suffix=".jsonl")
        try:
            events = [
                {"event": "tool_call", "tool": "github_search_repos", "query": "python frameworks", "success": True, "duration_ms": 2500, "tokens": 1100},
                {"event": "tool_call", "tool": "web_search", "query": "news", "success": False, "error": "API key missing"},
                {"event": "task_complete", "task": "find frameworks", "steps": 2, "total_cost": 0.004, "total_tokens": 1287, "duration_ms": 4600, "success": True, "model": "gpt-4o"},
            ]
            with open(tmp_log, "w") as f:
                for e in events:
                    f.write(json.dumps(e) + "\n")

            count = collector.ingest_file(tmp_log)
            assert count == 3

            s = exp.summary()
            assert s["tool_successes"] >= 1
            assert s["tool_failures"] >= 1
            assert s["executions_recorded"] >= 1
        finally:
            os.unlink(tmp_db)
            os.unlink(tmp_log)

    def test_ingest_incremental(self):
        collector, exp, tmp_db = self._make()
        tmp_log = tempfile.mktemp(suffix=".jsonl")
        try:
            # Write first batch
            with open(tmp_log, "w") as f:
                f.write(json.dumps({"event": "tool_call", "tool": "t1", "query": "q1", "success": True}) + "\n")

            count1 = collector.ingest_file(tmp_log, incremental=True)
            assert count1 == 1

            # Append second batch
            with open(tmp_log, "a") as f:
                f.write(json.dumps({"event": "tool_call", "tool": "t2", "query": "q2", "success": True}) + "\n")

            count2 = collector.ingest_file(tmp_log, incremental=True)
            assert count2 == 1  # only the new line
        finally:
            os.unlink(tmp_db)
            os.unlink(tmp_log)

    def test_ingest_governor_registry(self):
        collector, exp, tmp_db = self._make()
        tmp_gov = tempfile.mktemp(suffix=".json")
        try:
            registry = {
                "gpt-4o": {
                    "total_calls": 250,
                    "successes": 248,
                    "failures": 2,
                    "tool_stats": {
                        "github_search_repos": {
                            "calls": 100,
                            "successes": 99,
                            "avg_latency_ms": 2500
                        }
                    }
                }
            }
            with open(tmp_gov, "w") as f:
                json.dump(registry, f)

            count = collector.ingest_governor_registry(tmp_gov)
            assert count >= 2  # model stats + tool stats

            recs = exp.best_tool_for("search repos")
            assert len(recs) > 0
        finally:
            os.unlink(tmp_db)
            os.unlink(tmp_gov)

    def test_ingest_memory_file(self):
        collector, exp, tmp_db = self._make()
        tmp_mem = tempfile.mktemp(suffix=".json")
        try:
            memory_data = {
                "tool_stats": {
                    "github_search_repos": {"calls": 50, "successes": 49},
                    "file_list": {"calls": 10, "successes": 10}
                },
                "patterns": [
                    {"query": "find frameworks", "tool": "github_search_repos"},
                    {"query": "list files", "tool": "file_list"}
                ]
            }
            with open(tmp_mem, "w") as f:
                json.dump(memory_data, f)

            count = collector.ingest_memory_file(tmp_mem)
            assert count >= 2
        finally:
            os.unlink(tmp_db)
            os.unlink(tmp_mem)

    def test_ingest_router_learning(self):
        collector, exp, tmp_db = self._make()
        tmp_router = tempfile.mktemp(suffix=".json")
        try:
            rules = [
                {"step_type": "tool_call", "model": "gpt-4o-mini", "reason": "tool calls are simple"},
                {"step_type": "complete", "model": "gpt-4o", "reason": "reasoning needs strong model"},
                {"step_type": "retry", "model": "gpt-4o", "reason": "error recovery needs strong model"}
            ]
            with open(tmp_router, "w") as f:
                json.dump(rules, f)

            count = collector.ingest_router_learning(tmp_router)
            assert count == 3

            strategies = exp.what_worked_for("model routing")
            assert len(strategies) > 0
        finally:
            os.unlink(tmp_db)
            os.unlink(tmp_router)

    def test_ingest_all(self):
        collector, exp, tmp_db = self._make()
        tmp_gov = tempfile.mktemp(suffix=".json")
        tmp_mem = tempfile.mktemp(suffix=".json")
        tmp_router = tempfile.mktemp(suffix=".json")
        try:
            with open(tmp_gov, "w") as f:
                json.dump({"gpt-4o": {"total_calls": 100, "successes": 98, "failures": 2, "tool_stats": {}}}, f)
            with open(tmp_mem, "w") as f:
                json.dump({"tool_stats": {"t1": {"calls": 10, "successes": 9}}, "patterns": []}, f)
            with open(tmp_router, "w") as f:
                json.dump([{"step_type": "tool_call", "model": "gpt-4o-mini", "reason": "fast"}], f)

            results = collector.ingest_all(
                governor_path=tmp_gov,
                memory_path=tmp_mem,
                router_path=tmp_router,
            )
            assert results["total"] >= 3
        finally:
            os.unlink(tmp_db)
            os.unlink(tmp_gov)
            os.unlink(tmp_mem)
            os.unlink(tmp_router)

    def test_ingest_missing_file(self):
        collector, exp, tmp_db = self._make()
        try:
            count = collector.ingest_file("/nonexistent/file.jsonl")
            assert count == 0
        finally:
            os.unlink(tmp_db)

    def test_ingest_malformed_jsonl(self):
        collector, exp, tmp_db = self._make()
        tmp_log = tempfile.mktemp(suffix=".jsonl")
        try:
            with open(tmp_log, "w") as f:
                f.write("not json\n")
                f.write('{"event":"tool_call","tool":"t1","query":"q","success":true}\n')
                f.write("{broken json\n")

            count = collector.ingest_file(tmp_log)
            assert count == 1  # only the valid line
        finally:
            os.unlink(tmp_db)
            os.unlink(tmp_log)

    def test_verification_event(self):
        collector, exp, tmp_db = self._make()
        tmp_log = tempfile.mktemp(suffix=".jsonl")
        try:
            events = [
                {"event": "verification", "task": "write CSV reader", "level": "tested", "score": 0.95, "compiled": True, "tests_passed": True},
                {"event": "verification", "task": "complex task", "level": "structural", "score": 0.35, "compiled": False},
            ]
            with open(tmp_log, "w") as f:
                for e in events:
                    f.write(json.dumps(e) + "\n")

            count = collector.ingest_file(tmp_log)
            assert count == 2

            s = exp.summary()
            assert s["prompt_successes"] >= 1  # high score verification
            assert s["prompt_failures"] >= 1   # low score verification
        finally:
            os.unlink(tmp_db)
            os.unlink(tmp_log)

    def test_strategy_event(self):
        collector, exp, tmp_db = self._make()
        tmp_log = tempfile.mktemp(suffix=".jsonl")
        try:
            event = {"event": "strategy", "task_type": "coding", "strategy": "strip test instructions", "improvement": "eliminated import conflicts"}
            with open(tmp_log, "w") as f:
                f.write(json.dumps(event) + "\n")

            count = collector.ingest_file(tmp_log)
            assert count == 1

            strategies = exp.what_worked_for("coding")
            assert len(strategies) > 0
        finally:
            os.unlink(tmp_db)
            os.unlink(tmp_log)