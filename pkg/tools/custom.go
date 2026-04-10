package tools

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

type CustomToolConfig struct {
	Name        string
	Description string
	Method      string
	URL         string
	Params      []string
	Headers     map[string]string
	Body        string
	Timeout     time.Duration
	WriteOp     bool
}

func RegisterCustomHTTP(router *Router, cfg CustomToolConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("custom_tool: name is required")
	}
	if cfg.URL == "" {
		return fmt.Errorf("custom_tool/%s: uri is required", cfg.Name)
	}

	parsed, err := url.Parse(cfg.URL)
	if err != nil {
		return fmt.Errorf("custom_tool/%s: invalid URL %q: %w", cfg.Name, cfg.URL, err)
	}
	domain := parsed.Hostname()
	if domain == "" {
		return fmt.Errorf("custom_tool/%s: cannot extract domain from %q", cfg.Name, cfg.URL)
	}

	if exec, ok := router.GetExecutor().(*HTTPExecutor); ok {
		exec.AddDomain(domain)
	}
	resolvedHeaders := resolveEnvHeaders(cfg.Headers)
	method := cfg.Method
	if method == "" {
		method = "GET"
	}

	toolType := "read"
	if cfg.WriteOp || method == "POST" || method == "PUT" || method == "DELETE" || method == "PATCH" {
		toolType = "write"
	}

	body := cfg.Body
	if body == "" && (method == "POST" || method == "PUT" || method == "PATCH") {
		body = "json"
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	httpConfig := HTTPToolConfig{
		Method:  method,
		URL:     cfg.URL,
		Headers: resolvedHeaders,
		Body:    body,
		Timeout: timeout,
	}

	desc := cfg.Description
	if desc == "" {
		desc = fmt.Sprintf("%s: custom HTTP tool (%s %s)", cfg.Name, method, domain)
	}

	exec := router.GetExecutor()
	router.RegisterTool(Tool{
		Name:           cfg.Name,
		Description:    desc,
		Version:        "v1",
		RequiredParams: cfg.Params,
		Handler: func(params map[string]interface{}) (string, error) {
			result, err := exec.Execute(httpConfig, params)
			if err != nil {
				return "", fmt.Errorf("%s: %w", cfg.Name, err)
			}
			return result, nil
		},
		Metadata: map[string]interface{}{
			"type":          toolType,
			"auth_required": len(resolvedHeaders) > 0,
			"method":        method,
			"domain":        domain,
			"custom":        true,
		},
	})

	return nil
}

func CustomToolDef(name, description string, params []string) ToolDef {
	paramsJSON := "[]"
	if len(params) > 0 {
		if data, err := json.Marshal(params); err == nil {
			paramsJSON = string(data)
		}
	}

	desc := description
	if desc == "" {
		desc = fmt.Sprintf("%s: custom HTTP tool", name)
	}

	schema := fmt.Sprintf(`{"name":"%s","description":"%s","params":%s}`, name, desc, paramsJSON)

	return ToolDef{
		ID:     name,
		Name:   name,
		Desc:   desc,
		Schema: schema,
	}
}

func resolveEnvHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return map[string]string{}
	}

	resolved := make(map[string]string, len(headers))
	for key, val := range headers {
		resolved[key] = resolveEnvString(val)
	}
	return resolved
}

func resolveEnvString(s string) string {
	result := s
	for {
		start := strings.Index(result, "${")
		if start == -1 {
			break
		}
		end := strings.Index(result[start:], "}")
		if end == -1 {
			break
		}
		end += start

		varName := result[start+2 : end]
		varValue := os.Getenv(varName)
		result = result[:start] + varValue + result[end+1:]
	}
	return result
}
