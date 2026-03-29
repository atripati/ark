package context

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/atripati/ark/pkg/store"
)

type Engine struct {
	mu     sync.Mutex
	mgr    *Manager
	tracer *Tracer
	ranker *ToolRanker
	config EngineConfig
}

type EngineConfig struct {
	InitialTools        int
	ExpandStep          int
	MaxRetries          int
	CompressFirst       bool
	ValidateCompression bool
}

func DefaultEngineConfig() EngineConfig {
	return EngineConfig{
		InitialTools:        3,
		ExpandStep:          3,
		MaxRetries:          3,
		CompressFirst:       true,
		ValidateCompression: true,
	}
}

func NewEngine(mgr *Manager, config EngineConfig) *Engine {
	return &Engine{
		mgr:    mgr,
		tracer: NewTracer(),
		ranker: NewToolRanker(),
		config: config,
	}
}

func NewEngineWithStore(mgr *Manager, config EngineConfig, s store.Store) *Engine {
	return &Engine{
		mgr:    mgr,
		tracer: NewTracer(),
		ranker: NewToolRankerWithStore(s),
		config: config,
	}
}

func (e *Engine) RankTools(query string) []RankedTool {
	return e.ranker.Rank(query, e.mgr)
}

func (e *Engine) RecordToolCost(toolID string, costUSD float64) {
	e.ranker.RecordCost(toolID, costUSD)
}

func (e *Engine) TracerRef() *Tracer {
	return e.tracer
}

func (e *Engine) Manager() *Manager {
	return e.mgr
}

type ExecutionResult struct {
	Success     bool
	ToolUsed    string
	ToolsFailed []string
	ErrorType   ErrorType
	ErrorMsg    string
	TokensUsed  int
	Latency     time.Duration
}

type ErrorType int

const (
	NoError ErrorType = iota
	ErrToolNotFound
	ErrToolMisuse
	ErrToolFailed
	ErrNoRelevantTool
	ErrContextOverflow
)

func (et ErrorType) String() string {
	names := []string{"none", "tool_not_found", "tool_misuse", "tool_failed", "no_relevant_tool", "context_overflow"}
	if int(et) < len(names) {
		return names[et]
	}
	return "unknown"
}

type ContextPlan struct {
	TaskID      string
	Query       string
	Attempt     int
	ToolsLoaded []string
	ToolsFull   []string
	TokensUsed  int
	Strategy    string
	TraceID     string
}

func (e *Engine) PrepareContext(taskID, query string) *ContextPlan {
	e.mu.Lock()
	defer e.mu.Unlock()

	traceID := e.tracer.StartTrace(taskID, query)

	ranked := e.ranker.Rank(query, e.mgr)
	e.tracer.Record(traceID, TraceEvent{
		Type:    EventToolRanking,
		Message: fmt.Sprintf("Ranked %d candidate tools", len(ranked)),
		Data:    formatRankedTools(ranked),
	})

	loadCount := e.config.InitialTools
	if loadCount > len(ranked) {
		loadCount = len(ranked)
	}

	loaded := make([]string, 0)
	for i := 0; i < loadCount; i++ {
		tool := ranked[i]

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

func (e *Engine) AdaptContext(plan *ContextPlan, result ExecutionResult) *ContextPlan {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.tracer.Record(plan.TraceID, TraceEvent{
		Type:    EventExecutionResult,
		Message: fmt.Sprintf("Attempt %d: success=%v, error=%s", plan.Attempt, result.Success, result.ErrorType),
		Data:    result.ErrorMsg,
	})

	if result.Success && result.ToolUsed != "" {
		e.ranker.RecordSuccess(result.ToolUsed, result.Latency)

		e.ranker.RecordContext(plan.Query, []string{result.ToolUsed})
	}
	for _, failed := range result.ToolsFailed {
		e.ranker.RecordFailure(failed, result.ErrorType)
	}

	if result.Success {
		e.tracer.Record(plan.TraceID, TraceEvent{
			Type:    EventTraceComplete,
			Message: fmt.Sprintf("Task completed successfully in %d attempt(s)", plan.Attempt),
		})
		return nil
	}

	if plan.Attempt >= e.config.MaxRetries {
		e.tracer.Record(plan.TraceID, TraceEvent{
			Type:    EventMaxRetriesReached,
			Message: fmt.Sprintf("Max retries (%d) reached, giving up", e.config.MaxRetries),
		})
		return nil
	}

	newPlan := &ContextPlan{
		TaskID:  plan.TaskID,
		Query:   plan.Query,
		Attempt: plan.Attempt + 1,
		TraceID: plan.TraceID,
	}

	switch result.ErrorType {
	case ErrToolNotFound:
		newPlan.Strategy = "expanded"
		e.expandContext(newPlan, plan)

	case ErrToolMisuse:
		newPlan.Strategy = "full_schema"
		e.upgradeToFullSchema(newPlan, plan)

	case ErrNoRelevantTool:
		newPlan.Strategy = "broadened"
		e.broadenContext(newPlan, plan)

	case ErrToolFailed:
		newPlan.Strategy = "swapped"
		e.swapFailedTool(newPlan, plan, result.ToolsFailed)

	default:
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

func (e *Engine) expandContext(newPlan, oldPlan *ContextPlan) {
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
	fullTools := make([]string, 0)
	for _, id := range oldPlan.ToolsLoaded {
		e.mgr.Evict(id)
		block := e.mgr.GetBlock(id)
		if block != nil {
			savedCompressed := block.Compressed
			savedTokens := block.CompressedTokens
			block.Compressed = ""
			block.CompressedTokens = 0
			if err := e.mgr.Load(id); err == nil {
				fullTools = append(fullTools, id)
			}
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
	activeTypes := make(map[string]bool)
	for _, id := range oldPlan.ToolsLoaded {
		parts := strings.SplitN(id, "-", 2)
		if len(parts) > 0 {
			activeTypes[parts[0]] = true
		}
	}

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

	for _, failed := range failedTools {
		e.mgr.Evict(failed)
	}

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

type ScoreWeights struct {
	Relevance  float64
	Success    float64
	Latency    float64
	TokenCost  float64
	Confidence float64
}

func DefaultWeights() ScoreWeights {
	return ScoreWeights{
		Relevance:  0.40,
		Success:    0.30,
		Latency:    0.05,
		TokenCost:  0.15,
		Confidence: 0.10,
	}
}

type ToolRanker struct {
	mu         sync.RWMutex
	successLog map[string]*ToolStats
	memory     *ContextMemory
	weights    ScoreWeights
	store      store.Store
}

type ToolStats struct {
	TotalCalls       int
	Successes        int
	Failures         int
	ConsecutiveFails int
	AvgLatency       time.Duration
	AvgCost          float64
	LastUsed         time.Time
	LastErrorType    ErrorType
}

func (ts *ToolStats) SuccessRate() float64 {
	if ts.TotalCalls == 0 {
		return 0.5
	}
	return float64(ts.Successes) / float64(ts.TotalCalls)
}

func (ts *ToolStats) Confidence() float64 {
	return float64(ts.TotalCalls) / (float64(ts.TotalCalls) + 5.0)
}

type RankedTool struct {
	ID              string
	Score           float64
	RelevanceScore  float64
	SuccessScore    float64
	LatencyPenalty  float64
	CostPenalty     float64
	ActualCostUSD   float64
	ConfidenceScore float64
	MemoryBonus     float64

	Predicted       string
	HistoricalCalls int
}

func NewToolRanker() *ToolRanker {
	return &ToolRanker{
		successLog: make(map[string]*ToolStats),
		memory:     NewContextMemory(),
		weights:    DefaultWeights(),
	}
}

func NewToolRankerWithStore(s store.Store) *ToolRanker {
	r := &ToolRanker{
		successLog: make(map[string]*ToolStats),
		memory:     NewContextMemory(),
		weights:    DefaultWeights(),
		store:      s,
	}

	if s == nil {
		return r
	}

	records, err := s.LoadAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ark/store] load tool stats: %v\n", err)
	} else {
		for _, rec := range records {
			r.successLog[rec.ToolID] = &ToolStats{
				TotalCalls:       rec.TotalCalls,
				Successes:        rec.Successes,
				Failures:         rec.Failures,
				ConsecutiveFails: rec.ConsecutiveFails,
				AvgLatency:       time.Duration(rec.AvgLatencyMs) * time.Millisecond,
				LastUsed:         rec.LastUsed,
				LastErrorType:    ErrorType(rec.LastErrorType),
			}
		}
	}

	patterns, err := s.LoadPatterns()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ark/store] load patterns: %v\n", err)
	} else {
		for _, p := range patterns {
			r.memory.patterns[p.Pattern] = &MemoryEntry{
				SuccessfulTools: p.SuccessfulTools,
				TotalQueries:    p.TotalQueries,
				LastUsed:        p.LastUsed,
			}
		}
	}

	return r
}

func NewToolRankerWithWeights(weights ScoreWeights) *ToolRanker {
	return &ToolRanker{
		successLog: make(map[string]*ToolStats),
		memory:     NewContextMemory(),
		weights:    weights,
	}
}

func (r *ToolRanker) Rank(query string, mgr *Manager) []RankedTool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	queryLower := strings.ToLower(query)
	queryWords := strings.Fields(queryLower)

	blocks := mgr.AllBlocks()
	ranked := make([]RankedTool, 0)

	blockIDs := make([]string, 0, len(blocks))
	for id := range blocks {
		blockIDs = append(blockIDs, id)
	}
	sort.Strings(blockIDs)

	maxTokens := 1
	for _, block := range blocks {
		if block.Type == BlockTool && block.TokenCount > maxTokens {
			maxTokens = block.TokenCount
		}
	}

	for _, id := range blockIDs {
		block := blocks[id]
		if block.Type != BlockTool {
			continue
		}

		rt := RankedTool{ID: id}

		exactMatches := 0
		prefixMatches := 0
		totalQueryWords := len(queryWords)

		for _, tag := range block.Tags {
			for _, word := range queryWords {
				if tag == word {
					exactMatches++
				} else if len(word) >= 4 && len(tag) >= 4 {

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
			continue
		}

		if totalQueryWords > 0 {
			exactRatio := float64(exactMatches) / float64(totalQueryWords)
			prefixRatio := float64(prefixMatches) / float64(totalQueryWords)
			rt.RelevanceScore = clamp(exactRatio*1.0+prefixRatio*0.4, 0, 1)
		}

		nameLower := strings.ToLower(block.ID)
		for _, word := range queryWords {
			if len(word) >= 4 && strings.Contains(nameLower, word) {
				rt.RelevanceScore = clamp(rt.RelevanceScore+0.15, 0, 1)
			}
		}

		stats, hasHistory := r.successLog[id]
		if hasHistory && stats.TotalCalls > 0 {
			rt.SuccessScore = stats.SuccessRate()
			rt.HistoricalCalls = stats.TotalCalls

			if stats.ConsecutiveFails >= 3 {
				rt.SuccessScore *= 0.5
			}
		} else {
			rt.SuccessScore = 0.5
		}

		if hasHistory && stats.AvgLatency > 0 {

			latencyMs := float64(stats.AvgLatency.Milliseconds())
			rt.LatencyPenalty = clamp(latencyMs/5000.0, 0, 1)
		}

		if hasHistory && stats.AvgCost > 0 {
			rt.ActualCostUSD = stats.AvgCost
			rt.CostPenalty = clamp(stats.AvgCost/0.01, 0, 1)
		} else if block.TokenCount > 0 {
			rt.CostPenalty = float64(block.TokenCount) / float64(maxTokens)
		}

		if hasHistory {
			rt.ConfidenceScore = stats.Confidence()
		} else {
			rt.ConfidenceScore = 0.0
		}

		rt.MemoryBonus = r.memory.QueryBonus(id, queryWords)

		rt.Score = (rt.RelevanceScore * r.weights.Relevance) +
			(rt.SuccessScore * r.weights.Success) -
			(rt.LatencyPenalty * r.weights.Latency) -
			(rt.CostPenalty * r.weights.TokenCost) +
			(rt.ConfidenceScore * r.weights.Confidence) +
			rt.MemoryBonus

		rt.Predicted = predictOutcome(rt)

		ranked = append(ranked, rt)
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].ID < ranked[j].ID
	})

	return ranked
}

func (r *ToolRanker) RecordSuccess(toolID string, latency time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	stats := r.getOrCreate(toolID)
	stats.TotalCalls++
	stats.Successes++
	stats.ConsecutiveFails = 0
	stats.LastUsed = time.Now()
	if stats.AvgLatency == 0 {
		stats.AvgLatency = latency
	} else {
		stats.AvgLatency = time.Duration(float64(stats.AvgLatency)*0.7 + float64(latency)*0.3)
	}

	r.persist(toolID, stats)
}

func (r *ToolRanker) RecordFailure(toolID string, errType ErrorType) {
	r.mu.Lock()
	defer r.mu.Unlock()

	stats := r.getOrCreate(toolID)
	stats.TotalCalls++
	stats.Failures++
	stats.ConsecutiveFails++
	stats.LastUsed = time.Now()
	stats.LastErrorType = errType

	r.persist(toolID, stats)
}

func (r *ToolRanker) RecordContext(query string, successfulTools []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.memory.Record(query, successfulTools)

	pattern := extractPattern(query)
	if entry, ok := r.memory.patterns[pattern]; ok {
		r.persistPattern(pattern, entry)
	}
}

func (r *ToolRanker) GetStats(toolID string) *ToolStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if s, ok := r.successLog[toolID]; ok {
		return s
	}
	return nil
}

func (r *ToolRanker) RecordCost(toolID string, costUSD float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stats := r.getOrCreate(toolID)
	if stats.AvgCost == 0 {
		stats.AvgCost = costUSD
	} else {
		stats.AvgCost = stats.AvgCost*0.7 + costUSD*0.3
	}
}

func (r *ToolRanker) getOrCreate(id string) *ToolStats {
	if s, ok := r.successLog[id]; ok {
		return s
	}
	s := &ToolStats{}
	r.successLog[id] = s
	return s
}

func (r *ToolRanker) persist(toolID string, stats *ToolStats) {
	if r.store == nil {
		return
	}
	if err := r.store.Save(store.ToolStatsRecord{
		ToolID:           toolID,
		TotalCalls:       stats.TotalCalls,
		Successes:        stats.Successes,
		Failures:         stats.Failures,
		ConsecutiveFails: stats.ConsecutiveFails,
		AvgLatencyMs:     stats.AvgLatency.Milliseconds(),
		LastUsed:         stats.LastUsed,
		LastErrorType:    int(stats.LastErrorType),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "[ark/store] persist tool %s: %v\n", toolID, err)
	}
}

func (r *ToolRanker) persistPattern(pattern string, entry *MemoryEntry) {
	if r.store == nil {
		return
	}
	toolsCopy := make(map[string]int, len(entry.SuccessfulTools))
	for k, v := range entry.SuccessfulTools {
		toolsCopy[k] = v
	}
	if err := r.store.SavePattern(store.QueryPatternRecord{
		Pattern:         pattern,
		SuccessfulTools: toolsCopy,
		TotalQueries:    entry.TotalQueries,
		LastUsed:        entry.LastUsed,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "[ark/store] persist pattern %q: %v\n", pattern, err)
	}
}

type ContextMemory struct {
	patterns map[string]*MemoryEntry
}

type MemoryEntry struct {
	SuccessfulTools map[string]int
	TotalQueries    int
	LastUsed        time.Time
}

func NewContextMemory() *ContextMemory {
	return &ContextMemory{
		patterns: make(map[string]*MemoryEntry),
	}
}

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

func (cm *ContextMemory) QueryBonus(toolID string, queryWords []string) float64 {
	bestBonus := 0.0
	for pattern, entry := range cm.patterns {
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

		if count, ok := entry.SuccessfulTools[toolID]; ok && count > 0 {
			dataBonusFactor := float64(count) / (float64(count) + 2.0)
			bonus := similarity * dataBonusFactor * 0.40
			if bonus > bestBonus {
				bestBonus = bonus
			}
		}
	}
	return bestBonus
}

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

func predictOutcome(rt RankedTool) string {
	if rt.RelevanceScore >= 0.4 && rt.SuccessScore >= 0.7 && rt.ConfidenceScore >= 0.5 {
		return "high"
	}

	if rt.SuccessScore < 0.3 || (rt.HistoricalCalls > 5 && rt.SuccessScore < 0.5) {
		return "low"
	}
	return "medium"
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

type Tracer struct {
	mu     sync.Mutex
	traces map[string]*Trace
	nextID int
}

type Trace struct {
	ID        string
	TaskID    string
	Query     string
	StartTime time.Time
	Events    []TraceEvent
	Duration  time.Duration
}

type TraceEvent struct {
	Time    time.Time
	Type    TraceEventType
	Message string
	Data    string
}

type TraceEventType int

const (
	EventToolRanking TraceEventType = iota
	EventToolLoaded
	EventToolEvicted
	EventContextPrepared
	EventContextAdapted
	EventExecutionResult
	EventSchemaUpgrade
	EventTraceComplete
	EventMaxRetriesReached
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

func NewTracer() *Tracer {
	return &Tracer{
		traces: make(map[string]*Trace),
	}
}

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

func (t *Tracer) GetTrace(traceID string) *Trace {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.traces[traceID]
}

func (t *Tracer) AllTraces() []*Trace {
	t.mu.Lock()
	defer t.mu.Unlock()
	traces := make([]*Trace, 0, len(t.traces))
	for _, tr := range t.traces {
		traces = append(traces, tr)
	}
	return traces
}

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

func (m *Manager) IsActive(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active[id]
}

func (m *Manager) GetBlock(id string) *ContextBlock {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.registry[id]
}

func (m *Manager) AllBlocks() map[string]*ContextBlock {
	m.mu.RLock()
	defer m.mu.RUnlock()
	copy := make(map[string]*ContextBlock, len(m.registry))
	for k, v := range m.registry {
		copy[k] = v
	}
	return copy
}

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

		learned := ""
		if r.HistoricalCalls > 0 {
			learned = fmt.Sprintf(" calls=%d", r.HistoricalCalls)
		}
		if r.MemoryBonus > 0 {
			learned += fmt.Sprintf(" mem=+%.2f", r.MemoryBonus)
		}
		if r.ActualCostUSD > 0 {
			// Show real dollars AND the actual score impact (penalty × weight)
			costImpact := r.CostPenalty * 0.15 // weight is 15%
			learned += fmt.Sprintf(" cost=$%.6f impact=-%.3f", r.ActualCostUSD, costImpact)
		} else if r.CostPenalty > 0.001 {
			costImpact := r.CostPenalty * 0.15
			learned += fmt.Sprintf(" cost_impact=-%.3f", costImpact)
		}
		parts[i] = fmt.Sprintf("%s(%.2f [r=%.2f s=%.2f c=%.2f p=%s%s])",
			r.ID, r.Score, r.RelevanceScore, r.SuccessScore, r.ConfidenceScore, r.Predicted, learned)
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
