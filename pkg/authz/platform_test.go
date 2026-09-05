package authz

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The durable FileStore is POSIX-only by contract. These tests assert the platform gate is
// explicit and fail-closed, and that its message states the real architectural reason (never a
// vague OS error). The message builder is platform-independent so it is verified on any host;
// the per-platform DECISION (fileStorePlatformError) is verified for the host it runs on, and the
// Windows branch is compile-checked via `GOOS=windows go vet ./pkg/authz`.

func TestUnsupportedPlatformMessageIsExplicit(t *testing.T) {
	err := unsupportedPlatformFileStoreErr("windows")
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("message must wrap ErrUnsupportedPlatform; got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"POSIX", "windows", "ARK_AUTHZ_DIR"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("unsupported-platform message must mention %q (the real reason), got: %s", want, msg)
		}
	}
	if strings.Contains(strings.ToLower(msg), "access is denied") {
		t.Fatalf("must not surface the vague OS error as the explanation: %s", msg)
	}
}

func TestFileStorePlatformGateMatchesHost(t *testing.T) {
	err := fileStorePlatformError()
	if runtime.GOOS == "windows" {
		if !errors.Is(err, ErrUnsupportedPlatform) {
			t.Fatalf("on windows the durable FileStore must be unsupported; got %v", err)
		}
	} else if err != nil {
		t.Fatalf("on %s the durable FileStore must be supported (nil gate); got %v", runtime.GOOS, err)
	}
}

// On an unsupported platform, OpenFileStore must fail closed with ErrUnsupportedPlatform and must
// NOT create the directory (the refusal precedes any filesystem I/O). On POSIX it must open.
func TestOpenFileStoreHonorsPlatformGate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	s, err := OpenFileStore(dir)
	if runtime.GOOS == "windows" {
		if !errors.Is(err, ErrUnsupportedPlatform) {
			t.Fatalf("OpenFileStore on windows must return ErrUnsupportedPlatform; got %v", err)
		}
		if s != nil {
			t.Fatalf("OpenFileStore on windows must return a nil store")
		}
	} else {
		if err != nil {
			t.Fatalf("OpenFileStore on %s must succeed; got %v", runtime.GOOS, err)
		}
		if s == nil {
			t.Fatalf("OpenFileStore on %s must return a store", runtime.GOOS)
		}
		_ = s.Close()
	}
}
