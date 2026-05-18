"""
Agent — the primary interface to ARK Memory.

    from ark_memory import Agent

    agent = Agent("my-agent")
    agent.remember("user prefers dark mode")
    agent.remember("user is frustrated about billing", importance=2.0)

    results = agent.recall("what does the user feel?")
    for r in results:
        print(f"{r.content} (relevance: {r.decayed_score:.2f})")

    # Namespaces for isolation
    agent.remember("Q3 target is $1M", namespace="business")
    agent.remember("user likes Python", namespace="preferences")

    biz = agent.recall("targets", namespace="business")
    prefs = agent.recall("language", namespace="preferences")
"""

import time
from typing import List, Optional

from ark_memory.store import MemoryStore
from ark_memory.types import Memory, MemoryConfig, RecallResult


class Agent:
    """
    An agent with persistent semantic memory.

    Every call to remember() persists to disk. Every call to recall()
    searches semantically across all memories. Memories decay over time,
    gain importance when accessed, and are evicted when limits are hit.

    Thread-safe. Crash-safe. Zero external dependencies required.
    """

    def __init__(
        self,
        agent_id: str,
        db_path: str = "ark_memory.db",
        embedding_provider: str = "local",
        decay_half_life_hours: float = 168.0,
        max_memories: int = 10000,
    ):
        self.agent_id = agent_id
        self.config = MemoryConfig(
            db_path=db_path,
            embedding_provider=embedding_provider,
            decay_half_life_hours=decay_half_life_hours,
            max_memories_per_namespace=max_memories,
        )
        self.store = MemoryStore(self.config)
        self._session_start = time.time()

    def remember(
        self,
        content: str,
        namespace: str = "default",
        importance: float = 0.0,
        metadata: Optional[dict] = None,
        skip_dedup: bool = False,
    ) -> Memory:
        """
        Store a memory. Persists immediately to disk.

        Args:
            content: The text to remember.
            namespace: Isolation group.
            importance: Higher = survives decay longer. 0 = use default.
            metadata: Arbitrary key-value pairs.
            skip_dedup: Skip deduplication check (for bulk ingestion).
        """
        if not content or not content.strip():
            raise ValueError("Cannot remember empty content")

        content = content.strip()

        # Dedup check (skip for bulk ingestion — too expensive per-event)
        if not skip_dedup:
            existing = self.store.recall(
                query=content,
                agent_id=self.agent_id,
                namespace=namespace,
                limit=1,
            )
            if existing and existing[0].score > 0.90:
                return existing[0].memory

        mem = Memory(
            content=content,
            namespace=namespace,
            agent_id=self.agent_id,
            importance=importance if importance > 0 else self.config.default_importance,
            metadata=metadata or {},
        )

        return self.store.store(mem)

    def recall(
        self,
        query: str,
        namespace: str = "default",
        limit: int = 5,
    ) -> List[RecallResult]:
        """
        Recall memories relevant to a query. Semantic search with time decay.

        Args:
            query: What to search for (natural language).
            namespace: Which namespace to search in.
            limit: Max number of results.

        Returns:
            List of RecallResult, sorted by relevance (decayed_score desc).
        """
        if not query or not query.strip():
            return []

        return self.store.recall(
            query=query.strip(),
            agent_id=self.agent_id,
            namespace=namespace,
            limit=limit,
        )

    def recall_all(
        self,
        query: str,
        limit: int = 5,
    ) -> List[RecallResult]:
        """
        Recall across ALL namespaces for this agent.
        Single query, no per-namespace loop.
        """
        if not query or not query.strip():
            return []

        return self.store.recall_all(
            query=query.strip(),
            agent_id=self.agent_id,
            limit=limit,
        )

    def forget(self, memory_id: str) -> bool:
        """Forget a specific memory by ID."""
        return self.store.forget(memory_id)

    def forget_all(self, namespace: str = "default") -> int:
        """Forget all memories in a namespace."""
        return self.store.forget_namespace(self.agent_id, namespace)

    def context(
        self,
        query: str,
        namespace: str = "default",
        limit: int = 5,
    ) -> str:
        """
        Get recall results as a formatted context string.
        Ready to inject into an LLM prompt.

        Returns:
            A string like:
            "[Memory] user prefers dark mode (relevance: 0.87)
             [Memory] user frustrated about billing (relevance: 0.72)"
        """
        results = self.recall(query, namespace=namespace, limit=limit)
        if not results:
            return ""

        lines = []
        for r in results:
            lines.append(f"[Memory] {r.content} (relevance: {r.decayed_score:.2f})")
        return "\n".join(lines)

    def decay(self, namespace: str = "default") -> int:
        """Run decay — remove memories that have decayed below threshold."""
        return self.store.decay_all(self.agent_id, namespace)

    def explain_recall(
        self,
        query: str,
        namespace: str = "default",
        limit: int = 5,
    ) -> str:
        """
        Explain WHY memories were recalled. Shows all scoring signals.
        For debugging and observability — developers can see exactly
        what drives each memory's ranking.

        Returns:
            Human-readable explanation string.
        """
        results = self.recall(query, namespace=namespace, limit=limit)
        if not results:
            return f"No memories found for query: '{query}'"

        lines = [f"Query: '{query}'", f"Namespace: {namespace}", f"Results: {len(results)}", ""]

        for i, r in enumerate(results):
            age = r.memory.age_hours()
            lines.append(f"{i+1}. \"{r.content}\"")
            lines.append(f"   Similarity:  {r.score:.3f}")
            lines.append(f"   Final score: {r.decayed_score:.3f}")
            lines.append(f"   Importance:  {r.memory.importance:.2f}")
            lines.append(f"   Age:         {age:.1f}h")
            lines.append(f"   Accessed:    {r.memory.access_count}x")
            lines.append("")

        return "\n".join(lines)

    def count(self, namespace: str = "default") -> int:
        """Count memories in a namespace."""
        return self.store.count(self.agent_id, namespace)

    def stats(self) -> dict:
        """Get memory statistics."""
        return self.store.stats(self.agent_id)

    def __repr__(self) -> str:
        s = self.store.stats(self.agent_id)
        return (
            f"Agent('{self.agent_id}', "
            f"memories={s['total_memories']}, "
            f"namespaces={s['namespaces']})"
        )