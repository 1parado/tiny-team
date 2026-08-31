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

type calculatorTool struct{}

func NewCalculatorTool() Tool { return calculatorTool{} }

func (calculatorTool) Spec() FunctionSpec {
	return FunctionSpec{
		Name:        "calculator",
		Description: "Evaluate a simple arithmetic expression with + - * / and parentheses.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"expression": map[string]any{"type": "string", "description": "Arithmetic expression, e.g. \"(1+2)*3\"."},
			},
			"required": []string{"expression"},
		},
	}
}

func (calculatorTool) Execute(_ context.Context, argsJSON string) (string, error) {
	var args struct {
		Expression string `json:"expression"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	expr := strings.TrimSpace(args.Expression)
	if expr == "" {
		return "", fmt.Errorf("empty expression")
	}
	v, err := evalArith(expr)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%g", v), nil
}

func evalArith(expr string) (float64, error) {
	p := &arithParser{s: expr}
	v, err := p.parseExpr()
	if err != nil {
		return 0, err
	}
	p.skip()
	if p.i < len(p.s) {
		return 0, fmt.Errorf("unexpected trailing input at %d", p.i)
	}
	return v, nil
}

type arithParser struct {
	s string
	i int
}

func (p *arithParser) skip() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}

func (p *arithParser) parseExpr() (float64, error) {
	v, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for {
		p.skip()
		if p.i >= len(p.s) {
			return v, nil
		}
		op := p.s[p.i]
		if op != '+' && op != '-' {
			return v, nil
		}
		p.i++
		w, err := p.parseTerm()
		if err != nil {
			return 0, err
		}
		if op == '+' {
			v += w
		} else {
			v -= w
		}
	}
}

func (p *arithParser) parseTerm() (float64, error) {
	v, err := p.parseFactor()
	if err != nil {
		return 0, err
	}
	for {
		p.skip()
		if p.i >= len(p.s) {
			return v, nil
		}
		op := p.s[p.i]
		if op != '*' && op != '/' {
			return v, nil
		}
		p.i++
		w, err := p.parseFactor()
		if err != nil {
			return 0, err
		}
		if op == '*' {
			v *= w
		} else {
			if w == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			v /= w
		}
	}
}

func (p *arithParser) parseFactor() (float64, error) {
	p.skip()
	if p.i >= len(p.s) {
		return 0, fmt.Errorf("unexpected end of expression")
	}
	if p.s[p.i] == '(' {
		p.i++
		v, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		p.skip()
		if p.i >= len(p.s) || p.s[p.i] != ')' {
			return 0, fmt.Errorf("missing )")
		}
		p.i++
		return v, nil
	}
	if p.s[p.i] == '+' {
		p.i++
		return p.parseFactor()
	}
	if p.s[p.i] == '-' {
		p.i++
		v, err := p.parseFactor()
		return -v, err
	}
	start := p.i
	for p.i < len(p.s) && ((p.s[p.i] >= '0' && p.s[p.i] <= '9') || p.s[p.i] == '.') {
		p.i++
	}
	if start == p.i {
		return 0, fmt.Errorf("expected number at %d", p.i)
	}
	var v float64
	_, err := fmt.Sscanf(p.s[start:p.i], "%f", &v)
	return v, err
}

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
					"description": "Shell command template. Use {{param}} placeholders that match property names. " +
						"Example: \"wc -l {{path}}\"",
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
