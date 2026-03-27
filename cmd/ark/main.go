package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/atripati/ark/pkg/config"
	ctx "github.com/atripati/ark/pkg/context"
	"github.com/atripati/ark/pkg/models"
	"github.com/atripati/ark/pkg/runtime"
	"github.com/atripati/ark/pkg/store"
	"github.com/atripati/ark/pkg/tools"
)

const version = "0.4.0-alpha"

const banner = `
    _    ____  _  __
   / \  |  _ \| |/ /
  / _ \ | |_) | ' / 
 / ___ \|  _ <| . \ 
/_/   \_\_| \_\_|\_\

AI Runtime Kernel v%s
`

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	switch os.Args[1] {
	case "version", "--version", "-v":
		fmt.Printf("ark v%s\n", version)

	case "run":
		configPath := "agent.yaml"
		taskArgs := ""
		allowWrite := false
		dryRun := false

		for i := 2; i < len(os.Args); i++ {
			switch os.Args[i] {
			case "--task":
				if i+1 < len(os.Args) {
					taskArgs = os.Args[i+1]
					i++
				}
			case "--allow-write":
				allowWrite = true
			case "--dry-run":
				dryRun = true
			default:
				if !strings.HasPrefix(os.Args[i], "--") && configPath == "agent.yaml" {
					configPath = os.Args[i]
				}
			}
		}
		runAgent(configPath, taskArgs, allowWrite, dryRun)

	case "bench", "benchmark":
		runBenchmark()

	case "demo":
		runDemo()

	case "demo-learn":
		runDemoLearn()

	case "init":
		runInit()

	case "help", "--help", "-h":
		printUsage()

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Printf(banner, version)
	fmt.Println(`
Commands:
  run          Run an agent (ark run agent.yaml --task "your task here")
  init         Initialize a new ARK agent project
  bench        Run the MCP token bloat benchmark (see the savings)
  demo         Run the dynamic context engine demo (see load→adapt→retry)
  demo-learn   Prove ARK learns: ranking improves across 3 simulated runs
  version      Print version
  help         Show this help

Getting Started:
  $ ark init                                    # create agent.yaml
  $ export ANTHROPIC_API_KEY=sk-...             # or OPENAI_API_KEY
  $ ark run agent.yaml --task "list my github repos"

Safety Flags:
  --allow-write  Enable write operations (create issues, etc.)
  --dry-run      Simulate execution without calling real APIs

Providers:
  anthropic  Set ANTHROPIC_API_KEY env var
  openai     Set OPENAI_API_KEY env var
  ollama     No key needed (runs locally at localhost:11434)

Tools:
  github     Set GITHUB_TOKEN for write access (reads work without it)

Getting Started:
  $ ark init
  $ ark bench

Learn more: https://github.com/atripati/ark`)
}

func runInit() {
	fmt.Println("Creating new ARK agent project...")

	yaml := `# ARK Agent Definition
# This file defines your agent's behavior, tools, and context strategy.

name: my-agent
version: "0.1"

# Model configuration (swap providers with a single line change)
model:
  provider: anthropic  # anthropic | openai | ollama
  name: claude-sonnet-4-20250514
  max_tokens: 4096

# Context budget (ARK's killer feature)
# Instead of dumping all tool schemas into the prompt,
# ARK dynamically loads only what's relevant.
context:
  total_tokens: 200000
  strategy: adaptive  # adaptive | static | manual
  tool_budget: 10%    # vs 30-50% with raw MCP
  memory_budget: 10%
  conversation_budget: 35%

# Tools available to this agent
tools:
  - name: github
    type: mcp
    uri: "https://github.mcp.example.com"
    # ARK will compress these schemas and lazy-load them
    
  - name: search
    type: function
    description: "Search the web"
    
# Memory configuration
memory:
  backend: file  # file | memory (sqlite coming soon)
  shared: false    # Enable shared memory graph for multi-agent
  path: "./ark-memory.db"

# Observability (built-in, always on)
tracing:
  enabled: true
  output: stdout   # stdout | file | otlp
`

	if err := os.WriteFile("agent.yaml", []byte(yaml), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating agent.yaml: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ Created agent.yaml")
	fmt.Println("\nNext: edit agent.yaml, then run `ark bench` to see context savings")
}

func runBenchmark() {
	fmt.Printf(banner, version)
	fmt.Println("Running MCP Token Bloat Benchmark...")
	fmt.Println(strings.Repeat("═", 60))
	fmt.Println()

	totalTokens := 200000
	m := ctx.NewManager(ctx.DefaultBudget(totalTokens))

	servers := []struct {
		name  string
		tools []string
	}{
		{"GitHub", []string{"create_pr", "list_issues", "create_issue", "merge_pr", "list_repos", "create_branch", "delete_branch", "list_commits", "get_file", "create_file", "update_file", "list_prs", "review_pr", "create_label", "assign_issue", "create_milestone", "list_workflows", "trigger_workflow", "list_releases", "create_release", "list_gists", "create_gist", "search_code", "search_issues", "get_user", "list_teams", "create_webhook", "list_notifications"}},
		{"Slack", []string{"send_message", "list_channels", "create_channel", "upload_file", "list_users", "get_user_info", "set_status", "create_reminder", "list_bookmarks", "add_reaction", "search_messages", "update_message", "delete_message", "invite_user", "kick_user", "pin_message", "unpin_message", "list_pins", "set_topic", "archive_channel", "list_emojis", "schedule_message"}},
		{"Jira", []string{"create_issue", "update_issue", "list_issues", "search_issues", "assign_issue", "transition_issue", "add_comment", "list_projects", "create_sprint", "list_sprints", "move_to_sprint", "list_boards", "get_board", "create_filter", "list_components", "create_component", "list_versions", "create_version", "get_issue", "delete_issue", "add_attachment", "list_priorities", "list_statuses", "bulk_update", "get_changelog"}},
		{"Gmail", []string{"send_email", "list_emails", "read_email", "reply_email", "forward_email", "create_draft", "list_drafts", "delete_email", "search_emails", "list_labels", "create_label", "move_email", "mark_read", "mark_unread", "list_threads", "get_thread", "create_filter", "list_filters"}},
		{"Google Drive", []string{"list_files", "upload_file", "download_file", "create_folder", "share_file", "search_files", "move_file", "copy_file", "delete_file", "get_permissions", "update_permissions", "list_changes", "get_file_info", "export_file", "create_shortcut"}},
		{"Calendar", []string{"list_events", "create_event", "update_event", "delete_event", "list_calendars", "create_calendar", "find_free_time", "invite_attendee", "set_reminder", "list_recurring", "move_event", "get_event"}},
		{"PostgreSQL", []string{"query", "insert", "update", "delete", "list_tables", "describe_table", "create_table", "alter_table", "drop_table", "list_indexes", "create_index", "explain_query", "list_schemas", "list_views", "create_view", "list_functions", "vacuum", "analyze", "list_connections", "get_stats"}},
	}

	totalRawTokens := 0
	totalTools := 0

	fmt.Println("  Registering tools from 7 MCP servers...")
	fmt.Println()

	for _, server := range servers {
		serverTokens := 0
		for i, toolName := range server.tools {
			schema := generateRealisticSchema(server.name, toolName)
			block := m.RegisterTool(
				fmt.Sprintf("%s-%d", strings.ToLower(strings.ReplaceAll(server.name, " ", "_")), i),
				fmt.Sprintf("%s_%s", strings.ToLower(strings.ReplaceAll(server.name, " ", "_")), toolName),
				fmt.Sprintf("%s: %s operation for %s", toolName, describeAction(toolName), server.name),
				schema,
			)
			serverTokens += block.TokenCount
			totalTools++
		}
		totalRawTokens += serverTokens
		pct := float64(serverTokens) / float64(totalTokens) * 100
		bar := strings.Repeat("█", int(pct*2))
		fmt.Printf("  %-15s %3d tools  %6d tokens  %s %.1f%%\n",
			server.name, len(server.tools), serverTokens, bar, pct)
	}

	rawPct := float64(totalRawTokens) / float64(totalTokens) * 100

	fmt.Println()
	fmt.Println(strings.Repeat("─", 60))
	fmt.Printf("  ❌ RAW MCP (load everything upfront):\n")
	fmt.Printf("     %d tools → %d tokens → %.1f%% of context GONE\n", totalTools, totalRawTokens, rawPct)
	fmt.Printf("     %s\n", strings.Repeat("█", int(rawPct)))
	fmt.Printf("     Before your agent does ANY actual work.\n")
	fmt.Println(strings.Repeat("─", 60))
	fmt.Println()

	fmt.Printf("  ✅ ARK (load only what's relevant per task):\n\n")

	queries := []struct {
		task  string
		query string
	}{
		{"Create a GitHub PR", "create a pull request on github"},
		{"Send a Slack update", "send a message to the team on slack"},
		{"Query the database", "run a sql query to find active users"},
		{"Search Jira issues", "search jira issues assigned to me"},
		{"Check calendar", "find free time on calendar for meeting"},
	}

	totalArkTokens := 0

	for _, q := range queries {
		start := time.Now()
		loaded := m.LoadRelevant(q.query, 5)
		elapsed := time.Since(start)

		usage := m.TokenUsage()
		arkTokens := usage["tools"]
		details := m.ActiveBlockDetails()

		fullSchemaTokens := 0
		for _, d := range details {
			fullSchemaTokens += d.FullTokens
		}

		fmt.Printf("  ┌─ Task: %q\n", q.task)
		fmt.Printf("  │  Loaded: %d/%d tools (in %v)\n", len(loaded), totalTools, elapsed.Round(time.Microsecond))
		fmt.Printf("  │  ARK tokens:     %-6d  (compressed summaries)\n", arkTokens)
		fmt.Printf("  │  Raw would cost: %-6d  (full schemas for same %d tools)\n", fullSchemaTokens, len(loaded))
		fmt.Printf("  │  All-tools cost: %-6d  (what raw MCP actually loads)\n", totalRawTokens)

		arkPct := float64(arkTokens) / float64(totalTokens) * 100
		arkBar := "▏"
		if arkPct > 0.2 {
			arkBar = strings.Repeat("█", max(1, int(arkPct*2)))
		}
		fmt.Printf("  │  Context:  %s %.2f%%\n", arkBar, arkPct)
		fmt.Printf("  │  vs Raw:   %s %.1f%%\n", strings.Repeat("█", int(rawPct)), rawPct)
		fmt.Printf("  └─ Saved: %d tokens vs raw MCP\n", totalRawTokens-arkTokens)
		fmt.Println()

		totalArkTokens += arkTokens

		for _, id := range loaded {
			m.Evict(id)
		}
	}

	avgArkTokens := totalArkTokens / len(queries)
	avgArkPct := float64(avgArkTokens) / float64(totalTokens) * 100
	overallSavings := (1.0 - float64(avgArkTokens)/float64(totalRawTokens)) * 100
	tokensFreed := totalRawTokens - avgArkTokens

	fmt.Println(strings.Repeat("═", 60))
	fmt.Println()
	fmt.Println("  📊 RESULTS")
	fmt.Println()

	avgToolsLoaded := totalArkTokens / max(1, len(queries))
	avgToolCount := 0
	for _, q := range queries {
		loaded := m.LoadRelevant(q.query, 5)
		avgToolCount += len(loaded)
		for _, id := range loaded {
			m.Evict(id)
		}
	}
	avgToolCount = avgToolCount / len(queries)

	_ = avgToolsLoaded

	fmt.Printf("  ┌──────────────────────────────────────────────────┐\n")
	fmt.Printf("  │  Metric              Raw MCP         ARK         │\n")
	fmt.Printf("  ├──────────────────────────────────────────────────┤\n")
	fmt.Printf("  │  Tools loaded        ALL %-4d        ~%-4d/task   │\n", totalTools, avgToolCount)
	fmt.Printf("  │  Tokens consumed     %-6d          %-6d       │\n", totalRawTokens, avgArkTokens)
	fmt.Printf("  │  Context window      %-5.1f%%           %-5.1f%%       │\n", rawPct, avgArkPct)
	fmt.Printf("  │  Tokens freed        —               +%-6d     │\n", tokensFreed)
	fmt.Printf("  │  Reduction           —               %-5.1f%%       │\n", overallSavings)
	fmt.Printf("  └──────────────────────────────────────────────────┘\n")
	fmt.Println()
	fmt.Printf("  💡 ARK saves ~%.0f%% of context per task.\n", overallSavings)
	fmt.Printf("     That's %d extra tokens for actual conversation,\n", tokensFreed)
	fmt.Printf("     reasoning, and working memory.\n")
	fmt.Println()
	fmt.Printf("  → Get started: ark init\n")
	fmt.Printf("  → Learn more:  https://github.com/atripati/ark\n")
	fmt.Println()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func generateRealisticSchema(server, tool string) string {
	return fmt.Sprintf(`{
  "name": "%s_%s",
  "description": "Performs the %s operation on the %s server. This tool allows you to interact with %s resources including creating, reading, updating, and deleting items. Supports pagination, filtering, and sorting. Returns structured JSON responses with status codes and metadata.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "resource_id": {
        "type": "string",
        "description": "The unique identifier of the %s resource to operate on. Can be a UUID, slug, or numeric ID depending on the resource type."
      },
      "options": {
        "type": "object",
        "description": "Additional options for the %s operation",
        "properties": {
          "fields": {"type": "array", "items": {"type": "string"}, "description": "Specific fields to include in the response"},
          "limit": {"type": "integer", "default": 50, "description": "Maximum number of results to return"},
          "offset": {"type": "integer", "default": 0, "description": "Number of results to skip for pagination"},
          "sort_by": {"type": "string", "description": "Field to sort results by"},
          "sort_order": {"type": "string", "enum": ["asc", "desc"], "default": "desc"},
          "filter": {"type": "object", "description": "Key-value pairs to filter results"}
        }
      },
      "data": {
        "type": "object",
        "description": "The data payload for create/update operations on %s"
      },
      "dry_run": {
        "type": "boolean",
        "default": false,
        "description": "If true, validates the operation without executing it"
      }
    },
    "required": ["resource_id"]
  }
}`, strings.ToLower(server), tool, tool, server, server, server, tool, server)
}

func describeAction(tool string) string {
	if strings.HasPrefix(tool, "create") || strings.HasPrefix(tool, "add") {
		return "creation"
	}
	if strings.HasPrefix(tool, "list") || strings.HasPrefix(tool, "search") || strings.HasPrefix(tool, "get") {
		return "retrieval"
	}
	if strings.HasPrefix(tool, "update") || strings.HasPrefix(tool, "set") || strings.HasPrefix(tool, "move") {
		return "modification"
	}
	if strings.HasPrefix(tool, "delete") || strings.HasPrefix(tool, "remove") {
		return "deletion"
	}
	return "management"
}

func runDemo() {
	fmt.Printf(banner, version)

	mgr := ctx.NewManager(ctx.DefaultBudget(200000))

	servers := map[string][]string{
		"github":     {"create_pr", "list_issues", "create_issue", "merge_pr", "list_repos", "create_branch", "get_file", "list_commits"},
		"slack":      {"send_message", "list_channels", "create_channel", "upload_file", "search_messages"},
		"jira":       {"create_issue", "update_issue", "list_issues", "search_issues", "assign_issue"},
		"postgresql": {"query", "insert", "update", "delete", "list_tables"},
	}

	for server, tools := range servers {
		for i, tool := range tools {
			schema := generateRealisticSchema(server, tool)
			mgr.RegisterTool(
				fmt.Sprintf("%s-%d", server, i),
				server+"_"+tool,
				tool+": operation for "+server,
				schema,
			)
		}
	}

	runtime.RunDemo(mgr)
}

func runAgent(configPath, task string, allowWrite, dryRun bool) {
	fmt.Printf(banner, version)

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ❌ Error loading config: %v\n\n", err)
		fmt.Fprintf(os.Stderr, "  Run 'ark init' to create a starter agent.yaml\n")
		os.Exit(1)
	}

	fmt.Printf("  Agent:    %s (v%s)\n", cfg.Name, cfg.Version)
	fmt.Printf("  Provider: %s/%s\n", cfg.Model.Provider, cfg.Model.Name)
	fmt.Printf("  Context:  %dk tokens, strategy=%s\n", cfg.Context.TotalTokens/1000, cfg.Context.Strategy)

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "\n  ❌ Config error: %v\n", err)
		os.Exit(1)
	}

	if cfg.Model.APIKey != "" {
		key := cfg.Model.APIKey
		if len(key) > 12 {
			fmt.Printf("  API Key:  %s...%s\n", key[:7], key[len(key)-4:])
		} else {
			fmt.Printf("  API Key:  ***\n")
		}
	} else if cfg.Model.Provider == "ollama" {
		fmt.Printf("  Endpoint: %s\n", cfg.Model.BaseURL)
	}

	provider, err := models.New(
		cfg.Model.Provider,
		cfg.Model.Name,
		cfg.Model.APIKey,
		cfg.Model.BaseURL,
		cfg.Model.MaxTokens,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n  ❌ Provider error: %v\n", err)
		os.Exit(1)
	}

	budget := ctx.DefaultBudget(cfg.Context.TotalTokens)
	if cfg.Context.ToolBudgetPct > 0 {
		budget.Tools = cfg.Context.TotalTokens * cfg.Context.ToolBudgetPct / 100
	}
	if cfg.Context.MemoryBudgetPct > 0 {
		budget.Memory = cfg.Context.TotalTokens * cfg.Context.MemoryBudgetPct / 100
	}
	if cfg.Context.ConversationBudgetPct > 0 {
		budget.Conversation = cfg.Context.TotalTokens * cfg.Context.ConversationBudgetPct / 100
	}
	mgr := ctx.NewManager(budget)

	for i, tool := range cfg.Tools {
		desc := tool.Description
		if desc == "" {
			desc = fmt.Sprintf("%s tool (%s)", tool.Name, tool.Type)
		}
		mgr.RegisterTool(
			fmt.Sprintf("%s-%d", tool.Name, i),
			tool.Name,
			desc,
			fmt.Sprintf(`{"name":"%s","type":"%s","description":"%s"}`, tool.Name, tool.Type, desc),
		)
	}

	engineConfig := ctx.DefaultEngineConfig()
	if cfg.Context.InitialTools > 0 {
		engineConfig.InitialTools = cfg.Context.InitialTools
	}
	if cfg.Context.MaxRetries > 0 {
		engineConfig.MaxRetries = cfg.Context.MaxRetries
	}

	var engine *ctx.Engine
	var memStore *store.JSONFileStore

	if cfg.Memory.Backend == "file" || cfg.Memory.Backend == "sqlite" {
		memoryPath := cfg.Memory.Path
		if memoryPath == "" {
			memoryPath = "./ark-memory.json"
		}
		if strings.HasSuffix(memoryPath, ".db") {
			memoryPath = strings.TrimSuffix(memoryPath, ".db") + ".json"
		}

		if cfg.Memory.Backend == "sqlite" {
			fmt.Printf("  Memory:   ⚠️  SQLite not yet implemented, using file backend\n")
		}

		s, storeErr := store.NewJSONFileStore(memoryPath)
		if storeErr != nil {
			fmt.Fprintf(os.Stderr, "  ⚠️  Memory store failed: %v (continuing without persistence)\n", storeErr)
			engine = ctx.NewEngine(mgr, engineConfig)
		} else {
			memStore = s
			decayed := s.Decay()
			engine = ctx.NewEngineWithStore(mgr, engineConfig, s)
			stats := s.RecordCount()
			patterns := s.PatternCount()
			if stats > 0 || patterns > 0 {
				msg := fmt.Sprintf("  Memory:   ✅ loaded %d tool stats, %d patterns from %s", stats, patterns, memoryPath)
				if decayed > 0 {
					msg += fmt.Sprintf(" (decayed %d stale entries)", decayed)
				}
				fmt.Println(msg)
			} else {
				fmt.Printf("  Memory:   ✅ persistent (%s)\n", memoryPath)
			}
		}
	} else {
		engine = ctx.NewEngine(mgr, engineConfig)
		fmt.Printf("  Memory:   in-memory (set memory.backend: file for persistence)\n")
	}

	toolRouter := tools.NewRouter()
	toolRouter.AllowWrite = allowWrite
	toolRouter.DryRun = dryRun

	if dryRun {
		fmt.Printf("  Safety:   🔒 DRY RUN mode (no real execution)\n")
	} else if allowWrite {
		fmt.Printf("  Safety:   ⚠️  write operations ENABLED\n")
	} else {
		fmt.Printf("  Safety:   🔒 read-only (use --allow-write to enable writes)\n")
	}

	githubToken := os.Getenv("GITHUB_TOKEN")
	if githubToken != "" {
		tools.RegisterGitHub(toolRouter, githubToken)
		fmt.Printf("  GitHub:   ✅ connected (%d tools)\n", 6)
	} else {
		tools.RegisterGitHub(toolRouter, "")
		fmt.Printf("  GitHub:   ⚠️  no GITHUB_TOKEN (read-only, 60 req/hr limit)\n")
	}
	//// i added this later because i was testing to get openai repo but zero tool was loading
	githubToolDefs := []struct {
		id, name, desc, schema string
	}{
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
	}
	for _, t := range githubToolDefs {
		mgr.RegisterTool(t.id, t.name, t.desc, t.schema)
	}

	for _, tool := range cfg.Tools {
		if tool.Type == "http" && tool.URI != "" {
			toolRouter.RegisterHTTP(tool.Name, tools.HTTPToolConfig{
				Method:  "GET",
				URL:     tool.URI,
				Headers: map[string]string{},
			})
		}
	}

	fmt.Printf("  Tools:    %d registered\n", toolRouter.ToolCount())

	agentConfig := runtime.DefaultAgentConfig()
	agentConfig.Verbose = cfg.Tracing.Enabled
	agent := runtime.NewAgent(engine, provider, toolRouter, agentConfig)

	if task == "" {
		fmt.Print("\n  📝 Enter task: ")
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		task = strings.TrimSpace(input)
	}

	if task == "" {
		fmt.Println("\n  ❌ No task provided. Use: ark run agent.yaml --task \"your task\"")
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println(strings.Repeat("─", 60))

	result := agent.Run("ark-run", task)

	if cfg.Tracing.Enabled {
		fmt.Println()
		fmt.Println("  Context Decision Trace:")
		fmt.Println(engine.TracerRef().PrintTrace(result.TraceID))
	}

	fmt.Println(strings.Repeat("─", 60))
	if result.Success {
		fmt.Println("  ✅ Task completed successfully")
	} else {
		fmt.Println("  ❌ Task failed")
	}
	fmt.Printf("  Steps: %d | Tokens: %d | Time: %v\n",
		len(result.Steps), result.TotalTokens, result.TotalTime.Round(time.Millisecond))
	if result.Output != "" {
		fmt.Printf("\n  Output:\n  %s\n", result.Output)
	}
	fmt.Println()

	if memStore != nil {
		if err := memStore.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠️  Store flush: %v\n", err)
		}
	}
}

func runDemoLearn() {
	fmt.Printf(banner, version)
	fmt.Println("  Proving ARK learns: ranking improves across runs")
	fmt.Println(strings.Repeat("═", 60))

	storePath := "/tmp/ark-demo-learn.json"
	os.Remove(storePath)

	query := "search github issues"

	registerTools := func() *ctx.Manager {
		mgr := ctx.NewManager(ctx.DefaultBudget(200000))
		// 3 GitHub tools — the engine must learn which one works best
		mgr.RegisterTool("github-search", "github_search_issues",
			"search: retrieval operation for github",
			`{"name":"github_search_issues","description":"Search issues across GitHub repositories"}`)
		mgr.RegisterTool("github-list", "github_list_issues",
			"list_issues: retrieval operation for github",
			`{"name":"github_list_issues","description":"List issues in a specific GitHub repository"}`)
		mgr.RegisterTool("github-get", "github_get_issue",
			"get_issue: retrieval operation for github",
			`{"name":"github_get_issue","description":"Get a single issue by number from GitHub"}`)
		// Noise tools — should rank lower over time
		mgr.RegisterTool("slack-0", "slack_send_message",
			"send_message: operation for slack",
			`{"name":"slack_send_message","description":"Send a message to a Slack channel"}`)
		mgr.RegisterTool("jira-0", "jira_search_issues",
			"search_issues: retrieval operation for jira",
			`{"name":"jira_search_issues","description":"Search issues in Jira"}`)
		return mgr
	}

	fmt.Println()
	fmt.Println("  ┌─────────────────────────────────────────────────┐")
	fmt.Println("  │  RUN 1: Fresh start (no history)                │")
	fmt.Println("  └─────────────────────────────────────────────────┘")

	s1, _ := store.NewJSONFileStore(storePath)
	mgr1 := registerTools()
	engine1 := ctx.NewEngineWithStore(mgr1, ctx.DefaultEngineConfig(), s1)

	ranked1 := getRanking(engine1, mgr1, query)
	printRanking("  Run 1 ranking", ranked1)

	plan1 := engine1.PrepareContext("run1", query)
	engine1.AdaptContext(plan1, ctx.ExecutionResult{
		Success: false, ToolUsed: "github-search",
		ToolsFailed: []string{"github-search"},
		ErrorType:   ctx.ErrToolFailed, ErrorMsg: "GitHub API timeout",
		Latency: 5000 * time.Millisecond,
	})
	engine1.AdaptContext(plan1, ctx.ExecutionResult{
		Success: true, ToolUsed: "github-list",
		Latency: 120 * time.Millisecond,
	})

	fmt.Println()
	fmt.Println("  Executed: github-search → FAILED (5000ms timeout)")
	fmt.Println("  Executed: github-list   → SUCCESS (120ms)")

	time.Sleep(150 * time.Millisecond)
	s1.Close()

	fmt.Println()
	fmt.Println("  ┌─────────────────────────────────────────────────┐")
	fmt.Println("  │  RUN 2: Learning from Run 1                     │")
	fmt.Println("  └─────────────────────────────────────────────────┘")

	s2, _ := store.NewJSONFileStore(storePath)
	mgr2 := registerTools()
	engine2 := ctx.NewEngineWithStore(mgr2, ctx.DefaultEngineConfig(), s2)

	ranked2 := getRanking(engine2, mgr2, query)
	printRanking("  Run 2 ranking", ranked2)
	printDiff("  Change from Run 1", ranked1, ranked2)

	plan2 := engine2.PrepareContext("run2", query)
	engine2.AdaptContext(plan2, ctx.ExecutionResult{
		Success: true, ToolUsed: "github-list",
		Latency: 95 * time.Millisecond,
	})
	engine2.AdaptContext(plan2, ctx.ExecutionResult{
		Success: true, ToolUsed: "github-get",
		Latency: 80 * time.Millisecond,
	})

	fmt.Println()
	fmt.Println("  Executed: github-list → SUCCESS (95ms)")
	fmt.Println("  Executed: github-get  → SUCCESS (80ms)")

	time.Sleep(150 * time.Millisecond)
	s2.Close()

	fmt.Println()
	fmt.Println("  ┌─────────────────────────────────────────────────┐")
	fmt.Println("  │  RUN 3: Compounding knowledge                   │")
	fmt.Println("  └─────────────────────────────────────────────────┘")

	s3, _ := store.NewJSONFileStore(storePath)
	mgr3 := registerTools()
	engine3 := ctx.NewEngineWithStore(mgr3, ctx.DefaultEngineConfig(), s3)

	ranked3 := getRanking(engine3, mgr3, query)
	printRanking("  Run 3 ranking", ranked3)
	printDiff("  Change from Run 1", ranked1, ranked3)

	fmt.Println()
	fmt.Println(strings.Repeat("═", 60))
	fmt.Println()
	fmt.Println("  📊 LEARNING PROOF")
	fmt.Println()

	listRun1 := findScore(ranked1, "github-list")
	listRun2 := findScore(ranked2, "github-list")
	listRun3 := findScore(ranked3, "github-list")

	searchRun1 := findScore(ranked1, "github-search")
	searchRun2 := findScore(ranked2, "github-search")
	searchRun3 := findScore(ranked3, "github-search")

	fmt.Println("  github-list (the winner):")
	fmt.Printf("    Run 1: %.3f  (no data)\n", listRun1)
	fmt.Printf("    Run 2: %.3f  (1 success)\n", listRun2)
	fmt.Printf("    Run 3: %.3f  (2 successes, improving latency)\n", listRun3)
	fmt.Printf("    Improvement: +%.1f%%\n", (listRun3-listRun1)/listRun1*100)
	fmt.Println()

	fmt.Println("  github-search (the loser):")
	fmt.Printf("    Run 1: %.3f  (no data)\n", searchRun1)
	fmt.Printf("    Run 2: %.3f  (1 failure)\n", searchRun2)
	fmt.Printf("    Run 3: %.3f  (still failing)\n", searchRun3)
	fmt.Printf("    Change: %.1f%%\n", (searchRun3-searchRun1)/searchRun1*100)
	fmt.Println()

	if listRun3 > listRun1 && searchRun3 < searchRun1 {
		fmt.Println("  ✅ PROVEN: ARK promotes tools that work, demotes tools that fail.")
		fmt.Println("     Ranking improved across 3 runs with persistent memory.")
	} else {
		fmt.Println("  ⚠️  Learning effect was minimal for this scenario.")
	}

	fmt.Println()
	fmt.Println("  This is not caching. This is not heuristics.")
	fmt.Println("  This is a system that gets smarter every time it runs.")
	fmt.Println()

	// Cleanup
	os.Remove(storePath)
}

type toolScore struct {
	ID         string
	Score      float64
	Success    float64
	Confidence float64
	Calls      int
	Memory     float64
	Predicted  string
}

func getRanking(engine *ctx.Engine, mgr *ctx.Manager, query string) []toolScore {
	plan := engine.PrepareContext("ranking-check", query)

	trace := engine.TracerRef().GetTrace(plan.TraceID)
	_ = trace

	ranked := engine.RankTools(query)

	scores := make([]toolScore, 0, len(ranked))
	for _, r := range ranked {
		scores = append(scores, toolScore{
			ID:         r.ID,
			Score:      r.Score,
			Success:    r.SuccessScore,
			Confidence: r.ConfidenceScore,
			Calls:      r.HistoricalCalls,
			Memory:     r.MemoryBonus,
			Predicted:  r.Predicted,
		})
	}

	for _, id := range plan.ToolsLoaded {
		mgr.Evict(id)
	}

	return scores
}

func printRanking(label string, scores []toolScore) {
	fmt.Printf("\n%s:\n", label)
	for i, s := range scores {
		bar := strings.Repeat("█", int(s.Score*40))
		learned := ""
		if s.Calls > 0 {
			learned = fmt.Sprintf(" [%d calls, %.0f%% success, conf=%.2f]",
				s.Calls, s.Success*100, s.Confidence)
		}
		if s.Memory > 0 {
			learned += fmt.Sprintf(" [mem=+%.3f]", s.Memory)
		}
		fmt.Printf("    %d. %-20s %.3f %s%s\n", i+1, s.ID, s.Score, bar, learned)
	}
}

func printDiff(label string, before, after []toolScore) {
	fmt.Printf("\n%s:\n", label)
	for _, a := range after {
		for _, b := range before {
			if a.ID == b.ID {
				diff := a.Score - b.Score
				arrow := "→"
				if diff > 0.01 {
					arrow = "↑"
				} else if diff < -0.01 {
					arrow = "↓"
				}
				if diff != 0 {
					fmt.Printf("    %s %-20s %+.3f (%s %.3f → %.3f)\n",
						arrow, a.ID, diff, arrow, b.Score, a.Score)
				}
			}
		}
	}
}

func findScore(scores []toolScore, prefix string) float64 {
	for _, s := range scores {
		if s.ID == prefix {
			return s.Score
		}
	}
	return 0
}
