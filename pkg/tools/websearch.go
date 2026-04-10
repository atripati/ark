package tools

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
)

const (
	braveSearchAPI   = "https://api.search.brave.com/res/v1/web/search"
	braveNewsAPI     = "https://api.search.brave.com/res/v1/news/search"
	maxSearchResults = 5
)

func RegisterWebSearch(router *Router, apiKey string) {
	ws := &webSearchTools{
		exec:   router.GetExecutor(),
		apiKey: apiKey,
	}

	router.RegisterTool(Tool{
		Name:           "web_search",
		Description:    "web search: search the web for information using Brave Search",
		Version:        "v1",
		Handler:        ws.search,
		RequiredParams: []string{"query"},
		Metadata: map[string]interface{}{
			"type":          "read",
			"auth_required": true,
			"method":        "GET",
			"domain":        "api.search.brave.com",
		},
	})

	router.RegisterTool(Tool{
		Name:           "web_search_news",
		Description:    "news search: search for recent news articles using Brave Search",
		Version:        "v1",
		Handler:        ws.searchNews,
		RequiredParams: []string{"query"},
		Metadata: map[string]interface{}{
			"type":          "read",
			"auth_required": true,
			"method":        "GET",
			"domain":        "api.search.brave.com",
		},
	})
}

func RegisterWebSearchFromEnv(router *Router) {
	RegisterWebSearch(router, os.Getenv("BRAVE_API_KEY"))
}

func WebSearchToolDefs() []ToolDef {
	return []ToolDef{
		{"web_search", "web_search",
			"web search: search the web for information, facts, current events, or any topic",
			`{"name":"web_search","description":"Search the web for information","params":["query"]}`},
		{"web_search_news", "web_search_news",
			"news search: search for recent news articles and current events",
			`{"name":"web_search_news","description":"Search for recent news","params":["query"]}`},
	}
}

type webSearchTools struct {
	exec   Executor
	apiKey string
}

func (ws *webSearchTools) headers() map[string]string {
	return map[string]string{
		"Accept":               "application/json",
		"X-Subscription-Token": ws.apiKey,
	}
}

func (ws *webSearchTools) search(params map[string]interface{}) (string, error) {
	query, _ := params["query"].(string)
	if query == "" {
		return "", fmt.Errorf("web_search: 'query' param required")
	}

	count := maxSearchResults
	if c, ok := params["count"]; ok {
		if ci, ok := c.(float64); ok && ci > 0 && ci <= 20 {
			count = int(ci)
		}
	}

	searchURL := fmt.Sprintf("%s?q=%s&count=%d", braveSearchAPI, url.QueryEscape(query), count)

	result, err := ws.exec.Execute(HTTPToolConfig{
		Method:  "GET",
		URL:     searchURL,
		Headers: ws.headers(),
	}, nil)
	if err != nil {
		return "", fmt.Errorf("web_search: %w", err)
	}

	return simplifyWebResults(result)
}

func (ws *webSearchTools) searchNews(params map[string]interface{}) (string, error) {
	query, _ := params["query"].(string)
	if query == "" {
		return "", fmt.Errorf("web_search_news: 'query' param required")
	}

	count := maxSearchResults
	if c, ok := params["count"]; ok {
		if ci, ok := c.(float64); ok && ci > 0 && ci <= 20 {
			count = int(ci)
		}
	}

	searchURL := fmt.Sprintf("%s?q=%s&count=%d", braveNewsAPI, url.QueryEscape(query), count)

	result, err := ws.exec.Execute(HTTPToolConfig{
		Method:  "GET",
		URL:     searchURL,
		Headers: ws.headers(),
	}, nil)
	if err != nil {
		return "", fmt.Errorf("web_search_news: %w", err)
	}

	return simplifyNewsResults(result)
}

func simplifyWebResults(raw string) (string, error) {
	var resp struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				Age         string `json:"age"`
			} `json:"results"`
		} `json:"web"`
		Query struct {
			Original string `json:"original"`
		} `json:"query"`
	}

	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		if len(raw) > maxOutputLen {
			raw = raw[:maxOutputLen]
		}
		return raw, nil
	}

	if len(resp.Web.Results) == 0 {
		return fmt.Sprintf("No results found for: %s", resp.Query.Original), nil
	}

	type simplified struct {
		Title       string `json:"title"`
		URL         string `json:"url"`
		Description string `json:"description"`
		Age         string `json:"age,omitempty"`
	}

	results := make([]simplified, 0, len(resp.Web.Results))
	for _, r := range resp.Web.Results {
		desc := r.Description
		desc = stripHTML(desc)
		if len(desc) > 300 {
			desc = desc[:300] + "..."
		}
		results = append(results, simplified{
			Title:       r.Title,
			URL:         r.URL,
			Description: desc,
			Age:         r.Age,
		})
	}

	out, err := json.Marshal(results)
	if err != nil {
		return raw, nil
	}
	return string(out), nil
}
func simplifyNewsResults(raw string) (string, error) {
	var resp struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			Age         string `json:"age"`
			Source      struct {
				Name string `json:"name"`
			} `json:"meta_url"`
		} `json:"results"`
		Query struct {
			Original string `json:"original"`
		} `json:"query"`
	}

	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		if len(raw) > maxOutputLen {
			raw = raw[:maxOutputLen]
		}
		return raw, nil
	}

	if len(resp.Results) == 0 {
		return fmt.Sprintf("No news results found for: %s", resp.Query.Original), nil
	}

	type simplified struct {
		Title       string `json:"title"`
		URL         string `json:"url"`
		Description string `json:"description"`
		Age         string `json:"age,omitempty"`
		Source      string `json:"source,omitempty"`
	}

	results := make([]simplified, 0, len(resp.Results))
	for _, r := range resp.Results {
		desc := stripHTML(r.Description)
		if len(desc) > 300 {
			desc = desc[:300] + "..."
		}
		results = append(results, simplified{
			Title:       r.Title,
			URL:         r.URL,
			Description: desc,
			Age:         r.Age,
			Source:      r.Source.Name,
		})
	}

	out, err := json.Marshal(results)
	if err != nil {
		return raw, nil
	}
	return string(out), nil
}

func stripHTML(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return b.String()
}
