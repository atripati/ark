"""
Storage engine for ARK Memory.

SQLite-based persistent storage with embedded vector search.
No external database required. Survives crashes. Supports concurrent access.

Schema:
  memories: id, agent_id, namespace, content, importance, created_at,
            accessed_at, access_count, metadata_json, embedding_blob
"""

import json
import math
import sqlite3
import threading
import time
from pathlib import Path
from typing import List, Optional, Tuple

import numpy as np

from ark_memory.embeddings import Embedder, cosine_similarity
from ark_memory.types import Memory, MemoryConfig, RecallResult


class MemoryStore:
    """
    Persistent semantic memory store.

    Thread-safe SQLite storage with embedded vector search.
    All operations are atomic — no corrupted state on failure.
    """

    def __init__(self, config: Optional[MemoryConfig] = None):
        self.config = config or MemoryConfig()
        self.embedder = Embedder(self.config)
        self._lock = threading.Lock()
        self._conn = None
        self._init_db()

    def _init_db(self):
        """Initialize SQLite database and create tables if needed."""
        db_path = self.config.db_path
        Path(db_path).parent.mkdir(parents=True, exist_ok=True)

        self._conn = sqlite3.connect(db_path, check_same_thread=False)
        self._conn.execute("PRAGMA journal_mode=WAL")  # concurrent reads
        self._conn.execute("PRAGMA synchronous=NORMAL")  # balance safety/speed

        self._conn.execute("""
            CREATE TABLE IF NOT EXISTS memories (
                id TEXT PRIMARY KEY,
                agent_id TEXT NOT NULL,
                namespace TEXT NOT NULL DEFAULT 'default',
                content TEXT NOT NULL,
                importance REAL NOT NULL DEFAULT 1.0,
                created_at REAL NOT NULL,
                accessed_at REAL NOT NULL,
                access_count INTEGER NOT NULL DEFAULT 0,
                metadata_json TEXT DEFAULT '{}',
                embedding_blob BLOB
            )
        """)
        self._conn.execute("""
            CREATE INDEX IF NOT EXISTS idx_memories_agent
            ON memories(agent_id, namespace)
        """)
        self._conn.execute("""
            CREATE INDEX IF NOT EXISTS idx_memories_created
            ON memories(created_at)
        """)
        self._conn.commit()

    def store(self, memory: Memory) -> Memory:
        """Store a memory. Generates embedding if not present."""
        with self._lock:
            if memory.embedding is None:
                memory.embedding = self.embedder.embed(memory.content)

            embedding_bytes = np.array(memory.embedding, dtype=np.float32).tobytes()
            metadata_json = json.dumps(memory.metadata)

            self._conn.execute(
                """
                INSERT OR REPLACE INTO memories
                (id, agent_id, namespace, content, importance, created_at,
                 accessed_at, access_count, metadata_json, embedding_blob)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    memory.id,
                    memory.agent_id,
                    memory.namespace,
                    memory.content,
                    memory.importance,
                    memory.created_at,
                    memory.accessed_at,
                    memory.access_count,
                    metadata_json,
                    embedding_bytes,
                ),
            )
            self._conn.commit()

            # Enforce namespace limit (only every 100 inserts for performance)
            self._insert_count = getattr(self, '_insert_count', 0) + 1
            if self._insert_count % 100 == 0:
                self._enforce_limit(memory.agent_id, memory.namespace)

        return memory

    def recall(
        self,
        query: str,
        agent_id: str,
        namespace: str = "default",
        limit: int = 0,
    ) -> List[RecallResult]:
        """
        Semantic recall — find memories most relevant to the query.

        Multi-signal ranking:
          - Semantic similarity (cosine) — 50% weight
          - Time decay (half-life) — 20% weight
          - Importance (capped, decaying) — 15% weight
          - Recency (last accessed) — 10% weight
          - Access frequency (diminishing returns) — 5% weight

        Post-processing:
          - Results below relevance_threshold are discarded.
          - Anti-redundancy: near-duplicate memories are collapsed.
          - Accessed memories get a small importance boost (capped).
          - Non-accessed memories decay slightly (anti-monopoly).
        """
        if limit <= 0:
            limit = self.config.max_recall_results

        query_embedding = self.embedder.embed(query)

        with self._lock:
            rows = self._conn.execute(
                """
                SELECT id, content, importance, created_at, accessed_at,
                       access_count, metadata_json, embedding_blob, namespace
                FROM memories
                WHERE agent_id = ? AND namespace = ?
                """,
                (agent_id, namespace),
            ).fetchall()

        results = self._score_rows(rows, query_embedding, agent_id)
        results = self._deduplicate(results)

        top_results = results[:limit]
        self._update_importance(top_results, agent_id, namespace)

        return top_results

    def recall_all(
        self,
        query: str,
        agent_id: str,
        limit: int = 0,
    ) -> List[RecallResult]:
        """
        Recall across ALL namespaces in a single query.
        No per-namespace loop — one scan, one sort.
        """
        if limit <= 0:
            limit = self.config.max_recall_results

        query_embedding = self.embedder.embed(query)

        with self._lock:
            rows = self._conn.execute(
                """
                SELECT id, content, importance, created_at, accessed_at,
                       access_count, metadata_json, embedding_blob, namespace
                FROM memories
                WHERE agent_id = ?
                """,
                (agent_id,),
            ).fetchall()

        results = self._score_rows(rows, query_embedding, agent_id)
        results = self._deduplicate(results)

        return results[:limit]

    def _score_rows(
        self,
        rows: list,
        query_embedding: list,
        agent_id: str,
    ) -> List[RecallResult]:
        """Score and rank memory rows against a query embedding."""
        results = []
        now = time.time()

        for row in rows:
            mem_id, content, importance, created_at, accessed_at, access_count, meta_json, emb_blob, namespace = row

            if emb_blob is None:
                continue
            stored_embedding = np.frombuffer(emb_blob, dtype=np.float32).tolist()

            # Signal 1: Semantic similarity (0.0 to 1.0)
            similarity = cosine_similarity(query_embedding, stored_embedding)

            # Signal 2: Time decay (0.0 to 1.0)
            age_hours = (now - created_at) / 3600.0
            if self.config.decay_enabled:
                half_life = self.config.decay_half_life_hours
                time_factor = math.pow(0.5, age_hours / half_life)
            else:
                time_factor = 1.0

            # Signal 3: Importance (normalized to 0.0 - 1.0)
            importance_factor = min(importance, self.config.importance_max) / self.config.importance_max

            # Signal 4: Recency of access (0.0 to 1.0)
            access_age_hours = (now - accessed_at) / 3600.0
            recency_factor = math.pow(0.5, access_age_hours / 720.0)

            # Signal 5: Access frequency (logarithmic — diminishing returns)
            frequency_factor = math.log1p(access_count) / math.log1p(100)
            frequency_factor = min(frequency_factor, 1.0)

            # Weighted combination
            final_score = (
                0.50 * similarity +
                0.20 * time_factor +
                0.15 * importance_factor +
                0.10 * recency_factor +
                0.05 * frequency_factor
            )

            # Apply relevance threshold — discard junk
            if final_score < self.config.relevance_threshold:
                continue

            memory = Memory(
                id=mem_id,
                content=content,
                namespace=namespace,
                agent_id=agent_id,
                importance=importance,
                created_at=created_at,
                accessed_at=accessed_at,
                access_count=access_count,
                metadata=json.loads(meta_json) if meta_json else {},
            )

            results.append(
                RecallResult(
                    memory=memory,
                    score=similarity,
                    decayed_score=final_score,
                )
            )

        results.sort(key=lambda r: r.decayed_score, reverse=True)
        return results

    def _deduplicate(self, results: List[RecallResult]) -> List[RecallResult]:
        """
        Anti-redundancy: remove near-duplicate memories from results.
        If two memories have >80% content overlap, keep the higher-scored one.
        """
        if len(results) <= 1:
            return results

        deduped = [results[0]]
        for candidate in results[1:]:
            is_dup = False
            for kept in deduped:
                overlap = self._content_overlap(candidate.content, kept.content)
                if overlap > 0.80:
                    is_dup = True
                    break
            if not is_dup:
                deduped.append(candidate)

        return deduped

    def _content_overlap(self, a: str, b: str) -> float:
        """Compute word-level Jaccard similarity between two texts."""
        words_a = set(a.lower().split())
        words_b = set(b.lower().split())
        if not words_a or not words_b:
            return 0.0
        intersection = words_a & words_b
        union = words_a | words_b
        return len(intersection) / len(union)

    def _update_importance(
        self,
        top_results: List[RecallResult],
        agent_id: str,
        namespace: str,
    ):
        """Boost accessed memories, decay non-accessed."""
        if not top_results:
            return

        now = time.time()
        accessed_ids = []

        with self._lock:
            for r in top_results:
                accessed_ids.append(r.memory.id)
                self._conn.execute(
                    """
                    UPDATE memories
                    SET accessed_at = ?, access_count = access_count + 1,
                        importance = MIN(importance + ?, ?)
                    WHERE id = ?
                    """,
                    (now, self.config.importance_boost_on_access,
                     self.config.importance_max, r.memory.id),
                )

            # Decay non-accessed memories slightly (anti-monopoly)
            if accessed_ids and self.config.importance_decay_rate > 0:
                placeholders = ",".join("?" * len(accessed_ids))
                self._conn.execute(
                    f"""
                    UPDATE memories
                    SET importance = MAX(importance - ?, 0.1)
                    WHERE agent_id = ? AND namespace = ?
                    AND id NOT IN ({placeholders})
                    """,
                    [self.config.importance_decay_rate, agent_id, namespace] + accessed_ids,
                )

            self._conn.commit()

    def forget(self, memory_id: str) -> bool:
        """Remove a specific memory."""
        with self._lock:
            cursor = self._conn.execute(
                "DELETE FROM memories WHERE id = ?", (memory_id,)
            )
            self._conn.commit()
            return cursor.rowcount > 0

    def forget_namespace(self, agent_id: str, namespace: str) -> int:
        """Remove all memories in a namespace."""
        with self._lock:
            cursor = self._conn.execute(
                "DELETE FROM memories WHERE agent_id = ? AND namespace = ?",
                (agent_id, namespace),
            )
            self._conn.commit()
            return cursor.rowcount

    def count(self, agent_id: str, namespace: str = "default") -> int:
        """Count memories in a namespace."""
        with self._lock:
            row = self._conn.execute(
                "SELECT COUNT(*) FROM memories WHERE agent_id = ? AND namespace = ?",
                (agent_id, namespace),
            ).fetchone()
            return row[0] if row else 0

    def decay_all(self, agent_id: str, namespace: str = "default") -> int:
        """
        Run decay pass — remove memories that have decayed below threshold.
        Returns number of memories removed.
        """
        if not self.config.decay_enabled:
            return 0

        now = time.time()
        threshold_seconds = self.config.decay_half_life_hours * 3600.0 * 5
        # Remove memories older than 5 half-lives (< 3% relevance)
        cutoff = now - threshold_seconds

        with self._lock:
            cursor = self._conn.execute(
                """
                DELETE FROM memories
                WHERE agent_id = ? AND namespace = ?
                AND created_at < ? AND importance < 2.0
                """,
                (agent_id, namespace, cutoff),
            )
            self._conn.commit()
            return cursor.rowcount

    def list_all(
        self, agent_id: str, namespace: str = "default"
    ) -> List[Memory]:
        """List all memories in a namespace (no scoring)."""
        with self._lock:
            rows = self._conn.execute(
                """
                SELECT id, content, importance, created_at, accessed_at,
                       access_count, metadata_json
                FROM memories
                WHERE agent_id = ? AND namespace = ?
                ORDER BY created_at DESC
                """,
                (agent_id, namespace),
            ).fetchall()

        memories = []
        for row in rows:
            mem_id, content, importance, created_at, accessed_at, access_count, meta_json = row
            memories.append(
                Memory(
                    id=mem_id,
                    content=content,
                    namespace=namespace,
                    agent_id=agent_id,
                    importance=importance,
                    created_at=created_at,
                    accessed_at=accessed_at,
                    access_count=access_count,
                    metadata=json.loads(meta_json) if meta_json else {},
                )
            )
        return memories

    def stats(self, agent_id: str) -> dict:
        """Get memory statistics for an agent."""
        with self._lock:
            namespaces = self._conn.execute(
                "SELECT DISTINCT namespace FROM memories WHERE agent_id = ?",
                (agent_id,),
            ).fetchall()

            total = self._conn.execute(
                "SELECT COUNT(*) FROM memories WHERE agent_id = ?",
                (agent_id,),
            ).fetchone()[0]

            oldest = self._conn.execute(
                "SELECT MIN(created_at) FROM memories WHERE agent_id = ?",
                (agent_id,),
            ).fetchone()[0]

            newest = self._conn.execute(
                "SELECT MAX(created_at) FROM memories WHERE agent_id = ?",
                (agent_id,),
            ).fetchone()[0]

        return {
            "agent_id": agent_id,
            "total_memories": total,
            "namespaces": [n[0] for n in namespaces],
            "oldest_memory_age_hours": (time.time() - oldest) / 3600.0 if oldest else 0,
            "newest_memory_age_hours": (time.time() - newest) / 3600.0 if newest else 0,
        }

    def _enforce_limit(self, agent_id: str, namespace: str):
        """Remove oldest low-importance memories if over limit."""
        count = self._conn.execute(
            "SELECT COUNT(*) FROM memories WHERE agent_id = ? AND namespace = ?",
            (agent_id, namespace),
        ).fetchone()[0]

        if count <= self.config.max_memories_per_namespace:
            return

        excess = count - self.config.max_memories_per_namespace
        self._conn.execute(
            """
            DELETE FROM memories WHERE id IN (
                SELECT id FROM memories
                WHERE agent_id = ? AND namespace = ?
                ORDER BY importance ASC, accessed_at ASC
                LIMIT ?
            )
            """,
            (agent_id, namespace, excess),
        )
        self._conn.commit()

    def close(self):
        """Close database connection."""
        if self._conn:
            self._conn.close()
            self._conn = None

    def __del__(self):
        self.close()