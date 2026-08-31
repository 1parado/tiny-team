package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// shell — run a command inside the workspace (cross-platform)
//
// Runner selection:
//   Windows: pwsh → powershell → bash → cmd /C
//   Unix:    bash → sh

type shellTool struct {
	workspace string
	timeout   time.Duration
}

func NewShellTool(workspace string) Tool {
	return &shellTool{workspace: workspace, timeout: 30 * time.Second}
}

// pickShell returns the executable, its fixed argv prefix, and a short style
// label used in error messages. Preference order is Windows-friendly first.
func pickShell() (bin string, prefix []string, style string) {
	look := func(name string) (string, bool) {
		p, err := exec.LookPath(name)
		return p, err == nil
	}
	if runtime.GOOS == "windows" {
		if p, ok := look("pwsh"); ok {
			return p, []string{"-NoProfile", "-NonInteractive", "-Command"}, "pwsh"
		}
		if p, ok := look("powershell"); ok {
			return p, []string{"-NoProfile", "-NonInteractive", "-Command"}, "powershell"
		}
		if p, ok := look("bash"); ok {
			return p, []string{"-c"}, "bash"
		}
		// cmd is almost always present on Windows.
		if p, ok := look("cmd"); ok {
			return p, []string{"/C"}, "cmd"
		}
		return "cmd", []string{"/C"}, "cmd"
	}
	if p, ok := look("bash"); ok {
		return p, []string{"-c"}, "bash"
	}
	if p, ok := look("sh"); ok {
		return p, []string{"-c"}, "sh"
	}
	return "sh", []string{"-c"}, "sh"
}

func shellEnv(absWS string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"TERM=dumb",
	}
	if runtime.GOOS == "windows" {
		// SYSTEMROOT / COMSPEC are required for many Windows builtins and tools.
		for _, k := range []string{
			"SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "COMSPEC", "PATHEXT",
			"USERPROFILE", "HOME", "HOMEDRIVE", "HOMEPATH", "TMP", "TEMP",
			"NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE",
		} {
			if v := os.Getenv(k); v != "" {
				env = append(env, k+"="+v)
			}
		}
		if os.Getenv("HOME") == "" && os.Getenv("USERPROFILE") != "" {
			env = append(env, "HOME="+os.Getenv("USERPROFILE"))
		}
	} else {
		env = append(env, "HOME="+absWS, "LANG=C.UTF-8")
	}
	return env
}

func (t *shellTool) Spec() FunctionSpec {
	return FunctionSpec{
		Name: "shell",
		Description: "Execute a command inside the workspace directory (timeout 30s). " +
			"On Windows prefers pwsh, then Windows PowerShell, bash, or cmd; " +
			"on Unix prefers bash then sh. Stdout and stderr are returned. " +
			"Prefer portable commands, or PowerShell syntax on Windows (e.g. Get-ChildItem).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type": "string",
					"description": "Command to run. Examples: \"echo hi\", \"Get-ChildItem\" (Windows), " +
						"\"ls -la\" (Unix/Git Bash).",
				},
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

	bin, prefix, style := pickShell()
	argv := append(append([]string{}, prefix...), cmdStr)
	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Dir = absWS
	cmd.Env = shellEnv(absWS)

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
		errText := err.Error()
		if strings.Contains(errText, "executable file not found") ||
			strings.Contains(errText, "file does not exist") ||
			strings.Contains(errText, "not found in %PATH%") {
			return out, fmt.Errorf("shell runner %q (%s) not found on PATH; "+
				"on Windows install PowerShell 7 (pwsh) or Git Bash, or ensure powershell.exe is available: %w",
				bin, style, err)
		}
		return out, fmt.Errorf("exit via %s: %w", style, err)
	}
	if out == "" {
		return "(no output)", nil
	}
	return out, nil
}
