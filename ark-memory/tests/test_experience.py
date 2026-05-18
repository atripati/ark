"""Tests for ARK Experience Engine."""

import os
import tempfile
import pytest

from ark_memory import Agent, Experience


class TestExperience:
    def _make(self):
        tmp = tempfile.mktemp(suffix=".db")
        agent = Agent("test-agent", db_path=tmp)
        exp = Experience(agent)
        return exp, tmp

    def test_tool_succeeded(self):
        exp, tmp = self._make()
        try:
            exp.tool_succeeded("github_search_repos", "python frameworks",
                               duration_ms=2500, tokens_used=1100)
            count = exp.agent.count(namespace=Experience.NS_TOOL_SUCCESS)
            assert count == 1
        finally:
            os.unlink(tmp)

    def test_tool_failed(self):
        exp, tmp = self._make()
        try:
            exp.tool_failed("web_search", "latest news",
                            error="API key missing", recoverable=True)
            count = exp.agent.count(namespace=Experience.NS_TOOL_FAILURE)
            assert count == 1
        finally:
            os.unlink(tmp)

    def test_prompt_worked(self):
        exp, tmp = self._make()
        try:
            exp.prompt_worked("write a Go function", model="gpt-4o",
                              quality_score=0.95, tokens_used=571)
            count = exp.agent.count(namespace=Experience.NS_PROMPT_SUCCESS)
            assert count == 1
        finally:
            os.unlink(tmp)

    def test_prompt_failed(self):
        exp, tmp = self._make()
        try:
            exp.prompt_failed("complex CSV task", model="gpt-4o-mini",
                              error="compilation failed")
            count = exp.agent.count(namespace=Experience.NS_PROMPT_FAILURE)
            assert count == 1
        finally:
            os.unlink(tmp)

    def test_strategy_learned(self):
        exp, tmp = self._make()
        try:
            exp.strategy_learned("coding", "strip test instructions from first generation",
                                 improvement="eliminated import conflicts")
            strategies = exp.what_worked_for("coding")
            assert len(strategies) > 0
            assert "strip test" in strategies[0].lower()
        finally:
            os.unlink(tmp)

    def test_execution_recorded(self):
        exp, tmp = self._make()
        try:
            exp.execution_recorded("find Python frameworks", steps=2,
                                   total_cost=0.004, total_tokens=1287,
                                   duration_ms=4600, success=True, model="gpt-4o")
            count = exp.agent.count(namespace=Experience.NS_EXECUTION)
            assert count == 1
        finally:
            os.unlink(tmp)

    def test_best_tool_for(self):
        exp, tmp = self._make()
        try:
            exp.tool_succeeded("github_search_repos", "search repos", duration_ms=2000)
            exp.tool_succeeded("github_search_repos", "find frameworks", duration_ms=3000)
            exp.tool_succeeded("github_list_repos", "list repos", duration_ms=1500)
            exp.tool_failed("web_search", "search web", error="no API key")

            recs = exp.best_tool_for("search repositories")
            assert len(recs) > 0
            assert recs[0]["tool"] == "github_search_repos"
            assert recs[0]["success_rate"] == 1.0
        finally:
            os.unlink(tmp)

    def test_common_failures(self):
        exp, tmp = self._make()
        try:
            exp.tool_failed("web_search", "news", error="API key missing")
            exp.tool_failed("web_search", "articles", error="rate limited")

            failures = exp.common_failures("web_search")
            assert len(failures) >= 1
            errors = [f["error"] for f in failures]
            assert any("API key" in e or "rate" in e for e in errors)
        finally:
            os.unlink(tmp)

    def test_what_worked_for(self):
        exp, tmp = self._make()
        try:
            exp.strategy_learned("coding", "use strong model for retries")
            exp.strategy_learned("coding", "strip test instructions")
            exp.strategy_learned("ranking", "boost search tools for ranking tasks")

            coding_strategies = exp.what_worked_for("coding")
            assert len(coding_strategies) >= 1
            assert any("coding" in s.lower() for s in coding_strategies)
        finally:
            os.unlink(tmp)

    def test_model_performance(self):
        exp, tmp = self._make()
        try:
            exp.prompt_worked("task 1", model="gpt-4o", quality_score=0.9, tokens_used=500)
            exp.prompt_worked("task 2", model="gpt-4o", quality_score=0.8, tokens_used=600)
            exp.prompt_failed("task 3", model="gpt-4o", error="failed")

            perf = exp.model_performance("gpt-4o")
            assert perf["total_tasks"] >= 2
            assert perf["success_rate"] > 0
        finally:
            os.unlink(tmp)

    def test_execution_context(self):
        exp, tmp = self._make()
        try:
            exp.strategy_learned("coding", "use strong model for self-correction")
            exp.tool_succeeded("github_search_repos", "find repos", duration_ms=2500)
            exp.tool_failed("web_search", "search web", error="no API key")
            exp.execution_recorded("coding task", steps=1, total_cost=0.003, success=True)

            ctx = exp.execution_context("coding task")
            assert len(ctx) > 0
            assert "Learned strategies" in ctx or "Tool experience" in ctx or "Past execution" in ctx
        finally:
            os.unlink(tmp)

    def test_execution_context_empty(self):
        exp, tmp = self._make()
        try:
            ctx = exp.execution_context("unknown task")
            assert ctx == ""
        finally:
            os.unlink(tmp)

    def test_summary(self):
        exp, tmp = self._make()
        try:
            exp.tool_succeeded("tool1", "query1")
            exp.tool_failed("tool2", "query2", error="err")
            exp.prompt_worked("task1", model="gpt-4o")
            exp.strategy_learned("coding", "strategy1")

            s = exp.summary()
            assert s["tool_successes"] == 1
            assert s["tool_failures"] == 1
            assert s["prompt_successes"] == 1
            assert s["strategies_learned"] == 1
        finally:
            os.unlink(tmp)

    def test_persistence_across_sessions(self):
        tmp = tempfile.mktemp(suffix=".db")
        try:
            # Session 1: record experience
            agent1 = Agent("test-agent", db_path=tmp)
            exp1 = Experience(agent1)
            exp1.tool_succeeded("github_search_repos", "python frameworks",
                                duration_ms=2500, tokens_used=1100)
            exp1.strategy_learned("coding", "always verify before delivering")

            # Session 2: recall experience
            agent2 = Agent("test-agent", db_path=tmp)
            exp2 = Experience(agent2)

            recs = exp2.best_tool_for("python frameworks")
            assert len(recs) > 0

            strategies = exp2.what_worked_for("coding")
            assert len(strategies) > 0
            assert any("verify" in s.lower() for s in strategies)
        finally:
            os.unlink(tmp)