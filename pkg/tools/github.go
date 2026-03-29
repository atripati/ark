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
}
func RegisterGitHubFromEnv(router *Router) {
	RegisterGitHub(router, os.Getenv("GITHUB_TOKEN"))
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

	// If no user specified and no token, we can't hit /user/repos (requires auth).
	// Return a clear error so the LLM knows to provide a username.
	if user == "" && g.token == "" {
		return "", fmt.Errorf("github_list_repos: 'user' param required when no GITHUB_TOKEN is set (e.g. {\"user\": \"openai\"})")
	}

	url := githubAPI + "/user/repos?per_page=10&sort=updated"
	if user != "" {
		url = fmt.Sprintf("%s/users/%s/repos?per_page=10&sort=updated", githubAPI, user)
	}

	if perPage, ok := params["per_page"]; ok {
		url = strings.Replace(url, "per_page=10", fmt.Sprintf("per_page=%v", perPage), 1)
	}
	if sort, ok := params["sort"]; ok {
		url = strings.Replace(url, "sort=updated", fmt.Sprintf("sort=%v", sort), 1)
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

func simplifyRepoList(raw string) (string, error) {
	var repos []map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &repos); err != nil {
		return raw, nil
	}

	if len(repos) > maxListItems {
		repos = repos[:maxListItems]
	}

	simple := make([]map[string]interface{}, 0, len(repos))
	for _, r := range repos {
		simple = append(simple, stripNulls(map[string]interface{}{
			"name":        r["name"],
			"full_name":   r["full_name"],
			"description": r["description"],
			"language":    r["language"],
			"stars":       r["stargazers_count"],
			"updated_at":  r["updated_at"],
			"private":     r["private"],
			"url":         r["html_url"],
		}))
	}

	out, _ := json.Marshal(simple)
	return string(out), nil
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
