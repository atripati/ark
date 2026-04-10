package tools

import (
	"os"
	"testing"
	"time"
)

func TestCustomToolRegistration(t *testing.T) {
	router := NewRouterWithExecutor(NewHTTPExecutor([]string{}))

	err := RegisterCustomHTTP(router, CustomToolConfig{
		Name:        "test_api",
		Description: "test API endpoint",
		Method:      "GET",
		URL:         "https://api.example.com/data?q={query}",
		Params:      []string{"query"},
	})
	if err != nil {
		t.Fatalf("registration should succeed: %v", err)
	}

	if router.ToolCount() != 1 {
		t.Errorf("expected 1 tool, got %d", router.ToolCount())
	}

	tool := router.GetTool("test_api")
	if tool == nil {
		t.Fatal("tool should be registered")
	}
	if tool.Description != "test API endpoint" {
		t.Errorf("description mismatch: %s", tool.Description)
	}
	if len(tool.RequiredParams) != 1 || tool.RequiredParams[0] != "query" {
		t.Errorf("params mismatch: %v", tool.RequiredParams)
	}
}

func TestCustomToolDomainAllowlisted(t *testing.T) {
	exec := NewHTTPExecutor([]string{})
	router := NewRouterWithExecutor(exec)

	RegisterCustomHTTP(router, CustomToolConfig{
		Name:   "external_api",
		URL:    "https://api.custom-service.com/v1/data",
		Method: "GET",
	})

	// Check domain was added to allowlist
	found := false
	for _, d := range exec.AllowedDomains {
		if d == "api.custom-service.com" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("domain should be auto-allowlisted, got: %v", exec.AllowedDomains)
	}
}

func TestCustomToolWriteMetadata(t *testing.T) {
	router := NewRouterWithExecutor(NewHTTPExecutor([]string{}))

	// POST method should auto-set write type
	RegisterCustomHTTP(router, CustomToolConfig{
		Name:   "post_api",
		URL:    "https://api.example.com/create",
		Method: "POST",
		Params: []string{"data"},
	})

	tool := router.GetTool("post_api")
	if tool == nil {
		t.Fatal("tool should be registered")
	}

	toolType, _ := tool.Metadata["type"].(string)
	if toolType != "write" {
		t.Errorf("POST tool should be type=write, got %s", toolType)
	}
}

func TestCustomToolEnvVarInterpolation(t *testing.T) {
	os.Setenv("TEST_ARK_TOKEN", "secret123")
	defer os.Unsetenv("TEST_ARK_TOKEN")

	headers := map[string]string{
		"Authorization": "Bearer ${TEST_ARK_TOKEN}",
		"X-Custom":      "static-value",
	}

	resolved := resolveEnvHeaders(headers)

	if resolved["Authorization"] != "Bearer secret123" {
		t.Errorf("env var not resolved: got %q", resolved["Authorization"])
	}
	if resolved["X-Custom"] != "static-value" {
		t.Errorf("static header should be unchanged: got %q", resolved["X-Custom"])
	}
}

func TestCustomToolEnvVarMissing(t *testing.T) {
	os.Unsetenv("NONEXISTENT_VAR_XYZ")

	headers := map[string]string{
		"Authorization": "Bearer ${NONEXISTENT_VAR_XYZ}",
	}

	resolved := resolveEnvHeaders(headers)

	if resolved["Authorization"] != "Bearer " {
		t.Errorf("missing env var should resolve to empty: got %q", resolved["Authorization"])
	}
}

func TestCustomToolRejectsEmptyName(t *testing.T) {
	router := NewRouterWithExecutor(NewHTTPExecutor([]string{}))

	err := RegisterCustomHTTP(router, CustomToolConfig{
		URL: "https://api.example.com/data",
	})
	if err == nil {
		t.Fatal("should reject empty name")
	}
}

func TestCustomToolRejectsEmptyURL(t *testing.T) {
	router := NewRouterWithExecutor(NewHTTPExecutor([]string{}))

	err := RegisterCustomHTTP(router, CustomToolConfig{
		Name: "test",
	})
	if err == nil {
		t.Fatal("should reject empty URL")
	}
}

func TestCustomToolDefaultsToGET(t *testing.T) {
	router := NewRouterWithExecutor(NewHTTPExecutor([]string{}))

	RegisterCustomHTTP(router, CustomToolConfig{
		Name: "no_method",
		URL:  "https://api.example.com/data",
	})

	tool := router.GetTool("no_method")
	method, _ := tool.Metadata["method"].(string)
	if method != "GET" {
		t.Errorf("should default to GET, got %s", method)
	}
}

func TestCustomToolDef(t *testing.T) {
	def := CustomToolDef("my_tool", "does something cool", []string{"param1", "param2"})

	if def.ID != "my_tool" {
		t.Errorf("ID mismatch: %s", def.ID)
	}
	if def.Name != "my_tool" {
		t.Errorf("Name mismatch: %s", def.Name)
	}
	if def.Desc != "does something cool" {
		t.Errorf("Desc mismatch: %s", def.Desc)
	}
	if def.Schema == "" {
		t.Error("Schema should not be empty")
	}
}

func TestCustomToolTimeout(t *testing.T) {
	router := NewRouterWithExecutor(NewHTTPExecutor([]string{}))

	err := RegisterCustomHTTP(router, CustomToolConfig{
		Name:    "slow_api",
		URL:     "https://api.example.com/slow",
		Timeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("registration should succeed: %v", err)
	}

	// Tool should be registered
	tool := router.GetTool("slow_api")
	if tool == nil {
		t.Fatal("tool should exist")
	}
}
