// Package context - engine.go implements the Dynamic Context Engine.
//
// This is the "brain" upgrade that transforms ARK from a static context
// optimizer into an intelligent context decision engine.
//
// The key insight: instead of loading tools once and hoping for the best,
// the engine observes execution results and dynamically adjusts context.
//
//   Task arrives → Load minimal tools → Execute → Observe result
//       ↑                                              │
//       └──── Expand context if confused/failed ◄──────┘
//
// This is what makes ARK a runtime, not just a library.
package context

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ──────────────────────────────────────────────────────────
// Dynamic Context Engine
// ──────────────────────────────────────────────────────────

// Engine wraps the Manager with dynamic context intelligence.
// It observes execution outcomes and adjusts context in real-time.
type Engine struct {
	mu      sync.Mutex
	mgr     *Manager
	tracer  *Tracer
	ranker  *ToolRanker
	config  EngineConfig
}

// EngineConfig controls the engine's behavior.
type EngineConfig struct {
	// InitialTools: how many tools to load on first attempt (start minimal)
	InitialTools int
	// ExpandStep: how many additional tools to load on each retry
	ExpandStep int
	// MaxRetries: maximum expand-and-retry cycles before giving up
	MaxRetries int
	// CompressFirst: try compressed schemas first, fall back to full if model struggles
	CompressFirst bool
	// ValidateCompression: test if compressed schemas are still usable
	ValidateCompression bool
}

// DefaultEngineConfig returns sensible defaults.
func DefaultEngineConfig() EngineConfig {
	return EngineConfig{
		InitialTools:        3,
		ExpandStep:          3,
		MaxRetries:          3,
		CompressFirst:       true,
		ValidateCompression: true,
	}
}

// NewEngine creates a dynamic context engine.
func NewEngine(mgr *Manager, config EngineConfig) *Engine {
	return &Engine{
		mgr:    mgr,
		tracer: NewTracer(),
		ranker: NewToolRanker(),
		config: config,
	}
}

// Tracer returns the engine's tracer for external inspection.
func (e *Engine) TracerRef() *Tracer {
	return e.tracer
}

// Manager returns the underlying context manager.
func (e *Engine) Manager() *Manager {
	return e.mgr
}

// ──────────────────────────────────────────────────────────
// Execution Cycle — the core dynamic loop
// ──────────────────────────────────────────────────────────

// ExecutionResult represents what happened when the model tried to use context.
type ExecutionResult struct {
	Success     bool
	ToolUsed    string   // Which tool the model actually called
	ToolsFailed []string // Tools the model tried but failed
	ErrorType   ErrorType
	ErrorMsg    string
	TokensUsed  int
	Latency     time.Duration
}

// ErrorType classifies what went wrong so the engine can react appropriately.
type ErrorType int

const (
	NoError          ErrorType = iota
	ErrToolNotFound            // Model wanted a tool that wasn't loaded
	ErrToolMisuse              // Model called a tool with wrong params (likely bad compression)
	ErrToolFailed              // Tool was called correctly but external failure
	ErrNoRelevantTool          // Model couldn't find any relevant tool
	ErrContextOverflow         // Ran out of context budget
)

func (et ErrorType) String() string {
	names := []string{"none", "tool_not_found", "tool_misuse", "tool_failed", "no_relevant_tool", "context_overflow"}
	if int(et) < len(names) {
		return names[et]
	}
	return "unknown"
}

// ContextPlan represents the engine's decision for a given task.
type ContextPlan struct {
	TaskID        string
	Query         string
	Attempt       int
	ToolsLoaded   []string
	ToolsFull     []string  // Tools loaded at full schema (not compressed)
	TokensUsed    int
	Strategy      string    // "minimal", "expanded", "full_schema", "max_context"
	TraceID       string
}

// PrepareContext is the main entry point. Given a task query,
// it returns a ContextPlan describing what was loaded and why.
func (e *Engine) PrepareContext(taskID, query string) *ContextPlan {
	e.mu.Lock()
	defer e.mu.Unlock()

	traceID := e.tracer.StartTrace(taskID, query)

	// Step 1: Get ranked tool candidates
	ranked := e.ranker.Rank(query, e.mgr)
	e.tracer.Record(traceID, TraceEvent{
		Type:    EventToolRanking,
		Message: fmt.Sprintf("Ranked %d candidate tools", len(ranked)),
		Data:    formatRankedTools(ranked),
	})

	// Step 2: Load initial minimal set
	loadCount := e.config.InitialTools
	if loadCount > len(ranked) {
		loadCount = len(ranked)
	}

	loaded := make([]string, 0)
	for i := 0; i < loadCount; i++ {
		tool := ranked[i]

		// Try compressed first if configured
		if e.config.CompressFirst {
			err := e.mgr.Load(tool.ID)
			if err == nil {
				loaded = append(loaded, tool.ID)
				e.tracer.Record(traceID, TraceEvent{
					Type:    EventToolLoaded,
					Message: fmt.Sprintf("Loaded tool %q (compressed, score=%.2f)", tool.ID, tool.Score),
				})
			}
		} else {
			err := e.mgr.Load(tool.ID)
			if err == nil {
				loaded = append(loaded, tool.ID)
			}
		}
	}

	usage := e.mgr.TokenUsage()

	plan := &ContextPlan{
		TaskID:      taskID,
		Query:       query,
		Attempt:     1,
		ToolsLoaded: loaded,
		TokensUsed:  usage["tools"],
		Strategy:    "minimal",
		TraceID:     traceID,
	}

	e.tracer.Record(traceID, TraceEvent{
		Type:    EventContextPrepared,
		Message: fmt.Sprintf("Initial context: %d tools, %d tokens, strategy=%s", len(loaded), usage["tools"], plan.Strategy),
	})

	return plan
}

// AdaptContext is called AFTER execution with the result.
// This is the "observe → expand → retry" loop.
// Returns an updated plan if context was changed, or nil if no adaptation needed.
func (e *Engine) AdaptContext(plan *ContextPlan, result ExecutionResult) *ContextPlan {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Record the execution result
	e.tracer.Record(plan.TraceID, TraceEvent{
		Type:    EventExecutionResult,
		Message: fmt.Sprintf("Attempt %d: success=%v, error=%s", plan.Attempt, result.Success, result.ErrorType),
		Data:    result.ErrorMsg,
	})

	// Update tool ranking based on result
	if result.Success && result.ToolUsed != "" {
		e.ranker.RecordSuccess(result.ToolUsed, result.Latency)
		// Record to context memory: "this tool worked for this query"
		e.ranker.RecordContext(plan.Query, []string{result.ToolUsed})
	}
	for _, failed := range result.ToolsFailed {
		e.ranker.RecordFailure(failed, result.ErrorType)
	}

	// If successful, no adaptation needed
	if result.Success {
		e.tracer.Record(plan.TraceID, TraceEvent{
			Type:    EventTraceComplete,
			Message: fmt.Sprintf("Task completed successfully in %d attempt(s)", plan.Attempt),
		})
		return nil
	}

	// Check retry budget
	if plan.Attempt >= e.config.MaxRetries {
		e.tracer.Record(plan.TraceID, TraceEvent{
			Type:    EventMaxRetriesReached,
			Message: fmt.Sprintf("Max retries (%d) reached, giving up", e.config.MaxRetries),
		})
		return nil
	}

	// ── Adaptation strategies based on error type ──

	newPlan := &ContextPlan{
		TaskID:  plan.TaskID,
		Query:   plan.Query,
		Attempt: plan.Attempt + 1,
		TraceID: plan.TraceID,
	}

	switch result.ErrorType {
	case ErrToolNotFound:
		// Model wanted a tool we didn't load → expand with more tools
		newPlan.Strategy = "expanded"
		e.expandContext(newPlan, plan)

	case ErrToolMisuse:
		// Model misunderstood the tool → compression was too aggressive
		// Fall back to full schemas for the tools we have
		newPlan.Strategy = "full_schema"
		e.upgradeToFullSchema(newPlan, plan)

	case ErrNoRelevantTool:
		// None of the loaded tools matched → broaden search
		newPlan.Strategy = "broadened"
		e.broadenContext(newPlan, plan)

	case ErrToolFailed:
		// Tool itself failed → swap to alternative tool
		newPlan.Strategy = "swapped"
		e.swapFailedTool(newPlan, plan, result.ToolsFailed)

	default:
		// Generic expansion
		newPlan.Strategy = "expanded"
		e.expandContext(newPlan, plan)
	}

	usage := e.mgr.TokenUsage()
	newPlan.TokensUsed = usage["tools"]
	newPlan.ToolsLoaded = e.mgr.ActiveBlocks()

	e.tracer.Record(plan.TraceID, TraceEvent{
		Type:    EventContextAdapted,
		Message: fmt.Sprintf("Adapted: strategy=%s, tools=%d, tokens=%d", newPlan.Strategy, len(newPlan.ToolsLoaded), newPlan.TokensUsed),
	})

	return newPlan
}

// ── Adaptation strategies ──

func (e *Engine) expandContext(newPlan, oldPlan *ContextPlan) {
	// Load more tools beyond what we already have
	ranked := e.ranker.Rank(oldPlan.Query, e.mgr)
	loaded := 0
	for _, tool := range ranked {
		if loaded >= e.config.ExpandStep {
			break
		}
		if !e.mgr.IsActive(tool.ID) {
			if err := e.mgr.Load(tool.ID); err == nil {
				loaded++
			}
		}
	}
}

func (e *Engine) upgradeToFullSchema(newPlan, oldPlan *ContextPlan) {
	// For currently loaded tools, evict compressed and reload full
	// This is the compression validation safety net
	fullTools := make([]string, 0)
	for _, id := range oldPlan.ToolsLoaded {
		e.mgr.Evict(id)
		// Temporarily disable compression for this load
		block := e.mgr.GetBlock(id)
		if block != nil {
			savedCompressed := block.Compressed
			savedTokens := block.CompressedTokens
			block.Compressed = ""
			block.CompressedTokens = 0
			if err := e.mgr.Load(id); err == nil {
				fullTools = append(fullTools, id)
			}
			// Restore compressed version for future use
			block.Compressed = savedCompressed
			block.CompressedTokens = savedTokens
		}
	}
	newPlan.ToolsFull = fullTools

	e.tracer.Record(newPlan.TraceID, TraceEvent{
		Type:    EventSchemaUpgrade,
		Message: fmt.Sprintf("Upgraded %d tools from compressed → full schema", len(fullTools)),
	})
}

func (e *Engine) broadenContext(newPlan, oldPlan *ContextPlan) {
	// The current query didn't match well. Try loading tools from
	// different categories than what we already have.
	activeTypes := make(map[string]bool)
	for _, id := range oldPlan.ToolsLoaded {
		// Extract server prefix from tool ID (e.g., "github" from "github-3")
		parts := strings.SplitN(id, "-", 2)
		if len(parts) > 0 {
			activeTypes[parts[0]] = true
		}
	}

	// Load tools from servers we haven't tried yet
	loaded := 0
	for id, block := range e.mgr.AllBlocks() {
		if loaded >= e.config.ExpandStep {
			break
		}
		if block.Type != BlockTool {
			continue
		}
		parts := strings.SplitN(id, "-", 2)
		if len(parts) > 0 && !activeTypes[parts[0]] {
			if err := e.mgr.Load(id); err == nil {
				loaded++
			}
		}
	}
}

func (e *Engine) swapFailedTool(newPlan, oldPlan *ContextPlan, failedTools []string) {
	// Evict failed tools and load alternatives
	for _, failed := range failedTools {
		e.mgr.Evict(failed)
	}

	// Load replacements
	ranked := e.ranker.Rank(oldPlan.Query, e.mgr)
	loaded := 0
	for _, tool := range ranked {
		if loaded >= len(failedTools) {
			break
		}
		isFailed := false
		for _, f := range failedTools {
			if tool.ID == f {
				isFailed = true
				break
			}
		}
		if !isFailed && !e.mgr.IsActive(tool.ID) {
			if err := e.mgr.Load(tool.ID); err == nil {
				loaded++
			}
		}
	}
}

// ──────────────────────────────────────────────────────────
// Tool Ranker v2 — Real scoring, not flat guesses
// ──────────────────────────────────────────────────────────

// ScoreWeights controls how the composite score is calculated.
// Tuning these changes ARK's decision-making personality.
type ScoreWeights struct {
	Relevance  float64 // How well tool matches the query
	Success    float64 // Historical success rate
	Latency    float64 // Penalty for slow tools
	TokenCost  float64 // Penalty for expensive tools
	Confidence float64 // Bonus for well-known tools (more data)
}

// DefaultWeights returns the recommended scoring weights.
func DefaultWeights() ScoreWeights {
	return ScoreWeights{
		Relevance:  0.45,
		Success:    0.30,
		Latency:    0.10,
		TokenCost:  0.05,
		Confidence: 0.10,
	}
}

// ToolRanker scores tools based on multiple signals, not just tag matching.
type ToolRanker struct {
	mu         sync.RWMutex
	successLog map[string]*ToolStats
	memory     *ContextMemory
	weights    ScoreWeights
}

// ToolStats tracks a tool's historical performance.
type ToolStats struct {
	TotalCalls      int
	Successes       int
	Failures        int
	ConsecutiveFails int  // Track streaks for confidence
	AvgLatency      time.Duration
	LastUsed        time.Time
	LastErrorType   ErrorType
}

// SuccessRate returns the tool's success rate (0.0 to 1.0).
func (ts *ToolStats) SuccessRate() float64 {
	if ts.TotalCalls == 0 {
		return 0.5 // Unknown = neutral, not good
	}
	return float64(ts.Successes) / float64(ts.TotalCalls)
}

// Confidence returns how much data we have on this tool (0.0 to 1.0).
// More data = more confident in the success rate.
func (ts *ToolStats) Confidence() float64 {
	// Bayesian-inspired: confidence grows with observations
	// 10 calls = ~0.67 confidence, 30 calls = ~0.86, 100 calls = ~0.95
	return float64(ts.TotalCalls) / (float64(ts.TotalCalls) + 5.0)
}

// RankedTool is a tool with its composite score and full breakdown.
type RankedTool struct {
	ID    string
	Score float64

	// Score breakdown — visible in traces
	RelevanceScore  float64
	SuccessScore    float64
	LatencyPenalty  float64
	CostPenalty     float64
	ConfidenceScore float64
	MemoryBonus     float64 // Bonus from context memory

	// Metadata for decision-making
	Predicted       string // "high", "medium", "low" confidence prediction
	HistoricalCalls int
}

// NewToolRanker creates a new ranker with default weights.
func NewToolRanker() *ToolRanker {
	return &ToolRanker{
		successLog: make(map[string]*ToolStats),
		memory:     NewContextMemory(),
		weights:    DefaultWeights(),
	}
}

// NewToolRankerWithWeights creates a ranker with custom weights.
func NewToolRankerWithWeights(weights ScoreWeights) *ToolRanker {
	return &ToolRanker{
		successLog: make(map[string]*ToolStats),
		memory:     NewContextMemory(),
		weights:    weights,
	}
}

// Rank returns tools sorted by composite score (best first).
// Each tool gets a real, differentiated score based on multiple signals.
func (r *ToolRanker) Rank(query string, mgr *Manager) []RankedTool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	queryLower := strings.ToLower(query)
	queryWords := strings.Fields(queryLower)

	blocks := mgr.AllBlocks()
	ranked := make([]RankedTool, 0)

	// Find max token count for normalization
	maxTokens := 1
	for _, block := range blocks {
		if block.Type == BlockTool && block.TokenCount > maxTokens {
			maxTokens = block.TokenCount
		}
	}

	for id, block := range blocks {
		if block.Type != BlockTool {
			continue
		}

		rt := RankedTool{ID: id}

		// ── 1. RELEVANCE SCORE (0.0 to 1.0) ──
		// Multi-signal relevance: exact match > prefix match > tag density
		exactMatches := 0
		prefixMatches := 0
		totalQueryWords := len(queryWords)

		for _, tag := range block.Tags {
			for _, word := range queryWords {
				if tag == word {
					exactMatches++
				} else if len(word) >= 4 && len(tag) >= 4 {
					// Prefix matching (e.g., "search" matches "searching")
					minLen := len(word)
					if len(tag) < minLen {
						minLen = len(tag)
					}
					if minLen >= 4 && word[:minLen-1] == tag[:minLen-1] {
						prefixMatches++
					}
				}
			}
		}

		if exactMatches == 0 && prefixMatches == 0 {
			continue // No relevance at all → skip
		}

		// Normalize: what fraction of query words matched?
		if totalQueryWords > 0 {
			exactRatio := float64(exactMatches) / float64(totalQueryWords)
			prefixRatio := float64(prefixMatches) / float64(totalQueryWords)
			rt.RelevanceScore = clamp(exactRatio*1.0+prefixRatio*0.4, 0, 1)
		}

		// Bonus for tool name containing query words (strong signal)
		nameLower := strings.ToLower(block.ID)
		for _, word := range queryWords {
			if len(word) >= 4 && strings.Contains(nameLower, word) {
				rt.RelevanceScore = clamp(rt.RelevanceScore+0.15, 0, 1)
			}
		}

		// ── 2. SUCCESS SCORE (0.0 to 1.0) ──
		stats, hasHistory := r.successLog[id]
		if hasHistory && stats.TotalCalls > 0 {
			rt.SuccessScore = stats.SuccessRate()
			rt.HistoricalCalls = stats.TotalCalls

			// Penalize tools on a failure streak
			if stats.ConsecutiveFails >= 3 {
				rt.SuccessScore *= 0.5 // Halve score for persistent failures
			}
		} else {
			rt.SuccessScore = 0.5 // Unknown = neutral
		}

		// ── 3. LATENCY PENALTY (0.0 to 1.0, lower is better) ──
		if hasHistory && stats.AvgLatency > 0 {
			// Normalize latency: 0ms = 0 penalty, 5000ms+ = 1.0 penalty
			latencyMs := float64(stats.AvgLatency.Milliseconds())
			rt.LatencyPenalty = clamp(latencyMs/5000.0, 0, 1)
		}

		// ── 4. TOKEN COST PENALTY (0.0 to 1.0, lower is better) ──
		if block.TokenCount > 0 {
			rt.CostPenalty = float64(block.TokenCount) / float64(maxTokens)
		}

		// ── 5. CONFIDENCE SCORE (0.0 to 1.0) ──
		// How much do we trust the success score?
		if hasHistory {
			rt.ConfidenceScore = stats.Confidence()
		} else {
			rt.ConfidenceScore = 0.0 // No data = no confidence
		}

		// ── 6. CONTEXT MEMORY BONUS ──
		// "Last time we used this tool for a similar query, it worked"
		rt.MemoryBonus = r.memory.QueryBonus(id, queryWords)

		// ── COMPOSITE SCORE ──
		rt.Score = (rt.RelevanceScore * r.weights.Relevance) +
			(rt.SuccessScore * r.weights.Success) -
			(rt.LatencyPenalty * r.weights.Latency) -
			(rt.CostPenalty * r.weights.TokenCost) +
			(rt.ConfidenceScore * r.weights.Confidence) +
			rt.MemoryBonus

		// ── PREDICTION ──
		rt.Predicted = predictOutcome(rt)

		ranked = append(ranked, rt)
	}

	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].Score > ranked[j].Score
	})

	return ranked
}

// RecordSuccess logs a successful tool execution.
func (r *ToolRanker) RecordSuccess(toolID string, latency time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	stats := r.getOrCreate(toolID)
	stats.TotalCalls++
	stats.Successes++
	stats.ConsecutiveFails = 0 // Reset streak
	stats.LastUsed = time.Now()
	if stats.AvgLatency == 0 {
		stats.AvgLatency = latency
	} else {
		// Exponential moving average (recent latency weighted more)
		stats.AvgLatency = time.Duration(float64(stats.AvgLatency)*0.7 + float64(latency)*0.3)
	}
}

// RecordFailure logs a failed tool execution.
func (r *ToolRanker) RecordFailure(toolID string, errType ErrorType) {
	r.mu.Lock()
	defer r.mu.Unlock()

	stats := r.getOrCreate(toolID)
	stats.TotalCalls++
	stats.Failures++
	stats.ConsecutiveFails++
	stats.LastUsed = time.Now()
	stats.LastErrorType = errType
}

// RecordContext records which tools worked for a query pattern.
// This feeds the context memory for future predictions.
func (r *ToolRanker) RecordContext(query string, successfulTools []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.memory.Record(query, successfulTools)
}

// GetStats returns the stats for a tool (for tracing/debugging).
func (r *ToolRanker) GetStats(toolID string) *ToolStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if s, ok := r.successLog[toolID]; ok {
		return s
	}
	return nil
}

func (r *ToolRanker) getOrCreate(id string) *ToolStats {
	if s, ok := r.successLog[id]; ok {
		return s
	}
	s := &ToolStats{}
	r.successLog[id] = s
	return s
}

// ──────────────────────────────────────────────────────────
// Context Memory — ARK learns what works
// ──────────────────────────────────────────────────────────

// ContextMemory remembers which tools succeeded for similar queries.
// This is the "ARK gets smarter over time" mechanism.
type ContextMemory struct {
	// Maps query pattern → tool IDs that succeeded
	patterns map[string]*MemoryEntry
}

// MemoryEntry records what worked for a query pattern.
type MemoryEntry struct {
	SuccessfulTools map[string]int // tool ID → success count
	TotalQueries    int
	LastUsed        time.Time
}

// NewContextMemory creates a new context memory.
func NewContextMemory() *ContextMemory {
	return &ContextMemory{
		patterns: make(map[string]*MemoryEntry),
	}
}

// Record stores a successful tool combination for a query.
func (cm *ContextMemory) Record(query string, tools []string) {
	pattern := extractPattern(query)
	entry, ok := cm.patterns[pattern]
	if !ok {
		entry = &MemoryEntry{
			SuccessfulTools: make(map[string]int),
		}
		cm.patterns[pattern] = entry
	}
	entry.TotalQueries++
	entry.LastUsed = time.Now()
	for _, t := range tools {
		entry.SuccessfulTools[t]++
	}
}

// QueryBonus returns a score bonus for a tool based on memory.
// If this tool has succeeded for similar queries before, it gets a boost.
func (cm *ContextMemory) QueryBonus(toolID string, queryWords []string) float64 {
	bestBonus := 0.0
	for pattern, entry := range cm.patterns {
		// Check if this pattern is similar to current query
		patternWords := strings.Fields(pattern)
		overlap := 0
		for _, pw := range patternWords {
			for _, qw := range queryWords {
				if pw == qw {
					overlap++
				}
			}
		}
		if overlap == 0 {
			continue
		}

		similarity := float64(overlap) / float64(max(len(patternWords), len(queryWords)))

		// Check if this tool was successful for this pattern
		if count, ok := entry.SuccessfulTools[toolID]; ok && entry.TotalQueries > 0 {
			successRate := float64(count) / float64(entry.TotalQueries)
			bonus := similarity * successRate * 0.15 // Max 0.15 bonus
			if bonus > bestBonus {
				bestBonus = bonus
			}
		}
	}
	return bestBonus
}

// extractPattern normalizes a query into a pattern for matching.
// Strips stop words and sorts remaining words for order-independent matching.
func extractPattern(query string) string {
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true,
		"to": true, "for": true, "on": true, "in": true, "at": true,
		"my": true, "me": true, "i": true, "we": true, "our": true,
		"and": true, "or": true, "of": true, "with": true, "from": true,
	}

	words := strings.Fields(strings.ToLower(query))
	meaningful := make([]string, 0)
	for _, w := range words {
		if !stopWords[w] && len(w) > 2 {
			meaningful = append(meaningful, w)
		}
	}
	sort.Strings(meaningful)
	return strings.Join(meaningful, " ")
}

// ──────────────────────────────────────────────────────────
// Prediction — choose better before failing
// ──────────────────────────────────────────────────────────

// predictOutcome estimates likelihood of success based on all signals.
func predictOutcome(rt RankedTool) string {
	// High confidence: good relevance + good history + enough data
	if rt.RelevanceScore >= 0.4 && rt.SuccessScore >= 0.7 && rt.ConfidenceScore >= 0.5 {
		return "high"
	}
	// Low confidence: poor history or failure streak
	if rt.SuccessScore < 0.3 || (rt.HistoricalCalls > 5 && rt.SuccessScore < 0.5) {
		return "low"
	}
	return "medium"
}

// ── Utilities ──

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ──────────────────────────────────────────────────────────
// Context Tracer — full decision audit trail
// ──────────────────────────────────────────────────────────

// Tracer records every context decision for debugging and observability.
type Tracer struct {
	mu     sync.Mutex
	traces map[string]*Trace
	nextID int
}

// Trace represents the full audit trail for a single task execution.
type Trace struct {
	ID        string
	TaskID    string
	Query     string
	StartTime time.Time
	Events    []TraceEvent
	Duration  time.Duration
}

// TraceEvent is a single recorded decision or observation.
type TraceEvent struct {
	Time    time.Time
	Type    TraceEventType
	Message string
	Data    string // Optional structured data
}

// TraceEventType categorizes trace events.
type TraceEventType int

const (
	EventToolRanking      TraceEventType = iota // Tools were scored and ranked
	EventToolLoaded                              // A tool was loaded into context
	EventToolEvicted                             // A tool was evicted
	EventContextPrepared                         // Initial context was assembled
	EventContextAdapted                          // Context was modified after feedback
	EventExecutionResult                         // Model execution outcome
	EventSchemaUpgrade                           // Compressed → full schema upgrade
	EventTraceComplete                           // Task finished
	EventMaxRetriesReached                       // Gave up after max retries
)

func (t TraceEventType) String() string {
	names := []string{
		"tool_ranking", "tool_loaded", "tool_evicted",
		"context_prepared", "context_adapted", "execution_result",
		"schema_upgrade", "trace_complete", "max_retries",
	}
	if int(t) < len(names) {
		return names[t]
	}
	return "unknown"
}

// NewTracer creates a new tracer.
func NewTracer() *Tracer {
	return &Tracer{
		traces: make(map[string]*Trace),
	}
}

// StartTrace begins recording a new task execution.
func (t *Tracer) StartTrace(taskID, query string) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.nextID++
	traceID := fmt.Sprintf("trace-%d-%s", t.nextID, taskID)

	t.traces[traceID] = &Trace{
		ID:        traceID,
		TaskID:    taskID,
		Query:     query,
		StartTime: time.Now(),
		Events:    make([]TraceEvent, 0),
	}

	return traceID
}

// Record adds an event to a trace.
func (t *Tracer) Record(traceID string, event TraceEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()

	trace, ok := t.traces[traceID]
	if !ok {
		return
	}

	event.Time = time.Now()
	trace.Events = append(trace.Events, event)
}

// GetTrace returns a trace by ID.
func (t *Tracer) GetTrace(traceID string) *Trace {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.traces[traceID]
}

// AllTraces returns all recorded traces.
func (t *Tracer) AllTraces() []*Trace {
	t.mu.Lock()
	defer t.mu.Unlock()
	traces := make([]*Trace, 0, len(t.traces))
	for _, tr := range t.traces {
		traces = append(traces, tr)
	}
	return traces
}

// PrintTrace outputs a human-readable trace to stdout.
func (t *Tracer) PrintTrace(traceID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	trace, ok := t.traces[traceID]
	if !ok {
		return fmt.Sprintf("trace %q not found", traceID)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n┌─ Trace: %s\n", trace.ID))
	sb.WriteString(fmt.Sprintf("│  Task: %s\n", trace.TaskID))
	sb.WriteString(fmt.Sprintf("│  Query: %q\n", trace.Query))
	sb.WriteString(fmt.Sprintf("│  Started: %s\n", trace.StartTime.Format("15:04:05.000")))
	sb.WriteString("│\n")

	for i, event := range trace.Events {
		elapsed := event.Time.Sub(trace.StartTime)
		prefix := "├"
		if i == len(trace.Events)-1 {
			prefix = "└"
		}
		sb.WriteString(fmt.Sprintf("%s─ [+%v] [%s] %s\n",
			prefix, elapsed.Round(time.Microsecond), event.Type, event.Message))
		if event.Data != "" {
			dataPrefix := "│"
			if i == len(trace.Events)-1 {
				dataPrefix = " "
			}
			sb.WriteString(fmt.Sprintf("%s     %s\n", dataPrefix, event.Data))
		}
	}

	return sb.String()
}

// ──────────────────────────────────────────────────────────
// Helper methods added to Manager for Engine support
// ──────────────────────────────────────────────────────────

// IsActive checks if a block is currently loaded.
func (m *Manager) IsActive(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active[id]
}

// GetBlock returns a registered block by ID (or nil).
func (m *Manager) GetBlock(id string) *ContextBlock {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.registry[id]
}

// AllBlocks returns all registered blocks.
func (m *Manager) AllBlocks() map[string]*ContextBlock {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// Return a copy to avoid race conditions
	copy := make(map[string]*ContextBlock, len(m.registry))
	for k, v := range m.registry {
		copy[k] = v
	}
	return copy
}

// ──────────────────────────────────────────────────────────
// Utilities
// ──────────────────────────────────────────────────────────

func formatRankedTools(ranked []RankedTool) string {
	if len(ranked) == 0 {
		return "no candidates"
	}
	limit := 5
	if limit > len(ranked) {
		limit = len(ranked)
	}
	parts := make([]string, limit)
	for i := 0; i < limit; i++ {
		r := ranked[i]
		parts[i] = fmt.Sprintf("%s(%.2f [r=%.2f s=%.2f p=%s])",
			r.ID, r.Score, r.RelevanceScore, r.SuccessScore, r.Predicted)
	}
	result := strings.Join(parts, ", ")
	if len(ranked) > limit {
		result += fmt.Sprintf(" +%d more", len(ranked)-limit)
	}
	return result
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}