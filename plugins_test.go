package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tempWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir
}

func TestReadWriteListRoundTrip(t *testing.T) {
	ws := tempWorkspace(t)
	read := NewReadTool(ws)
	write := NewWriteTool(ws)
	list := NewListDirTool(ws)

	out, err := write.Execute(context.Background(), mustJSON(map[string]string{
		"path": "notes/hello.txt", "content": "hello plugin world",
	}))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(out, "wrote") {
		t.Fatalf("unexpected write result: %s", out)
	}

	out, err = read.Execute(context.Background(), mustJSON(map[string]string{"path": "notes/hello.txt"}))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if out != "hello plugin world" {
		t.Fatalf("read = %q, want hello plugin world", out)
	}

	out, err = list.Execute(context.Background(), mustJSON(map[string]string{"path": "notes"}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "hello.txt") {
		t.Fatalf("list missing hello.txt: %s", out)
	}
}

func TestPathEscapeRejected(t *testing.T) {
	ws := tempWorkspace(t)
	read := NewReadTool(ws)
	_, err := read.Execute(context.Background(), mustJSON(map[string]string{"path": "../../etc/passwd"}))
	if err == nil {
		t.Fatal("expected path escape to be rejected")
	}
}

func TestShellEcho(t *testing.T) {
	ws := tempWorkspace(t)
	sh := NewShellTool(ws)
	out, err := sh.Execute(context.Background(), mustJSON(map[string]string{"command": "echo hello-shell"}))
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	if !strings.Contains(out, "hello-shell") {
		t.Fatalf("shell output = %q", out)
	}
}

func TestSearchFindsContent(t *testing.T) {
	ws := tempWorkspace(t)
	if err := os.WriteFile(filepath.Join(ws, "data.txt"), []byte("alpha\nfind-me-please\nomega\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	search := NewSearchTool(ws)
	out, err := search.Execute(context.Background(), mustJSON(map[string]string{"query": "find-me-please"}))
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(out, "find-me-please") {
		t.Fatalf("search missed content: %s", out)
	}
}

func TestCalculator(t *testing.T) {
	calc := NewCalculatorTool()
	out, err := calc.Execute(context.Background(), mustJSON(map[string]any{"a": 6, "b": 7, "op": "*"}))
	if err != nil {
		t.Fatalf("calc: %v", err)
	}
	if out != "42" {
		t.Fatalf("calc = %q, want 42", out)
	}
}

func TestCreateToolRegistersAndRuns(t *testing.T) {
	ws := tempWorkspace(t)
	reg := NewToolRegistry()
	reg.MustRegister(NewShellTool(ws))
	create := NewCreateTool(reg, ws)
	reg.MustRegister(create)

	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
		"required": []string{"path"},
	}
	args, _ := json.Marshal(map[string]any{
		"name":        "count_lines",
		"description": "Count lines in a file",
		"parameters":   params,
		"command":     "wc -l {{path}}",
	})
	out, err := create.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("create_tool: %v", err)
	}
	if !strings.Contains(out, "registered") {
		t.Fatalf("unexpected create result: %s", out)
	}

	tool, ok := reg.Get("count_lines")
	if !ok {
		t.Fatal("count_lines not registered")
	}

	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = tool.Execute(context.Background(), mustJSON(map[string]string{"path": "a.txt"}))
	if err != nil {
		t.Fatalf("dynamic tool: %v", err)
	}
	if !strings.Contains(out, "3") {
		t.Fatalf("expected line count 3 in %q", out)
	}
}

func TestDefaultRegistryHasCorePlugins(t *testing.T) {
	ws := tempWorkspace(t)
	reg := DefaultRegistry(ws)
	for _, name := range []string{"read", "write", "list_dir", "shell", "search", "calculator", "create_tool"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("missing plugin %q", name)
		}
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
