package runtime

import (
	"regexp"
	"strings"
)

type QualityConfig struct {
	OptimizePrompts   bool
	CleanResponses    bool
	EnforceStructure  bool
	StripBoilerplate  bool
	StripExplanations bool
}

func DefaultQualityConfig() QualityConfig {
	return QualityConfig{
		OptimizePrompts:   true,
		CleanResponses:    true,
		EnforceStructure:  true,
		StripBoilerplate:  true,
		StripExplanations: false, // only enable for code-specific tasks
	}
}

func OptimizePrompt(raw string, taskType string) string {
	if isPrecisePrompt(raw) {
		return raw
	}

	var constraints []string

	switch taskType {
	case "coding":
		constraints = []string{
			"Write ONLY the code that directly solves this. No unnecessary abstractions.",
			"Do NOT include explanatory comments unless the logic is non-obvious.",
			"Do NOT wrap in unnecessary classes or design patterns unless asked.",
			"Use the simplest correct approach. Fewer lines = better.",
			"Include error handling only where it matters for correctness.",
			"If a standard library solution exists, use it instead of writing custom code.",
		}

	case "ranking":
		constraints = []string{
			"Return results in a clear numbered list with the key metric (e.g. stars, downloads) next to each item.",
			"Rank by the objective metric, not by your opinion.",
			"Include a direct link/URL for each item when available.",
			"Keep descriptions to one sentence each.",
		}

	case "reasoning":
		constraints = []string{
			"Lead with the conclusion, then explain why.",
			"Use concrete evidence, not general statements.",
			"If there are trade-offs, state them explicitly.",
			"Keep the response focused — don't pad with background the user already knows.",
		}

	case "summarization":
		constraints = []string{
			"Start with a one-sentence summary of the main point.",
			"Use bullet points for supporting details.",
			"Do NOT restate the question or add preamble.",
			"Keep it under 200 words unless asked for more.",
		}

	case "retrieval":
		constraints = []string{
			"Return the requested data directly.",
			"Do NOT add commentary unless asked.",
			"Format data consistently (same structure for each item).",
		}

	case "multi_step":
		constraints = []string{
			"Break your response into clear numbered steps.",
			"Complete each step fully before moving to the next.",
			"If a step requires a tool call, make the call before continuing.",
		}

	default:
		constraints = []string{
			"Be direct. Lead with the answer, not the preamble.",
			"If you're not sure about something, say so explicitly rather than guessing.",
		}
	}

	if len(constraints) == 0 {
		return raw
	}

	return raw + "\n\n[Quality constraints: " + strings.Join(constraints, " ") + "]"
}

func isPrecisePrompt(prompt string) bool {
	p := strings.ToLower(prompt)
	precisionSignals := []string{
		"respond in json",
		"return only",
		"format as",
		"do not include",
		"exactly",
		"strictly",
		"output format:",
		"constraints:",
		"requirements:",
	}
	for _, sig := range precisionSignals {
		if strings.Contains(p, sig) {
			return true
		}
	}
	return false
}

func CleanResponse(raw string, taskType string, config QualityConfig) string {
	if !config.CleanResponses {
		return raw
	}

	cleaned := raw

	// Strip boilerplate preamble
	if config.StripBoilerplate {
		cleaned = stripBoilerplate(cleaned)
	}

	// Task-specific cleaning
	switch taskType {
	case "coding":
		cleaned = cleanCodeResponse(cleaned, config)
	case "ranking":
		cleaned = cleanRankingResponse(cleaned)
	case "reasoning":
		cleaned = cleanReasoningResponse(cleaned)
	case "summarization":
		cleaned = cleanSummaryResponse(cleaned)
	}

	// Final cleanup
	cleaned = strings.TrimSpace(cleaned)

	// Don't return empty
	if cleaned == "" {
		return raw
	}

	return cleaned
}

// stripBoilerplate removes common AI preamble that adds no value.
func stripBoilerplate(text string) string {
	// Patterns that start responses with filler
	boilerplateStarts := []string{
		"Sure! ",
		"Sure, ",
		"Sure thing! ",
		"Of course! ",
		"Of course, ",
		"Certainly! ",
		"Certainly, ",
		"Absolutely! ",
		"Absolutely, ",
		"Great question! ",
		"Great question, ",
		"That's a great question! ",
		"I'd be happy to help! ",
		"I'd be happy to help. ",
		"I'd be happy to ",
		"I would be happy to ",
		"Let me help you with that. ",
		"Let me help you with that! ",
		"Here's what I found: ",
		"Here's what I found:\n",
		"Here is what I found:\n",
		"Based on the information provided, ",
		"Based on the data provided, ",
		"Based on the provided data, ",
	}

	trimmed := text
	for _, prefix := range boilerplateStarts {
		if strings.HasPrefix(trimmed, prefix) {
			trimmed = trimmed[len(prefix):]
			// Capitalize the first letter of the remaining text
			if len(trimmed) > 0 {
				trimmed = strings.ToUpper(trimmed[:1]) + trimmed[1:]
			}
			break
		}
	}

	// Remove trailing filler
	boilerplateEnds := []string{
		"\n\nLet me know if you have any questions!",
		"\n\nLet me know if you need anything else!",
		"\n\nLet me know if you'd like more details!",
		"\n\nLet me know if you want me to elaborate!",
		"\n\nFeel free to ask if you have any questions!",
		"\n\nFeel free to ask for more details!",
		"\n\nHope this helps!",
		"\n\nI hope this helps!",
		"\n\nIs there anything else you'd like to know?",
		"\n\nIs there anything else I can help with?",
		"\n\nWould you like me to explain anything further?",
	}

	for _, suffix := range boilerplateEnds {
		if strings.HasSuffix(trimmed, suffix) {
			trimmed = trimmed[:len(trimmed)-len(suffix)]
			break
		}
	}

	return trimmed
}

// cleanCodeResponse strips unnecessary content from code responses.
func cleanCodeResponse(text string, config QualityConfig) string {
	lines := strings.Split(text, "\n")
	var result []string
	inCodeBlock := false
	codeBlockCount := 0
	var codeBlocks []string
	var currentBlock []string

	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)

		// Track code blocks
		if strings.HasPrefix(trimmedLine, "```") {
			if inCodeBlock {
				// End of code block
				inCodeBlock = false
				codeBlockCount++
				codeBlocks = append(codeBlocks, strings.Join(currentBlock, "\n"))
				currentBlock = nil
			} else {
				// Start of code block
				inCodeBlock = true
				currentBlock = nil
			}
			result = append(result, line)
			continue
		}

		if inCodeBlock {
			currentBlock = append(currentBlock, line)
			result = append(result, line)
			continue
		}

		// Outside code blocks
		if config.StripExplanations && codeBlockCount > 0 {
			// After the first code block, skip explanatory text
			// unless it's another code block or important note
			if trimmedLine == "" || strings.HasPrefix(trimmedLine, "Note:") ||
				strings.HasPrefix(trimmedLine, "Important:") ||
				strings.HasPrefix(trimmedLine, "Warning:") {
				result = append(result, line)
			}
			continue
		}

		result = append(result, line)
	}

	_ = codeBlocks

	return strings.Join(result, "\n")
}
func cleanRankingResponse(text string) string {
	// Rankings should already be numbered — just clean up inconsistencies
	return text
}

// cleanReasoningResponse cleans up reasoning responses.
func cleanReasoningResponse(text string) string {
	// Remove hedging at the start
	hedges := []string{
		"This is a complex topic, but ",
		"This is a nuanced question, but ",
		"There are many factors to consider, but ",
		"It depends on many factors, but ",
	}
	cleaned := text
	for _, hedge := range hedges {
		if strings.HasPrefix(cleaned, hedge) {
			cleaned = cleaned[len(hedge):]
			if len(cleaned) > 0 {
				cleaned = strings.ToUpper(cleaned[:1]) + cleaned[1:]
			}
			break
		}
	}
	return cleaned
}

// cleanSummaryResponse ensures summaries are concise.
func cleanSummaryResponse(text string) string {
	// Remove "In summary," / "To summarize," prefixes — redundant when asked to summarize
	prefixes := []string{
		"In summary, ",
		"To summarize, ",
		"In conclusion, ",
		"To conclude, ",
	}
	cleaned := text
	for _, p := range prefixes {
		if strings.HasPrefix(cleaned, p) {
			cleaned = cleaned[len(p):]
			if len(cleaned) > 0 {
				cleaned = strings.ToUpper(cleaned[:1]) + cleaned[1:]
			}
			break
		}
	}
	return cleaned
}
func EnhanceSystemPrompt(basePrompt string, taskType string) string {
	var directive string

	switch taskType {
	case "coding":
		directive = `
RESPONSE QUALITY RULES:
- Write minimal, correct code. No unnecessary abstractions or patterns.
- Use standard library functions when available instead of custom implementations.
- No filler comments like "// This function does X" — only comment non-obvious logic.
- Do NOT wrap simple functions in classes unless the user asks for OOP.
- If the solution is 5 lines, don't write 50.

CRITICAL GO SYNTAX RULES:
- ALWAYS include proper error handling: if err != nil { return ..., err }
- NEVER leave a closing brace } on its own after an assignment line.
- ALL braces must be balanced — count them before responding.
- Do NOT put test code (import "testing") in the same code block as the main code.
- If the user asks for tests, put them in a SEPARATE code block labeled "// main_test.go".
- The main code block should be self-contained and compilable on its own.`

	case "ranking":
		directive = `
RESPONSE QUALITY RULES:
- Return a numbered list with the ranking metric (stars, downloads, etc.) next to each item.
- Rank by objective data, not opinion.
- Keep descriptions to one sentence per item.
- Include URLs when available.
- Do NOT add lengthy explanations for why each item is popular unless asked.`

	case "reasoning":
		directive = `
RESPONSE QUALITY RULES:
- Lead with your conclusion in the first sentence.
- Support with concrete evidence, not generalities.
- State trade-offs explicitly.
- Do NOT pad with background information the user likely already knows.
- Be direct. Short paragraphs. No filler.`

	case "summarization":
		directive = `
RESPONSE QUALITY RULES:
- First sentence = the main takeaway.
- Use bullet points for supporting details.
- Under 200 words unless asked for more.
- Do NOT restate the question.`

	default:
		directive = `
RESPONSE QUALITY RULES:
- Be direct. Answer first, explain second.
- Do NOT start with "Sure!", "Of course!", "Great question!", or similar filler.
- Do NOT end with "Let me know if you have questions!" or similar.
- If uncertain, say so explicitly instead of guessing.
- Keep responses focused on what was asked.`
	}

	return basePrompt + directive
}

// DeduplicateResponse removes lines that are substantially repeated.
func DeduplicateResponse(text string) string {
	lines := strings.Split(text, "\n")
	seen := make(map[string]bool)
	var result []string

	for _, line := range lines {
		normalized := strings.ToLower(strings.TrimSpace(line))
		// Skip empty lines dedup (keep formatting)
		if normalized == "" {
			result = append(result, line)
			continue
		}
		// Skip very short lines (bullets, numbers)
		if len(normalized) < 10 {
			result = append(result, line)
			continue
		}
		if !seen[normalized] {
			seen[normalized] = true
			result = append(result, line)
		}
	}

	return strings.Join(result, "\n")
}

// ValidateCodeResponse checks if a code response has basic quality signals.
func ValidateCodeResponse(text string) CodeQuality {
	q := CodeQuality{
		HasCode:        false,
		LinesOfCode:    0,
		LinesOfComment: 0,
		HasImports:     false,
		IsComplete:     false,
	}

	codeBlockRe := regexp.MustCompile("(?s)```\\w*\n(.*?)```")
	matches := codeBlockRe.FindAllStringSubmatch(text, -1)

	if len(matches) == 0 {
		return q
	}

	q.HasCode = true

	for _, match := range matches {
		code := match[1]
		lines := strings.Split(code, "\n")
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") ||
				strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*") {
				q.LinesOfComment++
			} else {
				q.LinesOfCode++
			}
			if strings.HasPrefix(trimmed, "import") || strings.HasPrefix(trimmed, "from") ||
				strings.HasPrefix(trimmed, "require") || strings.HasPrefix(trimmed, "use") ||
				strings.HasPrefix(trimmed, "#include") {
				q.HasImports = true
			}
		}
	}

	// Simple completeness check: code has a function/class definition and closing brace
	fullCode := text
	if strings.Contains(fullCode, "func ") || strings.Contains(fullCode, "def ") ||
		strings.Contains(fullCode, "function ") || strings.Contains(fullCode, "class ") {
		if strings.Contains(fullCode, "}") || strings.Contains(fullCode, "return") {
			q.IsComplete = true
		}
	}

	// Calculate comment ratio
	total := q.LinesOfCode + q.LinesOfComment
	if total > 0 {
		q.CommentRatio = float64(q.LinesOfComment) / float64(total)
	}

	return q
}

// CodeQuality holds quality metrics for a code response.
type CodeQuality struct {
	HasCode        bool
	LinesOfCode    int
	LinesOfComment int
	CommentRatio   float64
	HasImports     bool
	IsComplete     bool
}

func stripTestInstructions(prompt string) string {
	lines := strings.Split(prompt, "\n")
	var kept []string
	for _, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		// Skip lines that are purely about tests
		if strings.Contains(lower, "unit test") || strings.Contains(lower, "include test") ||
			strings.Contains(lower, "write test") || strings.Contains(lower, "add test") ||
			lower == "- include unit tests for all cases" || lower == "- include tests" ||
			strings.HasPrefix(lower, "- test ") || lower == "- include unit tests" {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func AutoFixGoCode(code string) string {
	lines := strings.Split(code, "\n")
	var fixed []string

	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])

		if i > 0 && trimmed == "}" {
			prevTrimmed := strings.TrimSpace(lines[i-1])
			if strings.Contains(prevTrimmed, ", err :=") || strings.Contains(prevTrimmed, ", err =") {
				// Detect indentation from the assignment line
				indent := getIndent(lines[i-1])
				fixed = append(fixed, indent+"if err != nil {")
				fixed = append(fixed, indent+indentUnit(indent)+"return nil, err")
				fixed = append(fixed, indent+"}")
				continue
			}
		}

		if strings.Contains(trimmed, "return") && strings.Contains(trimmed, "err") &&
			!strings.HasPrefix(trimmed, "if ") && !strings.HasPrefix(trimmed, "//") {
			if i > 0 {
				prevTrimmed := strings.TrimSpace(lines[i-1])
				if prevTrimmed == "}" && i >= 2 {
					prevPrev := strings.TrimSpace(lines[i-2])
					if strings.Contains(prevPrev, "io.EOF") || strings.Contains(prevPrev, "break") {
						indent := getIndent(lines[i])
						fixed = append(fixed, indent+"if err != nil {")
						fixed = append(fixed, indent+indentUnit(indent)+trimmed)
						fixed = append(fixed, indent+"}")
						continue
					}
				}
			}
		}

		fixed = append(fixed, lines[i])
	}

	return strings.Join(fixed, "\n")
}
func getIndent(line string) string {
	for i, c := range line {
		if c != ' ' && c != '\t' {
			return line[:i]
		}
	}
	return ""
}
func indentUnit(existingIndent string) string {
	if strings.Contains(existingIndent, "\t") {
		return "\t"
	}
	return "    "
}

func countLeadingTabs(s string) int {
	count := 0
	for _, c := range s {
		if c == '\t' {
			count++
		} else {
			break
		}
	}
	return count
}

func autoFixCodeInResponse(response string) string {
	re := regexp.MustCompile("(?s)(```(?:go)?\\s*\n)(.*?)(```)")
	return re.ReplaceAllStringFunc(response, func(match string) string {
		parts := re.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		opener := parts[1] // ```go\n
		code := parts[2]   // the code content
		closer := parts[3] // ```

		// Detect language — only fix Go code
		lang := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(opener), "```"))
		if lang == "" {
			lang = detectCodeLanguageFromContent(code)
		}

		if lang == "go" || lang == "" {
			fixed := AutoFixGoCode(code)
			return opener + fixed + closer
		}
		return match
	})
}

// detectCodeLanguageFromContent guesses language from code content.
func detectCodeLanguageFromContent(code string) string {
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
	return ""
}
