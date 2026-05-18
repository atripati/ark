"""
Embedding engine for ARK Memory.

Two modes:
  1. Local (default): Hash-based embeddings using numpy. No API key needed.
     Good enough for semantic similarity on short text. Zero latency.
  2. OpenAI: Uses text-embedding-3-small for production quality.
     Requires OPENAI_API_KEY.

The local embedder uses character n-gram hashing projected into a fixed
dimensional space. This is NOT a language model — it captures lexical
similarity, not deep semantics. But for agent memory (short factual
statements), lexical overlap IS semantic similarity 90% of the time.
"""

import hashlib
import math
import os
from typing import List, Optional

import numpy as np

from ark_memory.types import MemoryConfig


class Embedder:
    """Generates embeddings for text."""

    def __init__(self, config: MemoryConfig):
        self.config = config
        self.dim = config.embedding_dim
        self._openai_client = None

    def embed(self, text: str) -> List[float]:
        if self.config.embedding_provider == "openai":
            return self._embed_openai(text)
        return self._embed_local(text)

    def embed_batch(self, texts: List[str]) -> List[List[float]]:
        if self.config.embedding_provider == "openai":
            return self._embed_openai_batch(texts)
        return [self._embed_local(t) for t in texts]

    def _embed_local(self, text: str) -> List[float]:
        """
        Hash-based embedding. Fast, deterministic, zero external dependencies.

        Method: Extract character n-grams (3, 4, 5), hash each to a position
        in the embedding vector, accumulate weights. Normalize to unit vector.
        This produces embeddings where similar text has high cosine similarity.
        """
        vec = np.zeros(self.dim, dtype=np.float64)
        text_lower = text.lower().strip()

        if not text_lower:
            return vec.tolist()

        # Character n-grams at multiple scales
        for n in (3, 4, 5):
            weight = 1.0 / n  # shorter n-grams get more weight
            for i in range(len(text_lower) - n + 1):
                ngram = text_lower[i : i + n]
                h = int(hashlib.md5(ngram.encode()).hexdigest(), 16)
                pos = h % self.dim
                sign = 1.0 if (h // self.dim) % 2 == 0 else -1.0
                vec[pos] += sign * weight

        # Word-level hashing for broader semantic signal
        words = text_lower.split()
        for i, word in enumerate(words):
            h = int(hashlib.sha256(word.encode()).hexdigest(), 16)
            pos = h % self.dim
            # Position-weighted: earlier words matter more
            position_weight = 1.0 / (1.0 + 0.1 * i)
            vec[pos] += position_weight

        # Bigram words
        for i in range(len(words) - 1):
            bigram = words[i] + " " + words[i + 1]
            h = int(hashlib.md5(bigram.encode()).hexdigest(), 16)
            pos = h % self.dim
            vec[pos] += 0.5

        # Normalize to unit vector
        norm = np.linalg.norm(vec)
        if norm > 0:
            vec = vec / norm

        return vec.tolist()

    def _embed_openai(self, text: str) -> List[float]:
        client = self._get_openai_client()
        response = client.embeddings.create(
            model="text-embedding-3-small",
            input=text,
        )
        return response.data[0].embedding

    def _embed_openai_batch(self, texts: List[str]) -> List[List[float]]:
        client = self._get_openai_client()
        response = client.embeddings.create(
            model="text-embedding-3-small",
            input=texts,
        )
        return [item.embedding for item in response.data]

    def _get_openai_client(self):
        if self._openai_client is None:
            try:
                import openai
            except ImportError:
                raise ImportError(
                    "OpenAI embeddings require the openai package. "
                    "Install with: pip install ark-memory[openai]"
                )
            api_key = os.environ.get("OPENAI_API_KEY")
            if not api_key:
                raise ValueError(
                    "OPENAI_API_KEY environment variable required for OpenAI embeddings"
                )
            self._openai_client = openai.OpenAI(api_key=api_key)
        return self._openai_client


def cosine_similarity(a: List[float], b: List[float]) -> float:
    """Compute cosine similarity between two vectors."""
    a_arr = np.array(a, dtype=np.float64)
    b_arr = np.array(b, dtype=np.float64)
    dot = np.dot(a_arr, b_arr)
    norm_a = np.linalg.norm(a_arr)
    norm_b = np.linalg.norm(b_arr)
    if norm_a == 0 or norm_b == 0:
        return 0.0
    return float(dot / (norm_a * norm_b))