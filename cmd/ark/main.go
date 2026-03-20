// ////ARK — AI Runtime Kernel
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	ctx "github.com/atripati/ark/pkg/context"
	"github.com/atripati/ark/pkg/runtime"
)

const version = "0.1.0-alpha"

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

	case "bench", "benchmark":
		runBenchmark()

	case "demo":
		runDemo()

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
  init       Initialize a new ARK agent project
  bench      Run the MCP token bloat benchmark (see the savings)
  demo       Run the dynamic context engine demo (see load→adapt→retry)
  version    Print version
  help       Show this help

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
  provider: anthropic  # anthropic | openai | ollama | custom
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
  backend: sqlite  # sqlite | postgres | custom
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
