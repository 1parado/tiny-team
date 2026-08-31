package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorReportsWorkspaceAndTools(t *testing.T) {
	ws := tempWorkspace(t)
	reg := DefaultRegistry(ws)
	doc, ok := reg.Get("doctor")
	if !ok {
		t.Fatal("doctor not registered")
	}
	out, err := doc.Execute(context.Background(), `{"verbose":true}`)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	for _, want := range []string{"workspace", "read", "doctor", "status:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor report missing %q:\n%s", want, out)
		}
	}
}

func TestDoctorDoesNotExposeSecrets(t *testing.T) {
	ws := tempWorkspace(t)
	t.Setenv("API_KEY", "super-secret-value")
	t.Setenv("MODEL", "test-model")
	reg := DefaultRegistry(ws)
	doc, _ := reg.Get("doctor")
	out, err := doc.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if strings.Contains(out, "super-secret-value") {
		t.Fatalf("doctor leaked API_KEY value:\n%s", out)
	}
	if !strings.Contains(out, "env API_KEY: set") {
		t.Fatalf("expected API_KEY set marker:\n%s", out)
	}
}

func TestLogEvolutionCreatesAndAppends(t *testing.T) {
	ws := tempWorkspace(t)
	reg := DefaultRegistry(ws)
	logTool, ok := reg.Get("log_evolution")
	if !ok {
		t.Fatal("log_evolution not registered")
	}
	_, err := logTool.Execute(context.Background(), mustJSON(map[string]string{
		"summary": "first evolution",
		"files":   "plugins.go",
		"tests":   "PASS",
	}))
	if err != nil {
		t.Fatalf("first log: %v", err)
	}
	_, err = logTool.Execute(context.Background(), mustJSON(map[string]string{
		"summary": "second evolution",
	}))
	if err != nil {
		t.Fatalf("second log: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(ws, "EVOLUTION_LOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "# Evolution log") {
		t.Fatalf("missing header: %s", text)
	}
	if !strings.Contains(text, "first evolution") || !strings.Contains(text, "second evolution") {
		t.Fatalf("missing entries: %s", text)
	}
	if strings.Count(text, "## ") < 2 {
		t.Fatalf("expected at least 2 entries: %s", text)
	}
}
