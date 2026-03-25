package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/atripati/ark/pkg/runtime"
)

const (
	maxRetries      = 3
	initialBackoff  = 100 * time.Millisecond
	backoffMultiply = 3.0
)

func retryable(statusCode int, err error) bool {
	if err != nil {
		return true
	}
	if statusCode == 429 {
		return true
	}
	if statusCode >= 500 {
		return true
	}
	return false
}

func New(provider, model, apiKey, baseURL string, maxTokens int) (runtime.Executor, error) {
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	switch provider {
	case "anthropic":
		return newAnthropic(model, apiKey, maxTokens)
	case "openai":
		return newOpenAI(model, apiKey, baseURL, maxTokens)
	case "ollama":
		return newOllama(model, baseURL)
	default:
		return nil, fmt.Errorf("ark/models: unknown provider %q (valid: anthropic, openai, ollama)", provider)
	}
}

func newAnthropic(model, apiKey string, maxTokens int) (*AnthropicProvider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("ark/models/anthropic: API key required (set ANTHROPIC_API_KEY)")
	}
	return &AnthropicProvider{
		Model:     model,
		APIKey:    apiKey,
		MaxTokens: maxTokens,
		Client:    &http.Client{Timeout: 120 * time.Second},
	}, nil
}

func newOpenAI(model, apiKey, baseURL string, maxTokens int) (*OpenAIProvider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("ark/models/openai: API key required (set OPENAI_API_KEY)")
	}
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAIProvider{
		Model:     model,
		APIKey:    apiKey,
		MaxTokens: maxTokens,
		BaseURL:   baseURL,
		Client:    &http.Client{Timeout: 120 * time.Second},
	}, nil
}

func newOllama(model, baseURL string) (*OllamaProvider, error) {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = "llama3"
	}
	return &OllamaProvider{
		Model:   model,
		BaseURL: baseURL,
		Client:  &http.Client{Timeout: 300 * time.Second},
	}, nil
}
func doWithRetry(client *http.Client, req *http.Request, body []byte, provider string) ([]byte, int, error) {
	backoff := initialBackoff

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if body != nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
		}

		resp, err := client.Do(req)
		if err != nil {
			if attempt == maxRetries {
				return nil, 0, fmt.Errorf("ark/%s: network error after %d retries: %w",
					provider, maxRetries, err)
			}
			time.Sleep(backoff)
			backoff = time.Duration(float64(backoff) * backoffMultiply)
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()

		if readErr != nil {
			if attempt == maxRetries {
				return nil, resp.StatusCode, fmt.Errorf("ark/%s: read error (status %d): %w",
					provider, resp.StatusCode, readErr)
			}
			time.Sleep(backoff)
			backoff = time.Duration(float64(backoff) * backoffMultiply)
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return respBody, resp.StatusCode, nil
		}
		if retryable(resp.StatusCode, nil) {
			if attempt == maxRetries {
				return nil, resp.StatusCode, fmt.Errorf("ark/%s: HTTP %d after %d retries: %s",
					provider, resp.StatusCode, maxRetries, truncate(string(respBody), 200))
			}
			time.Sleep(backoff)
			backoff = time.Duration(float64(backoff) * backoffMultiply)
			continue
		}
		return respBody, resp.StatusCode, nil
	}
	return nil, 0, fmt.Errorf("ark/%s: exhausted retries", provider)
}

type AnthropicProvider struct {
	Model     string
	APIKey    string
	MaxTokens int
	Client    *http.Client
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (a *AnthropicProvider) Execute(context string, task string) (*runtime.ModelResponse, error) {
	start := time.Now()

	reqBody := anthropicRequest{
		Model:     a.Model,
		MaxTokens: a.MaxTokens,
		System:    context,
		Messages: []anthropicMessage{
			{Role: "user", Content: task},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("ark/anthropic: marshal: %w", err)
	}

	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ark/anthropic: request build: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	respBody, statusCode, err := doWithRetry(a.Client, req, body, "anthropic")
	if err != nil {
		return nil, err
	}
	if len(respBody) == 0 {
		return nil, fmt.Errorf("ark/anthropic: empty response (status %d)", statusCode)
	}

	var apiResp anthropicResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("ark/anthropic: invalid JSON (status %d): %s",
			statusCode, truncate(string(respBody), 200))
	}
	if apiResp.Error != nil {
		return nil, fmt.Errorf("ark/anthropic: %s: %s",
			apiResp.Error.Type, apiResp.Error.Message)
	}

	text := ""
	for _, c := range apiResp.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}

	return &runtime.ModelResponse{
		Text:       text,
		TokensUsed: apiResp.Usage.InputTokens + apiResp.Usage.OutputTokens,
		Latency:    time.Since(start),
	}, nil
}

type OpenAIProvider struct {
	Model     string
	APIKey    string
	MaxTokens int
	BaseURL   string
	Client    *http.Client
}

type openaiRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Messages  []openaiMessage `json:"messages"`
}

type openaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openaiResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func (o *OpenAIProvider) Execute(context string, task string) (*runtime.ModelResponse, error) {
	start := time.Now()

	reqBody := openaiRequest{
		Model:     o.Model,
		MaxTokens: o.MaxTokens,
		Messages: []openaiMessage{
			{Role: "system", Content: context},
			{Role: "user", Content: task},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("ark/openai: marshal: %w", err)
	}

	req, err := http.NewRequest("POST", o.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ark/openai: request build: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.APIKey)

	respBody, statusCode, err := doWithRetry(o.Client, req, body, "openai")
	if err != nil {
		return nil, err
	}

	if len(respBody) == 0 {
		return nil, fmt.Errorf("ark/openai: empty response (status %d)", statusCode)
	}

	var apiResp openaiResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("ark/openai: invalid JSON (status %d): %s",
			statusCode, truncate(string(respBody), 200))
	}

	if apiResp.Error != nil {
		return nil, fmt.Errorf("ark/openai: %s: %s",
			apiResp.Error.Type, apiResp.Error.Message)
	}

	text := ""
	if len(apiResp.Choices) > 0 {
		text = apiResp.Choices[0].Message.Content
	}

	return &runtime.ModelResponse{
		Text:       text,
		TokensUsed: apiResp.Usage.TotalTokens,
		Latency:    time.Since(start),
	}, nil
}

type OllamaProvider struct {
	Model   string
	BaseURL string
	Client  *http.Client
}

type ollamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	System string `json:"system,omitempty"`
	Stream bool   `json:"stream"`
}

type ollamaResponse struct {
	Response        string `json:"response"`
	Done            bool   `json:"done"`
	Error           string `json:"error,omitempty"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
}

func (o *OllamaProvider) Execute(context string, task string) (*runtime.ModelResponse, error) {
	start := time.Now()

	reqBody := ollamaRequest{
		Model:  o.Model,
		System: context,
		Prompt: task,
		Stream: false,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("ark/ollama: marshal: %w", err)
	}

	req, err := http.NewRequest("POST", o.BaseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ark/ollama: request build: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	respBody, statusCode, err := doWithRetry(o.Client, req, body, "ollama")
	if err != nil {
		return nil, fmt.Errorf("%w (is Ollama running at %s?)", err, o.BaseURL)
	}

	if len(respBody) == 0 {
		return nil, fmt.Errorf("ark/ollama: empty response (status %d)", statusCode)
	}

	var apiResp ollamaResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("ark/ollama: invalid JSON (status %d): %s",
			statusCode, truncate(string(respBody), 200))
	}

	if apiResp.Error != "" {
		return nil, fmt.Errorf("ark/ollama: %s", apiResp.Error)
	}

	return &runtime.ModelResponse{
		Text:       apiResp.Response,
		TokensUsed: apiResp.PromptEvalCount + apiResp.EvalCount,
		Latency:    time.Since(start),
	}, nil
}
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
