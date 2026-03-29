package store

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

type ToolStatsRecord struct {
	ToolID           string    `json:"tool_id"`
	TotalCalls       int       `json:"total_calls"`
	Successes        int       `json:"successes"`
	Failures         int       `json:"failures"`
	ConsecutiveFails int       `json:"consecutive_fails"`
	AvgLatencyMs     int64     `json:"avg_latency_ms"`
	LastUsed         time.Time `json:"last_used"`
	LastErrorType    int       `json:"last_error_type"`
}

type QueryPatternRecord struct {
	Pattern         string         `json:"pattern"`
	SuccessfulTools map[string]int `json:"successful_tools"`
	TotalQueries    int            `json:"total_queries"`
	LastUsed        time.Time      `json:"last_used"`
}

type Store interface {
	LoadAll() ([]ToolStatsRecord, error)
	Save(record ToolStatsRecord) error
	LoadPatterns() ([]QueryPatternRecord, error)
	SavePattern(record QueryPatternRecord) error
	Flush() error
	Close() error
}

const (
	defaultFlushEvery    = 10
	defaultFlushInterval = 5 * time.Second
	writeChannelSize     = 64
	decayThreshold       = 30 * 24 * time.Hour
)

type writeOp struct {
	toolStats *ToolStatsRecord
	pattern   *QueryPatternRecord
}

type JSONFileStore struct {
	mu     sync.RWMutex
	path   string
	data   storeData
	logger *log.Logger

	writeCh    chan writeOp
	dirty      int
	flushEvery int

	ticker   *time.Ticker
	done     chan struct{}
	workerWg sync.WaitGroup
	closed   bool
}

type storeData struct {
	ToolStats     map[string]ToolStatsRecord    `json:"tool_stats"`
	QueryPatterns map[string]QueryPatternRecord `json:"query_patterns"`
}

func NewJSONFileStore(path string) (*JSONFileStore, error) {
	s := &JSONFileStore{
		path:       path,
		flushEvery: defaultFlushEvery,
		logger:     log.New(os.Stderr, "[ark/store] ", log.LstdFlags),
		writeCh:    make(chan writeOp, writeChannelSize),
		done:       make(chan struct{}),
		data: storeData{
			ToolStats:     make(map[string]ToolStatsRecord),
			QueryPatterns: make(map[string]QueryPatternRecord),
		},
	}

	if _, err := os.Stat(path); err == nil {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("ark/store: read %s: %w", path, err)
		}
		if len(data) > 0 {
			if err := json.Unmarshal(data, &s.data); err != nil {
				// Try legacy format (flat array)
				var legacy []ToolStatsRecord
				if legacyErr := json.Unmarshal(data, &legacy); legacyErr == nil {
					for _, r := range legacy {
						s.data.ToolStats[r.ToolID] = r
					}
				} else {
					return nil, fmt.Errorf("ark/store: parse %s: %w", path, err)
				}
			}
			if s.data.ToolStats == nil {
				s.data.ToolStats = make(map[string]ToolStatsRecord)
			}
			if s.data.QueryPatterns == nil {
				s.data.QueryPatterns = make(map[string]QueryPatternRecord)
			}
		}
	}

	s.ticker = time.NewTicker(defaultFlushInterval)
	s.workerWg.Add(1)
	go s.worker()

	return s, nil
}

func (s *JSONFileStore) worker() {
	defer s.workerWg.Done()
	for {
		select {
		case op := <-s.writeCh:
			s.applyWrite(op)

		case <-s.ticker.C:
			s.mu.Lock()
			if s.dirty > 0 {
				if err := s.flushLocked(); err != nil {
					s.logger.Printf("periodic flush: %v", err)
				}
			}
			s.mu.Unlock()

		case <-s.done:
			for {
				select {
				case op := <-s.writeCh:
					s.applyWrite(op)
				default:
					s.mu.Lock()
					if s.dirty > 0 {
						if err := s.flushLocked(); err != nil {
							s.logger.Printf("shutdown flush: %v", err)
						}
					}
					s.mu.Unlock()
					return
				}
			}
		}
	}
}

func (s *JSONFileStore) applyWrite(op writeOp) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if op.toolStats != nil {
		s.data.ToolStats[op.toolStats.ToolID] = *op.toolStats
		s.dirty++
	}

	if op.pattern != nil {
		// SNAPSHOT semantics: replace entire record.
		// The engine owns the authoritative counts; the store is a mirror.
		// Cumulative merging caused count inflation over time.
		record := *op.pattern
		toolsCopy := make(map[string]int, len(record.SuccessfulTools))
		for k, v := range record.SuccessfulTools {
			toolsCopy[k] = v
		}
		record.SuccessfulTools = toolsCopy
		s.data.QueryPatterns[record.Pattern] = record
		s.dirty++
	}

	if s.dirty >= s.flushEvery {
		if err := s.flushLocked(); err != nil {
			s.logger.Printf("buffered flush: %v", err)
		}
	}
}

func (s *JSONFileStore) Save(record ToolStatsRecord) error {
	select {
	case s.writeCh <- writeOp{toolStats: &record}:
		return nil
	case <-time.After(100 * time.Millisecond):
		s.logger.Printf("write timeout, dropping save for %s", record.ToolID)
		return fmt.Errorf("ark/store: write timeout")
	}
}

func (s *JSONFileStore) SavePattern(record QueryPatternRecord) error {
	select {
	case s.writeCh <- writeOp{pattern: &record}:
		return nil
	case <-time.After(100 * time.Millisecond):
		s.logger.Printf("write timeout, dropping pattern %s", record.Pattern)
		return fmt.Errorf("ark/store: write timeout")
	}
}
func (s *JSONFileStore) LoadAll() ([]ToolStatsRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	records := make([]ToolStatsRecord, 0, len(s.data.ToolStats))
	for _, r := range s.data.ToolStats {
		records = append(records, r)
	}
	return records, nil
}
func (s *JSONFileStore) LoadPatterns() ([]QueryPatternRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	records := make([]QueryPatternRecord, 0, len(s.data.QueryPatterns))
	for _, r := range s.data.QueryPatterns {
		records = append(records, r)
	}
	return records, nil
}

func (s *JSONFileStore) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dirty > 0 {
		return s.flushLocked()
	}
	return nil
}
func (s *JSONFileStore) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	s.ticker.Stop()
	close(s.done)
	s.workerWg.Wait()
	return nil
}
func (s *JSONFileStore) RecordCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data.ToolStats)
}
func (s *JSONFileStore) PatternCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data.QueryPatterns)
}

func (s *JSONFileStore) Decay() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	removed := 0

	for id, rec := range s.data.ToolStats {
		if now.Sub(rec.LastUsed) > decayThreshold {
			delete(s.data.ToolStats, id)
			removed++
		}
	}

	for pattern, rec := range s.data.QueryPatterns {
		if now.Sub(rec.LastUsed) > decayThreshold {
			delete(s.data.QueryPatterns, pattern)
			removed++
		}
	}

	if removed > 0 {
		s.dirty++
		s.logger.Printf("decay: removed %d stale entries (>%v old)", removed, decayThreshold)
	}

	return removed
}

func (s *JSONFileStore) flushLocked() error {
	data, err := json.Marshal(s.data)
	if err != nil {
		s.logger.Printf("marshal error: %v", err)
		return fmt.Errorf("ark/store: marshal: %w", err)
	}
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		s.logger.Printf("write error: %v", err)
		return fmt.Errorf("ark/store: write %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		s.logger.Printf("rename error: %v", err)
		return fmt.Errorf("ark/store: rename: %w", err)
	}

	s.dirty = 0
	return nil
}
