package authz

import "sync"

// MemStore is the reference in-memory Store. It has the SAME observable semantics as the durable
// FileStore, but its state lives only for the process's lifetime — after termination, an old
// authorization becomes ErrNotFound (fail closed). Use it for the default in-process mode and as
// the semantic oracle in tests.
type MemStore struct {
	mu    sync.Mutex
	auths map[string]*Record
	retry map[string]int
	audit []map[string]any // in-memory audit trail (test-observable)
}

func NewMemStore() *MemStore {
	return &MemStore{auths: map[string]*Record{}, retry: map[string]int{}}
}

// AppendAudit records an audit event in memory (satisfies AuditSink for parity with FileStore).
func (m *MemStore) AppendAudit(rec map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audit = append(m.audit, rec)
	return nil
}

// Audit returns a copy of the recorded audit events (test helper).
func (m *MemStore) Audit() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]any, len(m.audit))
	copy(out, m.audit)
	return out
}

func (m *MemStore) Create(r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.auths[r.ID]; ok {
		if !sameIdentity(*existing, r) {
			return ErrIdentity
		}
		return nil // idempotent: same logical authorization
	}
	r.SchemaVersion = SchemaVersion
	r.State = Issued
	cp := r
	m.auths[r.ID] = &cp
	return nil
}

func (m *MemStore) Get(id string) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.auths[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	return *r, nil
}

func (m *MemStore) Consume(id string, atUnix int64) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.auths[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	if r.State != Issued { // CONSUMED or COMPLETED -> single-winner already decided
		return *r, ErrAlreadyConsumed
	}
	r.State = Consumed
	r.ConsumedAt = atUnix
	return *r, nil
}

func (m *MemStore) Complete(id string, atUnix int64) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.auths[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	if r.State == Completed {
		return *r, ErrConflict
	}
	if r.State != Consumed {
		return *r, ErrNotConsumed
	}
	r.State = Completed
	r.CompletedAt = atUnix
	return *r, nil
}

func (m *MemStore) RetryCount(key string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.retry[key], nil
}

func (m *MemStore) IncrRetry(key string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retry[key]++
	return m.retry[key], nil
}

func (m *MemStore) Close() error { return nil }
