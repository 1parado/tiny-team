package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// search
// ---------------------------------------------------------------------------

type searchTool struct{ workspace string }

func NewSearchTool(workspace string) Tool { return &searchTool{workspace: workspace} }

func (t *searchTool) Spec() FunctionSpec {
	return FunctionSpec{
		Name:        "search",
		Description: "Recursively search for a text pattern in files under the workspace.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Substring to search for."},
				"path":  map[string]any{"type": "string", "description": "Optional subdirectory. Defaults to root."},
			},
			"required": []string{"query"},
		},
	}
}

func (t *searchTool) Execute(_ context.Context, argsJSON string) (string, error) {
	var args struct {
		Query string `json:"query"`
		Path  string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	if args.Query == "" {
		return "", fmt.Errorf("empty query")
	}
	if args.Path == "" {
		args.Path = "."
	}
	root, err := resolvePath(t.workspace, args.Path)
	if err != nil {
		return "", err
	}
	const maxMatches = 50
	const maxFileSize = 1 << 20
	var matches []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".exe") || name == "go.sum" {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxFileSize {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if strings.Contains(line, args.Query) {
				matches = append(matches, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
				if len(matches) >= maxMatches {
					return io.EOF
				}
			}
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return "", err
	}
	if len(matches) == 0 {
		return "no matches", nil
	}
	out := strings.Join(matches, "\n")
	if len(matches) >= maxMatches {
		out += fmt.Sprintf("\n... (stopped after %d matches)", maxMatches)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// calculator
// ---------------------------------------------------------------------------

type calculatorTool struct{}

func NewCalculatorTool() Tool { return calculatorTool{} }

func (calculatorTool) Spec() FunctionSpec {
	return FunctionSpec{
		Name:        "calculator",
		Description: "Compute arithmetic on two numbers. Supports +, -, *, /.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"a":  map[string]any{"type": "number", "description": "First operand."},
				"b":  map[string]any{"type": "number", "description": "Second operand."},
				"op": map[string]any{"type": "string", "description": "One of \"+\", \"-\", \"*\", \"/\"."},
			},
			"required": []string{"a", "b", "op"},
		},
	}
}

func (calculatorTool) Execute(_ context.Context, argsJSON string) (string, error) {
	var args struct {
		A  float64 `json:"a"`
		B  float64 `json:"b"`
		Op string  `json:"op"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	var out float64
	switch args.Op {
	case "+":
		out = args.A + args.B
	case "-":
		out = args.A - args.B
	case "*":
		out = args.A * args.B
	case "/":
		if args.B == 0 {
			return "", fmt.Errorf("division by zero")
		}
		out = args.A / args.B
	default:
		return "", fmt.Errorf("unsupported op %q", args.Op)
	}
	return fmt.Sprintf("%g", out), nil
}

// ---------------------------------------------------------------------------
// create_tool
// ---------------------------------------------------------------------------

type createTool struct {
	registry  *ToolRegistry
	workspace string
}

func NewCreateTool(registry *ToolRegistry, workspace string) Tool {
	return &createTool{registry: registry, workspace: workspace}
}

func (t *createTool) Spec() FunctionSpec {
	return FunctionSpec{
		Name: "create_tool",
		Description: "Author a new tool at runtime and register it into the plugin catalogue. " +
			"The tool is backed by a shell command template: occurrences of " +
			"{{param}} are replaced with argument values (shell-quoted).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Unique tool name ([A-Za-z0-9_], max 64).",
				},
				"description": map[string]any{
					"type":        "string",
					"description": "What the tool does.",
				},
				"parameters": map[string]any{
					"type":        "object",
					"description": "JSON Schema object describing the tool arguments.",
				},
				"command": map[string]any{
					"type": "string",
					"description": "Shell command template. Use {{param}} placeholders. Example: \"wc -l {{path}}\"",
				},
			},
			"required": []string{"name", "description", "parameters", "command"},
		},
	}
}

func (t *createTool) Execute(_ context.Context, argsJSON string) (string, error) {
	var args struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
		Command     string          `json:"command"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	name := strings.TrimSpace(args.Name)
	if name == finalAnswerName || name == "create_tool" || !validToolName(name) {
		return "", fmt.Errorf("invalid or reserved tool name %q (use [A-Za-z0-9_]{1,64})", args.Name)
	}
	if strings.TrimSpace(args.Command) == "" {
		return "", fmt.Errorf("command template is empty")
	}
	if len(args.Parameters) == 0 {
		args.Parameters = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	var params any
	if err := json.Unmarshal(args.Parameters, &params); err != nil {
		return "", fmt.Errorf("parameters must be a JSON object: %w", err)
	}
	dyn := &dynamicShellTool{
		name: name, description: args.Description, parameters: params,
		command: args.Command, workspace: t.workspace,
	}
	if err := t.registry.Register(dyn); err != nil {
		return "", err
	}
	return fmt.Sprintf("tool %q registered — you can call it now", name), nil
}

type dynamicShellTool struct {
	name, description, command, workspace string
	parameters                             any
}

func (t *dynamicShellTool) Spec() FunctionSpec {
	return FunctionSpec{Name: t.name, Description: t.description, Parameters: t.parameters}
}

func (t *dynamicShellTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var argMap map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &argMap); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	cmd := t.command
	for k, v := range argMap {
		cmd = strings.ReplaceAll(cmd, "{{" + k + "}}", shellQuote(fmt.Sprint(v)))
	}
	sh := &shellTool{workspace: t.workspace, timeout: 30 * time.Second}
	return sh.Execute(ctx, mustMarshalCompact(map[string]string{"command": cmd}))
}
