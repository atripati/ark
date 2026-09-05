// Package authz is the durable authorization-lifecycle seam for ARK's supervision kernel.
//
// It stores exactly one small state machine per consequential authorization:
//
//	ISSUED -> CONSUMED -> COMPLETED
//
// and makes the ISSUED->CONSUMED transition ATOMIC and single-winner, so authorization
// integrity survives process crash, restart, and multiple ARK instances sharing one store.
//
// Design principles (see the durability invariants):
//   - The kernel stays deterministic; this package holds NO policy. It never decides ALLOW; it
//     only records identity and gates state transitions.
//   - Every failure is an explicit error. A store failure is NEVER silently turned into a
//     successful consume — the caller must fail closed.
//   - The reference MemStore and the durable FileStore share these exact semantics, so tests and
//     production behave identically.
package authz

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
)

// SchemaVersion is the on-disk format version. An incompatible persisted store fails loudly
// rather than being silently reinterpreted (never turned into ALLOW).
const SchemaVersion = 1

// State is the authorization lifecycle state.
type State string

const (
	Issued    State = "ISSUED"    // authorized, not yet claimed for execution
	Consumed  State = "CONSUMED"  // claimed for execution exactly once; external outcome may be unknown
	Completed State = "COMPLETED" // execution result recorded
)

// Explicit errors. NONE of these may be mapped to a successful/cleared consume by a caller.
var (
	ErrNotFound        = errors.New("authz: authorization not found")          // unknown -> never executes (DUR-04)
	ErrAlreadyConsumed = errors.New("authz: authorization already consumed")   // replay -> refuse (DUR-02/10)
	ErrNotConsumed     = errors.New("authz: authorization is not CONSUMED")    // complete out of order (DUR-03)
	ErrConflict        = errors.New("authz: authorization state conflict")     // illegal transition (DUR-03)
	ErrIdentity        = errors.New("authz: authorization identity mismatch")  // same id, different identity (DUR-06)
	ErrStore           = errors.New("authz: store unavailable")                // persistence failure -> fail closed (DUR-05/12)
	ErrSchema          = errors.New("authz: incompatible persisted schema")    // unknown version -> loud fail (DUR + Phase 15)
	ErrCorrupt         = errors.New("authz: corrupt authorization state")      // inconsistent/damaged on disk -> fail closed, reconcile
	ErrBadID           = errors.New("authz: malformed authorization id")       // not a canonical ARK id -> reject before any I/O
	// ErrUnsupportedPlatform is returned by OpenFileStore when the durable FileStore's POSIX
	// durability primitives (O_EXCL create + file fsync + parent-directory fsync) are not
	// available on the host platform (e.g. Windows, where a directory handle cannot be fsync'd).
	// Rather than silently degrade durability, the durable backend fails closed with this error;
	// callers must NOT fall back to in-memory. General ARK supervision (in-memory authorization)
	// is unaffected.
	ErrUnsupportedPlatform = errors.New("authz: durable FileStore unsupported on this platform")
)

// validAuthID matches ARK's canonical authorization id. Any id reaching the durable store is
// validated against this BEFORE it is used to build a filesystem path, so caller-supplied strings
// can never influence paths (no traversal, no aliasing, no overwrite).
var validAuthID = regexp.MustCompile(`^ark-authz-[0-9a-f]{32}$`)

// ValidID reports whether id is a canonical ARK authorization id.
func ValidID(id string) bool { return validAuthID.MatchString(id) }

// Record is the immutable identity of one authorization plus its lifecycle timestamps. The
// identity fields (everything except State and the *At timestamps) are written once at Create
// and never change, so validating a proposed execution against them is race-free — only the
// State transitions, and that transition is atomic.
type Record struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"` // stable, content-derived authorization id (== idempotency key)
	Namespace     string `json:"namespace,omitempty"` // tenant/application boundary (multi-tenant isolation)
	RunID         string `json:"run_id,omitempty"`
	DecisionID    string `json:"decision_id,omitempty"`
	Transaction   string `json:"transaction"`
	Scope         string `json:"scope,omitempty"`
	AgentID       string `json:"agent_id,omitempty"`
	Constraint    string `json:"constraint"`
	ActionFP      string `json:"action_fp"`   // fingerprint of the authorized action (DUR-08)
	EvidenceFP    string `json:"evidence_fp"` // fingerprint of the evidence state (DUR-09)
	// freshness window captured at issue time, re-evaluated at consume time against a fresh clock.
	ObservedAt int64 `json:"observed_at,omitempty"`
	ExpiresAt  int64 `json:"expires_at,omitempty"`
	MaxAgeSec  int64 `json:"max_age_sec,omitempty"`

	State       State `json:"state"`
	IssuedAt    int64 `json:"issued_at"`
	ConsumedAt  int64 `json:"consumed_at,omitempty"`
	CompletedAt int64 `json:"completed_at,omitempty"`
}

// Store is the minimal durable authorization seam. Implementations MUST make Consume atomic and
// single-winner across concurrent callers and processes.
type Store interface {
	// Create persists a new ISSUED authorization. If an authorization with the same ID already
	// exists AND its identity (transaction/action/evidence/constraint) matches, Create is
	// idempotent and returns nil (the same logical authorization). A same-ID/different-identity
	// collision returns ErrIdentity. A persistence failure returns ErrStore.
	Create(r Record) error

	// Get returns the current record (with its true State) or ErrNotFound. A persistence failure
	// returns ErrStore — callers MUST treat that as fail-closed, never as "unknown, proceed".
	Get(id string) (Record, error)

	// Consume atomically transitions ISSUED -> CONSUMED for EXACTLY ONE caller and returns the
	// updated record. Concurrent/duplicate callers get ErrAlreadyConsumed. Unknown id -> ErrNotFound.
	// Persistence failure -> ErrStore. Consume performs NO identity/freshness validation — the
	// caller validates against the immutable Get() record first; the atomic transition here is the
	// arbiter that guarantees a single consumer.
	Consume(id string, atUnix int64) (Record, error)

	// Complete atomically transitions CONSUMED -> COMPLETED (idempotent-safe: a second Complete on
	// an already-COMPLETED record returns ErrConflict). Not-yet-consumed -> ErrNotConsumed.
	Complete(id string, atUnix int64) (Record, error)

	// RetryCount / IncrRetry manage durable, monotonic retry counters keyed per (transaction,
	// constraint). IncrRetry returns the new count. These survive restart so a restart cannot
	// reset an exhausted budget into a fresh one (DUR + Phase 6).
	RetryCount(key string) (int, error)
	IncrRetry(key string) (int, error)

	Close() error
}

// AuditSink is an OPTIONAL durable audit sink a Store may also implement (Phase 9). It is kept
// separate from Store so the two concerns are not coupled, while a single backend may serve both.
type AuditSink interface {
	AppendAudit(rec map[string]any) error
}

// ID returns the stable, content-derived authorization id for one logical operation. It is a
// deterministic function of the FULL security-relevant namespace — namespace (tenant/app),
// transaction (lifecycle), scope (resource), constraint (policy), action fingerprint, and
// evidence fingerprint — so:
//   - the SAME logical operation always maps to the SAME id (survives restart; safe to reuse as
//     the external idempotency key when recovering from an ambiguous network failure — Phase 7);
//   - authorizations from a different tenant, transaction, scope, policy, action, or evidence
//     version get DIFFERENT ids and can NEVER alias each other (DUR-03/08/09).
//
// The inputs are encoded as a JSON string array with a scheme tag, so no field value can inject a
// delimiter that makes two different tuples collide.
func ID(namespace, transaction, scope, constraint, actionFP, evidenceFP string) string {
	b, _ := json.Marshal([]string{"ark-authz-id-v1", namespace, transaction, scope, constraint, actionFP, evidenceFP})
	h := sha256.Sum256(b)
	return "ark-authz-" + hex.EncodeToString(h[:])[:32]
}

// sameIdentity reports whether two records describe the same logical authorization. It compares
// the FULL identity (every field that feeds ID), so Create is idempotent for a genuine re-issue
// of the same operation but flags any same-id/different-identity collision (defense in depth
// against a hash collision — astronomically unlikely, but never assumed away).
func sameIdentity(a, b Record) bool {
	return a.Namespace == b.Namespace && a.Transaction == b.Transaction && a.Scope == b.Scope &&
		a.Constraint == b.Constraint && a.ActionFP == b.ActionFP && a.EvidenceFP == b.EvidenceFP
}
