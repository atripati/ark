package authz

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// storeFactories runs the same semantic suite against BOTH implementations, so the reference
// MemStore and the durable FileStore are proven to behave identically.
func storeFactories(t *testing.T) map[string]func() Store {
	return map[string]func() Store{
		"mem": func() Store { return NewMemStore() },
		"file": func() Store {
			s, err := OpenFileStore(t.TempDir())
			if err != nil {
				t.Fatalf("open file store: %v", err)
			}
			return s
		},
	}
}

func rec(id string) Record {
	return Record{ID: id, Transaction: "txn-1", Constraint: "rank", ActionFP: "fpA", EvidenceFP: "evA", IssuedAt: 100}
}

func TestStore_Lifecycle(t *testing.T) {
	for name, mk := range storeFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := mk()
			id := ID("", "txn-1", "", "rank", "fpA", "evA")
			r := rec(id)
			if err := s.Create(r); err != nil {
				t.Fatalf("create: %v", err)
			}
			got, err := s.Get(id)
			if err != nil || got.State != Issued {
				t.Fatalf("get issued: %v state=%s", err, got.State)
			}
			if _, err := s.Consume(id, 200); err != nil {
				t.Fatalf("consume: %v", err)
			}
			if got, _ := s.Get(id); got.State != Consumed || got.ConsumedAt != 200 {
				t.Fatalf("state after consume: %s at=%d", got.State, got.ConsumedAt)
			}
			if _, err := s.Complete(id, 300); err != nil {
				t.Fatalf("complete: %v", err)
			}
			if got, _ := s.Get(id); got.State != Completed || got.CompletedAt != 300 {
				t.Fatalf("state after complete: %s at=%d", got.State, got.CompletedAt)
			}
		})
	}
}

func TestStore_UnknownFailsClosed(t *testing.T) {
	unknown := ID("", "never-created", "", "rank", "x", "y") // a VALID id that was never stored
	for name, mk := range storeFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := mk()
			if _, err := s.Get(unknown); !errors.Is(err, ErrNotFound) {
				t.Fatalf("unknown Get must be ErrNotFound; got %v", err)
			}
			if _, err := s.Consume(unknown, 1); !errors.Is(err, ErrNotFound) {
				t.Fatalf("unknown Consume must be ErrNotFound (never success); got %v", err)
			}
		})
	}
}

// A malformed authorization id is rejected BEFORE any filesystem path is built (no traversal,
// no aliasing) — never treated as unknown-and-issuable.
func TestFileStore_MalformedIDRejected(t *testing.T) {
	s, _ := OpenFileStore(t.TempDir())
	for _, bad := range []string{"../../etc/passwd", "nope", "ark-authz-XYZ", "ark-authz-", "", "ark-authz-" + "0", "a/b/c"} {
		if _, err := s.Get(bad); !errors.Is(err, ErrBadID) {
			t.Fatalf("malformed id %q must be ErrBadID; got %v", bad, err)
		}
		if _, err := s.Consume(bad, 1); !errors.Is(err, ErrBadID) {
			t.Fatalf("Consume of malformed id %q must be ErrBadID; got %v", bad, err)
		}
	}
	if !ValidID("ark-authz-0123456789abcdef0123456789abcdef") {
		t.Fatal("a canonical 32-hex id must be valid")
	}
}

// Corrupt on-disk state fails closed (never re-issued / never ALLOW).
func TestFileStore_CorruptStateFailsClosed(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenFileStore(dir)
	id := ID("", "txn-1", "", "rank", "fpA", "evA")
	s.Create(rec(id))
	// (a) truncated/garbage json
	if err := writeFileSync(s.jsonPath(id), []byte("{ this is not json")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(id); err == nil {
		t.Fatalf("a corrupt record must not read as valid; got nil error")
	}
	if _, err := s.Consume(id, 1); err == nil {
		t.Fatalf("consume of a corrupt record must fail closed; got nil error")
	}
	// (b) completed marker without a consumed marker -> corruption
	id2 := ID("", "txn-2", "", "rank", "fpA", "evA")
	s.Create(rec2(id2, "txn-2"))
	if err := writeFileExclSync(s.completedPath(id2), []byte("123")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(id2); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("COMPLETED-without-CONSUMED must be ErrCorrupt; got %v", err)
	}
	// (c) orphan marker with no base record
	id3 := ID("", "txn-3", "", "rank", "fpA", "evA")
	if err := writeFileExclSync(s.consumedPath(id3), []byte("123")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(id3); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("orphan marker with no base record must be ErrCorrupt; got %v", err)
	}
}

func rec2(id, txn string) Record {
	return Record{ID: id, Transaction: txn, Constraint: "rank", ActionFP: "fpA", EvidenceFP: "evA", IssuedAt: 100}
}

func TestStore_DoubleConsumeRefused(t *testing.T) {
	for name, mk := range storeFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := mk()
			id := ID("", "txn-1", "", "rank", "fpA", "evA")
			s.Create(rec(id))
			if _, err := s.Consume(id, 1); err != nil {
				t.Fatalf("first consume: %v", err)
			}
			if _, err := s.Consume(id, 2); !errors.Is(err, ErrAlreadyConsumed) {
				t.Fatalf("second consume must be ErrAlreadyConsumed; got %v", err)
			}
		})
	}
}

func TestStore_NoStateRegression(t *testing.T) {
	for name, mk := range storeFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := mk()
			id := ID("", "txn-1", "", "rank", "fpA", "evA")
			s.Create(rec(id))
			if _, err := s.Complete(id, 1); !errors.Is(err, ErrNotConsumed) {
				t.Fatalf("complete before consume must be ErrNotConsumed; got %v", err)
			}
			s.Consume(id, 2)
			s.Complete(id, 3)
			if _, err := s.Complete(id, 4); !errors.Is(err, ErrConflict) {
				t.Fatalf("double complete must be ErrConflict; got %v", err)
			}
		})
	}
}

func TestStore_CreateIdempotentAndIdentityCollision(t *testing.T) {
	for name, mk := range storeFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := mk()
			id := ID("", "txn-1", "", "rank", "fpA", "evA")
			if err := s.Create(rec(id)); err != nil {
				t.Fatal(err)
			}
			if err := s.Create(rec(id)); err != nil { // idempotent
				t.Fatalf("re-create same identity must be idempotent; got %v", err)
			}
			bad := rec(id)
			bad.ActionFP = "DIFFERENT"
			if err := s.Create(bad); !errors.Is(err, ErrIdentity) {
				t.Fatalf("same id + different identity must be ErrIdentity; got %v", err)
			}
		})
	}
}

// EXACTLY ONE consumer under high concurrency (DUR-02/10) — the core durability guarantee.
func TestStore_ConcurrentConsumeExactlyOne(t *testing.T) {
	for name, mk := range storeFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := mk()
			id := ID("", "txn-1", "", "rank", "fpA", "evA")
			s.Create(rec(id))
			var wg sync.WaitGroup
			var mu sync.Mutex
			won := 0
			for i := 0; i < 64; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := s.Consume(id, 1); err == nil {
						mu.Lock()
						won++
						mu.Unlock()
					}
				}()
			}
			wg.Wait()
			if won != 1 {
				t.Fatalf("exactly one consumer must win; got %d", won)
			}
		})
	}
}

func TestStore_DurableRetryCounter(t *testing.T) {
	for name, mk := range storeFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := mk()
			key := "txn-1\x1frank"
			for i := 1; i <= 3; i++ {
				n, err := s.IncrRetry(key)
				if err != nil || n != i {
					t.Fatalf("incr %d: n=%d err=%v", i, n, err)
				}
			}
			if n, _ := s.RetryCount(key); n != 3 {
				t.Fatalf("retry count = %d, want 3", n)
			}
			// a different key is independent
			if n, _ := s.RetryCount("other\x1frank"); n != 0 {
				t.Fatalf("independent key must be 0, got %d", n)
			}
		})
	}
}

// ---- FileStore-specific: durability across "restart" (a new store on the same dir) ----

func TestFileStore_SurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	id := ID("", "txn-1", "", "rank", "fpA", "evA")
	s1, _ := OpenFileStore(dir)
	s1.Create(rec(id))
	s1.Consume(id, 200)       // CONSUMED, persisted
	s1.IncrRetry("k")         // retry persisted
	s1.Close()

	// "restart": brand-new store on the same directory
	s2, err := OpenFileStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, err := s2.Get(id)
	if err != nil || got.State != Consumed {
		t.Fatalf("CONSUMED must survive restart; got state=%s err=%v", got.State, err)
	}
	if _, err := s2.Consume(id, 999); !errors.Is(err, ErrAlreadyConsumed) {
		t.Fatalf("a consumed authorization must stay consumed after restart; got %v", err)
	}
	if n, _ := s2.RetryCount("k"); n != 1 {
		t.Fatalf("retry state must survive restart; got %d", n)
	}
}

// Two INDEPENDENT FileStore handles on the same dir do not share memory, so this models two
// separate processes racing to consume one authorization. Exactly one must win (DUR-02).
func TestFileStore_TwoHandlesExactlyOneConsumer(t *testing.T) {
	dir := t.TempDir()
	id := ID("", "txn-1", "", "rank", "fpA", "evA")
	seed, _ := OpenFileStore(dir)
	if err := seed.Create(rec(id)); err != nil {
		t.Fatal(err)
	}
	const procs = 16
	handles := make([]*FileStore, procs)
	for i := range handles {
		h, err := OpenFileStore(dir) // a distinct handle == a distinct "process"
		if err != nil {
			t.Fatal(err)
		}
		handles[i] = h
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	start := make(chan struct{})
	for i := 0; i < procs; i++ {
		wg.Add(1)
		go func(h *FileStore) {
			defer wg.Done()
			<-start
			if _, err := h.Consume(id, 1); err == nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}(handles[i])
	}
	close(start)
	wg.Wait()
	if won != 1 {
		t.Fatalf("exactly one independent handle must consume; got %d", won)
	}
}

// The audit writer opens per-append, so an external rename-based rotation loses no records and a
// crash mid-stream damages at most the final line. Reconstruction concatenates the files in order.
func TestFileStore_AuditSurvivesRotation(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenFileStore(dir)
	if err := s.AppendAudit(map[string]any{"event": "issued", "id": "a1"}); err != nil {
		t.Fatal(err)
	}
	// external rotation: move the current audit aside
	cur := filepath.Join(dir, "audit.jsonl")
	rotated := filepath.Join(dir, "audit.jsonl.1")
	if err := os.Rename(cur, rotated); err != nil {
		t.Fatal(err)
	}
	// the next append re-creates audit.jsonl (writer reopens each time)
	if err := s.AppendAudit(map[string]any{"event": "consumed", "id": "a1"}); err != nil {
		t.Fatal(err)
	}
	oldB, _ := os.ReadFile(rotated)
	newB, _ := os.ReadFile(cur)
	if !bytes.Contains(oldB, []byte(`"issued"`)) {
		t.Fatal("rotated file must retain the pre-rotation record")
	}
	if !bytes.Contains(newB, []byte(`"consumed"`)) {
		t.Fatal("fresh audit file must hold the post-rotation record")
	}
	// reconstruction: concatenation contains both, each line parses
	all := append(append([]byte{}, oldB...), newB...)
	lines := 0
	for _, ln := range bytes.Split(bytes.TrimSpace(all), []byte("\n")) {
		var m map[string]any
		if err := json.Unmarshal(ln, &m); err != nil {
			t.Fatalf("audit line must parse: %q: %v", ln, err)
		}
		lines++
	}
	if lines != 2 {
		t.Fatalf("expected 2 reconstructable audit records, got %d", lines)
	}
}

func TestFileStore_IncompatibleSchemaFailsLoud(t *testing.T) {
	dir := t.TempDir()
	if err := writeFileSync(filepath.Join(dir, "SCHEMA"), []byte("9999")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileStore(dir); !errors.Is(err, ErrSchema) {
		t.Fatalf("an incompatible schema must fail loudly with ErrSchema (never open); got %v", err)
	}
}

func TestFileStore_UnwritableFailsClosed(t *testing.T) {
	// point the store at a path under a regular FILE, so every write fails -> ErrStore, never success.
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	if err := writeFileSync(blocker, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileStore(filepath.Join(blocker, "store")); !errors.Is(err, ErrStore) {
		t.Fatalf("an unusable store path must fail with ErrStore; got %v", err)
	}
}
