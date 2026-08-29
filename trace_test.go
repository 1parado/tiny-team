package main

import (
	"context"
	"testing"
)

// TestTracerDelegationDepth verifies the trace records the delegation as a
// nested frame: the manager's events at depth 0, the sub-agent's events one
// level deeper.
func TestTracerDelegationDepth(t *testing.T) {
	subSrv, _ := fakeServer(t, openAIResp(finalAnswerMsg("42"), 5, 3))
	mgrSrv, _ := fakeServer(t,
		openAIResp(toolCallMsg("m1", "mathematician", `{"task":"compute 6*7"}`), 10, 4),
		openAIResp(finalAnswerMsg("42"), 20, 2),
	)

	tr := NewTracer()
	sub := &Agent{Name: "mathematician", Description: "Performs calculations.",
		Model: &OpenAIModel{cfg: ModelConfig{BaseURL: subSrv.URL, ModelID: "fake"}}, Tracer: tr}
	mgr := &Agent{Name: "manager", Model: &OpenAIModel{cfg: ModelConfig{BaseURL: mgrSrv.URL, ModelID: "fake"}},
		ManagedAgents: []*Agent{sub}, Tracer: tr}

	res, err := mgr.Run(context.Background(), "compute 6*7")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Answer != "42" {
		t.Fatalf("final answer = %q, want %q", res.Answer, "42")
	}

	events, done := tr.Snapshot()
	if done {
		t.Errorf("done = true before SetDone was called")
	}
	var sawDelegateStart, sawDelegateEnd, sawNestedStart, sawNestedFinal bool
	for _, e := range events {
		switch {
		case e.Type == "delegate_start" && e.Agent == "manager":
			sawDelegateStart = e.Depth == 0 && e.ToolName == "mathematician"
		case e.Type == "delegate_end" && e.Agent == "manager":
			sawDelegateEnd = e.Depth == 0 && e.Text == "42"
		case e.Agent == "mathematician" && e.Type == "run_start":
			sawNestedStart = e.Depth == 1
		case e.Agent == "mathematician" && e.Type == "final":
			sawNestedFinal = e.Depth == 1 && e.Text == "42"
		}
	}
	if !sawDelegateStart || !sawDelegateEnd || !sawNestedStart || !sawNestedFinal {
		t.Errorf("incomplete trace: delegateStart=%v delegateEnd=%v nestedStart=%v nestedFinal=%v\nevents: %+v",
			sawDelegateStart, sawDelegateEnd, sawNestedStart, sawNestedFinal, events)
	}
}
