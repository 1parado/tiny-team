package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeServer returns scripted HTTP responses in order and records every
// request body it received. Scripts are full JSON response bodies.
func fakeServer(t *testing.T, responses ...string) (*httptest.Server, *[]string) {
	t.Helper()
	var (
		mu     sync.Mutex
		call   int
		bodies []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if call >= len(responses) {
			t.Errorf("unexpected request #%d: %s", call+1, body)
			http.Error(w, "script exhausted", http.StatusInternalServerError)
			return
		}
		call++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, responses[call-1])
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

// openAIResp wraps a chat-completion message with a reported token usage, so
// tests exercise the usage-parsing path instead of local estimation.
func openAIResp(messageJSON string, promptTokens, completionTokens int) string {
	return fmt.Sprintf(`{"choices":[{"message":%s}],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`,
		messageJSON, promptTokens, completionTokens)
}

func toolCallMsg(id, name, argsJSON string) string {
	return fmt.Sprintf(`{"role":"assistant","content":"","tool_calls":[{"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]}`,
		id, name, argsJSON)
}

func finalAnswerMsg(answer string) string {
	return toolCallMsg("c1", finalAnswerName, fmt.Sprintf(`{"answer":%q}`, answer))
}

// TestManagerDelegatesToManagedAgent verifies the agent-as-tool flow: the
// manager calls a managed agent, whose final report comes back as the
// manager's observation, and whose token usage is aggregated into the run.
func TestManagerDelegatesToManagedAgent(t *testing.T) {
	subSrv, _ := fakeServer(t, openAIResp(finalAnswerMsg("42"), 5, 3))
	mgrSrv, mgrBodies := fakeServer(t,
		openAIResp(toolCallMsg("m1", "mathematician", `{"task":"compute 6*7"}`), 10, 4),
		openAIResp(finalAnswerMsg("42"), 20, 2),
	)

	sub := &Agent{Name: "mathematician", Description: "Performs calculations.",
		Model: &OpenAIModel{cfg: ModelConfig{BaseURL: subSrv.URL, ModelID: "fake"}}}
	mgr := &Agent{Name: "manager", Model: &OpenAIModel{cfg: ModelConfig{BaseURL: mgrSrv.URL, ModelID: "fake"}},
		ManagedAgents: []*Agent{sub}}

	res, err := mgr.Run(context.Background(), "compute 6*7")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Answer != "42" {
		t.Fatalf("final answer = %q, want %q", res.Answer, "42")
	}
	// Manager made 2 calls (10+20 prompt, 4+2 completion); the delegated
	// sub-agent made 1 call (5 prompt, 3 completion).
	wantUsage := TokenUsage{Calls: 3, PromptTokens: 35, CompletionTokens: 9}
	if res.Usage != wantUsage {
		t.Errorf("usage = %+v, want %+v", res.Usage, wantUsage)
	}
	if len(*mgrBodies) != 2 {
		t.Fatalf("manager made %d model calls, want 2", len(*mgrBodies))
	}
	// The sub-agent's report "42" must appear in the manager's memory on the
	// second call as a tool observation.
	if !strings.Contains((*mgrBodies)[1], `"role":"tool"`) || !strings.Contains((*mgrBodies)[1], "42") {
		t.Errorf("second manager request lacks tool observation with sub-agent report:\n%s", (*mgrBodies)[1])
	}
}

// TestUnknownToolSelfCorrection verifies that an unknown tool call becomes an
// error observation so the model can retry instead of crashing the run.
func TestUnknownToolSelfCorrection(t *testing.T) {
	srv, _ := fakeServer(t,
		openAIResp(toolCallMsg("m1", "no_such_tool", `{}`), 7, 2),
		openAIResp(finalAnswerMsg("recovered"), 7, 2),
	)
	agent := &Agent{Name: "solo", Model: &OpenAIModel{cfg: ModelConfig{BaseURL: srv.URL, ModelID: "fake"}}}

	res, err := agent.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Answer != "recovered" {
		t.Fatalf("final answer = %q, want %q", res.Answer, "recovered")
	}
	if res.Usage != (TokenUsage{Calls: 2, PromptTokens: 14, CompletionTokens: 4}) {
		t.Errorf("usage = %+v, want 2 calls with 14 prompt / 4 completion tokens", res.Usage)
	}
}

// TestMaxStepsReached verifies the run fails after MaxSteps without a final
// answer, mirroring smolagents' max-steps guard.
func TestMaxStepsReached(t *testing.T) {
	srv, _ := fakeServer(t,
		openAIResp(toolCallMsg("m1", "no_such_tool", `{}`), 1, 1),
		openAIResp(toolCallMsg("m2", "no_such_tool", `{}`), 1, 1),
	)
	agent := &Agent{Name: "solo", Model: &OpenAIModel{cfg: ModelConfig{BaseURL: srv.URL, ModelID: "fake"}}, MaxSteps: 2}

	_, err := agent.Run(context.Background(), "task")
	if err == nil || !strings.Contains(err.Error(), "max_steps") {
		t.Fatalf("err = %v, want max_steps error", err)
	}
}

// TestAnthropicProtocol verifies the anthropic mapping: tool_use blocks in the
// response become ToolCalls, usage is read from input/output tokens, and
// final_answer terminates the run.
func TestAnthropicProtocol(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			fmt.Fprint(w, `{"content":[{"type":"text","text":"thinking"},{"type":"tool_use","id":"tu1","name":"final_answer","input":{"answer":"hi from anthropic"}}],"usage":{"input_tokens":9,"output_tokens":4}}`)
			return
		}
		t.Errorf("unexpected extra request after final_answer")
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	model, err := NewModel(ModelConfig{Protocol: "anthropic", BaseURL: srv.URL, ModelID: "fake", APIKey: "k"})
	if err != nil {
		t.Fatalf("NewModel: %v", err)
	}
	agent := &Agent{Name: "solo", Model: model}

	res, err := agent.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Answer != "hi from anthropic" {
		t.Fatalf("final answer = %q, want %q", res.Answer, "hi from anthropic")
	}
	if res.Usage != (TokenUsage{Calls: 1, PromptTokens: 9, CompletionTokens: 4}) {
		t.Errorf("usage = %+v, want 1 call with 9 prompt / 4 completion tokens", res.Usage)
	}
}
