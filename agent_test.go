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

// openAIResp wraps a chat-completion message with a reported token usage.
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

// TestManagerDelegatesToManagedAgent verifies the agent-as-tool flow.
func TestManagerDelegatesToManagedAgent(t *testing.T) {
	subSrv, _ := fakeServer(t, openAIResp(finalAnswerMsg("42"), 5, 3))
	mgrSrv, _ := fakeServer(t,
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
	if res.Usage.Calls < 2 {
		t.Fatalf("usage.Calls = %d, want >= 2", res.Usage.Calls)
	}
}

// TestEiffelTowerDemo is the original demo scenario moved into tests.
func TestEiffelTowerDemo(t *testing.T) {
	stubSearch := &stubTool{
		name: "web_search",
		desc: "Search the web for factual information.",
		params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
			"required": []string{"query"},
		},
		fn: func(_ context.Context, _ string) (string, error) {
			return "The Eiffel Tower was built from 28 January 1887 to 31 March 1889.", nil
		},
	}

	researcherSrv, _ := fakeServer(t,
		openAIResp(toolCallMsg("r1", "web_search", `{"query":"Eiffel Tower construction dates"}`), 8, 3),
		openAIResp(finalAnswerMsg("Built 28 January 1887 to 31 March 1889"), 12, 4),
	)
	mathSrv, _ := fakeServer(t,
		openAIResp(toolCallMsg("c1", "calculator", `{"a":794,"b":1,"op":"+"}`), 6, 2),
		openAIResp(finalAnswerMsg("794"), 10, 2),
	)
	mgrSrv, _ := fakeServer(t,
		openAIResp(toolCallMsg("m1", "researcher", `{"task":"find Eiffel construction dates"}`), 15, 5),
		openAIResp(toolCallMsg("m2", "mathematician", `{"task":"days between 1887-01-28 and 1889-03-31"}`), 18, 4),
		openAIResp(finalAnswerMsg("794 days"), 20, 3),
	)

	resReg := NewToolRegistry()
	resReg.MustRegister(stubSearch)
	researcher := &Agent{
		Name: "researcher", Description: "Finds factual information using web search.",
		Model: &OpenAIModel{cfg: ModelConfig{BaseURL: researcherSrv.URL, ModelID: "fake"}},
		Registry: resReg, MaxSteps: 5,
	}

	mathReg := NewToolRegistry()
	mathReg.MustRegister(NewCalculatorTool())
	mathematician := &Agent{
		Name: "mathematician", Description: "Performs precise arithmetic calculations.",
		Model: &OpenAIModel{cfg: ModelConfig{BaseURL: mathSrv.URL, ModelID: "fake"}},
		Registry: mathReg, MaxSteps: 5,
	}

	manager := &Agent{
		Name: "manager", Description: "Coordinates team members.",
		Model: &OpenAIModel{cfg: ModelConfig{BaseURL: mgrSrv.URL, ModelID: "fake"}},
		ManagedAgents: []*Agent{researcher, mathematician}, MaxSteps: 10,
	}

	task := "The Eiffel Tower was built between 28 January 1887 and 31 March 1889. " +
		"How many days did its construction last in total? Use your team members for facts and math."
	res, err := manager.Run(context.Background(), task)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Answer, "794") {
		t.Fatalf("final answer = %q, want something containing 794", res.Answer)
	}
}

// TestAgentUsesPluginTools verifies a single agent can call registry plugins.
func TestAgentUsesPluginTools(t *testing.T) {
	ws := t.TempDir()
	reg := DefaultRegistry(ws)

	srv, _ := fakeServer(t,
		openAIResp(toolCallMsg("t1", "write", `{"path":"hi.txt","content":"plugin-ok"}`), 5, 2),
		openAIResp(toolCallMsg("t2", "read", `{"path":"hi.txt"}`), 6, 2),
		openAIResp(finalAnswerMsg("plugin-ok"), 7, 2),
	)

	agent := &Agent{
		Name: "assistant", Model: &OpenAIModel{cfg: ModelConfig{BaseURL: srv.URL, ModelID: "fake"}},
		Registry: reg, MaxSteps: 5,
	}
	res, err := agent.Run(context.Background(), "write hi.txt and read it back")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Answer != "plugin-ok" {
		t.Fatalf("answer = %q", res.Answer)
	}
}

// stubTool is a minimal Tool for tests.
type stubTool struct {
	name, desc string
	params     any
	fn         func(ctx context.Context, argsJSON string) (string, error)
}

func (s *stubTool) Spec() FunctionSpec {
	return FunctionSpec{Name: s.name, Description: s.desc, Parameters: s.params}
}
func (s *stubTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	return s.fn(ctx, argsJSON)
}
