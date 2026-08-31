package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// doctor — read-only self-check
// ---------------------------------------------------------------------------

type doctorTool struct {
	workspace string
	registry  *ToolRegistry
}

func NewDoctorTool(workspace string, registry *ToolRegistry) Tool {
	return &doctorTool{workspace: workspace, registry: registry}
}

func (t *doctorTool) Spec() FunctionSpec {
	return FunctionSpec{
		Name: "doctor",
		Description: "Read-only self-check of workspace, registered tools, and environment " +
			"(reports whether API_KEY/MODEL are set, never prints secret values).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"verbose": map[string]any{
					"type":        "boolean",
					"description": "If true, list every registered tool name. Defaults to false (still lists core presence).",
				},
			},
		},
	}
}

func (t *doctorTool) Execute(_ context.Context, argsJSON string) (string, error) {
	var args struct {
		Verbose bool `json:"verbose"`
	}
	if argsJSON != "" && argsJSON != "{}" {
		_ = json.Unmarshal([]byte(argsJSON), &args)
	}

	var b strings.Builder
	degraded := false
	fmt.Fprintf(&b, "tiny-team doctor\n")
	fmt.Fprintf(&b, "time: %s\n", time.Now().Format(time.RFC3339))

	ws := t.workspace
	if ws == "" {
		ws = "."
	}
	absWS, err := filepath.Abs(ws)
	if err != nil {
		fmt.Fprintf(&b, "workspace: error resolving %q: %v\n", ws, err)
		degraded = true
	} else {
		info, err := os.Stat(absWS)
		if err != nil {
			fmt.Fprintf(&b, "workspace: %s (missing: %v)\n", absWS, err)
			degraded = true
		} else {
			writable := false
			if info.IsDir() {
				tmp, err := os.CreateTemp(absWS, ".doctor-*")
				if err == nil {
					writable = true
					name := tmp.Name()
					tmp.Close()
					_ = os.Remove(name)
				}
			}
			fmt.Fprintf(&b, "workspace: %s\n", absWS)
			fmt.Fprintf(&b, "workspace_dir: %v\n", info.IsDir())
			fmt.Fprintf(&b, "workspace_writable: %v\n", writable)
			if !writable {
				degraded = true
			}
		}
	}

	// Env: presence only — never print values.
	for _, key := range []string{"API_KEY", "MODEL"} {
		if v := os.Getenv(key); v != "" {
			fmt.Fprintf(&b, "env %s: set\n", key)
		} else {
			fmt.Fprintf(&b, "env %s: missing\n", key)
		}
	}
	// .env file next to workspace or cwd (existence only).
	envCandidates := []string{filepath.Join(absWS, ".env"), ".env"}
	envFound := false
	for _, p := range envCandidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			fmt.Fprintf(&b, "dotenv_file: present (%s)\n", p)
			envFound = true
			break
		}
	}
	if !envFound {
		fmt.Fprintf(&b, "dotenv_file: not found near workspace\n")
	}

	names := []string{}
	if t.registry != nil {
		names = t.registry.Names()
	}
	fmt.Fprintf(&b, "tools_registered: %d\n", len(names))
	core := []string{"read", "write", "edit", "create_tool", "doctor", "log_evolution"}
	for _, n := range core {
		ok := false
		for _, have := range names {
			if have == n {
				ok = true
				break
			}
		}
		fmt.Fprintf(&b, "tool %s: %v\n", n, ok)
		if !ok && (n == "read" || n == "doctor") {
			degraded = true
		}
	}
	if args.Verbose {
		fmt.Fprintf(&b, "tools_all: %s\n", strings.Join(names, ", "))
	}

	// Optional short go version probe.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "version")
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(&b, "go: not available (%v)\n", err)
		degraded = true
	} else {
		fmt.Fprintf(&b, "go: %s\n", strings.TrimSpace(string(out)))
	}

	if degraded {
		fmt.Fprintf(&b, "status: degraded\n")
	} else {
		fmt.Fprintf(&b, "status: ok\n")
	}
	return b.String(), nil
}

// ---------------------------------------------------------------------------
// log_evolution — append-only evolution diary
// ---------------------------------------------------------------------------

const evolutionLogName = "EVOLUTION_LOG.md"

type logEvolutionTool struct{ workspace string }

func NewLogEvolutionTool(workspace string) Tool {
	return &logEvolutionTool{workspace: workspace}
}

func (t *logEvolutionTool) Spec() FunctionSpec {
	return FunctionSpec{
		Name: "log_evolution",
		Description: "Append one entry to EVOLUTION_LOG.md in the workspace root " +
			"(evolution diary for self-modifications).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary": map[string]any{"type": "string", "description": "Short summary of this evolution step (required)."},
				"files":   map[string]any{"type": "string", "description": "Optional comma-separated files touched."},
				"tests":   map[string]any{"type": "string", "description": "Optional test result summary."},
				"detail":  map[string]any{"type": "string", "description": "Optional longer notes."},
			},
			"required": []string{"summary"},
		},
	}
}

func (t *logEvolutionTool) Execute(_ context.Context, argsJSON string) (string, error) {
	var args struct {
		Summary string `json:"summary"`
		Files   string `json:"files"`
		Tests   string `json:"tests"`
		Detail  string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	args.Summary = strings.TrimSpace(args.Summary)
	if args.Summary == "" {
		return "", fmt.Errorf("summary is required")
	}
	full, err := resolvePath(t.workspace, evolutionLogName)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	if _, err := os.Stat(full); os.IsNotExist(err) {
		header := "# Evolution log\n\n"
		if err := os.WriteFile(full, []byte(header), 0o644); err != nil {
			return "", err
		}
	}
	var entry strings.Builder
	fmt.Fprintf(&entry, "## %s\n\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&entry, "- summary: %s\n", args.Summary)
	if strings.TrimSpace(args.Files) != "" {
		fmt.Fprintf(&entry, "- files: %s\n", strings.TrimSpace(args.Files))
	}
	if strings.TrimSpace(args.Tests) != "" {
		fmt.Fprintf(&entry, "- tests: %s\n", strings.TrimSpace(args.Tests))
	}
	if strings.TrimSpace(args.Detail) != "" {
		fmt.Fprintf(&entry, "- detail: %s\n", strings.TrimSpace(args.Detail))
	}
	entry.WriteString("\n")
	f, err := os.OpenFile(full, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	n, err := f.WriteString(entry.String())
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("logged evolution entry to %s (%d bytes)", evolutionLogName, n), nil
}
