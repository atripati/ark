package authz

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// randInt64 returns a non-negative random int64 for unique marker names.
func randInt64() int64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int64(binary.LittleEndian.Uint64(b[:]) >> 1)
}

// FileStore is a durable, dependency-free authorization store built on the filesystem's atomic
// O_CREATE|O_EXCL primitive (the same primitive lockfiles use). It needs no external database,
// no cgo, and works with CGO_ENABLED=0 static builds — which the ARK bridge wheel requires.
//
// Layout under dir:
//
//	SCHEMA              schema version (loud fail on mismatch)
//	<id>.json           immutable identity record (written once, fsync'd)
//	<id>.consumed       EXISTS iff CONSUMED — created atomically via O_EXCL (single winner)
//	<id>.completed      EXISTS iff COMPLETED — created atomically via O_EXCL
//	retry/<hash>/<uniq> one marker file per retry attempt; count == number of files
//	audit.jsonl         append-only durable audit sink (Phase 9)
//
// Marker EXISTENCE is the authoritative lifecycle state; the .json holds immutable identity. So a
// crash between writing the marker and rewriting the .json cannot lose or regress state.
type FileStore struct {
	dir string
	// mu serializes THIS process's own audit appends + retry-dir counting; the CROSS-PROCESS
	// exactly-one-consumer guarantee comes from the OS O_EXCL create, not this lock.
	mu sync.Mutex
}

// unsupportedPlatformFileStoreErr builds the explicit, architectural fail-closed error used when
// the durable FileStore is requested on a platform that cannot honor its POSIX durability
// contract. Kept as a plain function of goos so the message is unit-testable on any host, while
// the per-platform DECISION lives in the build-tagged fileStorePlatformError.
func unsupportedPlatformFileStoreErr(goos string) error {
	return fmt.Errorf("%w: durable FileStore requires a supported local POSIX filesystem "+
		"(Linux/macOS); it is not supported on %s because a parent-directory fsync — required for "+
		"the crash/power-loss durability guarantee — is unavailable there. Run ARK supervision "+
		"with in-memory authorization (leave ARK_AUTHZ_DIR unset) on this platform.",
		ErrUnsupportedPlatform, goos)
}

// OpenFileStore opens (creating if needed) a durable store rooted at dir. An existing store with
// an incompatible SchemaVersion fails loudly with ErrSchema — never silently reinterpreted.
//
// On a platform whose filesystem cannot honor the durability contract (see fileStorePlatformError,
// e.g. Windows), it fails closed with ErrUnsupportedPlatform BEFORE touching the filesystem — it
// never silently degrades to a weaker/in-memory guarantee.
func OpenFileStore(dir string) (*FileStore, error) {
	if err := fileStorePlatformError(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("%w: mkdir %s: %v", ErrStore, dir, err)
	}
	schemaPath := filepath.Join(dir, "SCHEMA")
	// Atomic init: exactly one racing process wins the O_EXCL create; the rest read+validate.
	werr := writeFileExclSync(schemaPath, []byte(strconv.Itoa(SchemaVersion)))
	if werr != nil && !errors.Is(werr, os.ErrExist) {
		return nil, fmt.Errorf("%w: init schema: %v", ErrStore, werr)
	}
	if errors.Is(werr, os.ErrExist) {
		b, rerr := os.ReadFile(schemaPath)
		if rerr != nil {
			return nil, fmt.Errorf("%w: read schema: %v", ErrStore, rerr)
		}
		got, perr := strconv.Atoi(strings.TrimSpace(string(b)))
		if perr != nil || got != SchemaVersion {
			// includes an empty/half-written SCHEMA from a crashed initializer -> loud fail.
			return nil, fmt.Errorf("%w: store schema version %q, this build supports %d", ErrSchema, strings.TrimSpace(string(b)), SchemaVersion)
		}
	}
	return &FileStore{dir: dir}, nil
}

func (f *FileStore) jsonPath(id string) string      { return filepath.Join(f.dir, safeName(id)+".json") }
func (f *FileStore) consumedPath(id string) string  { return filepath.Join(f.dir, safeName(id)+".consumed") }
func (f *FileStore) completedPath(id string) string { return filepath.Join(f.dir, safeName(id)+".completed") }

func (f *FileStore) Create(r Record) error {
	r.SchemaVersion = SchemaVersion
	r.State = Issued
	p := f.jsonPath(r.ID)
	if existing, err := f.readRecord(r.ID); err == nil {
		if !sameIdentity(existing, r) {
			return ErrIdentity
		}
		return nil // idempotent
	} else if !errors.Is(err, ErrNotFound) {
		return err // ErrStore
	}
	b, _ := json.Marshal(r)
	// O_EXCL: two processes racing to Create the same id -> at most one writes; the loser re-reads
	// and validates identity.
	if err := writeFileExclSync(p, b); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, rerr := f.readRecord(r.ID)
			if rerr != nil {
				return rerr
			}
			if !sameIdentity(existing, r) {
				return ErrIdentity
			}
			return nil
		}
		return fmt.Errorf("%w: create %s: %v", ErrStore, r.ID, err)
	}
	return nil
}

func (f *FileStore) readRecord(id string) (Record, error) {
	if !ValidID(id) { // reject malformed ids BEFORE building a path (no traversal/aliasing)
		return Record{}, fmt.Errorf("%w: %q", ErrBadID, id)
	}
	b, err := os.ReadFile(f.jsonPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("%w: read %s: %v", ErrStore, id, err)
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return Record{}, fmt.Errorf("%w: corrupt record %s: %v", ErrStore, id, err)
	}
	if r.SchemaVersion != SchemaVersion {
		return Record{}, fmt.Errorf("%w: record %s schema %d != %d", ErrSchema, id, r.SchemaVersion, SchemaVersion)
	}
	return r, nil
}

func (f *FileStore) Get(id string) (Record, error) {
	if !ValidID(id) {
		return Record{}, fmt.Errorf("%w: %q", ErrBadID, id)
	}
	r, err := f.readRecord(id)
	if errors.Is(err, ErrNotFound) {
		// Orphan markers with no base record are CORRUPTION, not a clean "unknown" — fail closed
		// and require reconciliation rather than silently treating it as re-issuable.
		if fileExists(f.consumedPath(id)) || fileExists(f.completedPath(id)) {
			return Record{}, fmt.Errorf("%w: markers for %s exist without a base record", ErrCorrupt, id)
		}
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	// Marker files are authoritative for lifecycle state; an inconsistent marker set is corruption.
	cts, consumed := f.markerTime(f.consumedPath(id))
	compts, completed := f.markerTime(f.completedPath(id))
	if completed && !consumed {
		return Record{}, fmt.Errorf("%w: %s is COMPLETED without a CONSUMED marker", ErrCorrupt, id)
	}
	switch {
	case completed:
		r.State, r.ConsumedAt, r.CompletedAt = Completed, cts, compts
	case consumed:
		r.State, r.ConsumedAt = Consumed, cts
	default:
		r.State = Issued
	}
	return r, nil
}

func (f *FileStore) Consume(id string, atUnix int64) (Record, error) {
	r, err := f.readRecord(id) // known authorization? (ErrNotFound / ErrStore both fail closed)
	if err != nil {
		return Record{}, err
	}
	// ATOMIC single-winner: exactly one O_EXCL create of the .consumed marker can succeed, across
	// threads AND processes.
	if err := writeFileExclSync(f.consumedPath(id), []byte(strconv.FormatInt(atUnix, 10))); err != nil {
		if errors.Is(err, os.ErrExist) {
			return r, ErrAlreadyConsumed
		}
		return Record{}, fmt.Errorf("%w: consume %s: %v", ErrStore, id, err)
	}
	r.State, r.ConsumedAt = Consumed, atUnix
	return r, nil
}

func (f *FileStore) Complete(id string, atUnix int64) (Record, error) {
	r, err := f.readRecord(id)
	if err != nil {
		return Record{}, err
	}
	if _, ok := f.markerTime(f.consumedPath(id)); !ok {
		return r, ErrNotConsumed
	}
	if err := writeFileExclSync(f.completedPath(id), []byte(strconv.FormatInt(atUnix, 10))); err != nil {
		if errors.Is(err, os.ErrExist) {
			return r, ErrConflict // already completed
		}
		return Record{}, fmt.Errorf("%w: complete %s: %v", ErrStore, id, err)
	}
	r.State, r.CompletedAt = Completed, atUnix
	return r, nil
}

func (f *FileStore) retryDir(key string) string {
	h := sha256.Sum256([]byte(key))
	return filepath.Join(f.dir, "retry", hex.EncodeToString(h[:])[:32])
}

func (f *FileStore) RetryCount(key string) (int, error) {
	entries, err := os.ReadDir(f.retryDir(key))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("%w: retry count: %v", ErrStore, err)
	}
	return len(entries), nil
}

// IncrRetry records one retry attempt as a distinct O_EXCL file with a unique name, so concurrent
// increments never collide or lose updates (no lock, no lost writes). The count is the file count.
func (f *FileStore) IncrRetry(key string) (int, error) {
	d := f.retryDir(key)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return 0, fmt.Errorf("%w: retry mkdir: %v", ErrStore, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// unique name + atomic durable create: each retry attempt is its own O_EXCL file (no lock, no
	// lost increments across processes), fsync'd with its directory so the count survives restart.
	for i := 0; ; i++ {
		name := filepath.Join(d, strconv.FormatInt(randInt64(), 16))
		err := writeFileExclSync(name, nil)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return 0, fmt.Errorf("%w: retry incr: %v", ErrStore, err)
		}
		if i > 1000 {
			return 0, fmt.Errorf("%w: retry incr: could not allocate marker", ErrStore)
		}
	}
	return f.RetryCount(key)
}

// AppendAudit durably appends one audit record as a JSON line (Phase 9). Best-effort ordering;
// each line is an independent, self-describing event.
func (f *FileStore) AppendAudit(rec map[string]any) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	fh, err := os.OpenFile(filepath.Join(f.dir, "audit.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("%w: audit open: %v", ErrStore, err)
	}
	defer fh.Close()
	if _, err := fh.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("%w: audit write: %v", ErrStore, err)
	}
	return fh.Sync()
}

func (f *FileStore) Close() error { return nil }

func (f *FileStore) markerTime(path string) (int64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	ts, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return ts, true
}

// ---- filesystem helpers ----

// fsyncDir fsyncs a directory so a newly-created/renamed entry within it is durable across power
// loss (the file's own fsync persists its DATA; the parent-dir fsync persists the directory
// ENTRY that names it). Required for the durability guarantee — see the durability note in the
// package doc. The actual power-loss guarantee is only as strong as the filesystem+hardware
// honoring fsync (e.g. Linux ext4/xfs honor it; macOS may need F_FULLFSYNC for hardware flush).
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if serr := d.Sync(); serr != nil {
		d.Close()
		return serr
	}
	return d.Close()
}

// writeFileExclSync atomically creates path (fails if it exists), fsyncs the file, then fsyncs
// the parent directory so the create is durable across power loss.
func writeFileExclSync(path string, b []byte) error {
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, werr := fh.Write(b); werr != nil {
		fh.Close()
		os.Remove(path)
		return werr
	}
	if serr := fh.Sync(); serr != nil {
		fh.Close()
		return serr
	}
	if cerr := fh.Close(); cerr != nil {
		return cerr
	}
	return fsyncDir(filepath.Dir(path))
}

// writeFileSync writes (truncating) path, fsyncs the file, then fsyncs the parent directory.
func writeFileSync(path string, b []byte) error {
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, werr := fh.Write(b); werr != nil {
		fh.Close()
		return werr
	}
	if serr := fh.Sync(); serr != nil {
		fh.Close()
		return serr
	}
	if cerr := fh.Close(); cerr != nil {
		return cerr
	}
	return fsyncDir(filepath.Dir(path))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// safeName keeps ids filesystem-safe (ARK ids are already [a-z0-9-]; this is defense in depth).
func safeName(id string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, id)
}
