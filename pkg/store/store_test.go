package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreWriteAndRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test-store.json")
	s, err := NewJSONFileStore(path)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer s.Close()

	err = s.Save(ToolStatsRecord{
		ToolID: "github_list_repos", TotalCalls: 5, Successes: 4, Failures: 1,
		AvgLatencyMs: 100, LastUsed: time.Now(),
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	s.Flush()

	records, err := s.LoadAll()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].ToolID != "github_list_repos" {
		t.Errorf("expected tool_id=github_list_repos, got %s", records[0].ToolID)
	}
	if records[0].TotalCalls != 5 {
		t.Errorf("expected 5 calls, got %d", records[0].TotalCalls)
	}
}

func TestStorePatternSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test-pattern.json")
	s, err := NewJSONFileStore(path)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer s.Close()

	s.SavePattern(QueryPatternRecord{
		Pattern:         "list repos",
		SuccessfulTools: map[string]int{"github_list_repos": 1},
		TotalQueries:    1,
		LastUsed:        time.Now(),
	})
	time.Sleep(50 * time.Millisecond)

	s.SavePattern(QueryPatternRecord{
		Pattern:         "list repos",
		SuccessfulTools: map[string]int{"github_list_repos": 2},
		TotalQueries:    2,
		LastUsed:        time.Now(),
	})
	time.Sleep(50 * time.Millisecond)
	s.Flush()

	patterns, err := s.LoadPatterns()
	if err != nil {
		t.Fatalf("load patterns: %v", err)
	}
	if len(patterns) != 1 {
		t.Fatalf("expected 1 pattern, got %d", len(patterns))
	}

	count := patterns[0].SuccessfulTools["github_list_repos"]
	if count != 2 {
		t.Errorf("expected snapshot count=2, got %d (cumulative merge bug if >2)", count)
	}
}

func TestStoreDecay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test-decay.json")
	s, err := NewJSONFileStore(path)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer s.Close()

	s.Save(ToolStatsRecord{
		ToolID: "old_tool", TotalCalls: 10, Successes: 10,
		LastUsed: time.Now().Add(-60 * 24 * time.Hour),
	})
	s.Save(ToolStatsRecord{
		ToolID: "fresh_tool", TotalCalls: 5, Successes: 5,
		LastUsed: time.Now(),
	})
	time.Sleep(50 * time.Millisecond)
	s.Flush()

	removed := s.Decay()
	if removed != 1 {
		t.Errorf("expected 1 decayed, got %d", removed)
	}

	records, _ := s.LoadAll()
	if len(records) != 1 {
		t.Errorf("expected 1 remaining record, got %d", len(records))
	}
	if records[0].ToolID != "fresh_tool" {
		t.Errorf("wrong tool survived: %s", records[0].ToolID)
	}
}

func TestStorePersistsToDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test-persist.json")

	s1, err := NewJSONFileStore(path)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	s1.Save(ToolStatsRecord{
		ToolID: "test_tool", TotalCalls: 3, Successes: 3, LastUsed: time.Now(),
	})
	time.Sleep(50 * time.Millisecond)
	s1.Flush()
	s1.Close()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("store file should exist on disk")
	}

	s2, err := NewJSONFileStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer s2.Close()

	records, _ := s2.LoadAll()
	if len(records) != 1 {
		t.Fatalf("expected 1 record after reopen, got %d", len(records))
	}
	if records[0].ToolID != "test_tool" || records[0].TotalCalls != 3 {
		t.Errorf("data corrupted: %+v", records[0])
	}
}

func TestStoreConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test-concurrent.json")
	s, err := NewJSONFileStore(path)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer s.Close()

	done := make(chan bool, 50)
	for i := 0; i < 50; i++ {
		go func(n int) {
			s.Save(ToolStatsRecord{
				ToolID: "concurrent_tool", TotalCalls: n, Successes: n, LastUsed: time.Now(),
			})
			done <- true
		}(i)
	}

	for i := 0; i < 50; i++ {
		<-done
	}
	s.Flush()

	records, _ := s.LoadAll()
	if len(records) != 1 {
		t.Errorf("expected 1 record (last write wins), got %d", len(records))
	}
	t.Logf("Concurrent writes completed, final calls=%d", records[0].TotalCalls)
}
