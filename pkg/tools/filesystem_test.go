package tools

import (
	"os"
	"testing"

	"github.com/atripati/ark/pkg/runtime"
)

func TestFileReadSuccess(t *testing.T) {
	fs := &fileSystemTools{}

	origDir, _ := os.Getwd()
	os.Chdir(t.TempDir())
	defer os.Chdir(origDir)

	// Create file in current dir
	os.WriteFile("test.txt", []byte("hello world\nsecond line\n"), 0644)

	result, err := fs.readFile(map[string]interface{}{"path": "test.txt"})
	if err != nil {
		t.Fatalf("read should succeed: %v", err)
	}
	if result == "" {
		t.Fatal("result should not be empty")
	}
	t.Logf("Read result: %s", result[:min(len(result), 100)])
}

func TestFileReadNotFound(t *testing.T) {
	fs := &fileSystemTools{}
	_, err := fs.readFile(map[string]interface{}{"path": "nonexistent_file_xyz.txt"})
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	t.Logf("Correctly rejected: %v", err)
}

func TestFileReadDirectory(t *testing.T) {
	fs := &fileSystemTools{}

	origDir, _ := os.Getwd()
	os.Chdir(t.TempDir())
	defer os.Chdir(origDir)

	os.Mkdir("testdir", 0755)
	_, err := fs.readFile(map[string]interface{}{"path": "testdir"})
	if err == nil {
		t.Fatal("expected error when reading a directory")
	}
	t.Logf("Correctly rejected directory read: %v", err)
}

func TestFileWriteBlocked(t *testing.T) {
	router := NewRouterWithExecutor(&mockExecutor{})
	router.AllowWrite = false // default — writes blocked
	RegisterFileSystem(router)

	call := &runtime.ToolCall{
		Name:   "file_write",
		Params: map[string]interface{}{"path": "test.txt", "content": "hello"},
	}
	err := router.Handle(call)
	if err == nil {
		t.Fatal("write should be blocked without --allow-write")
	}
	t.Logf("Correctly blocked write: %v", err)
}

func TestFileWriteAllowed(t *testing.T) {
	fs := &fileSystemTools{}

	origDir, _ := os.Getwd()
	os.Chdir(t.TempDir())
	defer os.Chdir(origDir)

	result, err := fs.writeFile(map[string]interface{}{
		"path":    "output.txt",
		"content": "hello from ARK\n",
	})
	if err != nil {
		t.Fatalf("write should succeed: %v", err)
	}
	t.Logf("Write result: %s", result)

	// Verify file was created
	data, err := os.ReadFile("output.txt")
	if err != nil {
		t.Fatal("file should exist after write")
	}
	if string(data) != "hello from ARK\n" {
		t.Errorf("file content mismatch: got %q", string(data))
	}
}

func TestFileWriteCreatesDirectories(t *testing.T) {
	fs := &fileSystemTools{}

	origDir, _ := os.Getwd()
	os.Chdir(t.TempDir())
	defer os.Chdir(origDir)

	result, err := fs.writeFile(map[string]interface{}{
		"path":    "sub/dir/file.txt",
		"content": "nested",
	})
	if err != nil {
		t.Fatalf("write should create parent dirs: %v", err)
	}
	t.Logf("Write result: %s", result)

	data, _ := os.ReadFile("sub/dir/file.txt")
	if string(data) != "nested" {
		t.Error("nested file content mismatch")
	}
}

func TestFileListSuccess(t *testing.T) {
	fs := &fileSystemTools{}

	origDir, _ := os.Getwd()
	dir := t.TempDir()
	os.Chdir(dir)
	defer os.Chdir(origDir)

	// Create test files
	os.WriteFile("file1.txt", []byte("a"), 0644)
	os.WriteFile("file2.go", []byte("b"), 0644)
	os.Mkdir("subdir", 0755)

	result, err := fs.listDir(map[string]interface{}{"path": "."})
	if err != nil {
		t.Fatalf("list should succeed: %v", err)
	}
	if result == "" {
		t.Fatal("result should not be empty")
	}
	t.Logf("List result: %s", result)
}

func TestFileListNotFound(t *testing.T) {
	fs := &fileSystemTools{}
	_, err := fs.listDir(map[string]interface{}{"path": "nonexistent_dir_xyz"})
	if err == nil {
		t.Fatal("expected error for nonexistent directory")
	}
	t.Logf("Correctly rejected: %v", err)
}

func TestPathTraversalBlocked(t *testing.T) {
	tests := []string{
		"../../../etc/passwd",
		"foo/../../bar",
		"../secret",
	}

	for _, path := range tests {
		err := validatePath(path)
		if err == nil {
			t.Errorf("expected error for path traversal: %s", path)
		}
	}
}

func TestAbsolutePathBlocked(t *testing.T) {
	tests := []string{
		"/etc/passwd",
		"/home/user/file.txt",
		"/usr/bin/go",
	}

	for _, path := range tests {
		err := validatePath(path)
		if err == nil {
			t.Errorf("expected error for absolute path: %s", path)
		}
	}
}

func TestHiddenPathBlocked(t *testing.T) {
	tests := []string{
		".git/config",
		".env",
		"foo/.secret",
	}

	for _, path := range tests {
		err := validatePath(path)
		if err == nil {
			t.Errorf("expected error for hidden path: %s", path)
		}
	}
}

func TestValidPathsAccepted(t *testing.T) {
	tests := []string{
		"file.txt",
		"src/main.go",
		"pkg/tools/http.go",
		"README.md",
		".",
	}

	for _, path := range tests {
		err := validatePath(path)
		if err != nil {
			t.Errorf("valid path %q should be accepted: %v", path, err)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
