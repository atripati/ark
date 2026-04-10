package tools

import (
	"testing"

	"github.com/atripati/ark/pkg/runtime"
)

func TestParamValidationRejectsMissing(t *testing.T) {
	router := NewRouterWithExecutor(&mockExecutor{})
	router.RegisterTool(Tool{
		Name:           "test_tool",
		Version:        "v1",
		RequiredParams: []string{"owner", "repo"},
		Handler:        func(p map[string]interface{}) (string, error) { return "ok", nil },
	})

	// Missing both params
	call := &runtime.ToolCall{Name: "test_tool", Params: map[string]interface{}{}}
	err := router.Handle(call)
	if err == nil {
		t.Fatal("expected error for missing required params")
	}
	if call.Result != "" {
		t.Error("result should be empty when validation fails")
	}
	t.Logf("Correctly rejected: %v", err)
}

func TestParamValidationAcceptsValid(t *testing.T) {
	router := NewRouterWithExecutor(&mockExecutor{})
	router.RegisterTool(Tool{
		Name:           "test_tool",
		Version:        "v1",
		RequiredParams: []string{"owner", "repo"},
		Handler:        func(p map[string]interface{}) (string, error) { return "success", nil },
	})

	call := &runtime.ToolCall{Name: "test_tool", Params: map[string]interface{}{"owner": "test", "repo": "ark"}}
	err := router.Handle(call)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if call.Result != "success" {
		t.Errorf("expected 'success', got %q", call.Result)
	}
}

func TestParamValidationRejectsEmptyString(t *testing.T) {
	router := NewRouterWithExecutor(&mockExecutor{})
	router.RegisterTool(Tool{
		Name:           "test_tool",
		Version:        "v1",
		RequiredParams: []string{"owner"},
		Handler:        func(p map[string]interface{}) (string, error) { return "ok", nil },
	})

	call := &runtime.ToolCall{Name: "test_tool", Params: map[string]interface{}{"owner": ""}}
	err := router.Handle(call)
	if err == nil {
		t.Fatal("expected error for empty required param")
	}
}

func TestWriteBlocking(t *testing.T) {
	router := NewRouterWithExecutor(&mockExecutor{})
	router.AllowWrite = false
	router.RegisterTool(Tool{
		Name:     "write_tool",
		Version:  "v1",
		Handler:  func(p map[string]interface{}) (string, error) { return "written", nil },
		Metadata: map[string]interface{}{"type": "write"},
	})

	call := &runtime.ToolCall{Name: "write_tool", Params: map[string]interface{}{}}
	err := router.Handle(call)
	if err == nil {
		t.Fatal("write should be blocked")
	}
	t.Logf("Correctly blocked: %v", err)
}

func TestWriteAllowed(t *testing.T) {
	router := NewRouterWithExecutor(&mockExecutor{})
	router.AllowWrite = true
	router.RegisterTool(Tool{
		Name:     "write_tool",
		Version:  "v1",
		Handler:  func(p map[string]interface{}) (string, error) { return "written", nil },
		Metadata: map[string]interface{}{"type": "write"},
	})

	call := &runtime.ToolCall{Name: "write_tool", Params: map[string]interface{}{}}
	err := router.Handle(call)
	if err != nil {
		t.Fatalf("write should be allowed: %v", err)
	}
	if call.Result != "written" {
		t.Errorf("expected 'written', got %q", call.Result)
	}
}

func TestDryRunMode(t *testing.T) {
	router := NewRouterWithExecutor(&mockExecutor{})
	router.DryRun = true
	router.RegisterTool(Tool{
		Name:    "test_tool",
		Version: "v1",
		Handler: func(p map[string]interface{}) (string, error) { return "real result", nil },
	})

	call := &runtime.ToolCall{Name: "test_tool", Params: map[string]interface{}{"key": "val"}}
	err := router.Handle(call)
	if err != nil {
		t.Fatalf("dry run should not error: %v", err)
	}
	if call.Result == "real result" {
		t.Error("dry run should NOT execute the real handler")
	}
	if call.Result == "" {
		t.Error("dry run should produce a message")
	}
	t.Logf("Dry run result: %s", call.Result)
}

func TestUnknownToolReturnsError(t *testing.T) {
	router := NewRouterWithExecutor(&mockExecutor{})
	call := &runtime.ToolCall{Name: "nonexistent_tool", Params: map[string]interface{}{}}
	err := router.Handle(call)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

func TestNoRequiredParamsSkipsValidation(t *testing.T) {
	router := NewRouterWithExecutor(&mockExecutor{})
	router.RegisterTool(Tool{
		Name:           "flexible_tool",
		Version:        "v1",
		RequiredParams: []string{}, // no required params
		Handler:        func(p map[string]interface{}) (string, error) { return "ok", nil },
	})

	call := &runtime.ToolCall{Name: "flexible_tool", Params: map[string]interface{}{}}
	err := router.Handle(call)
	if err != nil {
		t.Fatalf("tool with no required params should accept empty params: %v", err)
	}
}

type mockExecutor struct{}

func (m *mockExecutor) Execute(config HTTPToolConfig, params map[string]interface{}) (string, error) {
	return "mock", nil
}

func TestOutputValidationRejectsEmpty(t *testing.T) {
	router := NewRouterWithExecutor(&mockExecutor{})
	router.RegisterTool(Tool{
		Name:    "empty_tool",
		Version: "v1",
		Handler: func(p map[string]interface{}) (string, error) { return "", nil },
	})

	call := &runtime.ToolCall{Name: "empty_tool", Params: map[string]interface{}{}}
	err := router.Handle(call)
	if err == nil {
		t.Fatal("expected error for empty output")
	}
	if call.Result != "" {
		t.Error("result should be empty when output validation fails")
	}
	t.Logf("Correctly rejected empty output: %v", err)
}

func TestOutputValidationRejectsBinaryGarbage(t *testing.T) {
	router := NewRouterWithExecutor(&mockExecutor{})
	// Simulate gzipped/binary response
	garbage := "\x1f\x8b\x08\x00\x00\x00\x00\x00\x00\xff\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0b\x0c\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f"
	router.RegisterTool(Tool{
		Name:    "binary_tool",
		Version: "v1",
		Handler: func(p map[string]interface{}) (string, error) { return garbage, nil },
	})

	call := &runtime.ToolCall{Name: "binary_tool", Params: map[string]interface{}{}}
	err := router.Handle(call)
	if err == nil {
		t.Fatal("expected error for binary garbage output")
	}
	t.Logf("Correctly rejected binary garbage: %v", err)
}

func TestOutputValidationAcceptsJSON(t *testing.T) {
	router := NewRouterWithExecutor(&mockExecutor{})
	router.RegisterTool(Tool{
		Name:    "json_tool",
		Version: "v1",
		Handler: func(p map[string]interface{}) (string, error) {
			return `[{"title":"Test","url":"https://example.com"}]`, nil
		},
	})

	call := &runtime.ToolCall{Name: "json_tool", Params: map[string]interface{}{}}
	err := router.Handle(call)
	if err != nil {
		t.Fatalf("valid JSON output should pass validation: %v", err)
	}
	if call.Result == "" {
		t.Error("result should not be empty for valid output")
	}
}

func TestOutputValidationAcceptsPlainText(t *testing.T) {
	router := NewRouterWithExecutor(&mockExecutor{})
	router.RegisterTool(Tool{
		Name:    "text_tool",
		Version: "v1",
		Handler: func(p map[string]interface{}) (string, error) {
			return "This is a normal text response with results.", nil
		},
	})

	call := &runtime.ToolCall{Name: "text_tool", Params: map[string]interface{}{}}
	err := router.Handle(call)
	if err != nil {
		t.Fatalf("valid text output should pass validation: %v", err)
	}
}
