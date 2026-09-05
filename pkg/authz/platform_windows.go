//go:build windows

package authz

import "runtime"

// fileStorePlatformError makes the durable FileStore explicitly UNSUPPORTED on Windows.
//
// The store's crash/power-loss durability rests on a parent-directory fsync (persisting the
// directory ENTRY that names a freshly-created marker file). On Windows a directory handle cannot
// be fsync'd — the call fails with "Access is denied" — so we cannot honor the same durability
// contract we honor on POSIX. Rather than silently degrade (e.g. skip the directory fsync, or fall
// back to in-memory state that is lost on exit), the durable backend fails closed with an explicit,
// architectural error. General ARK supervision — deterministic constraints, trusted evidence,
// action binding, freshness, transaction isolation, and IN-MEMORY authorization — is fully
// supported on Windows; only the durable FileStore backend is restricted.
func fileStorePlatformError() error { return unsupportedPlatformFileStoreErr(runtime.GOOS) }
