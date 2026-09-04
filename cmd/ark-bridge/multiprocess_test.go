package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// These tests spawn INDEPENDENT bridge PROCESSES (built from the current source) sharing one
// durable store, so the cross-process single-winner / replay guarantees are exercised end to end
// in CI — not just via in-process handles. Deterministic and fast (small process counts, no sleeps).

func buildBridge(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	bin := filepath.Join(t.TempDir(), "ark-bridge-mp")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build bridge from current source: %v\n%s", err, out)
	}
	return bin
}

// driveSession runs ONE bridge process: sends each command (line-delimited JSON), collects the
// replies, then finishes the session cleanly.
func driveSession(t *testing.T, bin, authzDir string, cmds []map[string]any) []map[string]any {
	t.Helper()
	c := exec.Command(bin, "--session")
	c.Env = append(os.Environ(), "ARK_AUTHZ_DIR="+authzDir)
	stdin, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReaderSize(stdout, 1<<20)
	send := func(m map[string]any) map[string]any {
		b, _ := json.Marshal(m)
		stdin.Write(append(b, '\n'))
		line, _ := r.ReadBytes('\n')
		var reply map[string]any
		_ = json.Unmarshal(line, &reply)
		return reply
	}
	out := make([]map[string]any, 0, len(cmds))
	for _, m := range cmds {
		out = append(out, send(m))
	}
	send(map[string]any{"cmd": "finish", "success": true})
	stdin.Close()
	_ = c.Wait()
	return out
}

func rankEv() map[string]any {
	return map[string]any{"requested_rank": 2, "evidence_complete": true,
		"options": []any{map[string]any{"id": "A", "price": 100}, map[string]any{"id": "B", "price": 200}}}
}

func startCmd(runID string) map[string]any {
	return map[string]any{"cmd": "start", "supervision": "experimental", "run_id": runID}
}

func checkB() map[string]any {
	return map[string]any{"cmd": "check", "action": "tool_call", "tool": "book", "constraint": "rank",
		"scope": "order-1", "transaction_id": "txn-1", "proposed": map[string]any{"option": "B"}, "evidence": rankEv()}
}

func consumeB(authID string) map[string]any {
	return map[string]any{"cmd": "consume", "authorization_id": authID,
		"executed_action": map[string]any{"option": "B"}}
}

// A: N independent processes race to consume ONE durable authorization -> exactly one wins.
func TestMultiProcess_ExactlyOneConsumer(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process test skipped in -short")
	}
	bin := buildBridge(t)
	dir := t.TempDir()

	issue := driveSession(t, bin, dir, []map[string]any{startCmd("issuer"), checkB()})
	if issue[1]["verdict"] != "ALLOW" {
		t.Fatalf("issuer check must ALLOW; got %v", issue[1])
	}
	authID, _ := issue[1]["authorization_id"].(string)
	if authID == "" {
		t.Fatalf("no authorization_id in issuer reply: %v", issue[1])
	}

	const procs = 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	cleared := 0
	for i := 0; i < procs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			reply := driveSession(t, bin, dir, []map[string]any{startCmd("c"), consumeB(authID)})
			if reply[1]["cleared"] == true {
				mu.Lock()
				cleared++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if cleared != 1 {
		t.Fatalf("exactly one independent process must consume the authorization; got %d", cleared)
	}
}

// B: a consumed authorization is refused by a LATER, independent process ("restart" / replay).
func TestMultiProcess_ReplayAfterRestartRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process test skipped in -short")
	}
	bin := buildBridge(t)
	dir := t.TempDir()

	issue := driveSession(t, bin, dir, []map[string]any{startCmd("p1"), checkB()})
	authID := issue[1]["authorization_id"].(string)
	// process 1 consumes
	c1 := driveSession(t, bin, dir, []map[string]any{startCmd("p1b"), consumeB(authID)})
	if c1[1]["cleared"] != true {
		t.Fatalf("first consume must clear; got %v", c1[1])
	}
	// process 2 (a fresh process = restart) must be refused
	c2 := driveSession(t, bin, dir, []map[string]any{startCmd("p2"), consumeB(authID)})
	if c2[1]["error"] == nil {
		t.Fatalf("a consumed authorization must be refused by a later process; got %v", c2[1])
	}
}

// C: a durable store the process cannot write fails CLOSED across processes (unavailable store).
func TestMultiProcess_UnavailableStoreFailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-process test skipped in -short")
	}
	if os.Getuid() == 0 {
		t.Skip("running as root bypasses directory permissions")
	}
	bin := buildBridge(t)
	base := t.TempDir()
	// ARK_AUTHZ_DIR under a regular file -> the store cannot be created -> check fails closed.
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	reply := driveSession(t, bin, filepath.Join(blocker, "store"),
		[]map[string]any{startCmd("p"), checkB()})
	if reply[1]["error"] == nil {
		t.Fatalf("check against an unavailable durable store must fail closed; got %v", reply[1])
	}
}
