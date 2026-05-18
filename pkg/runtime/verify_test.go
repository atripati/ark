package runtime

import (
	"strings"
	"testing"
)

func TestExtractCodeBlocks(t *testing.T) {
	input := "Here:\n```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```\nDone."
	blocks := extractCodeBlocks(input)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Language != "go" {
		t.Errorf("expected go, got %s", blocks[0].Language)
	}
}

func TestExtractCodeBlocksMultiple(t *testing.T) {
	input := "```python\ndef hello(): pass\n```\n\n```go\nfunc main() {}\n```"
	blocks := extractCodeBlocks(input)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
}

func TestExtractCodeBlocksAutoDetect(t *testing.T) {
	input := "```\npackage main\nfunc main() {}\n```"
	blocks := extractCodeBlocks(input)
	if len(blocks) != 1 || blocks[0].Language != "go" {
		t.Error("expected auto-detect go")
	}
}

func TestDetectCodeLanguage(t *testing.T) {
	cases := map[string]string{
		"package main\nfunc main() {}":       "go",
		"def hello():\n    print('hi')":       "python",
		"import os\nprint(os.getcwd())":       "python",
		"const x = 42\nlet y = x + 1":         "javascript",
		"#include <stdio.h>\nint main() {}":   "c",
		"public class Main {}":                "java",
		"fn main() { println!(\"hi\"); }":     "rust",
	}
	for code, expected := range cases {
		if result := detectCodeLanguage(code); result != expected {
			t.Errorf("detectCodeLanguage(%q...) = %q, want %q", code[:15], result, expected)
		}
	}
}

func TestStructuralLintBalanced(t *testing.T) {
	block := CodeBlock{Language: "go", Code: "func main() {\n\tfmt.Println(\"hi\")\n}"}
	if issues := structuralLint(block); len(issues) != 0 {
		t.Errorf("expected no issues, got: %v", issues)
	}
}

func TestStructuralLintUnbalanced(t *testing.T) {
	block := CodeBlock{Language: "go", Code: "func main() {\n\tfmt.Println(\"hi\")\n"}
	issues := structuralLint(block)
	found := false
	for _, i := range issues {
		if strings.Contains(i, "unbalanced") {
			found = true
		}
	}
	if !found {
		t.Error("expected unbalanced braces")
	}
}

func TestStructuralLintTODO(t *testing.T) {
	block := CodeBlock{Language: "go", Code: "func main() {\n\t// TODO: implement\n}"}
	issues := structuralLint(block)
	found := false
	for _, i := range issues {
		if strings.Contains(i, "placeholder") {
			found = true
		}
	}
	if !found {
		t.Error("expected placeholder issue")
	}
}

func TestStructuralLintMixedIndent(t *testing.T) {
	block := CodeBlock{Language: "python", Code: "def f():\n\tprint('tab')\n    print('space')"}
	issues := structuralLint(block)
	found := false
	for _, i := range issues {
		if strings.Contains(i, "mixed") {
			found = true
		}
	}
	if !found {
		t.Error("expected mixed indent issue")
	}
}

func TestEnforceConstraintsOverCommented(t *testing.T) {
	code := "// This function does X\n// This function takes Y\n// This function returns Z\nfunc main() {\n\tfmt.Println(\"hi\")\n}"
	issues := enforceConstraints(CodeBlock{Language: "go", Code: code})
	found := false
	for _, i := range issues {
		if strings.Contains(i, "filler comments") {
			found = true
		}
	}
	if !found {
		t.Error("expected filler comment detection")
	}
}

func TestEnforceConstraintsClean(t *testing.T) {
	code := "func main() {\n\tfmt.Println(\"hi\")\n}"
	issues := enforceConstraints(CodeBlock{Language: "go", Code: code})
	if len(issues) != 0 {
		t.Errorf("expected no issues for clean code, got: %v", issues)
	}
}

func TestVerifyCodeResponseNoCode(t *testing.T) {
	result := verifyCodeResponse("Just text.")
	if result.Passed {
		t.Error("should not pass without code")
	}
	if result.Report == nil {
		t.Error("should have report")
	}
}

func TestVerifyCodeResponseWithCode(t *testing.T) {
	input := "```go\npackage main\n\nfunc main() {}\n```"
	result := verifyCodeResponse(input)
	if result.Score < 0.5 {
		t.Errorf("expected decent score, got %.2f", result.Score)
	}
	if result.Report == nil {
		t.Error("should have report")
	}
}

func TestVerifyRankingGood(t *testing.T) {
	good := "1. Express - 68,951 stars https://github.com/expressjs/express\n2. NestJS - 75,256 stars https://github.com/nestjs/nest"
	result := verifyRankingResponse(good)
	if !result.Passed {
		t.Errorf("expected pass, score: %.2f, issues: %v", result.Score, result.Issues)
	}
	if result.Report == nil {
		t.Error("should have report")
	}
}

func TestVerifyRankingBad(t *testing.T) {
	bad := "Express is popular and NestJS is also good."
	result := verifyRankingResponse(bad)
	if result.Score > 0.7 {
		t.Errorf("expected lower score, got %.2f", result.Score)
	}
}

func TestVerifyReasoningGood(t *testing.T) {
	good := "Go is better because it handles 50,000 requests/second versus 5,000 for Python. The benchmark data confirms this across multiple tests."
	result := verifyReasoningResponse(good)
	if !result.Passed {
		t.Errorf("expected pass, score: %.2f", result.Score)
	}
}

func TestVerifyReasoningHedgy(t *testing.T) {
	hedgy := "Well it might work, perhaps it could be good, but it's hard to say, possibly depends on factors, could be relevant."
	result := verifyReasoningResponse(hedgy)
	if result.Score > 0.7 {
		t.Errorf("expected lower score for hedgy, got %.2f", result.Score)
	}
}

func TestVerifySummaryGood(t *testing.T) {
	result := verifySummaryResponse("ARK reduces costs by 80%. Key: per-step routing and verification.")
	if !result.Passed {
		t.Errorf("expected pass, score: %.2f", result.Score)
	}
}

func TestVerifyGeneralEmpty(t *testing.T) {
	result := verifyGeneralResponse("")
	if result.Passed || result.Score != 0.0 {
		t.Error("empty should fail with 0 score")
	}
}

func TestBuildRetryPrompt(t *testing.T) {
	v := VerificationResult{
		Issues:     []string{"unbalanced braces"},
		Suggestion: "Fix the braces.",
		CodeResult: &CodeExecResult{Error: "main.go:5: syntax error"},
	}
	prompt := BuildRetryPrompt("code", v)
	if !strings.Contains(prompt, "unbalanced braces") {
		t.Error("should mention issues")
	}
	if !strings.Contains(prompt, "syntax error") {
		t.Error("should include compiler error")
	}
}

func TestVerifyReportExists(t *testing.T) {
	input := "```go\npackage main\nfunc main() {}\n```"
	result := verifyCodeResponse(input)
	if result.Report == nil {
		t.Fatal("report should exist")
	}
	if len(result.Report.Checks) == 0 {
		t.Error("report should have checks")
	}

	names := make(map[string]bool)
	for _, c := range result.Report.Checks {
		names[c.Name] = true
	}
	if !names["code_extraction"] {
		t.Error("missing code_extraction check")
	}
	if !names["structural_lint"] {
		t.Error("missing structural_lint check")
	}
	if !names["constraints"] {
		t.Error("missing constraints check")
	}
}

func TestExtractFuncSignatures(t *testing.T) {
	code := "package main\n\nfunc readCSV(filePath string) ([][]string, error) {\n\treturn nil, nil\n}\n\nfunc main() {}"
	funcs := extractFuncSignatures(code)
	if len(funcs) != 2 {
		t.Fatalf("expected 2 functions, got %d", len(funcs))
	}
	if funcs[0].Name != "readCSV" {
		t.Errorf("expected readCSV, got %s", funcs[0].Name)
	}
	if len(funcs[0].Params) != 1 {
		t.Fatalf("expected 1 param, got %d", len(funcs[0].Params))
	}
	if funcs[0].Params[0].Type != "string" {
		t.Errorf("expected string param, got %s", funcs[0].Params[0].Type)
	}
}

func TestIsFilePathParam(t *testing.T) {
	cases := map[string]bool{
		"filePath": true,
		"file":     true,
		"path":     true,
		"filename": true,
		"fp":       true,
		"src":      true,
		"name":     false,
		"count":    false,
		"x":        false,
	}
	for name, expected := range cases {
		if result := isFilePathParam(name); result != expected {
			t.Errorf("isFilePathParam(%q) = %v, want %v", name, result, expected)
		}
	}
}

func TestDefaultValue(t *testing.T) {
	cases := map[string]string{
		"string":          `""`,
		"int":             "0",
		"float64":         "0.0",
		"bool":            "false",
		"[]string":        "nil",
		"*http.Request":   "nil",
		"map[string]int":  "nil",
	}
	for typ, expected := range cases {
		if result := defaultValue(typ); result != expected {
			t.Errorf("defaultValue(%q) = %q, want %q", typ, result, expected)
		}
	}
}

func TestGenerateFuncTestFileParam(t *testing.T) {
	fn := FuncSignature{
		Name:    "readCSV",
		Params:  []FuncParam{{Name: "filePath", Type: "string"}},
		Returns: "([][]string, error)",
	}
	test := generateFuncTest(fn)
	if !strings.Contains(test, "CreateTemp") {
		t.Error("should create temp file for file path param")
	}
	if !strings.Contains(test, "readCSV(") {
		t.Error("should call readCSV")
	}
	if !strings.Contains(test, "Alice") {
		t.Error("should write test data")
	}
}

func TestGenerateFuncTestNonFile(t *testing.T) {
	fn := FuncSignature{
		Name:    "add",
		Params:  []FuncParam{{Name: "a", Type: "int"}, {Name: "b", Type: "int"}},
		Returns: "int",
	}
	test := generateFuncTest(fn)
	if !strings.Contains(test, "add(0, 0)") {
		t.Error("should call with default int values")
	}
}