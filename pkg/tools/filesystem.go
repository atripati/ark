package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxFileReadBytes = 100_000
	maxListEntries   = 50
)

func RegisterFileSystem(router *Router) {
	fs := &fileSystemTools{}

	router.RegisterTool(Tool{
		Name:           "file_read",
		Description:    "read file: read the contents of a file from the local file system",
		Version:        "v1",
		Handler:        fs.readFile,
		RequiredParams: []string{"path"},
		Metadata: map[string]interface{}{
			"type":          "read",
			"auth_required": false,
		},
	})

	router.RegisterTool(Tool{
		Name:           "file_write",
		Description:    "write file: create or overwrite a file on the local file system",
		Version:        "v1",
		Handler:        fs.writeFile,
		RequiredParams: []string{"path", "content"},
		Metadata: map[string]interface{}{
			"type":          "write",
			"auth_required": false,
		},
	})

	router.RegisterTool(Tool{
		Name:           "file_list",
		Description:    "list directory: list files and directories in a given path",
		Version:        "v1",
		Handler:        fs.listDir,
		RequiredParams: []string{"path"},
		Metadata: map[string]interface{}{
			"type":          "read",
			"auth_required": false,
		},
	})
}
func FileSystemToolDefs() []ToolDef {
	return []ToolDef{
		{"file_read", "file_read",
			"read file: read the contents of a file from the local file system",
			`{"name":"file_read","description":"Read file contents","params":["path"]}`},
		{"file_write", "file_write",
			"write file: create or overwrite a file on the local file system",
			`{"name":"file_write","description":"Write content to a file","params":["path","content"]}`},
		{"file_list", "file_list",
			"list directory: list files and directories in a given path",
			`{"name":"file_list","description":"List files in a directory","params":["path"]}`},
	}
}

type fileSystemTools struct{}

func validatePath(path string) error {
	if path == "" {
		return fmt.Errorf("file_system: empty path")
	}

	// Reject path traversal
	cleaned := filepath.Clean(path)
	if strings.Contains(cleaned, "..") {
		return fmt.Errorf("file_system: path traversal blocked (contains '..')")
	}

	if filepath.IsAbs(path) {
		return fmt.Errorf("file_system: absolute paths blocked (use relative paths)")
	}

	dangerous := []string{"/etc", "/usr", "/bin", "/sbin", "/var", "/root", "/home"}
	for _, d := range dangerous {
		if strings.HasPrefix(cleaned, d) {
			return fmt.Errorf("file_system: access to %s blocked", d)
		}
	}

	parts := strings.Split(cleaned, string(filepath.Separator))
	for _, part := range parts {
		if len(part) > 1 && strings.HasPrefix(part, ".") && part != "." {
			return fmt.Errorf("file_system: hidden path %q blocked", part)
		}
	}

	return nil
}

func (fs *fileSystemTools) readFile(params map[string]interface{}) (string, error) {
	path, _ := params["path"].(string)

	if err := validatePath(path); err != nil {
		return "", fmt.Errorf("file_read: %w", err)
	}

	// Check if file exists and get info
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file_read: file not found: %s", path)
		}
		return "", fmt.Errorf("file_read: %w", err)
	}

	if info.IsDir() {
		return "", fmt.Errorf("file_read: %s is a directory, use file_list instead", path)
	}

	if info.Size() > maxFileReadBytes {
		return "", fmt.Errorf("file_read: file too large (%d bytes, max %d). Read a specific section or use a smaller file",
			info.Size(), maxFileReadBytes)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("file_read: %w", err)
	}

	type fileResult struct {
		Path    string `json:"path"`
		Size    int64  `json:"size_bytes"`
		Lines   int    `json:"lines"`
		Content string `json:"content"`
	}

	lineCount := strings.Count(string(data), "\n") + 1

	result := fileResult{
		Path:    path,
		Size:    info.Size(),
		Lines:   lineCount,
		Content: string(data),
	}

	out, err := json.Marshal(result)
	if err != nil {
		return string(data), nil // fallback to raw content
	}
	return string(out), nil
}

func (fs *fileSystemTools) writeFile(params map[string]interface{}) (string, error) {
	path, _ := params["path"].(string)
	content, _ := params["content"].(string)

	if err := validatePath(path); err != nil {
		return "", fmt.Errorf("file_write: %w", err)
	}

	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("file_write: failed to create directory %s: %w", dir, err)
		}
	}

	existed := false
	if _, err := os.Stat(path); err == nil {
		existed = true
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("file_write: %w", err)
	}

	type writeResult struct {
		Path      string `json:"path"`
		Action    string `json:"action"`
		SizeBytes int    `json:"size_bytes"`
		Lines     int    `json:"lines"`
	}

	action := "created"
	if existed {
		action = "overwritten"
	}

	result := writeResult{
		Path:      path,
		Action:    action,
		SizeBytes: len(content),
		Lines:     strings.Count(content, "\n") + 1,
	}

	out, err := json.Marshal(result)
	if err != nil {
		return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
	}
	return string(out), nil
}

func (fs *fileSystemTools) listDir(params map[string]interface{}) (string, error) {
	path, _ := params["path"].(string)

	if err := validatePath(path); err != nil {
		return "", fmt.Errorf("file_list: %w", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file_list: directory not found: %s", path)
		}
		return "", fmt.Errorf("file_list: %w", err)
	}

	if !info.IsDir() {
		return "", fmt.Errorf("file_list: %s is a file, use file_read instead", path)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return "", fmt.Errorf("file_list: %w", err)
	}

	type dirEntry struct {
		Name  string `json:"name"`
		Type  string `json:"type"` // "file" or "dir"
		Size  int64  `json:"size_bytes,omitempty"`
		Lines int    `json:"lines,omitempty"`
	}

	type dirResult struct {
		Path       string     `json:"path"`
		TotalItems int        `json:"total_items"`
		Entries    []dirEntry `json:"entries"`
		Truncated  bool       `json:"truncated,omitempty"`
	}

	result := dirResult{
		Path:       path,
		TotalItems: len(entries),
	}

	count := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		if count >= maxListEntries {
			result.Truncated = true
			break
		}

		de := dirEntry{
			Name: entry.Name(),
		}

		if entry.IsDir() {
			de.Type = "dir"
		} else {
			de.Type = "file"
			if fi, err := entry.Info(); err == nil {
				de.Size = fi.Size()
			}
		}

		result.Entries = append(result.Entries, de)
		count++
	}

	out, err := json.Marshal(result)
	if err != nil {
		return fmt.Sprintf("%d items in %s", len(entries), path), nil
	}
	return string(out), nil
}
