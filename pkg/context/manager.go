package context

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type TokenBudget struct {
	Total        int
	System       int
	Tools        int
	Memory       int
	Conversation int
	Working      int
	Reserved     int
}

func DefaultBudget(totalTokens int) TokenBudget {
	return TokenBudget{
		Total:        totalTokens,
		System:       int(float64(totalTokens) * 0.05),
		Tools:        int(float64(totalTokens) * 0.10),
		Memory:       int(float64(totalTokens) * 0.10),
		Conversation: int(float64(totalTokens) * 0.35),
		Working:      int(float64(totalTokens) * 0.30),
		Reserved:     int(float64(totalTokens) * 0.10),
	}
}

type ContextBlock struct {
	ID               string
	Type             BlockType
	Content          string
	TokenCount       int
	Priority         float64
	LastUsed         time.Time
	UseCount         int
	Compressed       string
	CompressedTokens int
	Tags             []string
}

type BlockType int

const (
	BlockTool BlockType = iota
	BlockMemory
	BlockConversation
	BlockWorking
	BlockSystem
)

func (bt BlockType) String() string {
	switch bt {
	case BlockTool:
		return "tool"
	case BlockMemory:
		return "memory"
	case BlockConversation:
		return "conversation"
	case BlockWorking:
		return "working"
	case BlockSystem:
		return "system"
	default:
		return "unknown"
	}
}

type Manager struct {
	mu sync.RWMutex

	budget   TokenBudget
	registry map[string]*ContextBlock
	active   map[string]bool
	usage    map[BlockType]int

	onEvict func(block *ContextBlock)
	onLoad  func(block *ContextBlock)

	stats ManagerStats
}

type ManagerStats struct {
	TotalLoads        int64
	TotalEvictions    int64
	TotalCompressions int64
	CacheHits         int64
	CacheMisses       int64
	TokensSaved       int64
	PeakUsage         int
}

func NewManager(budget TokenBudget) *Manager {
	return &Manager{
		budget:   budget,
		registry: make(map[string]*ContextBlock),
		active:   make(map[string]bool),
		usage:    make(map[BlockType]int),
	}
}

func (m *Manager) Register(block *ContextBlock) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registry[block.ID] = block
}

func (m *Manager) RegisterTool(id, name, description string, fullSchema string) *ContextBlock {
	block := &ContextBlock{
		ID:               id,
		Type:             BlockTool,
		Content:          fullSchema,
		TokenCount:       EstimateTokens(fullSchema),
		Priority:         0.5, // Default priority, adjusted by usage
		LastUsed:         time.Now(),
		Compressed:       compressToolSchema(name, description),
		CompressedTokens: EstimateTokens(compressToolSchema(name, description)),
		Tags:             extractTags(name, description),
	}
	m.Register(block)
	return block
}

func (m *Manager) Load(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	block, exists := m.registry[id]
	if !exists {
		return fmt.Errorf("ark/context: block %q not found in registry", id)
	}

	if m.active[id] {
		block.LastUsed = time.Now()
		block.UseCount++
		m.stats.CacheHits++
		return nil
	}

	m.stats.CacheMisses++

	budgetForType := m.budgetFor(block.Type)
	currentUsage := m.usage[block.Type]
	needed := block.TokenCount

	useCompressed := false
	if currentUsage+needed > budgetForType && block.Compressed != "" {
		needed = block.CompressedTokens
		useCompressed = true
	}

	if currentUsage+needed > budgetForType {
		freed := m.evictLowestPriority(block.Type, needed-(budgetForType-currentUsage))
		if freed < needed-(budgetForType-currentUsage) {
			return fmt.Errorf("ark/context: cannot fit block %q (need %d tokens, budget %d, used %d)",
				id, needed, budgetForType, currentUsage)
		}
	}

	m.active[id] = true
	if useCompressed {
		m.usage[block.Type] += block.CompressedTokens
		m.stats.TokensSaved += int64(block.TokenCount - block.CompressedTokens)
		m.stats.TotalCompressions++
	} else {
		m.usage[block.Type] += block.TokenCount
	}

	block.LastUsed = time.Now()
	block.UseCount++
	m.stats.TotalLoads++

	totalUsage := m.totalUsage()
	if totalUsage > m.stats.PeakUsage {
		m.stats.PeakUsage = totalUsage
	}

	if m.onLoad != nil {
		m.onLoad(block)
	}

	return nil
}

func (m *Manager) LoadRelevant(query string, maxBlocks int) []string {
	m.mu.RLock()
	candidates := make([]*ContextBlock, 0)
	for _, block := range m.registry {
		if !m.active[block.ID] {
			candidates = append(candidates, block)
		}
	}
	m.mu.RUnlock()

	type scored struct {
		block *ContextBlock
		score float64
	}
	scoredBlocks := make([]scored, 0, len(candidates))
	queryLower := strings.ToLower(query)
	queryWords := strings.Fields(queryLower)

	for _, block := range candidates {
		tagScore := 0.0
		for _, tag := range block.Tags {
			for _, word := range queryWords {
				if tag == word {
					tagScore += 0.5
				} else if len(word) >= 5 && len(tag) >= 5 && (tag == word[:len(word)-1] || word == tag[:len(tag)-1]) {
					tagScore += 0.1
				}
			}
		}

		if tagScore == 0 {
			continue
		}

		score := tagScore

		age := time.Since(block.LastUsed)
		if age < 5*time.Minute {
			score += 0.2
		} else if age < 30*time.Minute {
			score += 0.1
		}
		score += float64(block.UseCount) * 0.05

		score += block.Priority * 0.2

		scoredBlocks = append(scoredBlocks, scored{block, score})
	}

	sort.Slice(scoredBlocks, func(i, j int) bool {
		return scoredBlocks[i].score > scoredBlocks[j].score
	})

	loaded := make([]string, 0)
	limit := maxBlocks
	if limit > len(scoredBlocks) {
		limit = len(scoredBlocks)
	}

	for i := 0; i < limit; i++ {
		m.mu.Lock()
		err := m.loadInternal(scoredBlocks[i].block)
		m.mu.Unlock()
		if err == nil {
			loaded = append(loaded, scoredBlocks[i].block.ID)
		}
	}

	return loaded
}

func (m *Manager) Render() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Sort active IDs for deterministic rendering (map order is random)
	activeIDs := make([]string, 0, len(m.active))
	for id := range m.active {
		activeIDs = append(activeIDs, id)
	}
	sort.Strings(activeIDs)

	var system, tools, memory, working, conversation []string

	for _, id := range activeIDs {
		block := m.registry[id]
		content := block.Content
		if block.CompressedTokens > 0 && m.usage[block.Type] <= m.budgetFor(block.Type) {
			content = m.effectiveContent(block)
		}

		switch block.Type {
		case BlockSystem:
			system = append(system, content)
		case BlockTool:
			tools = append(tools, content)
		case BlockMemory:
			memory = append(memory, content)
		case BlockWorking:
			working = append(working, content)
		case BlockConversation:
			conversation = append(conversation, content)
		}
	}

	var parts []string
	if len(system) > 0 {
		parts = append(parts, "## System\n"+strings.Join(system, "\n"))
	}
	if len(tools) > 0 {
		parts = append(parts, "## Available Tools\n"+strings.Join(tools, "\n---\n"))
	}
	if len(memory) > 0 {
		parts = append(parts, "## Memory\n"+strings.Join(memory, "\n"))
	}
	if len(conversation) > 0 {
		parts = append(parts, strings.Join(conversation, "\n"))
	}
	if len(working) > 0 {
		parts = append(parts, "## Current Task\n"+strings.Join(working, "\n"))
	}

	return strings.Join(parts, "\n\n")
}

func (m *Manager) Evict(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evictBlock(id)
}

func (m *Manager) Stats() ManagerStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stats
}

func (m *Manager) Usage() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	total := m.totalUsage()
	pct := float64(total) / float64(m.budget.Total) * 100

	return fmt.Sprintf(
		"Context Usage: %d/%d tokens (%.1f%%)\n"+
			"  System:       %d/%d\n"+
			"  Tools:        %d/%d\n"+
			"  Memory:       %d/%d\n"+
			"  Conversation: %d/%d\n"+
			"  Working:      %d/%d\n"+
			"  Tokens Saved: %d (via compression)",
		total, m.budget.Total, pct,
		m.usage[BlockSystem], m.budget.System,
		m.usage[BlockTool], m.budget.Tools,
		m.usage[BlockMemory], m.budget.Memory,
		m.usage[BlockConversation], m.budget.Conversation,
		m.usage[BlockWorking], m.budget.Working,
		m.stats.TokensSaved,
	)
}

func (m *Manager) ActiveBlocks() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.active))
	for id := range m.active {
		ids = append(ids, id)
	}
	return ids
}

func (m *Manager) TokenUsage() map[string]int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return map[string]int{
		"total":        m.totalUsage(),
		"system":       m.usage[BlockSystem],
		"tools":        m.usage[BlockTool],
		"memory":       m.usage[BlockMemory],
		"conversation": m.usage[BlockConversation],
		"working":      m.usage[BlockWorking],
		"budget_total": m.budget.Total,
	}
}

func (m *Manager) ActiveBlockDetails() []BlockInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	details := make([]BlockInfo, 0, len(m.active))
	for id := range m.active {
		block := m.registry[id]
		details = append(details, BlockInfo{
			ID:           block.ID,
			Type:         block.Type.String(),
			FullTokens:   block.TokenCount,
			LoadedTokens: m.effectiveTokens(block),
			Compressed:   block.CompressedTokens > 0 && block.CompressedTokens < block.TokenCount,
		})
	}
	return details
}

type BlockInfo struct {
	ID           string
	Type         string
	FullTokens   int
	LoadedTokens int
	Compressed   bool
}

func (m *Manager) budgetFor(bt BlockType) int {
	switch bt {
	case BlockSystem:
		return m.budget.System
	case BlockTool:
		return m.budget.Tools
	case BlockMemory:
		return m.budget.Memory
	case BlockConversation:
		return m.budget.Conversation
	case BlockWorking:
		return m.budget.Working
	default:
		return 0
	}
}

func (m *Manager) totalUsage() int {
	total := 0
	for _, v := range m.usage {
		total += v
	}
	return total
}

func (m *Manager) evictLowestPriority(bt BlockType, tokensNeeded int) int {
	type candidate struct {
		id    string
		block *ContextBlock
	}
	candidates := make([]candidate, 0)
	for id := range m.active {
		block := m.registry[id]
		if block.Type == bt {
			candidates = append(candidates, candidate{id, block})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].block.Priority == candidates[j].block.Priority {
			return candidates[i].block.LastUsed.Before(candidates[j].block.LastUsed)
		}
		return candidates[i].block.Priority < candidates[j].block.Priority
	})

	freed := 0
	for _, c := range candidates {
		if freed >= tokensNeeded {
			break
		}
		tokens := m.effectiveTokens(c.block)
		m.evictBlock(c.id)
		freed += tokens
	}
	return freed
}

func (m *Manager) evictBlock(id string) {
	if !m.active[id] {
		return
	}
	block := m.registry[id]
	tokens := m.effectiveTokens(block)
	delete(m.active, id)
	m.usage[block.Type] -= tokens
	if m.usage[block.Type] < 0 {
		m.usage[block.Type] = 0
	}
	m.stats.TotalEvictions++
	if m.onEvict != nil {
		m.onEvict(block)
	}
}

func (m *Manager) effectiveContent(block *ContextBlock) string {
	if block.Compressed != "" && block.CompressedTokens < block.TokenCount {
		return block.Compressed
	}
	return block.Content
}

func (m *Manager) effectiveTokens(block *ContextBlock) int {
	if block.Compressed != "" && block.CompressedTokens < block.TokenCount {
		return block.CompressedTokens
	}
	return block.TokenCount
}

func (m *Manager) loadInternal(block *ContextBlock) error {
	if m.active[block.ID] {
		block.LastUsed = time.Now()
		block.UseCount++
		return nil
	}

	budgetForType := m.budgetFor(block.Type)
	needed := m.effectiveTokens(block)

	if m.usage[block.Type]+needed > budgetForType {
		freed := m.evictLowestPriority(block.Type, needed-(budgetForType-m.usage[block.Type]))
		if freed < needed-(budgetForType-m.usage[block.Type]) {
			return fmt.Errorf("ark/context: insufficient budget for block %q", block.ID)
		}
	}

	m.active[block.ID] = true
	m.usage[block.Type] += needed
	block.LastUsed = time.Now()
	block.UseCount++
	m.stats.TotalLoads++
	return nil
}

func EstimateTokens(s string) int {
	if len(s) == 0 {
		return 0
	}
	return (len(s) + 3) / 4
}

func compressToolSchema(name, description string) string {
	desc := description
	if idx := strings.Index(desc, ". "); idx > 0 && idx < 100 {
		desc = desc[:idx+1]
	} else if len(desc) > 100 {
		desc = desc[:100] + "..."
	}
	return fmt.Sprintf("Tool: %s — %s", name, desc)
}
func extractTags(parts ...string) []string {
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true, "being": true,
		"have": true, "has": true, "had": true, "do": true, "does": true,
		"did": true, "will": true, "would": true, "could": true, "should": true,
		"may": true, "might": true, "can": true, "this": true, "that": true,
		"these": true, "those": true, "it": true, "its": true, "of": true,
		"in": true, "on": true, "at": true, "to": true, "for": true,
		"with": true, "by": true, "from": true, "and": true, "or": true,
		"not": true, "no": true, "but": true, "if": true, "then": true,
	}

	tags := make(map[string]bool)
	for _, part := range parts {
		words := strings.Fields(strings.ToLower(part))
		for _, w := range words {
			w = strings.Trim(w, ".,;:!?()[]{}\"'")
			if len(w) > 2 && !stopWords[w] {
				tags[w] = true
			}
		}
	}

	result := make([]string, 0, len(tags))
	for tag := range tags {
		result = append(result, tag)
	}
	return result
}
