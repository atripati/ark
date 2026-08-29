package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/atripati/ark/pkg/supervise"
)

// runSupervise is the EXPERIMENTAL constrained-supervision entry point. It is a pure
// judge-and-gate mechanism: read a supervision Request as JSON (stdin), emit the Decision as
// JSON (stdout). It never authors an action. Off by default — gated behind
// ARK_EXPERIMENTAL_SUPERVISION=1 so it is not part of ARK's default behavior.
//
//	echo '{"constraint":"rank","proposed":{"option":"A"},"evidence":{...},"retry_count":0,"budget":4}' \
//	  | ARK_EXPERIMENTAL_SUPERVISION=1 ark supervise
//
// --batch reads one Request per line and emits one Decision per line (JSONL).
func runSupervise() {
	if os.Getenv("ARK_EXPERIMENTAL_SUPERVISION") != "1" {
		fmt.Fprintln(os.Stderr, "ark supervise is EXPERIMENTAL and off by default.")
		fmt.Fprintln(os.Stderr, "Enable with: ARK_EXPERIMENTAL_SUPERVISION=1")
		os.Exit(2)
	}
	batch := false
	for _, a := range os.Args[2:] {
		if a == "--batch" {
			batch = true
		}
	}
	sup := supervise.New()

	if batch {
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
		out := bufio.NewWriter(os.Stdout)
		defer out.Flush()
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			emit(out, evalOne(sup, line))
		}
		return
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read stdin: %v\n", err)
		os.Exit(1)
	}
	emit(os.Stdout, evalOne(sup, data))
}

func evalOne(sup *supervise.Supervisor, raw []byte) supervise.Decision {
	var req supervise.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return supervise.Decision{Verdict: supervise.Allow,
			Reason: "unparseable request; failing open to ALLOW: " + err.Error()}
	}
	return sup.Evaluate(req)
}

func emit(w io.Writer, d supervise.Decision) {
	b, _ := json.Marshal(d)
	fmt.Fprintln(w, string(b))
}
