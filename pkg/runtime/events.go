package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// ═══════════════════════════════════════════════════════════
// Event Emitter
//
// Writes structured JSONL events during execution.
// ARK Memory's Collector ingests these automatically.
// This is the bridge between Go Runtime and Python Memory.
//
// Events:
//   tool_call     — tool execution with outcome
//   step_complete — model step with routing info
//   task_complete — full task summary
//   verification  — code verification result
//   strategy      — learned strategy
// ═══════════════════════════════════════════════════════════

// EventEmitter writes execution events to a JSONL file.
type EventEmitter struct {
	path string
	file *os.File
	mu   sync.Mutex
}

// NewEventEmitter creates an emitter that appends to the given file.
func NewEventEmitter(path string) (*EventEmitter, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("event emitter: %w", err)
	}
	return &EventEmitter{path: path, file: f}, nil
}

// Emit writes a single event as a JSON line.
func (e *EventEmitter) Emit(event map[string]interface{}) {
	if e == nil || e.file == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	if event["timestamp"] == nil {
		event["timestamp"] = time.Now().Unix()
	}

	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	e.file.Write(data)
	e.file.Write([]byte("\n"))
}

// Close flushes and closes the event file.
func (e *EventEmitter) Close() {
	if e != nil && e.file != nil {
		e.file.Close()
	}
}

// ── Convenience methods for common events ──

// EmitToolCall records a tool execution.
func (e *EventEmitter) EmitToolCall(tool, query string, success bool, durationMs float64, tokens int, cost float64, errMsg string) {
	event := map[string]interface{}{
		"event":       "tool_call",
		"tool":        tool,
		"query":       query,
		"success":     success,
		"duration_ms": durationMs,
		"tokens":      tokens,
		"cost":        cost,
	}
	if errMsg != "" {
		event["error"] = errMsg
	}
	e.Emit(event)
}

// EmitStepComplete records a completed execution step.
func (e *EventEmitter) EmitStepComplete(task string, step int, model, action string, tokens int, cost float64) {
	e.Emit(map[string]interface{}{
		"event":  "step_complete",
		"task":   task,
		"step":   step,
		"model":  model,
		"action": action,
		"tokens": tokens,
		"cost":   cost,
	})
}

// EmitTaskComplete records a full task execution.
func (e *EventEmitter) EmitTaskComplete(task string, steps int, totalCost float64, totalTokens int, durationMs float64, success bool, model string) {
	e.Emit(map[string]interface{}{
		"event":        "task_complete",
		"task":         task,
		"steps":        steps,
		"total_cost":   totalCost,
		"total_tokens": totalTokens,
		"duration_ms":  durationMs,
		"success":      success,
		"model":        model,
	})
}

// EmitVerification records a code verification result.
func (e *EventEmitter) EmitVerification(task, level string, score float64, compiled, testsPassed bool) {
	e.Emit(map[string]interface{}{
		"event":        "verification",
		"task":         task,
		"level":        level,
		"score":        score,
		"compiled":     compiled,
		"tests_passed": testsPassed,
	})
}

// EmitStrategy records a learned strategy.
func (e *EventEmitter) EmitStrategy(taskType, strategy, improvement string) {
	e.Emit(map[string]interface{}{
		"event":       "strategy",
		"task_type":   taskType,
		"strategy":    strategy,
		"improvement": improvement,
	})
}
