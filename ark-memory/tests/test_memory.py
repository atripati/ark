"""Tests for ARK Memory."""

import os
import tempfile
import time
import pytest

from ark_memory import Agent, Memory, RecallResult
from ark_memory.store import MemoryStore
from ark_memory.embeddings import Embedder, cosine_similarity
from ark_memory.types import MemoryConfig


# ═══════════════════════════════════════════════════════════
# Embedding Tests
# ═══════════════════════════════════════════════════════════


class TestEmbeddings:
    def test_embed_returns_vector(self):
        config = MemoryConfig()
        embedder = Embedder(config)
        vec = embedder.embed("hello world")
        assert len(vec) == config.embedding_dim
        assert any(v != 0 for v in vec)

    def test_embed_deterministic(self):
        config = MemoryConfig()
        embedder = Embedder(config)
        v1 = embedder.embed("test input")
        v2 = embedder.embed("test input")
        assert v1 == v2

    def test_similar_text_high_similarity(self):
        config = MemoryConfig()
        embedder = Embedder(config)
        v1 = embedder.embed("user prefers dark mode")
        v2 = embedder.embed("user likes dark theme")
        sim = cosine_similarity(v1, v2)
        assert sim > 0.3, f"expected similar texts to have similarity > 0.3, got {sim}"

    def test_different_text_low_similarity(self):
        config = MemoryConfig()
        embedder = Embedder(config)
        v1 = embedder.embed("user prefers dark mode")
        v2 = embedder.embed("the stock market crashed yesterday")
        sim = cosine_similarity(v1, v2)
        assert sim < 0.5, f"expected different texts to have similarity < 0.5, got {sim}"

    def test_empty_text(self):
        config = MemoryConfig()
        embedder = Embedder(config)
        vec = embedder.embed("")
        assert all(v == 0 for v in vec)

    def test_embed_batch(self):
        config = MemoryConfig()
        embedder = Embedder(config)
        vecs = embedder.embed_batch(["hello", "world", "test"])
        assert len(vecs) == 3
        assert all(len(v) == config.embedding_dim for v in vecs)


# ═══════════════════════════════════════════════════════════
# Store Tests
# ═══════════════════════════════════════════════════════════


class TestStore:
    def _make_store(self):
        tmp = tempfile.mktemp(suffix=".db")
        config = MemoryConfig(db_path=tmp)
        return MemoryStore(config), tmp

    def test_store_and_recall(self):
        store, tmp = self._make_store()
        try:
            mem = Memory(content="user prefers dark mode", agent_id="test")
            store.store(mem)

            results = store.recall("dark mode", agent_id="test")
            assert len(results) > 0
            assert "dark mode" in results[0].content
        finally:
            store.close()
            os.unlink(tmp)

    def test_namespace_isolation(self):
        store, tmp = self._make_store()
        try:
            store.store(Memory(content="business fact", agent_id="test", namespace="biz"))
            store.store(Memory(content="personal pref", agent_id="test", namespace="prefs"))

            biz = store.recall("fact", agent_id="test", namespace="biz")
            prefs = store.recall("pref", agent_id="test", namespace="prefs")

            assert len(biz) > 0
            assert "business" in biz[0].content
            assert len(prefs) > 0
            assert "personal" in prefs[0].content
        finally:
            store.close()
            os.unlink(tmp)

    def test_forget(self):
        store, tmp = self._make_store()
        try:
            mem = Memory(content="forget me", agent_id="test")
            store.store(mem)
            assert store.count("test") == 1

            store.forget(mem.id)
            assert store.count("test") == 0
        finally:
            store.close()
            os.unlink(tmp)

    def test_forget_namespace(self):
        store, tmp = self._make_store()
        try:
            store.store(Memory(content="a", agent_id="test", namespace="ns1"))
            store.store(Memory(content="b", agent_id="test", namespace="ns1"))
            store.store(Memory(content="c", agent_id="test", namespace="ns2"))

            removed = store.forget_namespace("test", "ns1")
            assert removed == 2
            assert store.count("test", "ns2") == 1
        finally:
            store.close()
            os.unlink(tmp)

    def test_count(self):
        store, tmp = self._make_store()
        try:
            for i in range(5):
                store.store(Memory(content=f"memory {i}", agent_id="test"))
            assert store.count("test") == 5
        finally:
            store.close()
            os.unlink(tmp)

    def test_stats(self):
        store, tmp = self._make_store()
        try:
            store.store(Memory(content="a", agent_id="test", namespace="ns1"))
            store.store(Memory(content="b", agent_id="test", namespace="ns2"))

            stats = store.stats("test")
            assert stats["total_memories"] == 2
            assert "ns1" in stats["namespaces"]
            assert "ns2" in stats["namespaces"]
        finally:
            store.close()
            os.unlink(tmp)

    def test_limit_enforcement(self):
        config = MemoryConfig(
            db_path=tempfile.mktemp(suffix=".db"),
            max_memories_per_namespace=3,
        )
        store = MemoryStore(config)
        try:
            for i in range(5):
                store.store(Memory(content=f"memory {i}", agent_id="test"))
            assert store.count("test") <= 3
        finally:
            store.close()
            os.unlink(config.db_path)

    def test_persistence_across_instances(self):
        tmp = tempfile.mktemp(suffix=".db")
        config = MemoryConfig(db_path=tmp)
        try:
            store1 = MemoryStore(config)
            store1.store(Memory(content="persistent fact", agent_id="test"))
            store1.close()

            store2 = MemoryStore(config)
            results = store2.recall("persistent", agent_id="test")
            assert len(results) > 0
            assert "persistent" in results[0].content
            store2.close()
        finally:
            os.unlink(tmp)


# ═══════════════════════════════════════════════════════════
# Agent Tests
# ═══════════════════════════════════════════════════════════


class TestAgent:
    def _make_agent(self):
        tmp = tempfile.mktemp(suffix=".db")
        return Agent("test-agent", db_path=tmp), tmp

    def test_remember_and_recall(self):
        agent, tmp = self._make_agent()
        try:
            agent.remember("user prefers dark mode")
            agent.remember("user is frustrated about billing")

            results = agent.recall("what does the user prefer?")
            assert len(results) > 0
            contents = [r.content for r in results]
            assert any("dark mode" in c for c in contents)
        finally:
            os.unlink(tmp)

    def test_context_string(self):
        agent, tmp = self._make_agent()
        try:
            agent.remember("user prefers dark mode")
            ctx = agent.context("preferences")
            assert "[Memory]" in ctx
            assert "dark mode" in ctx
        finally:
            os.unlink(tmp)

    def test_namespaces(self):
        agent, tmp = self._make_agent()
        try:
            agent.remember("revenue target is $1M", namespace="business")
            agent.remember("user likes Python", namespace="preferences")

            biz = agent.recall("target", namespace="business")
            prefs = agent.recall("language", namespace="preferences")

            assert len(biz) > 0
            assert "revenue" in biz[0].content
            assert len(prefs) > 0
            assert "Python" in prefs[0].content
        finally:
            os.unlink(tmp)

    def test_recall_all_namespaces(self):
        agent, tmp = self._make_agent()
        try:
            agent.remember("fact A", namespace="ns1")
            agent.remember("fact B", namespace="ns2")

            results = agent.recall_all("fact")
            assert len(results) >= 2
        finally:
            os.unlink(tmp)

    def test_forget(self):
        agent, tmp = self._make_agent()
        try:
            mem = agent.remember("temporary")
            assert agent.count() == 1
            agent.forget(mem.id)
            assert agent.count() == 0
        finally:
            os.unlink(tmp)

    def test_forget_all(self):
        agent, tmp = self._make_agent()
        try:
            agent.remember("a")
            agent.remember("b")
            agent.remember("c")
            assert agent.count() == 3
            agent.forget_all()
            assert agent.count() == 0
        finally:
            os.unlink(tmp)

    def test_empty_content_raises(self):
        agent, tmp = self._make_agent()
        try:
            with pytest.raises(ValueError):
                agent.remember("")
            with pytest.raises(ValueError):
                agent.remember("   ")
        finally:
            os.unlink(tmp)

    def test_empty_recall(self):
        agent, tmp = self._make_agent()
        try:
            results = agent.recall("")
            assert results == []
        finally:
            os.unlink(tmp)

    def test_stats(self):
        agent, tmp = self._make_agent()
        try:
            agent.remember("fact 1")
            agent.remember("fact 2")
            stats = agent.stats()
            assert stats["total_memories"] == 2
        finally:
            os.unlink(tmp)

    def test_importance(self):
        agent, tmp = self._make_agent()
        try:
            agent.remember("low importance fact", importance=0.1)
            agent.remember("high importance fact", importance=5.0)

            results = agent.recall("fact")
            # High importance should rank higher
            assert results[0].memory.importance >= results[-1].memory.importance
        finally:
            os.unlink(tmp)

    def test_repr(self):
        agent, tmp = self._make_agent()
        try:
            agent.remember("test")
            r = repr(agent)
            assert "test-agent" in r
            assert "memories=1" in r
        finally:
            os.unlink(tmp)

    def test_dedup_prevents_duplicates(self):
        agent, tmp = self._make_agent()
        try:
            agent.remember("user prefers dark mode")
            agent.remember("user prefers dark mode")
            assert agent.count() == 1, "near-duplicate should not create new memory"
        finally:
            os.unlink(tmp)

    def test_dedup_allows_different_content(self):
        agent, tmp = self._make_agent()
        try:
            agent.remember("user prefers dark mode")
            agent.remember("quarterly revenue was $5M")
            assert agent.count() == 2, "different content should create separate memories"
        finally:
            os.unlink(tmp)

    def test_explain_recall(self):
        agent, tmp = self._make_agent()
        try:
            agent.remember("user prefers dark mode")
            agent.remember("user is frustrated")
            explanation = agent.explain_recall("what does user prefer?")
            assert "Query:" in explanation
            assert "Similarity:" in explanation
            assert "Final score:" in explanation
            assert "Importance:" in explanation
        finally:
            os.unlink(tmp)

    def test_explain_recall_empty(self):
        agent, tmp = self._make_agent()
        try:
            explanation = agent.explain_recall("anything")
            assert "No memories found" in explanation
        finally:
            os.unlink(tmp)

    def test_anti_redundancy(self):
        agent, tmp = self._make_agent()
        try:
            agent.remember("the user likes dark mode in the app")
            agent.remember("the user likes dark mode in the application")
            agent.remember("user prefers pizza for lunch")

            results = agent.recall("dark mode")
            # Should not return both near-identical dark mode memories
            dark_results = [r for r in results if "dark" in r.content]
            assert len(dark_results) <= 1, "anti-redundancy should collapse near-duplicates"
        finally:
            os.unlink(tmp)