"""Core types for ARK Memory."""

import time
import uuid
from dataclasses import dataclass, field
from typing import Optional


@dataclass
class Memory:
    """A single memory unit stored in the memory system."""

    id: str = field(default_factory=lambda: uuid.uuid4().hex[:16])
    content: str = ""
    namespace: str = "default"
    agent_id: str = ""
    importance: float = 1.0
    created_at: float = field(default_factory=time.time)
    accessed_at: float = field(default_factory=time.time)
    access_count: int = 0
    metadata: dict = field(default_factory=dict)
    embedding: Optional[list] = None

    def age_seconds(self) -> float:
        return time.time() - self.created_at

    def age_hours(self) -> float:
        return self.age_seconds() / 3600.0

    def age_days(self) -> float:
        return self.age_seconds() / 86400.0


@dataclass
class RecallResult:
    """Result of a recall query — a memory plus its relevance score."""

    memory: Memory
    score: float  # 0.0 = irrelevant, 1.0 = exact match
    decayed_score: float = 0.0  # score after time decay applied

    @property
    def content(self) -> str:
        return self.memory.content


@dataclass
class MemoryConfig:
    """Configuration for the memory system."""

    # Storage
    db_path: str = "ark_memory.db"

    # Embedding
    embedding_dim: int = 384  # dimension of embedding vectors
    embedding_provider: str = "local"  # "local" (hash-based) or "openai"

    # Decay
    decay_enabled: bool = True
    decay_half_life_hours: float = 168.0  # 7 days — memory loses half relevance

    # Limits
    max_memories_per_namespace: int = 10000
    max_recall_results: int = 10

    # Relevance
    relevance_threshold: float = 0.05  # discard results below this score
    
    # Importance
    default_importance: float = 1.0
    importance_boost_on_access: float = 0.05  # smaller boost prevents monopoly
    importance_max: float = 5.0  # hard cap — no memory dominates forever
    importance_decay_rate: float = 0.01  # importance decays slightly each recall cycle