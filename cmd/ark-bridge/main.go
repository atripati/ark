// Command ark-bridge is the isolated Go<->Python bridge for the ARK Runtime SDK.
//
// Two transports share this one binary; both keep the Go runtime as the source of truth.
//
// One-shot (default) — reads one JSON request on stdin, writes one JSON response on stdout:
//
//	{"kind":"run","task":"..."}            -> canonical telemetry.RunResult JSON
//	{"kind":"supervise",...}               -> {verdict, supervision} (experimental)
//
// Session (`ark-bridge --session`) — a persistent, line-delimited request/response loop for
// EXTERNAL-agent integration (`with ark.trace(...) as run:`). The developer keeps their own
// agent/model/tools and REPORTS decisions; ARK observes + supervises. Crucially the SESSION
// STATE lives here in Go, not in Python: the retry counters, verdict semantics, decision
// registry and telemetry.Builder are all authoritative on this side, so a stateless Python
// client cannot drift from the real supervision/recovery logic. See session.go.
//
// The one-shot "mode":"mock" (default) exercises the REAL runtime (real agent loop, real
// router decisions, real cost machinery) with a deterministic model executor, so the SDK is
// reproducible with no API cost. "mode":"live" loads agent.yaml and uses the configured
// provider. The transport (subprocess) is an implementation detail behind the Python API.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/atripati/ark/pkg/config"
	ctx "github.com/atripati/ark/pkg/context"
	"github.com/atripati/ark/pkg/models"
	"github.com/atripati/ark/pkg/router"
	"github.com/atripati/ark/pkg/runtime"
	"github.com/atripati/ark/pkg/supervise"
	"github.com/atripati/ark/pkg/telemetry"
)

type request struct {
	Kind        string `json:"kind"`
	Task        string `json:"task"`
	Config      string `json:"config"`
	Mode        string `json:"mode"`        // "mock" (default) | "live"
	Supervision string `json:"supervision"` // "off" (default) | "experimental"

	// supervise:
	Constraint string                   `json:"constraint"`
	Proposed   supervise.ProposedAction `json:"proposed"`
	Evidence   supervise.Evidence       `json:"evidence"`
	RetryCount int                      `json:"retry_count"`
	Budget     int                      `json:"budget"`
}

func main() {
	for _, a := range os.Args[1:] {
		if a == "--session" {
			runSession() // persistent external-agent session (see session.go)
			return
		}
	}
	runOneShot()
}

func runOneShot() {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		emitErr("read stdin: " + err.Error())
		return
	}
	var req request
	if err := json.Unmarshal(data, &req); err != nil {
		emitErr("bad request json: " + err.Error())
		return
	}
	switch req.Kind {
	case "run":
		handleRun(req)
	case "supervise":
		handleSupervise(req)
	default:
		emitErr("unknown kind: " + req.Kind)
	}
}

// ---- supervise: expose the existing generic mechanism, unchanged ----

func handleSupervise(req request) {
	if req.Supervision != "experimental" {
		emitErr("constrained supervision is experimental and off by default; pass supervision=experimental")
		return
	}
	budget := req.Budget
	if budget == 0 {
		budget = 4
	}
	d := supervise.New().Evaluate(supervise.Request{
		Constraint: req.Constraint, Proposed: req.Proposed, Evidence: req.Evidence,
		RetryCount: req.RetryCount, Budget: budget,
	})
	emitJSON(map[string]any{
		"kind":        "supervision",
		"verdict":     string(d.Verdict),
		"reason":      d.Reason,
		"supervision": telemetry.SupervisionFromDecision(d, "inline_evidence"),
	})
}

// ---- run: execute a real task, map to the canonical RunResult ----

func handleRun(req request) {
	mgr := ctx.NewManager(ctx.DefaultBudget(200000))
	mgr.RegisterTool("github_search_repos", "github_search_repos",
		"search repos: search GitHub repositories", `{"name":"github_search_repos"}`)
	engine := ctx.NewEngine(mgr, ctx.DefaultEngineConfig())
	tools := &mockTools{results: map[string]string{
		"github_search_repos": `[{"name":"django","stars":89028},{"name":"flask","stars":72145},{"name":"tornado","stars":22179}]`}}

	provider, model := "openai", "gpt-4o"
	var exec runtime.Executor

	if req.Mode == "live" {
		path := req.Config
		if path == "" {
			path = "agent.yaml"
		}
		cfg, err := config.Load(path)
		if err != nil {
			emitErr("live: load config: " + err.Error())
			return
		}
		provider, model = cfg.Model.Provider, cfg.Model.Name
		p, err := models.New(cfg.Model.Provider, cfg.Model.Name, cfg.Model.APIKey, cfg.Model.BaseURL, cfg.Model.MaxTokens)
		if err != nil {
			emitErr("live: provider: " + err.Error())
			return
		}
		exec = router.NewSingle(p)
	} else {
		// deterministic model, REAL router (records real routing decisions)
		stub := &mockExec{seq: defaultRun()}
		cfg := router.Config{
			Strategy:    router.StrategyCostOptimized,
			FastModel:   router.ModelSpec{Provider: "openai", Name: "gpt-4o-mini"},
			StrongModel: router.ModelSpec{Provider: "openai", Name: "gpt-4o"},
		}
		exec = router.New(cfg, stub, stub) // same stub for both tiers -> shared sequence
	}

	agent := runtime.NewAgent(engine, exec, tools, runtime.AgentConfig{
		Provider: provider, Model: model, MaxSteps: 10, MaxRetriesPerTool: 3,
		TotalTimeout: 30 * time.Second, Verbose: false,
	})
	result := agent.Run("ark-run", req.Task)

	var routing []router.Decision
	if r, ok := exec.(*router.Router); ok {
		routing = r.Decisions()
	}

	run := telemetry.FromTaskResult(req.Task, result, routing)
	// provider status only — never secrets
	run.Providers = map[string]string{provider: telemetry.ProviderStatus(true)}
	if req.Mode != "live" {
		run.TaskType = "ranking"
	}
	emitJSON(run)
}

// ---- deterministic model + tools (mock mode) ----

type mockResp struct {
	text, tool string
	params     map[string]interface{}
	in, out    int
}

type mockExec struct {
	seq []mockResp
	i   int
}

func (e *mockExec) Execute(_ string, _ string) (*runtime.ModelResponse, error) {
	r := e.seq[len(e.seq)-1]
	if e.i < len(e.seq) {
		r = e.seq[e.i]
		e.i++
	}
	m := &runtime.ModelResponse{Text: r.text, InputTokens: r.in, OutputTokens: r.out,
		TokensUsed: r.in + r.out, Latency: 500 * time.Millisecond}
	if r.tool != "" {
		m.ToolCall = &runtime.ToolCall{Name: r.tool, Params: r.params}
	}
	return m, nil
}

type mockTools struct{ results map[string]string }

func (t *mockTools) Handle(c *runtime.ToolCall) error { c.Result = t.results[c.Name]; return nil }

func defaultRun() []mockResp {
	return []mockResp{
		{tool: "github_search_repos", params: map[string]interface{}{"query": "web frameworks", "sort": "stars"}, in: 449, out: 16},
		{text: "Based on the GitHub search results:\n1. Django - 89028 stars - https://github.com/django/django\n" +
			"2. Flask - 72145 stars - https://github.com/pallets/flask\n3. Tornado - 22179 stars - https://github.com/tornadoweb/tornado",
			in: 882, out: 186},
	}
}

// ---- output ----

func emitJSON(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		emitErr("marshal: " + err.Error())
		return
	}
	fmt.Println(string(b))
}

func emitErr(msg string) {
	b, _ := json.Marshal(map[string]string{"error": msg})
	fmt.Println(string(b))
	os.Exit(1)
}
