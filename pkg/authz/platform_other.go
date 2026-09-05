//go:build !windows

package authz

// fileStorePlatformError reports whether the durable FileStore's POSIX durability primitives
// (O_CREATE|O_EXCL, file fsync, and parent-directory fsync) are available on this platform.
//
// On POSIX systems (Linux, macOS, and other Unix), they are — a directory handle can be opened
// and fsync'd — so the durable FileStore is supported and this returns nil. The real power-loss
// guarantee remains only as strong as the filesystem+hardware honoring fsync (see the durability
// note in file.go); network filesystems are excluded by the documented contract, not enforced here.
func fileStorePlatformError() error { return nil }
