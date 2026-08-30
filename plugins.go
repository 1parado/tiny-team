package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// resolvePath joins workspace and a user-supplied relative path, rejecting
// any attempt to escape the workspace via ".." or absolute paths.
func resolvePath(workspace, userPath string) (string, error) {
	if workspace == "" {
		workspace = "."
	}
	absWS, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	clean := filepath.Clean("/" + userPath)
	clean = strings.TrimPrefix(clean, "/")
	full := filepath.Join(absWS, clean)
	rel, err := filepath.Rel(absWS, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %q escapes workspace", userPath)
	}
	return full, nil
}

type readTool struct{ workspace string }

func NewReadTool(workspace string) Tool { return &readTool{workspace: workspace} }

func (t *readTool) Spec() FunctionSpec {
	return FunctionSpec{
		Name:        "read",
		Description: "Read the contents of a text file inside the workspace. Returns the file content or an error.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Relative path of the file to read."},
			},
			"required": []string{"path"},
		},
	}
}

func (t *readTool) Execute(_ context.Context, argsJSON string) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	full, err := resolvePath(t.workspace, args.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	const maxBytes = 64 * 1024
	if len(data) > maxBytes {
		return string(data[:maxBytes]) + fmt.Sprintf("\n\n... [truncated, file is %d bytes]", len(data)), nil
	}
	return string(data), nil
}

type writeTool struct{ workspace string }

func NewWriteTool(workspace string) Tool { return &writeTool{workspace: workspace} }

func (t *writeTool) Spec() FunctionSpec {
	return FunctionSpec{
		Name:        "write",
		Description: "Write content to a text file inside the workspace. Creates parent directories if needed.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Relative path of the file to write."},
				"content": map[string]any{"type": "string", "description": "The full content to write."},
			},
			"required": []string{"path", "content"},
		},
	}
}

func (t *writeTool) Execute(_ context.Context, argsJSON string) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	full, err := resolvePath(t.workspace, args.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(full, []byte(args.Content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path), nil
}

type listDirTool struct{ workspace string }

func NewListDirTool(workspace string) Tool { return &listDirTool{workspace: workspace} }

func (t *listDirTool) Spec() FunctionSpec {
	return FunctionSpec{
		Name:        "list_dir",
		Description: "List files and directories under a path inside the workspace.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Relative directory path. Use \".\" for root."},
			},
			"required": []string{"path"},
		},
	}
}

func (t *listDirTool) Execute(_ context.Context, argsJSON string) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	if args.Path == "" {
		args.Path = "."
	}
	full, err := resolvePath(t.workspace, args.Path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			fmt.Fprintf(&b, "%s\t?\n", e.Name())
			continue
		}
		kind := "file"
		if e.IsDir() {
			kind = "dir"
		}
		fmt.Fprintf(&b, "%s\t%s\t%d\n", e.Name(), kind, info.Size())
	}
	if b.Len() == 0 {
		return "(empty)", nil
	}
	return b.String(), nil
}

type shellTool struct {
	workspace string
	timeout   time.Duration
}

func NewShellTool(workspace string) Tool {
	return &shellTool{workspace: workspace, timeout: 30 * time.Second}
}

func (t *shellTool) Spec() FunctionSpec {
	return FunctionSpec{
		Name: "shell",
		Description: "Execute a shell command inside the workspace directory. Stdout/stderr returned. 30s timeout.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "The shell command to run."},
			},
			"required": []string{"command"},
		},
	}
}

func (t *shellTool) Execute(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	cmdStr := strings.TrimSpace(args.Command)
	if cmdStr == "" {
		return "", fmt.Errorf("empty command")
	}
	ws := t.workspace
	if ws == "" {
		ws = "."
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		return "", err
	}
	timeout := t.timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", cmdStr)
	cmd.Dir = absWS
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + absWS, "TERM=dumb", "LANG=C.UTF-8"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	var b strings.Builder
	if stdout.Len() > 0 {
		b.WriteString(stdout.String())
	}
	if stderr.Len() > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("[stderr]\n")
		b.WriteString(stderr.String())
	}
	out := b.String()
	const maxOut = 32 * 1024
	if len(out) > maxOut {
		out = out[:maxOut] + "\n... [truncated]"
	}
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return out, fmt.Errorf("command timed out after %s", timeout)
		}
		return out, fmt.Errorf("exit: %w", err)
	}
	if out == "" {
		return "(no output)", nil
	}
	return out, nil
}

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
		Description: "Author a new tool at runtime and register it. Shell command template with {{arg}} placeholders.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "Unique tool name (snake_case)."},
				"description": map[string]any{"type": "string", "description": "What the tool does."},
				"parameters":   map[string]any{"type": "object", "description": "JSON Schema for arguments."},
				"command":     map[string]any{"type": "string", "description": "Shell template with {{param}} placeholders."},
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
	if name == "" || name == finalAnswerName || name == "create_tool" {
		return "", fmt.Errorf("invalid or reserved tool name %q", args.Name)
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
	dyn := &dynamicShellTool{name: name, description: args.Description, parameters: params, command: args.Command, workspace: t.workspace}
	if err := t.registry.Register(dyn); err != nil {
		return "", err
	}
	return fmt.Sprintf("tool %q registered — you can call it now", name), nil
}

type dynamicShellTool struct {
	name, description, command, workspace string
	parameters                           any
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
		cmd = strings.ReplaceAll(cmd, "{{" + k + "}}", fmt.Sprint(v))
	}
	sh := &shellTool{workspace: t.workspace, timeout: 30 * time.Second}
	return sh.Execute(ctx, mustMarshalCompact(map[string]string{"command": cmd}))
}
