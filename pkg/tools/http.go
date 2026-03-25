package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/atripati/ark/pkg/runtime"
)

type Executor interface {
	Execute(config HTTPToolConfig, params map[string]interface{}) (string, error)
}

type Tool struct {
	Name        string
	Description string
	Version     string
	Handler     ToolFunc
	Metadata    map[string]interface{}
}
type ToolFunc func(params map[string]interface{}) (string, error)

type Router struct {
	tools      map[string]*Tool
	executor   Executor
	AllowWrite bool
	DryRun     bool
}

var DefaultAllowedDomains = []string{
	"api.github.com",
}

func NewRouter() *Router {
	return &Router{
		tools:    make(map[string]*Tool),
		executor: NewHTTPExecutor(DefaultAllowedDomains),
	}
}
func NewRouterWithExecutor(exec Executor) *Router {
	return &Router{
		tools:    make(map[string]*Tool),
		executor: exec,
	}
}
func (r *Router) Register(name string, fn ToolFunc) {
	r.tools[name] = &Tool{
		Name:    name,
		Version: "v1",
		Handler: fn,
	}
}
func (r *Router) RegisterTool(tool Tool) {
	if tool.Version == "" {
		tool.Version = "v1"
	}
	r.tools[tool.Name] = &tool
}
func (r *Router) Handle(call *runtime.ToolCall) error {
	t, ok := r.tools[call.Name]
	if !ok {
		for pattern, tool := range r.tools {
			if strings.HasSuffix(pattern, "_*") {
				prefix := strings.TrimSuffix(pattern, "_*")
				if strings.HasPrefix(call.Name, prefix) {
					t = tool
					ok = true
					break
				}
			}
		}
	}

	if !ok {
		call.Error = fmt.Errorf("ark/tools: no handler for %q", call.Name)
		return call.Error
	}
	toolType, _ := t.Metadata["type"].(string)
	if r.DryRun {
		label := "[DRY RUN]"
		if toolType == "write" && !r.AllowWrite {
			label = "[DRY RUN — WRITE BLOCKED]"
		}
		call.Result = fmt.Sprintf("%s Would execute %s with params: %v", label, call.Name, call.Params)
		return nil
	}
	if toolType == "write" && !r.AllowWrite {
		call.Error = fmt.Errorf("ark: write operation %q blocked (use --allow-write to enable)", call.Name)
		return call.Error
	}
	result, err := t.Handler(call.Params)
	if err != nil {
		call.Error = err
		return err
	}

	call.Result = sanitizeOutput(result)
	return nil
}
func (r *Router) RegisterHTTP(name string, config HTTPToolConfig) {
	exec := r.executor
	r.Register(name, func(params map[string]interface{}) (string, error) {
		return exec.Execute(config, params)
	})
}
func (r *Router) ToolCount() int {
	return len(r.tools)
}
func (r *Router) GetTool(name string) *Tool {
	return r.tools[name]
}
func (r *Router) GetExecutor() Executor {
	return r.executor
}

const (
	maxRetries     = 3
	initialBackoff = 150 * time.Millisecond
	backoffFactor  = 2.5
	maxJitter      = 100
	maxOutputLen   = 4000
)

type HTTPExecutor struct {
	Client         *http.Client
	AllowedDomains []string
	Debug          bool
}

type HTTPToolConfig struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    string
	RawBody []byte
	Timeout time.Duration
}

func NewHTTPExecutor(allowedDomains []string) *HTTPExecutor {
	if allowedDomains == nil {
		allowedDomains = DefaultAllowedDomains
	}
	return &HTTPExecutor{
		Client:         &http.Client{Timeout: 30 * time.Second},
		AllowedDomains: allowedDomains,
	}
}
func (h *HTTPExecutor) Execute(config HTTPToolConfig, params map[string]interface{}) (string, error) {
	reqURL := config.URL
	for key, val := range params {
		reqURL = strings.ReplaceAll(reqURL, "{"+key+"}", fmt.Sprintf("%v", val))
	}
	if err := h.checkDomain(reqURL); err != nil {
		return "", err
	}
	var bodyBytes []byte
	if len(config.RawBody) > 0 {
		bodyBytes = config.RawBody
	} else if config.Method != "GET" && config.Method != "DELETE" {
		if config.Body == "json" {
			var err error
			bodyBytes, err = json.Marshal(params)
			if err != nil {
				return "", fmt.Errorf("ark/tools/http: marshal body: %w", err)
			}
		} else if config.Body != "" {
			body := config.Body
			for key, val := range params {
				body = strings.ReplaceAll(body, "{"+key+"}", fmt.Sprintf("%v", val))
			}
			bodyBytes = []byte(body)
		}
	}
	headers := make(map[string]string)
	for k, v := range config.Headers {
		for pk, pv := range params {
			v = strings.ReplaceAll(v, "{"+pk+"}", fmt.Sprintf("%v", pv))
		}
		headers[k] = v
	}
	client := h.Client
	if config.Timeout > 0 {
		client = &http.Client{
			Timeout:   config.Timeout,
			Transport: h.Client.Transport,
		}
	}
	backoff := initialBackoff
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(backoff + jitter())
			backoff = time.Duration(float64(backoff) * backoffFactor)
		}

		if h.Debug {
			fmt.Printf("[ark/http] %s %s (attempt %d/%d)\n",
				config.Method, reqURL, attempt+1, maxRetries+1)
		}

		var bodyReader io.Reader
		if bodyBytes != nil {
			bodyReader = bytes.NewReader(bodyBytes)
		}

		req, err := http.NewRequest(config.Method, reqURL, bodyReader)
		if err != nil {
			return "", fmt.Errorf("ark/tools/http: build request: %w", err)
		}

		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if req.Header.Get("Content-Type") == "" && bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := client.Do(req)

		if err != nil {
			lastErr = fmt.Errorf("ark/tools/http: %s %s: %w", config.Method, reqURL, err)
			if h.Debug {
				fmt.Printf("[ark/http] network error: %v\n", err)
			}
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()

		if readErr != nil {
			lastErr = fmt.Errorf("ark/tools/http: read response: %w", readErr)
			continue
		}

		if h.Debug {
			fmt.Printf("[ark/http] status=%d body=%d bytes\n", resp.StatusCode, len(respBody))
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return string(respBody), nil
		}
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("ark/tools/http: %s %s returned %d: %s",
				config.Method, reqURL, resp.StatusCode, truncate(string(respBody), 200))
			if ra := parseRetryAfter(resp.Header.Get("Retry-After")); ra > 0 {
				backoff = ra
				if h.Debug {
					fmt.Printf("[ark/http] Retry-After: %v\n", ra)
				}
			}
			continue
		}
		return "", fmt.Errorf("ark/tools/http: %s %s returned %d: %s",
			config.Method, reqURL, resp.StatusCode, truncate(string(respBody), 200))
	}

	return "", fmt.Errorf("%w (after %d retries)", lastErr, maxRetries)
}
func (h *HTTPExecutor) checkDomain(rawURL string) error {
	if len(h.AllowedDomains) == 0 {
		return nil
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("ark/tools/http: invalid URL %q: %w", rawURL, err)
	}

	host := parsed.Hostname()
	for _, allowed := range h.AllowedDomains {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return nil
		}
	}

	return fmt.Errorf("ark/tools/http: domain %q not in allowlist %v", host, h.AllowedDomains)
}
func sanitizeOutput(s string) string {
	if len(s) <= maxOutputLen {
		return s
	}
	return s[:maxOutputLen] + "\n... [truncated by ARK: output exceeded " +
		fmt.Sprintf("%d", maxOutputLen) + " chars]"
}
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
func jitter() time.Duration {
	return time.Duration(rand.Intn(maxJitter)) * time.Millisecond
}
func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}
	seconds, err := strconv.Atoi(header)
	if err != nil || seconds <= 0 {
		return 0
	}
	if seconds > 30 {
		seconds = 30
	}
	return time.Duration(seconds) * time.Second
}
