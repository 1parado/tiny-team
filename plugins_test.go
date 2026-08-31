package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tempWorkspace(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func TestReadWriteListRoundTrip(t *testing.T) {
	ws := tempWorkspace(t)
	read := NewReadTool(ws)
	write := NewWriteTool(ws)
	list := NewListDirTool(ws)
	out, err := write.Execute(context.Background(), mustJSON(map[string]string{"path": "notes/hello.txt", "content": "hello plugin world"}))
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
	params := map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}}
	args, _ := json.Marshal(map[string]any{"name": "count_lines", "description": "Count lines in a file", "parameters": params, "command": "wc -l {{path}}"})
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
	for _, name := range []string{"read", "write", "edit", "list_dir", "shell", "search", "calculator", "create_tool"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("missing plugin %q", name)
		}
	}
}

func TestEditUniqueReplace(t *testing.T) {
	ws := tempWorkspace(t)
	path := filepath.Join(ws, "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := NewEditTool(ws)
	out, err := edit.Execute(context.Background(), mustJSON(map[string]any{"path": "sample.txt", "old_str": "beta", "new_str": "BETA"}))
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !strings.Contains(out, "edited") {
		t.Fatalf("unexpected result: %s", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "alpha\nBETA\ngamma\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestEditNotFound(t *testing.T) {
	ws := tempWorkspace(t)
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("only-this\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := NewEditTool(ws)
	_, err := edit.Execute(context.Background(), mustJSON(map[string]any{"path": "a.txt", "old_str": "missing", "new_str": "x"}))
	if err == nil {
		t.Fatal("expected error when old_str is absent")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v", err)
	}
}

func TestEditAmbiguousWithoutReplaceAll(t *testing.T) {
	ws := tempWorkspace(t)
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("xx\nxx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := NewEditTool(ws)
	_, err := edit.Execute(context.Background(), mustJSON(map[string]any{"path": "a.txt", "old_str": "xx", "new_str": "yy"}))
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	if !strings.Contains(err.Error(), "matches") {
		t.Fatalf("error = %v", err)
	}
}

func TestEditReplaceAll(t *testing.T) {
	ws := tempWorkspace(t)
	path := filepath.Join(ws, "a.txt")
	if err := os.WriteFile(path, []byte("xx\nxx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := NewEditTool(ws)
	_, err := edit.Execute(context.Background(), mustJSON(map[string]any{"path": "a.txt", "old_str": "xx", "new_str": "yy", "replace_all": true}))
	if err != nil {
		t.Fatalf("edit replace_all: %v", err)
	}
	data, _ := os.ReadFile(path)
	if got := string(data); got != "yy\nyy\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestEditPathEscapeRejected(t *testing.T) {
	ws := tempWorkspace(t)
	edit := NewEditTool(ws)
	_, err := edit.Execute(context.Background(), mustJSON(map[string]any{"path": "../outside.txt", "old_str": "a", "new_str": "b"}))
	if err == nil {
		t.Fatal("expected path escape to be rejected")
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{"hello": "'hello'", "x; rm -rf /": "'x; rm -rf /'"}
	cases["it's"] = "'" + strings.ReplaceAll("it's", "'", `'\''`) + "'"
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Fatalf("shellQuote(%q)=%q want %q", in, got, want)
		}
	}
}

func TestCreateToolQuotesArgs(t *testing.T) {
	dir := t.TempDir()
	risky := "hello world.txt"
	if err := os.WriteFile(filepath.Join(dir, risky), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := DefaultRegistry(dir)
	ct, ok := reg.Get("create_tool")
	if !ok {
		t.Fatal("create_tool missing")
	}
	args := `{"name":"count_lines","description":"count lines","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]},"command":"wc -l {{path}}"}`
	if _, err := ct.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	tool, ok := reg.Get("count_lines")
	if !ok {
		t.Fatal("count_lines not registered")
	}
	out, err := tool.Execute(context.Background(), `{"path":"hello world.txt"}`)
	if err != nil {
		t.Fatalf("execute: %v (out=%q)", err, out)
	}
	if !strings.Contains(out, "2") && !strings.Contains(out, "hello") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestCreateToolRejectsBadNames(t *testing.T) {
	reg := DefaultRegistry(t.TempDir())
	ct, _ := reg.Get("create_tool")
	for _, name := range []string{"", "final_answer", "create_tool", "bad-name", "has space"} {
		args := fmt.Sprintf(`{"name":%q,"description":"x","parameters":{},"command":"echo hi"}`, name)
		if _, err := ct.Execute(context.Background(), args); err == nil {
			t.Fatalf("expected error for name %q", name)
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
