package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Workspace helpers — all file/shell plugins are confined to a root dir.

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

const maxEditBytes = 1 << 20

type editTool struct{ workspace string }

func NewEditTool(workspace string) Tool { return &editTool{workspace: workspace} }

func (t *editTool) Spec() FunctionSpec {
	return FunctionSpec{
		Name: "edit",
		Description: "Replace a literal (non-regex) snippet inside a file in the workspace. " +
			"old_str must occur exactly once unless replace_all is true.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Relative path of the file to edit."},
				"old_str": map[string]any{"type": "string", "description": "Exact literal text to replace. Must occur exactly once unless replace_all is true."},
				"new_str": map[string]any{"type": "string", "description": "Replacement text. Use an empty string to delete."},
				"replace_all": map[string]any{
					"type":        "boolean",
					"description": "Replace every occurrence instead of requiring a unique match. Defaults to false.",
				},
			},
			"required": []string{"path", "old_str", "new_str"},
		},
	}
}

func (t *editTool) Execute(_ context.Context, argsJSON string) (string, error) {
	var args struct {
		Path       string `json:"path"`
		OldStr     string `json:"old_str"`
		NewStr     string `json:"new_str"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	full, err := resolvePath(t.workspace, args.Path)
	if err != nil {
		return "", err
	}
	if args.OldStr == "" {
		return "", fmt.Errorf("old_str must not be empty")
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	if len(data) > maxEditBytes {
		return "", fmt.Errorf("file %s is too large to edit (%d bytes, limit %d)", args.Path, len(data), maxEditBytes)
	}
	content := string(data)
	n := strings.Count(content, args.OldStr)
	switch {
	case n == 0:
		return "", fmt.Errorf("old_str not found in %s", args.Path)
	case n > 1 && !args.ReplaceAll:
		return "", fmt.Errorf("old_str matches %d times in %s; provide a longer, unique snippet or set replace_all=true", n, args.Path)
	}
	var updated string
	if args.ReplaceAll {
		updated = strings.ReplaceAll(content, args.OldStr, args.NewStr)
	} else {
		updated = strings.Replace(content, args.OldStr, args.NewStr, 1)
	}
	mode := fs.FileMode(0o644)
	if info, statErr := os.Stat(full); statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(full, []byte(updated), mode); err != nil {
		return "", err
	}
	if args.ReplaceAll && n > 1 {
		return fmt.Sprintf("edited %s (%d occurrences, replaced %d bytes with %d bytes each)", args.Path, n, len(args.OldStr), len(args.NewStr)), nil
	}
	return fmt.Sprintf("edited %s (replaced %d bytes with %d bytes)", args.Path, len(args.OldStr), len(args.NewStr)), nil
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

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func validToolName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}
