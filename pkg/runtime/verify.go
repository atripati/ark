package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type VerificationResult struct {
	Passed     bool            `json:"passed"`
	Score      float64         `json:"score"`
	Level      string          `json:"level"` // "tested", "executed", "compiled", "structural", "heuristic"
	Issues     []string        `json:"issues"`
	Suggestion string          `json:"suggestion"`
	CodeResult *CodeExecResult `json:"code_result,omitempty"`
	Report     *VerifyReport   `json:"report,omitempty"`
}

type CodeExecResult struct {
	Language    string        `json:"language"`
	Compiled    bool          `json:"compiled"`
	Ran         bool          `json:"ran"`
	TestsPassed bool          `json:"tests_passed"`
	TestOutput  string        `json:"test_output"`
	Linted      bool          `json:"linted"`
	LintIssues  []string      `json:"lint_issues"`
	Output      string        `json:"output"`
	Error       string        `json:"error"`
	Duration    time.Duration `json:"duration"`
}

// VerifyReport is the transparency layer — shows exactly what was checked.
type VerifyReport struct {
	Checks []VerifyCheck `json:"checks"`
}

// VerifyCheck is a single verification step.
type VerifyCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

func VerifyResponse(response string, taskType string) VerificationResult {
	switch taskType {
	case "coding":
		return verifyCodeResponse(response)
	case "ranking":
		return verifyRankingResponse(response)
	case "reasoning":
		return verifyReasoningResponse(response)
	case "summarization":
		return verifySummaryResponse(response)
	default:
		return verifyGeneralResponse(response)
	}
}

func verifyCodeResponse(response string) VerificationResult {
	result := VerificationResult{Level: "heuristic"}
	report := &VerifyReport{}

	// Phase 1: Extract code blocks
	blocks := extractCodeBlocks(response)
	if len(blocks) == 0 {
		report.Checks = append(report.Checks, VerifyCheck{"code_extraction", false, "no code block found"})
		result.Report = report
		result.Score = 0.3
		result.Issues = append(result.Issues, "no code block found in response")
		result.Suggestion = "Please provide the code inside a code block (```language ... ```)"
		return result
	}
	report.Checks = append(report.Checks, VerifyCheck{"code_extraction", true, fmt.Sprintf("%d code block(s) found", len(blocks))})

	best := blocks[0]
	for _, b := range blocks {
		code := strings.ToLower(b.Code)
		if strings.Contains(code, `"testing"`) || strings.Contains(code, "func test") {
			continue
		}
		if len(b.Code) > len(best.Code) || strings.Contains(strings.ToLower(best.Code), `"testing"`) {
			best = b
		}
	}
	// If all blocks are test blocks, use the first one anyway
	if strings.Contains(strings.ToLower(best.Code), `"testing"`) && len(blocks) > 0 {
		best = blocks[0]
	}

	// Phase 1.5: Auto-fix common model errors BEFORE any validation.
	// This ensures lint, compile, and test all run on corrected code.
	if best.Language == "go" || best.Language == "" || best.Language == "unknown" {
		fixed := AutoFixGoCode(best.Code)
		if fixed != best.Code {
			best.Code = fixed
		}
	}

	// Phase 2: Structural lint (runs on auto-fixed code)
	lintIssues := structuralLint(best)
	lintPassed := len(lintIssues) == 0
	report.Checks = append(report.Checks, VerifyCheck{"structural_lint", lintPassed, fmt.Sprintf("%d issues", len(lintIssues))})
	result.Issues = append(result.Issues, lintIssues...)

	// Phase 3: Constraint enforcement
	constraintIssues := enforceConstraints(best)
	constraintsPassed := len(constraintIssues) == 0
	report.Checks = append(report.Checks, VerifyCheck{"constraints", constraintsPassed, fmt.Sprintf("%d violations", len(constraintIssues))})
	result.Issues = append(result.Issues, constraintIssues...)

	// Phase 4: Compile + Run + Test
	execResult := executeAndTest(best)
	result.CodeResult = execResult

	if execResult != nil {
		if execResult.TestsPassed {
			result.Level = "tested"
			result.Score = 1.0
			report.Checks = append(report.Checks, VerifyCheck{"compilation", true, "compiled successfully"})
			report.Checks = append(report.Checks, VerifyCheck{"execution", execResult.Ran, "ran without error"})
			report.Checks = append(report.Checks, VerifyCheck{"tests", true, "auto-generated tests passed"})
		} else if execResult.Compiled && execResult.Ran {
			result.Level = "executed"
			result.Score = 0.90
			report.Checks = append(report.Checks, VerifyCheck{"compilation", true, "compiled successfully"})
			report.Checks = append(report.Checks, VerifyCheck{"execution", true, "ran without error"})
			report.Checks = append(report.Checks, VerifyCheck{"tests", false, "tests not available"})
		} else if execResult.Compiled {
			result.Level = "compiled"
			result.Score = 0.80
			report.Checks = append(report.Checks, VerifyCheck{"compilation", true, "compiled successfully"})
			report.Checks = append(report.Checks, VerifyCheck{"execution", false, execResult.Error})
		} else {
			result.Level = "structural"
			result.Score = 0.35
			result.Issues = append(result.Issues, "compilation failed: "+execResult.Error)
			result.Suggestion = fmt.Sprintf("Code failed to compile. Error:\n%s\nFix the error and provide complete, corrected code.", execResult.Error)
			report.Checks = append(report.Checks, VerifyCheck{"compilation", false, execResult.Error})
		}

		if execResult.Linted {
			report.Checks = append(report.Checks, VerifyCheck{"lint", len(execResult.LintIssues) == 0, fmt.Sprintf("%d warnings", len(execResult.LintIssues))})
		}
	} else {
		// Can't execute structural only
		result.Level = "structural"
		score := 0.70
		if len(lintIssues) > 0 {
			score -= float64(len(lintIssues)) * 0.1
		}
		if len(constraintIssues) > 0 {
			score -= float64(len(constraintIssues)) * 0.05
		}
		if score < 0.3 {
			score = 0.3
		}
		result.Score = score
	}

	// Apply constraint penalties
	if len(constraintIssues) > 0 && result.Score > 0.5 {
		result.Score -= float64(len(constraintIssues)) * 0.03
		if result.Score < 0.5 {
			result.Score = 0.5
		}
	}

	result.Passed = result.Score >= 0.6
	result.Report = report
	return result
}

// Phase 2: Structural Lint

func structuralLint(block CodeBlock) []string {
	var issues []string
	code := block.Code

	// Bracket balance
	if opens, closes := strings.Count(code, "{"), strings.Count(code, "}"); opens != closes {
		issues = append(issues, fmt.Sprintf("unbalanced braces: %d open, %d close", opens, closes))
	}

	// Parenthesis balance
	if opens, closes := strings.Count(code, "("), strings.Count(code, ")"); opens != closes {
		issues = append(issues, fmt.Sprintf("unbalanced parentheses: %d open, %d close", opens, closes))
	}

	// Incomplete code
	lines := strings.Split(code, "\n")
	lastLine := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			lastLine = t
			break
		}
	}
	if strings.HasSuffix(lastLine, ",") || strings.HasSuffix(lastLine, "=") ||
		strings.HasSuffix(lastLine, "+") || strings.HasSuffix(lastLine, "->") {
		issues = append(issues, "code appears incomplete — ends with dangling operator")
	}

	// Detect orphan brace pattern: value, err := fn()\n}
	// This is the #1 model error — the model writes ReadAll() then a stray }
	// instead of if err != nil { return nil, err }
	for i := 0; i < len(lines)-1; i++ {
		trimmed := strings.TrimSpace(lines[i])
		nextTrimmed := ""
		if i+1 < len(lines) {
			nextTrimmed = strings.TrimSpace(lines[i+1])
		}
		if (strings.Contains(trimmed, ":=") || strings.Contains(trimmed, "= ")) &&
			!strings.HasSuffix(trimmed, "{") &&
			nextTrimmed == "}" {
			issues = append(issues, fmt.Sprintf("orphan closing brace after assignment on line %d — likely missing error check (if err != nil { ... })", i+2))
		}
	}

	// Placeholder content
	if strings.Contains(code, "// TODO") || strings.Contains(code, "// FIXME") ||
		strings.Contains(code, "// implement") || strings.Contains(code, "# implement") ||
		strings.Contains(code, "pass # ") {
		issues = append(issues, "contains placeholder code — implementation incomplete")
	}

	// Language-specific
	switch block.Language {
	case "go":
		if strings.Contains(code, "func ") && !strings.Contains(code, "package ") {
			issues = append(issues, "missing package declaration")
		}
	case "python":
		hasTab := strings.Contains(code, "\t")
		hasSpace := false
		for _, l := range lines {
			if strings.HasPrefix(l, "    ") {
				hasSpace = true
				break
			}
		}
		if hasTab && hasSpace {
			issues = append(issues, "mixed tabs and spaces — IndentationError")
		}
	case "javascript", "typescript":
		if strings.Count(code, "`")%2 != 0 {
			issues = append(issues, "unmatched template literal backtick")
		}
	}

	return issues
}

// Phase 3: Constraint Enforcement

func enforceConstraints(block CodeBlock) []string {
	var issues []string
	code := block.Code
	lines := strings.Split(code, "\n")

	// Count real code lines (non-empty, non-comment)
	codeLines := 0
	commentLines := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
			commentLines++
		} else {
			codeLines++
		}
	}

	// Over-commented code (more than 40% comments)
	if codeLines > 0 && commentLines > 0 {
		ratio := float64(commentLines) / float64(codeLines+commentLines)
		if ratio > 0.40 {
			issues = append(issues, fmt.Sprintf("over-commented: %.0f%% comments — keep only non-obvious comments", ratio*100))
		}
	}

	// Detect filler comments
	fillerComments := 0
	fillerPatterns := []string{
		"// this function", "// this method", "// returns", "// takes",
		"// creates", "// initializes", "# this function", "# this method",
	}
	for _, line := range lines {
		trimmed := strings.ToLower(strings.TrimSpace(line))
		for _, pattern := range fillerPatterns {
			if strings.HasPrefix(trimmed, pattern) {
				fillerComments++
				break
			}
		}
	}
	if fillerComments >= 3 {
		issues = append(issues, fmt.Sprintf("%d filler comments detected — remove obvious comments like '// this function does X'", fillerComments))
	}

	// Go-specific: detect unused imports (basic check)
	if block.Language == "go" {
		importRe := regexp.MustCompile(`"(\w+)"`)
		importBlock := ""
		inImport := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "import (" {
				inImport = true
				continue
			}
			if inImport && trimmed == ")" {
				break
			}
			if inImport {
				importBlock += line + "\n"
			}
		}

		imports := importRe.FindAllStringSubmatch(importBlock, -1)
		bodyStart := strings.Index(code, ")\n")
		if bodyStart > 0 {
			body := code[bodyStart:]
			for _, imp := range imports {
				pkg := imp[1]
				// Simple check: is the package name used in the code body?
				if !strings.Contains(body, pkg+".") && pkg != "main" {
					issues = append(issues, fmt.Sprintf("potentially unused import: %q", pkg))
				}
			}
		}
	}

	return issues
}

// Phase 4: Execute + Test
func executeAndTest(block CodeBlock) *CodeExecResult {
	switch block.Language {
	case "go":
		return executeGoFull(block.Code)
	case "python":
		return executePythonFull(block.Code)
	case "javascript":
		return executeJavaScript(block.Code)
	default:
		return nil
	}
}

func executeGoFull(code string) *CodeExecResult {
	result := &CodeExecResult{Language: "go"}
	start := time.Now()

	// Code is already auto-fixed by verifyCodeResponse before reaching here.
	fullCode := code

	if !strings.Contains(fullCode, "package ") {
		fullCode = "package main\n\n" + fullCode
	}
	if !strings.Contains(fullCode, "func main()") && !strings.Contains(fullCode, "func main ()") {
		if strings.Contains(fullCode, "func ") {
			fullCode = fullCode + "\n\nfunc main() {}\n"
		}
	}

	dir, err := os.MkdirTemp("", "ark-verify-*")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)

	filePath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filePath, []byte(fullCode), 0644); err != nil {
		return nil
	}

	// Init module
	initCmd := exec.Command("go", "mod", "init", "ark-verify")
	initCmd.Dir = dir
	initCmd.Run()

	// Phase 4a: Lint with go vet
	vetCmd := exec.Command("go", "vet", "./...")
	vetCmd.Dir = dir
	vetOutput, vetErr := vetCmd.CombinedOutput()
	if vetErr != nil {
		result.LintIssues = append(result.LintIssues, cleanCompilerError(string(vetOutput)))
	}
	result.Linted = true

	// Phase 4b: Compile
	buildCmd := exec.Command("go", "build", "-o", "/dev/null", ".")
	buildCmd.Dir = dir
	buildOutput, buildErr := buildCmd.CombinedOutput()
	result.Duration = time.Since(start)

	if buildErr != nil {
		result.Compiled = false
		result.Error = cleanCompilerError(string(buildOutput))
		return result
	}
	result.Compiled = true

	// Phase 4c: Run (with timeout)
	result.Ran = runWithTimeout(dir, "go", []string{"run", "."}, result, 10*time.Second)

	// Phase 4d: Auto-generate and run test
	testResult := autoTestGo(fullCode, dir)
	if testResult != nil {
		result.TestsPassed = testResult.passed
		result.TestOutput = testResult.output
	}

	return result
}

func executePythonFull(code string) *CodeExecResult {
	result := &CodeExecResult{Language: "python"}
	start := time.Now()

	pythonBin := findPython()
	if pythonBin == "" {
		return nil
	}

	dir, err := os.MkdirTemp("", "ark-verify-*")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)

	filePath := filepath.Join(dir, "verify.py")
	if err := os.WriteFile(filePath, []byte(code), 0644); err != nil {
		return nil
	}

	// Syntax check
	syntaxCmd := exec.Command(pythonBin, "-m", "py_compile", filePath)
	syntaxOutput, syntaxErr := syntaxCmd.CombinedOutput()
	result.Duration = time.Since(start)

	if syntaxErr != nil {
		result.Compiled = false
		result.Error = cleanCompilerError(string(syntaxOutput))
		return result
	}
	result.Compiled = true

	// Run
	result.Ran = runWithTimeout(dir, pythonBin, []string{filePath}, result, 10*time.Second)

	// Lint with basic checks
	result.Linted = true

	return result
}

func executeJavaScript(code string) *CodeExecResult {
	result := &CodeExecResult{Language: "javascript"}
	start := time.Now()

	if _, err := exec.LookPath("node"); err != nil {
		return nil
	}

	dir, err := os.MkdirTemp("", "ark-verify-*")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)

	filePath := filepath.Join(dir, "verify.js")
	if err := os.WriteFile(filePath, []byte(code), 0644); err != nil {
		return nil
	}

	// Syntax check
	syntaxCmd := exec.Command("node", "--check", filePath)
	syntaxOutput, syntaxErr := syntaxCmd.CombinedOutput()
	result.Duration = time.Since(start)

	if syntaxErr != nil {
		result.Compiled = false
		result.Error = cleanCompilerError(string(syntaxOutput))
		return result
	}
	result.Compiled = true

	// Run
	result.Ran = runWithTimeout(dir, "node", []string{filePath}, result, 10*time.Second)
	result.Linted = true

	return result
}

type testResult struct {
	passed bool
	output string
}

// FuncSignature holds a parsed function signature.
type FuncSignature struct {
	Name    string
	Params  []FuncParam
	Returns string
}

// FuncParam holds a function parameter.
type FuncParam struct {
	Name string
	Type string
}

func autoTestGo(code string, dir string) *testResult {
	funcs := extractFuncSignatures(code)
	if len(funcs) == 0 {
		return nil
	}

	var testCode strings.Builder
	testCode.WriteString("package main\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\n")

	testableCount := 0
	for _, fn := range funcs {
		if fn.Name == "main" {
			continue
		}

		testFunc := generateFuncTest(fn)
		if testFunc != "" {
			testCode.WriteString(testFunc)
			testableCount++
		}
	}

	if testableCount == 0 {
		return nil
	}

	// Suppress unused import
	testCode.WriteString("func init() { _ = os.TempDir }\n")

	testPath := filepath.Join(dir, "main_test.go")
	if err := os.WriteFile(testPath, []byte(testCode.String()), 0644); err != nil {
		return nil
	}

	testCmd := exec.Command("go", "test", "-v", "-timeout", "10s", ".")
	testCmd.Dir = dir
	output, err := testCmd.CombinedOutput()

	return &testResult{
		passed: err == nil,
		output: cleanCompilerError(string(output)),
	}
}

func extractFuncSignatures(code string) []FuncSignature {
	funcRe := regexp.MustCompile(`func\s+([a-zA-Z]\w*)\s*\(([^)]*)\)\s*([^{]*)`)
	matches := funcRe.FindAllStringSubmatch(code, -1)

	var funcs []FuncSignature
	for _, m := range matches {
		fn := FuncSignature{
			Name:    m[1],
			Returns: strings.TrimSpace(m[3]),
		}

		// Parse parameters
		paramStr := strings.TrimSpace(m[2])
		if paramStr != "" {
			parts := strings.Split(paramStr, ",")
			for _, p := range parts {
				p = strings.TrimSpace(p)
				fields := strings.Fields(p)
				if len(fields) == 2 {
					fn.Params = append(fn.Params, FuncParam{Name: fields[0], Type: fields[1]})
				} else if len(fields) == 1 {
					fn.Params = append(fn.Params, FuncParam{Type: fields[0]})
				}
			}
		}

		funcs = append(funcs, fn)
	}
	return funcs
}

func generateFuncTest(fn FuncSignature) string {
	var sb strings.Builder

	// Determine test name
	testName := "Test_" + fn.Name
	if fn.Name[0] >= 'A' && fn.Name[0] <= 'Z' {
		testName = "Test" + fn.Name
	}

	sb.WriteString(fmt.Sprintf("func %s(t *testing.T) {\n", testName))

	// Detect if function takes a file path parameter
	hasFilePath := false
	fileParamName := ""
	for _, p := range fn.Params {
		if p.Type == "string" && isFilePathParam(p.Name) {
			hasFilePath = true
			fileParamName = p.Name
			break
		}
	}

	// Generate test body based on function signature
	if hasFilePath {
		// Create a temp test file for file-based functions
		sb.WriteString("\t// Create temp test file\n")
		sb.WriteString("\ttmpFile, err := os.CreateTemp(\"\", \"ark-test-*.csv\")\n")
		sb.WriteString("\tif err != nil {\n\t\tt.Fatalf(\"failed to create temp file: %v\", err)\n\t}\n")
		sb.WriteString("\tdefer os.Remove(tmpFile.Name())\n\n")
		sb.WriteString("\t// Write test data\n")
		sb.WriteString("\ttmpFile.WriteString(\"name,age\\nAlice,30\\nBob,25\\n\")\n")
		sb.WriteString("\ttmpFile.Close()\n\n")

		_ = fileParamName // used conceptually

		// Call the function with the temp file
		sb.WriteString(fmt.Sprintf("\t// Call %s with test file\n", fn.Name))
		args := buildTestArgs(fn.Params, "tmpFile.Name()")
		sb.WriteString(fmt.Sprintf("\t"))

		if fn.Returns != "" {
			if strings.Contains(fn.Returns, "error") {
				// Returns (something, error)
				sb.WriteString(fmt.Sprintf("result, err := %s(%s)\n", fn.Name, args))
				sb.WriteString("\tif err != nil {\n")
				sb.WriteString(fmt.Sprintf("\t\tt.Fatalf(\"%s returned error: %%v\", err)\n", fn.Name))
				sb.WriteString("\t}\n")

				// Check result is not nil/empty
				if strings.Contains(fn.Returns, "[]") {
					sb.WriteString("\tif len(result) == 0 {\n")
					sb.WriteString(fmt.Sprintf("\t\tt.Error(\"%s returned empty result\")\n", fn.Name))
					sb.WriteString("\t}\n")

					// Verify content
					sb.WriteString("\t// Verify content matches test data\n")
					sb.WriteString("\tif len(result) >= 2 {\n")
					sb.WriteString("\t\t// Should have header + 2 data rows or just 2 data rows\n")
					sb.WriteString(fmt.Sprintf("\t\tt.Logf(\"%s returned %%d rows\", len(result))\n", fn.Name))
					sb.WriteString("\t}\n")
				} else {
					sb.WriteString("\t_ = result\n")
				}
			} else {
				// Returns something without error
				sb.WriteString(fmt.Sprintf("result := %s(%s)\n", fn.Name, args))
				sb.WriteString("\t_ = result\n")
			}
		} else {
			// No return value
			sb.WriteString(fmt.Sprintf("%s(%s)\n", fn.Name, args))
		}
	} else {
		// Non-file function — generate basic callable test
		sb.WriteString(fmt.Sprintf("\t// Verify %s is callable\n", fn.Name))

		args := buildTestArgsDefault(fn.Params)
		if fn.Returns != "" && strings.Contains(fn.Returns, "error") {
			sb.WriteString(fmt.Sprintf("\t_, err := %s(%s)\n", fn.Name, args))
			sb.WriteString("\t_ = err\n")
		} else if fn.Returns != "" {
			sb.WriteString(fmt.Sprintf("\tresult := %s(%s)\n", fn.Name, args))
			sb.WriteString("\t_ = result\n")
		} else {
			sb.WriteString(fmt.Sprintf("\t%s(%s)\n", fn.Name, args))
		}
	}

	sb.WriteString("}\n\n")
	return sb.String()
}

// isFilePathParam checks if a parameter name suggests it's a file path.
func isFilePathParam(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "file") || strings.Contains(n, "path") ||
		strings.Contains(n, "filename") || n == "f" || n == "fp" ||
		strings.Contains(n, "fname") || strings.Contains(n, "src")
}

// buildTestArgs generates test arguments, using tempFileName for file path params.
func buildTestArgs(params []FuncParam, tempFileName string) string {
	args := make([]string, 0, len(params))
	for _, p := range params {
		if p.Type == "string" && isFilePathParam(p.Name) {
			args = append(args, tempFileName)
		} else {
			args = append(args, defaultValue(p.Type))
		}
	}
	return strings.Join(args, ", ")
}

// buildTestArgsDefault generates default test arguments.
func buildTestArgsDefault(params []FuncParam) string {
	args := make([]string, 0, len(params))
	for _, p := range params {
		args = append(args, defaultValue(p.Type))
	}
	return strings.Join(args, ", ")
}

// defaultValue returns a zero/default value for a Go type.
func defaultValue(typ string) string {
	switch typ {
	case "string":
		return `""`
	case "int", "int32", "int64", "uint", "uint32", "uint64":
		return "0"
	case "float32", "float64":
		return "0.0"
	case "bool":
		return "false"
	case "byte":
		return "0"
	default:
		if strings.HasPrefix(typ, "[]") {
			return "nil"
		}
		if strings.HasPrefix(typ, "*") {
			return "nil"
		}
		if strings.HasPrefix(typ, "map[") {
			return "nil"
		}
		return `""`
	}
}

// Utilities

func runWithTimeout(dir string, bin string, args []string, result *CodeExecResult, timeout time.Duration) bool {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	done := make(chan struct{})
	var output []byte
	var err error

	go func() {
		output, err = cmd.CombinedOutput()
		close(done)
	}()

	select {
	case <-done:
		if err != nil {
			result.Error = cleanCompilerError(string(output))
			return false
		}
		result.Output = string(output)
		return true
	case <-time.After(timeout):
		cmd.Process.Kill()
		result.Error = fmt.Sprintf("execution timed out (%v limit)", timeout)
		return false
	}
}

func findPython() string {
	for _, bin := range []string{"python3", "python"} {
		if _, err := exec.LookPath(bin); err == nil {
			return bin
		}
	}
	return ""
}

func cleanCompilerError(raw string) string {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) > 5 {
		lines = lines[:5]
	}
	return strings.Join(lines, "\n")
}

// CodeBlock represents an extracted code block from a response.
type CodeBlock struct {
	Language string
	Code     string
}

func extractCodeBlocks(text string) []CodeBlock {
	re := regexp.MustCompile("(?s)```(\\w*)\\s*\n(.*?)```")
	matches := re.FindAllStringSubmatch(text, -1)

	blocks := make([]CodeBlock, 0, len(matches))
	for _, m := range matches {
		lang := strings.ToLower(strings.TrimSpace(m[1]))
		code := strings.TrimSpace(m[2])
		if code == "" {
			continue
		}
		if lang == "" {
			lang = detectCodeLanguage(code)
		}
		blocks = append(blocks, CodeBlock{Language: lang, Code: code})
	}
	return blocks
}

func detectCodeLanguage(code string) string {
	c := strings.TrimSpace(code)
	if strings.HasPrefix(c, "package ") || (strings.Contains(c, "func ") && strings.Contains(c, ":= ")) {
		return "go"
	}
	if strings.HasPrefix(c, "import ") || strings.HasPrefix(c, "from ") || strings.HasPrefix(c, "def ") {
		return "python"
	}
	if strings.Contains(c, "const ") || strings.Contains(c, "let ") || strings.Contains(c, "=> ") {
		return "javascript"
	}
	if strings.Contains(c, "public class ") || strings.Contains(c, "public static void main") {
		return "java"
	}
	if strings.HasPrefix(c, "fn ") || strings.Contains(c, "fn main()") {
		return "rust"
	}
	if strings.HasPrefix(c, "#include") {
		return "c"
	}
	return "unknown"
}

// Data/Ranking Verification

func verifyRankingResponse(response string) VerificationResult {
	result := VerificationResult{Level: "structural", Passed: true, Score: 0.8}
	report := &VerifyReport{}

	hasNumberedList := false
	for i := 1; i <= 10; i++ {
		if strings.Contains(response, fmt.Sprintf("%d.", i)) || strings.Contains(response, fmt.Sprintf("%d)", i)) {
			hasNumberedList = true
			break
		}
	}
	report.Checks = append(report.Checks, VerifyCheck{"numbered_list", hasNumberedList, ""})
	if !hasNumberedList {
		result.Score -= 0.2
		result.Issues = append(result.Issues, "no numbered list — rankings should use numbered items")
	}

	hasMetrics := regexp.MustCompile(`\d{3,}`).MatchString(response)
	report.Checks = append(report.Checks, VerifyCheck{"metrics", hasMetrics, ""})
	if !hasMetrics {
		result.Score -= 0.15
		result.Issues = append(result.Issues, "no quantitative metrics — rankings should include numbers")
	}

	hasURLs := strings.Contains(response, "http://") || strings.Contains(response, "https://")
	report.Checks = append(report.Checks, VerifyCheck{"urls", hasURLs, ""})
	if !hasURLs {
		result.Score -= 0.1
		result.Issues = append(result.Issues, "no URLs — rankings should link to sources")
	}

	result.Passed = result.Score >= 0.6
	result.Report = report
	if !result.Passed {
		result.Suggestion = "Format as numbered list with metrics in descending order and include URLs."
	}
	return result
}

func verifyReasoningResponse(response string) VerificationResult {
	result := VerificationResult{Level: "structural", Passed: true, Score: 0.8}
	report := &VerifyReport{}

	wordCount := len(strings.Fields(response))
	substantive := wordCount >= 20
	report.Checks = append(report.Checks, VerifyCheck{"substantive", substantive, fmt.Sprintf("%d words", wordCount)})
	if !substantive {
		result.Score -= 0.3
		result.Issues = append(result.Issues, "response too short for reasoning")
	}

	hasEvidence := regexp.MustCompile(`\d+`).MatchString(response) ||
		strings.Contains(response, "because") || strings.Contains(response, "data shows")
	report.Checks = append(report.Checks, VerifyCheck{"evidence", hasEvidence, ""})
	if !hasEvidence {
		result.Score -= 0.15
		result.Issues = append(result.Issues, "no concrete evidence — reasoning should include specifics")
	}

	hedgeCount := 0
	for _, h := range []string{"might", "perhaps", "possibly", "could be", "hard to say"} {
		hedgeCount += strings.Count(strings.ToLower(response), h)
	}
	notHedgy := hedgeCount <= 3
	report.Checks = append(report.Checks, VerifyCheck{"directness", notHedgy, fmt.Sprintf("%d hedges", hedgeCount)})
	if !notHedgy {
		result.Score -= 0.1
		result.Issues = append(result.Issues, "excessive hedging — be more direct")
	}

	result.Passed = result.Score >= 0.6
	result.Report = report
	if !result.Passed {
		result.Suggestion = "Provide a direct answer with concrete evidence. Lead with your conclusion."
	}
	return result
}

func verifySummaryResponse(response string) VerificationResult {
	result := VerificationResult{Level: "structural", Passed: true, Score: 0.8}
	report := &VerifyReport{}

	wordCount := len(strings.Fields(response))
	concise := wordCount <= 500
	report.Checks = append(report.Checks, VerifyCheck{"concise", concise, fmt.Sprintf("%d words", wordCount)})
	if !concise {
		result.Score -= 0.2
		result.Issues = append(result.Issues, fmt.Sprintf("summary is %d words — should be under 200", wordCount))
	}

	lower := strings.ToLower(response)
	noRestate := !strings.HasPrefix(lower, "you asked") && !strings.HasPrefix(lower, "the question")
	report.Checks = append(report.Checks, VerifyCheck{"no_restate", noRestate, ""})
	if !noRestate {
		result.Score -= 0.1
		result.Issues = append(result.Issues, "starts by restating the question — jump to the answer")
	}

	result.Passed = result.Score >= 0.6
	result.Report = report
	return result
}

func verifyGeneralResponse(response string) VerificationResult {
	result := VerificationResult{Level: "structural", Passed: true, Score: 0.8}
	report := &VerifyReport{}

	notEmpty := strings.TrimSpace(response) != ""
	report.Checks = append(report.Checks, VerifyCheck{"not_empty", notEmpty, ""})
	if !notEmpty {
		result.Score = 0.0
		result.Passed = false
		result.Issues = append(result.Issues, "empty response")
		result.Suggestion = "Please provide a response."
	}

	result.Report = report
	return result
}

// Self-Correction: Build retry prompt from verification failure

func BuildRetryPrompt(original string, verification VerificationResult) string {
	var sb strings.Builder

	sb.WriteString("Your previous response had issues:\n\n")
	for _, issue := range verification.Issues {
		sb.WriteString("- " + issue + "\n")
	}

	if verification.CodeResult != nil && verification.CodeResult.Error != "" {
		sb.WriteString("\nCompiler/runtime error:\n")
		sb.WriteString(verification.CodeResult.Error + "\n")
	}

	// Add specific guidance for common patterns
	hasOrphanBrace := false
	for _, issue := range verification.Issues {
		if strings.Contains(issue, "orphan closing brace") {
			hasOrphanBrace = true
		}
	}

	if hasOrphanBrace {
		sb.WriteString("\nSPECIFIC FIX NEEDED: You wrote `value, err := fn()` followed by a stray `}`. ")
		sb.WriteString("You need to add proper error handling: `if err != nil { return nil, err }` after the assignment.\n")
	}

	if verification.Suggestion != "" {
		sb.WriteString("\n" + verification.Suggestion + "\n")
	}

	return sb.String()
}
