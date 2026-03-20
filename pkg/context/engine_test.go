package context

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestEngineBasicPrepare(t *testing.T) {
	mgr := NewManager(DefaultBudget(200000))
	registerTestTools(mgr)

	engine := NewEngine(mgr, DefaultEngineConfig())
	plan := engine.PrepareContext("task-1", "create a pull request on github")

	if plan == nil {
		t.Fatal("PrepareContext returned nil")
	}
	if plan.Attempt != 1 {
		t.Errorf("expected attempt 1, got %d", plan.Attempt)
	}
	if plan.Strategy != "minimal" {
		t.Errorf("expected strategy 'minimal', got %s", plan.Strategy)
	}
	if len(plan.ToolsLoaded) == 0 {
		t.Error("expected at least 1 tool loaded")
	}
	if len(plan.ToolsLoaded) > 3 {
		t.Errorf("initial load should be ≤3 tools (config), got %d", len(plan.ToolsLoaded))
	}

	t.Logf("Plan: %d tools loaded, %d tokens, strategy=%s",
		len(plan.ToolsLoaded), plan.TokensUsed, plan.Strategy)
}

func TestEngineExpandOnFailure(t *testing.T) {
	mgr := NewManager(DefaultBudget(200000))
	registerTestTools(mgr)

	engine := NewEngine(mgr, DefaultEngineConfig())

	plan := engine.PrepareContext("task-2", "create a github issue about a bug")
	initialCount := len(plan.ToolsLoaded)

	result := ExecutionResult{
		Success:   false,
		ErrorType: ErrToolNotFound,
		ErrorMsg:  "model requested 'github_create_issue' but it wasn't loaded",
	}

	newPlan := engine.AdaptContext(plan, result)
	if newPlan == nil {
		t.Fatal("expected adaptation, got nil")
	}

	if newPlan.Strategy != "expanded" {
		t.Errorf("expected strategy 'expanded', got %s", newPlan.Strategy)
	}
	if newPlan.Attempt != 2 {
		t.Errorf("expected attempt 2, got %d", newPlan.Attempt)
	}
	if len(newPlan.ToolsLoaded) <= initialCount {
		t.Errorf("expected more tools after expansion: had %d, now %d",
			initialCount, len(newPlan.ToolsLoaded))
	}

	t.Logf("Expanded from %d to %d tools", initialCount, len(newPlan.ToolsLoaded))
}

func TestEngineUpgradeOnMisuse(t *testing.T) {
	mgr := NewManager(DefaultBudget(200000))
	registerTestTools(mgr)

	engine := NewEngine(mgr, DefaultEngineConfig())
	plan := engine.PrepareContext("task-3", "create a pull request on github")

	result := ExecutionResult{
		Success:     false,
		ToolUsed:    plan.ToolsLoaded[0],
		ToolsFailed: []string{plan.ToolsLoaded[0]},
		ErrorType:   ErrToolMisuse,
		ErrorMsg:    "invalid parameter: expected 'repo' not 'repository'",
	}

	newPlan := engine.AdaptContext(plan, result)
	if newPlan == nil {
		t.Fatal("expected adaptation for tool misuse")
	}

	if newPlan.Strategy != "full_schema" {
		t.Errorf("expected strategy 'full_schema', got %s", newPlan.Strategy)
	}

	t.Logf("Upgraded to full schema, %d tokens", newPlan.TokensUsed)
}

func TestEngineSuccessNoAdaptation(t *testing.T) {
	mgr := NewManager(DefaultBudget(200000))
	registerTestTools(mgr)

	engine := NewEngine(mgr, DefaultEngineConfig())
	plan := engine.PrepareContext("task-4", "send a message on slack")

	result := ExecutionResult{
		Success:  true,
		ToolUsed: "slack-0",
		Latency:  50 * time.Millisecond,
	}

	newPlan := engine.AdaptContext(plan, result)
	if newPlan != nil {
		t.Error("expected nil (no adaptation needed) on success")
	}
}

func TestEngineMaxRetries(t *testing.T) {
	mgr := NewManager(DefaultBudget(200000))
	registerTestTools(mgr)

	config := DefaultEngineConfig()
	config.MaxRetries = 2
	engine := NewEngine(mgr, config)

	plan := engine.PrepareContext("task-5", "query the database")

	for i := 0; i < 2; i++ {
		result := ExecutionResult{
			Success:   false,
			ErrorType: ErrToolNotFound,
			ErrorMsg:  "tool not available",
		}
		newPlan := engine.AdaptContext(plan, result)
		if newPlan != nil {
			plan = newPlan
		}
	}

	result := ExecutionResult{
		Success:   false,
		ErrorType: ErrToolNotFound,
		ErrorMsg:  "still failing",
	}
	finalPlan := engine.AdaptContext(plan, result)
	if finalPlan != nil {
		t.Error("expected nil after max retries")
	}
}

func TestEngineSwapFailedTool(t *testing.T) {
	mgr := NewManager(DefaultBudget(200000))
	registerTestTools(mgr)

	engine := NewEngine(mgr, DefaultEngineConfig())
	plan := engine.PrepareContext("task-6", "create a pull request on github")

	if len(plan.ToolsLoaded) == 0 {
		t.Skip("no tools loaded for this query")
	}

	failedTool := plan.ToolsLoaded[0]

	result := ExecutionResult{
		Success:     false,
		ToolUsed:    failedTool,
		ToolsFailed: []string{failedTool},
		ErrorType:   ErrToolFailed,
		ErrorMsg:    "GitHub API returned 500",
	}

	newPlan := engine.AdaptContext(plan, result)
	if newPlan == nil {
		t.Fatal("expected swap adaptation")
	}

	if newPlan.Strategy != "swapped" {
		t.Errorf("expected strategy 'swapped', got %s", newPlan.Strategy)
	}

	if mgr.IsActive(failedTool) {
		t.Error("failed tool should have been evicted")
	}

	t.Logf("Swapped: evicted %s, now have %d tools", failedTool, len(newPlan.ToolsLoaded))
}

func TestToolRankerLearning(t *testing.T) {
	mgr := NewManager(DefaultBudget(200000))
	registerTestTools(mgr)

	ranker := NewToolRanker()

	for i := 0; i < 10; i++ {
		ranker.RecordSuccess("github-0", 50*time.Millisecond)
		ranker.RecordFailure("github-1", ErrToolFailed)
	}

	ranked := ranker.Rank("create github pull request", mgr)

	if len(ranked) == 0 {
		t.Fatal("expected ranked tools")
	}

	idx0, idx1 := -1, -1
	var score0, score1 float64
	for i, r := range ranked {
		if r.ID == "github-0" {
			idx0 = i
			score0 = r.Score
		}
		if r.ID == "github-1" {
			idx1 = i
			score1 = r.Score
		}
	}

	if idx0 >= 0 && idx1 >= 0 {
		if idx0 > idx1 {
			t.Errorf("github-0 (100%% success) should rank higher than github-1 (0%% success)")
		}
		if score0 <= score1 {
			t.Errorf("github-0 score (%.3f) should be > github-1 score (%.3f)", score0, score1)
		}
		t.Logf("github-0: score=%.3f (rank %d), github-1: score=%.3f (rank %d)",
			score0, idx0, score1, idx1)
	}

	if len(ranked) >= 2 && ranked[0].Score == ranked[1].Score {
		t.Error("scores should NOT be identical (the whole point of v2 scoring)")
	}

	for _, r := range ranked {
		if r.ID == "github-0" && r.Predicted != "high" {
			t.Errorf("github-0 with 100%% success should be predicted 'high', got %q", r.Predicted)
		}
		if r.ID == "github-1" && r.Predicted != "low" {
			t.Errorf("github-1 with 0%% success should be predicted 'low', got %q", r.Predicted)
		}
	}

	t.Logf("Top ranked: %s (score=%.3f, predicted=%s)",
		ranked[0].ID, ranked[0].Score, ranked[0].Predicted)
}

func TestContextMemoryLearning(t *testing.T) {
	mgr := NewManager(DefaultBudget(200000))
	registerTestTools(mgr)

	ranker := NewToolRanker()

	ranked1 := ranker.Rank("search jira issues", mgr)
	if len(ranked1) == 0 {
		t.Fatal("expected ranked tools")
	}

	ranker.RecordContext("search jira issues assigned to me", []string{"jira-3"})
	ranker.RecordSuccess("jira-3", 30*time.Millisecond)

	ranked2 := ranker.Rank("search jira issues", mgr)

	var score3Before, score3After float64
	for _, r := range ranked1 {
		if r.ID == "jira-3" {
			score3Before = r.Score
		}
	}
	for _, r := range ranked2 {
		if r.ID == "jira-3" {
			score3After = r.Score
		}
	}

	if score3After <= score3Before {
		t.Errorf("jira-3 should score higher after memory (before=%.3f, after=%.3f)",
			score3Before, score3After)
	}

	t.Logf("jira-3 score: before=%.3f, after=%.3f (memory bonus applied)", score3Before, score3After)
}

func TestScoreDifferentiation(t *testing.T) {

	mgr := NewManager(DefaultBudget(200000))
	registerTestTools(mgr)

	ranker := NewToolRanker()

	ranker.RecordSuccess("github-0", 20*time.Millisecond)
	ranker.RecordSuccess("github-0", 25*time.Millisecond)
	ranker.RecordFailure("github-2", ErrToolFailed)
	ranker.RecordSuccess("github-4", 500*time.Millisecond)

	ranked := ranker.Rank("create github pull request", mgr)

	scores := make(map[string]float64)
	for _, r := range ranked {
		scores[r.ID] = r.Score
	}

	uniqueScores := make(map[float64]bool)
	for _, s := range scores {

		rounded := float64(int(s*1000)) / 1000
		uniqueScores[rounded] = true
	}

	if len(uniqueScores) < 2 {
		t.Errorf("expected at least 2 distinct scores, got %d (scores are still flat!)", len(uniqueScores))
		for id, s := range scores {
			t.Logf("  %s: %.4f", id, s)
		}
	} else {
		t.Logf("Score differentiation: %d unique scores across %d tools", len(uniqueScores), len(ranked))
		for _, r := range ranked {
			t.Logf("  %s: %.4f (rel=%.2f, suc=%.2f, pred=%s)",
				r.ID, r.Score, r.RelevanceScore, r.SuccessScore, r.Predicted)
		}
	}
}

func TestTracerRecording(t *testing.T) {
	mgr := NewManager(DefaultBudget(200000))
	registerTestTools(mgr)

	engine := NewEngine(mgr, DefaultEngineConfig())

	plan := engine.PrepareContext("task-trace", "create a github issue")

	engine.AdaptContext(plan, ExecutionResult{
		Success:   false,
		ErrorType: ErrToolNotFound,
		ErrorMsg:  "tool not loaded",
	})

	trace := engine.TracerRef().GetTrace(plan.TraceID)
	if trace == nil {
		t.Fatal("expected trace to exist")
	}

	if len(trace.Events) < 3 {
		t.Errorf("expected at least 3 events (ranking, prepared, result), got %d", len(trace.Events))
	}

	output := engine.TracerRef().PrintTrace(plan.TraceID)
	if !strings.Contains(output, "task-trace") {
		t.Error("trace output should contain task ID")
	}
	if !strings.Contains(output, "tool_ranking") || !strings.Contains(output, "execution_result") {
		t.Error("trace should contain ranking and result events")
	}

	t.Logf("Trace output:\n%s", output)
}

func TestMultiStepContextEvolution(t *testing.T) {

	mgr := NewManager(DefaultBudget(200000))
	registerTestTools(mgr)

	engine := NewEngine(mgr, DefaultEngineConfig())

	plan1 := engine.PrepareContext("multi-step", "get latest commits from github")
	t.Logf("Step 1 (GitHub): %d tools loaded", len(plan1.ToolsLoaded))

	engine.AdaptContext(plan1, ExecutionResult{
		Success:  true,
		ToolUsed: plan1.ToolsLoaded[0],
		Latency:  30 * time.Millisecond,
	})

	for _, id := range plan1.ToolsLoaded {
		mgr.Evict(id)
	}
	plan2 := engine.PrepareContext("multi-step-2", "search jira issues assigned to me")
	t.Logf("Step 2 (Jira): %d tools loaded", len(plan2.ToolsLoaded))

	jiraCount := 0
	githubCount := 0
	for _, id := range plan2.ToolsLoaded {
		if strings.HasPrefix(id, "jira") {
			jiraCount++
		}
		if strings.HasPrefix(id, "github") {
			githubCount++
		}
	}

	if jiraCount == 0 {
		t.Error("step 2 should have loaded Jira tools")
	}
	if githubCount > jiraCount {
		t.Errorf("step 2: Jira tools should dominate (jira=%d, github=%d)", jiraCount, githubCount)
	}

	for _, id := range plan2.ToolsLoaded {
		mgr.Evict(id)
	}
	plan3 := engine.PrepareContext("multi-step-3", "send slack message about the new jira issue")
	t.Logf("Step 3 (Slack): %d tools loaded", len(plan3.ToolsLoaded))

	hasSlack := false
	for _, id := range plan3.ToolsLoaded {
		if strings.HasPrefix(id, "slack") {
			hasSlack = true
		}
	}
	if !hasSlack {
		t.Error("step 3 should have loaded Slack tools")
	}

	t.Logf("Multi-step context evolution: GitHub(%d) → Jira(%d) → Slack(%d)",
		len(plan1.ToolsLoaded), len(plan2.ToolsLoaded), len(plan3.ToolsLoaded))
}

func registerTestTools(mgr *Manager) {
	servers := map[string][]string{
		"github": {"create_pr", "list_issues", "create_issue", "merge_pr", "list_repos",
			"create_branch", "get_file", "list_commits", "search_code", "create_webhook"},
		"slack": {"send_message", "list_channels", "create_channel", "upload_file",
			"search_messages", "add_reaction", "set_status"},
		"jira": {"create_issue", "update_issue", "list_issues", "search_issues",
			"assign_issue", "transition_issue", "add_comment"},
		"postgresql": {"query", "insert", "update", "delete", "list_tables",
			"describe_table", "explain_query"},
	}

	for server, tools := range servers {
		for i, tool := range tools {
			schema := generateTestSchema(server, tool)
			mgr.RegisterTool(
				strings.ToLower(server)+"-"+itoa(i),
				server+"_"+tool,
				tool+": operation for "+server,
				schema,
			)
		}
	}
}

func generateTestSchema(server, tool string) string {
	return `{"name":"` + server + `_` + tool + `","description":"` + tool + ` operation for ` + server + `","inputSchema":{"type":"object","properties":{"id":{"type":"string"},"data":{"type":"object"}},"required":["id"]}}`
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}
