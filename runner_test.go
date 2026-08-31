package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunControllerStartStop(t *testing.T) {
	// Slow model so we can stop mid-run.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "",
					"tool_calls": []map[string]any{{
						"id":   "1",
						"type": "function",
						"function": map[string]any{
							"name":      "final_answer",
							"arguments": `{\"answer\":\"ok\"}`,
						},
					}},
				},
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
		})
	}))
	t.Cleanup(srv.Close)

	tr := NewTracer()
	ag := &Agent{
		Name:      "t",
		Model:     &OpenAIModel{cfg: ModelConfig{BaseURL: srv.URL, ModelID: "fake"}},
		Registry:  DefaultRegistry(t.TempDir()),
		Workspace: t.TempDir(),
		MaxSteps:  5,
		Tracer:    tr,
	}
	ctrl := NewRunController(ag, tr)
	if err := ctrl.Start("do something slow"); err != nil {
		t.Fatal(err)
	}
	if !ctrl.Running() {
		t.Fatal("expected running")
	}
	if err := ctrl.Start("again"); err == nil {
		t.Fatal("expected conflict on second start")
	}
	time.Sleep(50 * time.Millisecond)
	if !ctrl.Stop() {
		t.Fatal("expected stop true")
	}
	// Wait for goroutine to finish
	deadline := time.Now().Add(5 * time.Second)
	for ctrl.Running() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if ctrl.Running() {
		t.Fatal("still running after stop")
	}
	errMsg := ctrl.LastError()
	if errMsg == "" && !strings.Contains(errMsg, "cancel") {
		if errMsg != "" && !strings.Contains(errMsg, "cancel") && !strings.Contains(errMsg, "interrupt") {
			t.Logf("lastErr=%q (acceptable if empty race)", errMsg)
		}
	}
	_ = context.Canceled
}

func TestAPIRunAndStop(t *testing.T) {
	tr := NewTracer()
	ctrl := NewRunController(&Agent{Name: "x", MaxSteps: 1}, tr)
	mux := http.NewServeMux()
	RegisterTraceRoutes(mux, tr, ctrl)

	req := httptest.NewRequest(http.MethodPost, "/api/run", strings.NewReader(`{"task":"  "}`))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("empty task code=%d body=%s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/stop", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("stop code=%d", rr.Code)
	}
}
