"""
ARK Memory — Persistent semantic memory for autonomous AI agents.

    from ark_memory import Agent

    agent = Agent("my-agent")
    agent.remember("user prefers dark mode")
    context = agent.recall("what does the user prefer?")
"""

from ark_memory.agent import Agent
from ark_memory.store import MemoryStore
from ark_memory.experience import Experience
from ark_memory.collector import Collector
from ark_memory.types import Memory, RecallResult

__version__ = "0.1.0"
__all__ = ["Agent", "MemoryStore", "Experience", "Collector", "Memory", "RecallResult"]