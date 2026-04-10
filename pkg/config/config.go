package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Name    string        `json:"name"`
	Version string        `json:"version"`
	Model   ModelConfig   `json:"model"`
	Context ContextConfig `json:"context"`
	Tools   []ToolConfig  `json:"tools"`
	Memory  MemoryConfig  `json:"memory"`
	Tracing TracingConfig `json:"tracing"`
}
type ModelConfig struct {
	Provider  string `json:"provider"`
	Name      string `json:"name"`
	MaxTokens int    `json:"max_tokens"`
	APIKey    string `json:"api_key"`
	BaseURL   string `json:"base_url"`
}
type ContextConfig struct {
	TotalTokens           int     `json:"total_tokens"`
	Strategy              string  `json:"strategy"`
	ToolBudgetPct         int     `json:"tool_budget_pct"`
	MemoryBudgetPct       int     `json:"memory_budget_pct"`
	ConversationBudgetPct int     `json:"conversation_budget_pct"`
	InitialTools          int     `json:"initial_tools"`
	MaxRetries            int     `json:"max_retries"`
	MaxSteps              int     `json:"max_steps"`
	TimeoutSeconds        int     `json:"timeout_seconds"`
	MaxCostPerTask        float64 `json:"max_cost_per_task"`
}
type ToolConfig struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Method      string            `json:"method"`
	URI         string            `json:"uri"`
	Description string            `json:"description"`
	Params      []string          `json:"params"`
	Headers     map[string]string `json:"headers"`
	Body        string            `json:"body"`
	Timeout     int               `json:"timeout"`
	WriteOp     bool              `json:"write"`
}
type MemoryConfig struct {
	Backend string `json:"backend"`
	Path    string `json:"path"`
	Shared  bool   `json:"shared"`
}
type TracingConfig struct {
	Enabled bool   `json:"enabled"`
	Output  string `json:"output"`
	Level   string `json:"level"`
}

func DefaultConfig() Config {
	return Config{
		Name:    "ark-agent",
		Version: "0.1",
		Model: ModelConfig{
			Provider:  "anthropic",
			Name:      "claude-sonnet-4-20250514",
			MaxTokens: 4096,
		},
		Context: ContextConfig{
			TotalTokens:           200000,
			Strategy:              "adaptive",
			ToolBudgetPct:         10,
			MemoryBudgetPct:       10,
			ConversationBudgetPct: 35,
			InitialTools:          3,
			MaxRetries:            3,
		},
		Memory: MemoryConfig{
			Backend: "memory",
			Path:    "./ark-memory.db",
		},
		Tracing: TracingConfig{
			Enabled: true,
			Output:  "stdout",
			Level:   "detailed",
		},
	}
}
func Load(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("ark/config: cannot open %q: %w", path, err)
	}
	defer file.Close()

	cfg := DefaultConfig()
	cfg.Tools = nil

	p := &parser{
		cfg:     &cfg,
		section: "",
		toolIdx: -1,
	}

	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		if err := p.parseLine(scanner.Text(), lineNum); err != nil {
			return nil, fmt.Errorf("ark/config: %s (line %d in %s)", err, lineNum, path)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ark/config: read error: %w", err)
	}

	resolveEnvVars(&cfg)

	return &cfg, nil
}

type parser struct {
	cfg     *Config
	section string
	toolIdx int
}

func (p *parser) parseLine(raw string, lineNum int) error {
	line := stripComment(raw)
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil
	}

	indent := countIndent(raw)
	if indent == 0 {
		key, val := splitKV(trimmed)
		if val == "" {
			p.section = key
			p.toolIdx = -1
			return nil
		}
		p.section = ""
		return p.applyScalar("", key, val)
	}
	if strings.HasPrefix(trimmed, "- ") {
		if p.section != "tools" {
			return nil
		}
		inner := strings.TrimPrefix(trimmed, "- ")
		key, val := splitKV(inner)
		if key == "name" {
			p.toolIdx = len(p.cfg.Tools)
			p.cfg.Tools = append(p.cfg.Tools, ToolConfig{Name: val})
		}
		return nil
	}
	key, val := splitKV(trimmed)
	if key == "" {
		return nil
	}
	if p.section == "tools" && p.toolIdx >= 0 && p.toolIdx < len(p.cfg.Tools) {
		return p.applyToolField(key, val)
	}

	return p.applyScalar(p.section, key, val)
}

func (p *parser) applyScalar(section, key, val string) error {
	switch section {
	case "":
		switch key {
		case "name":
			p.cfg.Name = val
		case "version":
			p.cfg.Version = val
		}

	case "model":
		switch key {
		case "provider":
			p.cfg.Model.Provider = val
		case "name":
			p.cfg.Model.Name = val
		case "max_tokens":
			v, err := strconv.Atoi(val)
			if err != nil {
				return fmt.Errorf("model.max_tokens: expected integer, got %q", val)
			}
			p.cfg.Model.MaxTokens = v
		case "api_key":
			p.cfg.Model.APIKey = val
		case "base_url":
			p.cfg.Model.BaseURL = val
		}

	case "context":
		switch key {
		case "total_tokens":
			v, err := strconv.Atoi(val)
			if err != nil {
				return fmt.Errorf("context.total_tokens: expected integer, got %q", val)
			}
			p.cfg.Context.TotalTokens = v
		case "strategy":
			p.cfg.Context.Strategy = val
		case "tool_budget":
			p.cfg.Context.ToolBudgetPct = parsePct(val)
		case "memory_budget":
			p.cfg.Context.MemoryBudgetPct = parsePct(val)
		case "conversation_budget":
			p.cfg.Context.ConversationBudgetPct = parsePct(val)
		case "initial_tools":
			v, err := strconv.Atoi(val)
			if err != nil {
				return fmt.Errorf("context.initial_tools: expected integer, got %q", val)
			}
			p.cfg.Context.InitialTools = v
		case "max_retries":
			v, err := strconv.Atoi(val)
			if err != nil {
				return fmt.Errorf("context.max_retries: expected integer, got %q", val)
			}
			p.cfg.Context.MaxRetries = v
		}

	case "memory":
		switch key {
		case "backend":
			p.cfg.Memory.Backend = val
		case "path":
			p.cfg.Memory.Path = val
		case "shared":
			p.cfg.Memory.Shared = (val == "true")
		}

	case "tracing":
		switch key {
		case "enabled":
			p.cfg.Tracing.Enabled = (val == "true")
		case "output":
			p.cfg.Tracing.Output = val
		case "level":
			p.cfg.Tracing.Level = val
		}
	}

	return nil
}

func (p *parser) applyToolField(key, val string) error {
	t := &p.cfg.Tools[p.toolIdx]
	switch key {
	case "name":
		t.Name = val
	case "type":
		t.Type = val
	case "uri":
		t.URI = val
	case "description":
		t.Description = val
	}
	return nil
}

var validProviders = map[string]bool{
	"anthropic": true,
	"openai":    true,
	"ollama":    true,
}

var validStrategies = map[string]bool{
	"adaptive": true,
	"static":   true,
	"manual":   true,
}

func (c *Config) Validate() error {
	var errs []string
	if !validProviders[c.Model.Provider] {
		errs = append(errs, fmt.Sprintf(
			"model.provider: unknown %q (valid: anthropic, openai, ollama)", c.Model.Provider))
	}
	if c.Model.Name == "" {
		errs = append(errs, "model.name: cannot be empty")
	}
	if c.Model.Provider != "ollama" && c.Model.APIKey == "" {
		envVar := providerEnvVar(c.Model.Provider)
		errs = append(errs, fmt.Sprintf(
			"model.api_key: missing for %s (set %s env var or api_key in config)",
			c.Model.Provider, envVar))
	}
	if c.Model.MaxTokens <= 0 {
		errs = append(errs, "model.max_tokens: must be > 0")
	}
	if c.Context.TotalTokens <= 0 {
		errs = append(errs, "context.total_tokens: must be > 0")
	}

	if c.Context.Strategy != "" && !validStrategies[c.Context.Strategy] {
		errs = append(errs, fmt.Sprintf(
			"context.strategy: unknown %q (valid: adaptive, static, manual)", c.Context.Strategy))
	}
	budgetSum := c.Context.ToolBudgetPct + c.Context.MemoryBudgetPct + c.Context.ConversationBudgetPct
	if budgetSum > 90 {
		errs = append(errs, fmt.Sprintf(
			"context: tool_budget (%d%%) + memory_budget (%d%%) + conversation_budget (%d%%) = %d%%, "+
				"exceeds 90%% (need room for system prompt and working context)",
			c.Context.ToolBudgetPct, c.Context.MemoryBudgetPct, c.Context.ConversationBudgetPct, budgetSum))
	}
	for i, t := range c.Tools {
		if t.Name == "" {
			errs = append(errs, fmt.Sprintf("tools[%d]: name cannot be empty", i))
		}
	}

	if len(errs) == 0 {
		return nil
	}

	if len(errs) == 1 {
		return fmt.Errorf("ark/config: %s", errs[0])
	}

	return fmt.Errorf("ark/config: %d problems:\n  - %s", len(errs), strings.Join(errs, "\n  - "))
}
func resolveEnvVars(cfg *Config) {
	if cfg.Model.APIKey == "" {
		envVar := providerEnvVar(cfg.Model.Provider)
		if envVar != "" {
			cfg.Model.APIKey = os.Getenv(envVar)
		}
	}
	if cfg.Model.Provider == "ollama" && cfg.Model.BaseURL == "" {
		cfg.Model.BaseURL = "http://localhost:11434"
	}
}

func providerEnvVar(provider string) string {
	switch provider {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	default:
		return ""
	}
}
func stripComment(line string) string {
	inSingle := false
	inDouble := false

	for i, ch := range line {
		switch ch {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return line[:i]
			}
		}
	}
	return line
}
func countIndent(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}
func splitKV(s string) (string, string) {
	idx := strings.Index(s, ":")
	if idx < 0 {
		return strings.TrimSpace(s), ""
	}

	key := strings.TrimSpace(s[:idx])
	val := strings.TrimSpace(s[idx+1:])
	val = unquote(val)

	return key, val
}
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
func parsePct(s string) int {
	s = strings.TrimSuffix(strings.TrimSpace(s), "%")
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 10
	}
	return v
}
