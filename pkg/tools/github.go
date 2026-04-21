package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const (
	githubAPI    = "https://api.github.com"
	maxListItems = 10
)

func RegisterGitHub(router *Router, token string) {
	gh := &githubTools{
		exec:  router.GetExecutor(),
		token: token,
	}

	router.RegisterTool(Tool{
		Name:           "github_list_repos",
		Description:    "list repos: list GitHub repositories for a user or organization",
		Version:        "v1",
		Handler:        gh.listRepos,
		RequiredParams: []string{},
		Metadata:       map[string]interface{}{"type": "read", "auth_required": false, "method": "GET", "domain": "api.github.com"},
	})
	router.RegisterTool(Tool{
		Name:           "github_get_repo",
		Description:    "get repo: get details of a specific GitHub repository",
		Version:        "v1",
		Handler:        gh.getRepo,
		RequiredParams: []string{"owner", "repo"},
		Metadata:       map[string]interface{}{"type": "read", "auth_required": false, "method": "GET", "domain": "api.github.com"},
	})
	router.RegisterTool(Tool{
		Name:           "github_list_issues",
		Description:    "list issues: list issues in a GitHub repository",
		Version:        "v1",
		Handler:        gh.listIssues,
		RequiredParams: []string{"owner", "repo"},
		Metadata:       map[string]interface{}{"type": "read", "auth_required": false, "method": "GET", "domain": "api.github.com"},
	})
	router.RegisterTool(Tool{
		Name:           "github_create_issue",
		Description:    "create issue: create a new issue in a GitHub repository",
		Version:        "v1",
		Handler:        gh.createIssue,
		RequiredParams: []string{"owner", "repo", "title"},
		Metadata:       map[string]interface{}{"type": "write", "auth_required": true, "method": "POST", "domain": "api.github.com"},
	})
	router.RegisterTool(Tool{
		Name:           "github_list_pulls",
		Description:    "list pulls: list pull requests in a GitHub repository",
		Version:        "v1",
		Handler:        gh.listPulls,
		RequiredParams: []string{"owner", "repo"},
		Metadata:       map[string]interface{}{"type": "read", "auth_required": false, "method": "GET", "domain": "api.github.com"},
	})
	router.RegisterTool(Tool{
		Name:           "github_get_user",
		Description:    "get user: get GitHub user information",
		Version:        "v1",
		Handler:        gh.getUser,
		RequiredParams: []string{},
		Metadata:       map[string]interface{}{"type": "read", "auth_required": true, "method": "GET", "domain": "api.github.com"},
	})
	router.RegisterTool(Tool{
		Name:           "github_search_repos",
		Description:    "search repos: search GitHub repositories by topic, language, or keyword (best for finding top/popular/best repos)",
		Version:        "v1",
		Handler:        gh.searchRepos,
		RequiredParams: []string{"query"},
		Metadata:       map[string]interface{}{"type": "read", "auth_required": false, "method": "GET", "domain": "api.github.com"},
	})
}
func RegisterGitHubFromEnv(router *Router) {
	RegisterGitHub(router, os.Getenv("GITHUB_TOKEN"))
}

type ToolDef struct {
	ID, Name, Desc, Schema string
}

func GitHubToolDefs() []ToolDef {
	return []ToolDef{
		{"github_list_repos", "github_list_repos",
			"list repos: list GitHub repositories for a user or organization",
			`{"name":"github_list_repos","description":"List GitHub repositories for a user","params":["user"]}`},
		{"github_get_repo", "github_get_repo",
			"get repo: get details of a specific GitHub repository",
			`{"name":"github_get_repo","description":"Get repository details","params":["owner","repo"]}`},
		{"github_list_issues", "github_list_issues",
			"list issues: list issues in a GitHub repository",
			`{"name":"github_list_issues","description":"List issues in a repository","params":["owner","repo"]}`},
		{"github_create_issue", "github_create_issue",
			"create issue: create a new issue in a GitHub repository",
			`{"name":"github_create_issue","description":"Create a new issue","params":["owner","repo","title"]}`},
		{"github_list_pulls", "github_list_pulls",
			"list pulls: list pull requests in a GitHub repository",
			`{"name":"github_list_pulls","description":"List pull requests","params":["owner","repo"]}`},
		{"github_get_user", "github_get_user",
			"get user: get GitHub user information",
			`{"name":"github_get_user","description":"Get authenticated user info","params":[]}`},
		{"github_search_repos", "github_search_repos",
			"search repos: search GitHub repositories by topic, language, or keyword (best for finding top/popular/best repos)",
			`{"name":"github_search_repos","description":"Search GitHub repositories by keyword, topic, or language. Use for finding top, popular, or best repos.","params":["query"]}`},
	}
}

type githubTools struct {
	exec  Executor
	token string
}

func (g *githubTools) headers() map[string]string {
	h := map[string]string{
		"Accept":     "application/vnd.github.v3+json",
		"User-Agent": "ARK-AI-Runtime",
	}
	if g.token != "" {
		h["Authorization"] = "Bearer " + g.token
	}
	return h
}

func (g *githubTools) listRepos(params map[string]interface{}) (string, error) {
	user, _ := params["user"].(string)

	if user == "" && g.token == "" {
		return "", fmt.Errorf("github_list_repos: 'user' param required when no GITHUB_TOKEN is set (e.g. {\"user\": \"openai\"})")
	}

	url := githubAPI + "/user/repos?per_page=30&sort=stars&direction=desc"
	if user != "" {
		// Use search API for user/org repos — returns sorted by stars correctly
		url = fmt.Sprintf("%s/search/repositories?q=user:%s&sort=stars&order=desc&per_page=30", githubAPI, user)
	}

	if perPage, ok := params["per_page"]; ok {
		url = strings.Replace(url, "per_page=30", fmt.Sprintf("per_page=%v", perPage), 1)
	}
	if sort, ok := params["sort"]; ok {
		url = strings.Replace(url, "sort=stars", fmt.Sprintf("sort=%v", sort), 1)
	}

	result, err := g.exec.Execute(HTTPToolConfig{
		Method:  "GET",
		URL:     url,
		Headers: g.headers(),
	}, nil)
	if err != nil {
		return "", fmt.Errorf("github_list_repos: %w", err)
	}

	return simplifyRepoList(result)
}

func (g *githubTools) getRepo(params map[string]interface{}) (string, error) {
	owner, _ := params["owner"].(string)
	repo, _ := params["repo"].(string)
	if owner == "" || repo == "" {
		return "", fmt.Errorf("github_get_repo: 'owner' and 'repo' params required")
	}

	result, err := g.exec.Execute(HTTPToolConfig{
		Method:  "GET",
		URL:     fmt.Sprintf("%s/repos/%s/%s", githubAPI, owner, repo),
		Headers: g.headers(),
	}, nil)
	if err != nil {
		return "", fmt.Errorf("github_get_repo: %w", err)
	}

	return simplifyRepo(result)
}

func (g *githubTools) listIssues(params map[string]interface{}) (string, error) {
	owner, _ := params["owner"].(string)
	repo, _ := params["repo"].(string)
	if owner == "" || repo == "" {
		return "", fmt.Errorf("github_list_issues: 'owner' and 'repo' params required")
	}

	state := "open"
	if s, ok := params["state"].(string); ok {
		state = s
	}

	result, err := g.exec.Execute(HTTPToolConfig{
		Method:  "GET",
		URL:     fmt.Sprintf("%s/repos/%s/%s/issues?state=%s&per_page=10", githubAPI, owner, repo, state),
		Headers: g.headers(),
	}, nil)
	if err != nil {
		return "", fmt.Errorf("github_list_issues: %w", err)
	}

	return simplifyIssueList(result)
}
func (g *githubTools) createIssue(params map[string]interface{}) (string, error) {
	owner, _ := params["owner"].(string)
	repo, _ := params["repo"].(string)
	title, _ := params["title"].(string)
	if owner == "" || repo == "" || title == "" {
		return "", fmt.Errorf("github_create_issue: 'owner', 'repo', and 'title' params required")
	}

	if g.token == "" {
		return "", fmt.Errorf("github_create_issue: GITHUB_TOKEN required for write operations")
	}

	body := map[string]interface{}{
		"title": title,
	}
	if b, ok := params["body"].(string); ok {
		body["body"] = b
	}
	if labels, ok := params["labels"]; ok {
		body["labels"] = labels
	}

	bodyBytes, _ := json.Marshal(body)

	result, err := g.exec.Execute(HTTPToolConfig{
		Method:  "POST",
		URL:     fmt.Sprintf("%s/repos/%s/%s/issues", githubAPI, owner, repo),
		Headers: g.headers(),
		RawBody: bodyBytes,
	}, nil)
	if err != nil {
		return "", fmt.Errorf("github_create_issue: %w", err)
	}

	return simplifyIssue(result)
}

func (g *githubTools) listPulls(params map[string]interface{}) (string, error) {
	owner, _ := params["owner"].(string)
	repo, _ := params["repo"].(string)
	if owner == "" || repo == "" {
		return "", fmt.Errorf("github_list_pulls: 'owner' and 'repo' params required")
	}

	state := "open"
	if s, ok := params["state"].(string); ok {
		state = s
	}

	result, err := g.exec.Execute(HTTPToolConfig{
		Method:  "GET",
		URL:     fmt.Sprintf("%s/repos/%s/%s/pulls?state=%s&per_page=10", githubAPI, owner, repo, state),
		Headers: g.headers(),
	}, nil)
	if err != nil {
		return "", fmt.Errorf("github_list_pulls: %w", err)
	}

	return simplifyPRList(result)
}

func (g *githubTools) getUser(params map[string]interface{}) (string, error) {
	if g.token == "" {
		return "", fmt.Errorf("github_get_user: GITHUB_TOKEN required")
	}

	result, err := g.exec.Execute(HTTPToolConfig{
		Method:  "GET",
		URL:     githubAPI + "/user",
		Headers: g.headers(),
	}, nil)
	if err != nil {
		return "", fmt.Errorf("github_get_user: %w", err)
	}

	return simplifyUser(result)
}

func (g *githubTools) searchRepos(params map[string]interface{}) (string, error) {
	query, _ := params["query"].(string)
	if query == "" {
		return "", fmt.Errorf("github_search_repos: 'query' param required (e.g. {\"query\": \"python web framework\"})")
	}

	// this if PHASE 1: Query Intelligence
	// Extract language + strip noise + add ecosystem anchors

	lang, _ := params["language"].(string)
	if lang == "" {
		lang = detectLanguage(query)
	}

	// this is to Strip noise words — keep only domain keywords
	cleanQuery := stripNoiseWords(query)

	// this is the Ecosystem that  hints to disambiguate (javascript→nodejs, go→golang)
	ecosystemHints := map[string]string{
		"javascript": "nodejs",
		"typescript": "nodejs",
		"go":         "golang",
		"c#":         "dotnet",
		"csharp":     "dotnet",
	}

	// this is PHASE 2: basically this is Retrieval whihch means it:
	// Keep language in the query for signal strength.
	// The noise word stripper already removed "backend", "frontend", "server"
	// which were causing strict AND failures on GitHub.

	var allRepos []map[string]interface{}

	searchQ := strings.ReplaceAll(cleanQuery, " ", "+")
	if hint, ok := ecosystemHints[strings.ToLower(lang)]; ok {
		searchQ += "+" + hint
	}

	// This is For languages with ecosystem overlap (JS includes TS, and vice versa),
	// This don't add language: filter to the API — it would exclude valid repos.
	// My local filterByLanguage handles the cross-language matching.
	langOverlap := map[string]bool{
		"javascript": true,
		"typescript": true,
	}
	if lang != "" && !langOverlap[strings.ToLower(lang)] {
		searchQ += "+language:" + strings.ToLower(lang)
	}

	url := fmt.Sprintf("%s/search/repositories?q=%s&sort=stars&order=desc&per_page=30",
		githubAPI, searchQ)

	result, err := g.exec.Execute(HTTPToolConfig{
		Method:  "GET",
		URL:     url,
		Headers: g.headers(),
	}, nil)
	if err != nil {
		return "", fmt.Errorf("github_search_repos: %w", err)
	}

	allRepos = parseSearchResults(result)

	// this is my PHASE 3: whihc is Hard language filter
	if lang != "" {
		allRepos = filterByLanguage(allRepos, lang)
	}

	// this is PHASE 4 for Junk filtering
	allRepos = filterJunkRepos(allRepos)

	// this is PHASE 4.5 for  Relevance scoring
	// Three-tier scoring:
	//  Positive match (web framework signal) → 2.0x boost
	//  No match (unknown relevance)          → 0.3x penalty
	//  Anti-match (clearly wrong category)   → 0.01x buried
	// This ensures only repos with framework signals rank high.

	webFrameworkSignals := []string{
		"web framework", "http framework", "api framework",
		"rest framework", "rest api", "restful",
		"web server", "http server",
		"web application framework", "server framework",
		"micro framework", "microframework",
		"web development framework",
		"web application", "http routing",
		"server-side applications", "server-side",
		"node.js framework", "nodejs framework",
		"progressive node", "minimalist web",
	}

	antiSignals := []string{
		// Data/ORM
		"orm", "object-relational", "datastore", "database",
		// Cloud/serverless
		"lambda", "aws lambda", "serverless middleware",
		// Frontend/CSS
		"css framework", "css-in-js", "lightweight css",
		// Debugging/profiling
		"memory leak", "heap snapshot", "debugging", "profiling",
		// Build tools
		"compiler", "bundler", "build tool",
		// Infrastructure
		"queue", "cache", "message broker",
		// Testing
		"testing framework", "test framework", "test runner",
		"end-to-end test", "unit test", "e2e test",
		// Bots/chat
		"bot framework", "chatbot", "bot sdk", "conversational",
		// Desktop/mobile
		"desktop app", "mobile app", "app platform",
		// Static/frontend
		"static site", "jamstack", "site generator",
		// Learning resources
		"cheatsheet", "curated list", "tutorial",
		"interview question", "roadmap",
		// CLI/workflow tools
		"alfred workflow", "command line", "cli tool", "command-line",
		// Scraping/crawling (not web serving)
		"scraping", "crawling", "crawler", "scraper", "spider",
		// Error handling libraries
		"error handling", "error library",
		// Utility libraries
		"utility", "helper library", "collection of functions",
		// Blog/CMS frameworks (not backend web frameworks)
		"blog framework", "blog platform", "blogging",
		// AI/ML frameworks (not web frameworks)
		"ai-powered", "ai framework", "machine learning framework",
		"llm framework", "ai agent",
		// Realtime-only libraries (not full web frameworks)
		"realtime application framework", "realtime app framework",
	}
	//still i need to work on this to make it more then perfect

	type scoredRepo struct {
		repo  map[string]interface{}
		score float64
	}

	scored := make([]scoredRepo, 0, len(allRepos))
	for _, r := range allRepos {
		desc := strings.ToLower(fmt.Sprintf("%v", r["description"]))
		name := strings.ToLower(fmt.Sprintf("%v", r["name"]))
		stars := toFloat(r["stargazers_count"])

		// this if to check anti-signals first (highest priority)
		isAnti := false
		for _, sig := range antiSignals {
			if strings.Contains(desc, sig) || strings.Contains(name, sig) {
				isAnti = true
				break
			}
		}

		// this is to check positive signals
		isPositive := false
		for _, sig := range webFrameworkSignals {
			if strings.Contains(desc, sig) || strings.Contains(name, sig) {
				isPositive = true
				break
			}
		}

		// this is to kip awesome-lists entirely
		if strings.HasPrefix(name, "awesome") || strings.Contains(name, "awesome-") {
			continue
		}

		// This is three-tier relevance scoring
		var relevance float64
		if isAnti {
			relevance = 0.01
		} else if isPositive {
			relevance = 2.0
		} else {
			relevance = 0.3
		}

		scored = append(scored, scoredRepo{repo: r, score: stars * relevance})
	}

	for i := 1; i < len(scored); i++ {
		for j := i; j > 0; j-- {
			if scored[j].score > scored[j-1].score {
				scored[j], scored[j-1] = scored[j-1], scored[j]
			}
		}
	}

	allRepos = make([]map[string]interface{}, 0, len(scored))
	for _, s := range scored {
		allRepos = append(allRepos, s.repo)
	}
	// here was phase 5 whihc i deleted as i have to work on it

	// this is PHASE 6: Diversity guard
	// Max 2 repos per owner to prevent clustering
	ownerSeen := make(map[string]int)
	diverse := make([]map[string]interface{}, 0)
	for _, r := range allRepos {
		owner := ""
		if fn, ok := r["full_name"].(string); ok {
			parts := strings.SplitN(fn, "/", 2)
			if len(parts) > 0 {
				owner = strings.ToLower(parts[0])
			}
		}
		ownerSeen[owner]++
		if ownerSeen[owner] <= 2 {
			diverse = append(diverse, r)
		}
	}

	// this is PHASE 7: Simplify output
	if len(diverse) > maxListItems {
		diverse = diverse[:maxListItems]
	}

	simple := make([]map[string]interface{}, 0, len(diverse))
	for _, r := range diverse {
		simple = append(simple, stripNulls(map[string]interface{}{
			"name":        r["name"],
			"full_name":   r["full_name"],
			"description": r["description"],
			"language":    r["language"],
			"stars":       r["stargazers_count"],
			"forks":       r["forks_count"],
			"url":         r["html_url"],
		}))
	}

	out, _ := json.Marshal(simple)
	return string(out), nil
}

func parseSearchResults(raw string) []map[string]interface{} {
	var repos []map[string]interface{}

	// Try search API format first
	var searchResult map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &searchResult); err == nil {
		if items, ok := searchResult["items"].([]interface{}); ok {
			for _, item := range items {
				if m, ok := item.(map[string]interface{}); ok {
					repos = append(repos, m)
				}
			}
		}
	}

	// Fallback: direct array
	if len(repos) == 0 {
		json.Unmarshal([]byte(raw), &repos)
	}

	return repos
}

func filterByLanguage(repos []map[string]interface{}, lang string) []map[string]interface{} {
	langLower := strings.ToLower(lang)

	// Build set of accepted languages
	accepted := map[string]bool{langLower: true}
	// JavaScript ecosystem includes TypeScript
	if langLower == "javascript" {
		accepted["typescript"] = true
	}
	if langLower == "typescript" {
		accepted["javascript"] = true
	}

	filtered := make([]map[string]interface{}, 0)
	for _, r := range repos {
		repoLang := strings.ToLower(fmt.Sprintf("%v", r["language"]))
		if accepted[repoLang] {
			filtered = append(filtered, r)
		}
	}
	// If filter removed everything, return unfiltered
	if len(filtered) == 0 {
		return repos
	}
	return filtered
}

func filterJunkRepos(repos []map[string]interface{}) []map[string]interface{} {
	filtered := make([]map[string]interface{}, 0, len(repos))
	for _, r := range repos {
		name := strings.ToLower(fmt.Sprintf("%v", r["name"]))
		desc := strings.ToLower(fmt.Sprintf("%v", r["description"]))

		// Skip awesome-lists and curated collections
		if strings.HasPrefix(name, "awesome") || strings.Contains(name, "awesome") {
			continue
		}
		if strings.Contains(desc, "cheatsheet") || strings.Contains(desc, "curated list") ||
			strings.Contains(desc, "collection of") || strings.Contains(desc, "list of") {
			continue
		}
		if strings.Contains(desc, "tutorial") || strings.Contains(desc, "interview question") ||
			strings.Contains(desc, "roadmap") || strings.Contains(name, "learn-") {
			continue
		}

		// Category mismatch: testing tools
		if strings.Contains(desc, "testing framework") || strings.Contains(desc, "test framework") ||
			strings.Contains(desc, "test runner") || strings.Contains(desc, "end-to-end test") ||
			strings.Contains(desc, "unit test") || strings.Contains(desc, "e2e test") ||
			strings.Contains(desc, "testing tool") || strings.Contains(desc, "testing library") {
			continue
		}
		// Category mismatch: CLI tools
		if strings.Contains(desc, "command line") || strings.Contains(desc, "cli tool") ||
			strings.Contains(desc, "command-line") {
			continue
		}
		// Category mismatch: CMS / admin panels
		if strings.Contains(desc, " cms ") || strings.Contains(desc, "content management") ||
			strings.Contains(desc, "admin panel") || strings.Contains(desc, "admin interface") {
			continue
		}
		// Category mismatch: boilerplate / starter
		if strings.Contains(desc, "boilerplate") || strings.Contains(desc, "starter template") ||
			strings.Contains(desc, "starter kit") || strings.Contains(name, "boilerplate") {
			continue
		}
		// Category mismatch: bots, SDKs, chatbots
		if strings.Contains(desc, "bot framework") || strings.Contains(desc, "chatbot") ||
			strings.Contains(desc, "bot sdk") || strings.Contains(desc, "conversational") {
			continue
		}
		// Category mismatch: desktop/mobile app platforms
		if strings.Contains(desc, "desktop app") || strings.Contains(desc, "mobile app") ||
			strings.Contains(desc, "app platform") {
			continue
		}
		// Category mismatch: frontend / static-site / Jamstack (not backend)
		if strings.Contains(desc, "static site") || strings.Contains(desc, "jamstack") ||
			strings.Contains(desc, "static web") || strings.Contains(desc, "front-end") ||
			strings.Contains(desc, "frontend framework") || strings.Contains(desc, "ui framework") ||
			strings.Contains(desc, "vue-powered") || strings.Contains(desc, "react-powered") ||
			strings.Contains(desc, "single page app") || strings.Contains(desc, "progressive web") ||
			strings.Contains(desc, "static generator") || strings.Contains(desc, "site generator") {
			continue
		}

		filtered = append(filtered, r)
	}
	return filtered
}

// stripNoiseWords removes common filler words from a query, keeping only
// meaningful domain keywords. GitHub search works best with focused terms.
func stripNoiseWords(query string) string {
	noise := map[string]bool{
		"find": true, "the": true, "top": true, "most": true,
		"popular": true, "best": true, "on": true, "github": true,
		"with": true, "stars": true, "list": true, "show": true,
		"me": true, "get": true, "search": true, "for": true,
		"all": true, "and": true, "their": true, "star": true,
		"counts": true, "count": true, "explain": true, "why": true,
		"each": true, "is": true, "are": true, "what": true,
		"how": true, "a": true, "an": true, "of": true,
		"in": true, "to": true, "by": true, "from": true,
		// Structural qualifiers — these cause strict AND failures on GitHub
		// because Express says "web framework" not "backend framework"
		"backend": true, "frontend": true, "server": true, "client": true,
		"side": true, "based": true, "powered": true,
	}

	words := strings.Fields(strings.ToLower(query))
	kept := make([]string, 0)
	for _, w := range words {
		w = strings.Trim(w, ".,;:!?\"'()[]{}—-")
		if len(w) < 2 {
			continue
		}
		if noise[w] {
			continue
		}
		// Skip pure numbers
		isNum := true
		for _, c := range w {
			if c < '0' || c > '9' {
				isNum = false
				break
			}
		}
		if isNum {
			continue
		}
		kept = append(kept, w)
	}

	if len(kept) == 0 {
		return strings.ToLower(query)
	}
	return strings.Join(kept, " ")
}

// simplifyRepoListWithLang filters results to match the requested language.
func simplifyRepoListWithLang(raw string, requestedLang string) (string, error) {
	if requestedLang == "" {
		return simplifyRepoList(raw)
	}

	// Parse the raw response first
	var repos []map[string]interface{}

	var searchResult map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &searchResult); err == nil {
		if items, ok := searchResult["items"].([]interface{}); ok {
			for _, item := range items {
				if m, ok := item.(map[string]interface{}); ok {
					repos = append(repos, m)
				}
			}
		}
	}
	if len(repos) == 0 {
		if err := json.Unmarshal([]byte(raw), &repos); err != nil {
			return raw, nil
		}
	}

	// Hard filter: only keep repos that match the requested language
	langLower := strings.ToLower(requestedLang)
	filtered := make([]map[string]interface{}, 0)
	for _, r := range repos {
		repoLang := strings.ToLower(fmt.Sprintf("%v", r["language"]))
		if repoLang == langLower || repoLang == "" {
			filtered = append(filtered, r)
		}
	}

	// Re-encode as array for simplifyRepoList
	data, err := json.Marshal(filtered)
	if err != nil {
		return simplifyRepoList(raw)
	}

	return simplifyRepoList(string(data))
}

// detectLanguage extracts a programming language name from a natural language query.
func detectLanguage(query string) string {
	q := strings.ToLower(query)
	languages := map[string]string{
		"python":     "python",
		"javascript": "javascript",
		"typescript": "typescript",
		"go":         "go",
		"golang":     "go",
		"rust":       "rust",
		"java":       "java",
		"ruby":       "ruby",
		"c++":        "cpp",
		"cpp":        "cpp",
		"c#":         "csharp",
		"csharp":     "csharp",
		"php":        "php",
		"swift":      "swift",
		"kotlin":     "kotlin",
		"scala":      "scala",
		"elixir":     "elixir",
	}
	for keyword, lang := range languages {
		if strings.Contains(q, keyword) {
			return lang
		}
	}
	return ""
}

func simplifyRepoList(raw string) (string, error) {
	var repos []map[string]interface{}

	// Search API returns {items: [...]} wrapper
	var searchResult map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &searchResult); err == nil {
		if items, ok := searchResult["items"].([]interface{}); ok {
			for _, item := range items {
				if m, ok := item.(map[string]interface{}); ok {
					repos = append(repos, m)
				}
			}
		}
	}

	// Fallback: direct array (user/repos endpoint)
	if len(repos) == 0 {
		if err := json.Unmarshal([]byte(raw), &repos); err != nil {
			return raw, nil
		}
	}

	// Filter junk repos — awesome-lists, collections, non-code repos
	filtered := make([]map[string]interface{}, 0, len(repos))
	for _, r := range repos {
		name := strings.ToLower(fmt.Sprintf("%v", r["name"]))
		desc := strings.ToLower(fmt.Sprintf("%v", r["description"]))
		fullName := strings.ToLower(fmt.Sprintf("%v", r["full_name"]))

		// Skip awesome-lists and curated collections
		if strings.HasPrefix(name, "awesome") || strings.Contains(desc, "curated list") ||
			strings.Contains(desc, "cheatsheet") || strings.Contains(desc, "collection of") ||
			strings.Contains(desc, "list of") || strings.Contains(name, "awesome") {
			continue
		}

		// Skip if it's clearly a learning resource, not a library/framework
		if strings.Contains(desc, "tutorial") || strings.Contains(desc, "interview question") ||
			strings.Contains(desc, "roadmap") || strings.Contains(name, "learn-") {
			continue
		}

		_ = fullName
		filtered = append(filtered, r)
	}
	repos = filtered

	// Sort by stars descending (in case API didn't sort correctly)
	for i := 1; i < len(repos); i++ {
		for j := i; j > 0; j-- {
			starsJ := toFloat(repos[j]["stargazers_count"])
			starsJm1 := toFloat(repos[j-1]["stargazers_count"])
			if starsJ > starsJm1 {
				repos[j], repos[j-1] = repos[j-1], repos[j]
			}
		}
	}

	if len(repos) > maxListItems {
		repos = repos[:maxListItems]
	}

	// Diversity guard: max 2 repos per owner to prevent clustering
	ownerSeen := make(map[string]int)
	diverse := make([]map[string]interface{}, 0, len(repos))
	for _, r := range repos {
		owner := ""
		if fn, ok := r["full_name"].(string); ok {
			parts := strings.SplitN(fn, "/", 2)
			if len(parts) > 0 {
				owner = strings.ToLower(parts[0])
			}
		}
		ownerSeen[owner]++
		if ownerSeen[owner] <= 2 {
			diverse = append(diverse, r)
		}
	}
	repos = diverse

	simple := make([]map[string]interface{}, 0, len(repos))
	for _, r := range repos {
		simple = append(simple, stripNulls(map[string]interface{}{
			"name":        r["name"],
			"full_name":   r["full_name"],
			"description": r["description"],
			"language":    r["language"],
			"stars":       r["stargazers_count"],
			"forks":       r["forks_count"],
			"updated_at":  r["updated_at"],
			"private":     r["private"],
			"url":         r["html_url"],
		}))
	}

	out, _ := json.Marshal(simple)
	return string(out), nil
}

func toFloat(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	default:
		return 0
	}
}

func simplifyRepo(raw string) (string, error) {
	var r map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return raw, nil
	}

	simple := stripNulls(map[string]interface{}{
		"name":           r["name"],
		"full_name":      r["full_name"],
		"description":    r["description"],
		"language":       r["language"],
		"stars":          r["stargazers_count"],
		"forks":          r["forks_count"],
		"open_issues":    r["open_issues_count"],
		"default_branch": r["default_branch"],
		"created_at":     r["created_at"],
		"updated_at":     r["updated_at"],
		"private":        r["private"],
		"url":            r["html_url"],
	})

	out, _ := json.Marshal(simple)
	return string(out), nil
}

func simplifyIssueList(raw string) (string, error) {
	var issues []map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &issues); err != nil {
		return raw, nil
	}

	if len(issues) > maxListItems {
		issues = issues[:maxListItems]
	}

	simple := make([]map[string]interface{}, 0, len(issues))
	for _, i := range issues {
		// Filter out pull requests — GitHub API returns PRs in issues endpoint
		if _, isPR := i["pull_request"]; isPR {
			continue
		}

		entry := map[string]interface{}{
			"number":     i["number"],
			"title":      i["title"],
			"state":      i["state"],
			"created_at": i["created_at"],
			"url":        i["html_url"],
		}
		if user, ok := i["user"].(map[string]interface{}); ok {
			entry["author"] = user["login"]
		}
		if labels, ok := i["labels"].([]interface{}); ok {
			names := make([]string, 0)
			for _, l := range labels {
				if lm, ok := l.(map[string]interface{}); ok {
					if name, ok := lm["name"].(string); ok {
						names = append(names, name)
					}
				}
			}
			if len(names) > 0 {
				entry["labels"] = names
			}
		}
		simple = append(simple, stripNulls(entry))
	}

	out, _ := json.Marshal(simple)
	return string(out), nil
}

func simplifyIssue(raw string) (string, error) {
	var i map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &i); err != nil {
		return raw, nil
	}

	simple := stripNulls(map[string]interface{}{
		"number":     i["number"],
		"title":      i["title"],
		"state":      i["state"],
		"url":        i["html_url"],
		"created_at": i["created_at"],
	})

	out, _ := json.Marshal(simple)
	return string(out), nil
}

func simplifyPRList(raw string) (string, error) {
	var prs []map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &prs); err != nil {
		return raw, nil
	}

	if len(prs) > maxListItems {
		prs = prs[:maxListItems]
	}

	simple := make([]map[string]interface{}, 0, len(prs))
	for _, p := range prs {
		entry := map[string]interface{}{
			"number":     p["number"],
			"title":      p["title"],
			"state":      p["state"],
			"created_at": p["created_at"],
			"url":        p["html_url"],
		}
		if user, ok := p["user"].(map[string]interface{}); ok {
			entry["author"] = user["login"]
		}
		if base, ok := p["base"].(map[string]interface{}); ok {
			entry["base"] = base["ref"]
		}
		if head, ok := p["head"].(map[string]interface{}); ok {
			entry["head"] = head["ref"]
		}
		simple = append(simple, stripNulls(entry))
	}

	out, _ := json.Marshal(simple)
	return string(out), nil
}

func simplifyUser(raw string) (string, error) {
	var u map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		return raw, nil
	}

	simple := stripNulls(map[string]interface{}{
		"login":        u["login"],
		"name":         u["name"],
		"bio":          u["bio"],
		"public_repos": u["public_repos"],
		"followers":    u["followers"],
		"following":    u["following"],
		"url":          u["html_url"],
	})

	out, _ := json.Marshal(simple)
	return string(out), nil
}
func stripNulls(m map[string]interface{}) map[string]interface{} {
	clean := make(map[string]interface{}, len(m))
	for k, v := range m {
		if v == nil {
			continue
		}
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		clean[k] = v
	}
	return clean
}
