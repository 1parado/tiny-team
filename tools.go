package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// Tool is the smallest unit of capability an agent can use.
// Everything is a plugin: built-in tools, managed agents, and tools
// the model authors at runtime all implement this interface.
type Tool interface {
	// Spec describes the tool to the LLM as an OpenAI function schema.
	Spec() FunctionSpec
	// Execute runs the tool with JSON-encoded arguments and returns the
	// observation to feed back into the caller's memory.
	Execute(ctx context.Context, argsJSON string) (string, error)
}

// finalAnswerName is the built-in tool that terminates the ReAct loop,
// mirroring smolagents' FinalAnswerTool.
const finalAnswerName = "final_answer"

// FinalAnswerTool reports the end of a task; its result is the agent's answer.
type FinalAnswerTool struct{}

func (FinalAnswerTool) Spec() FunctionSpec {
	return FunctionSpec{
		Name:        finalAnswerName,
		Description: "Provide the final answer to the task. This ends your run.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"answer": map[string]any{"type": "string", "description": "The final answer."},
			},
			"required": []string{"answer"},
		},
	}
}

func (FinalAnswerTool) Execute(_ context.Context, argsJSON string) (string, error) {
	var args struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	return args.Answer, nil
}

// ---------------------------------------------------------------------------
// ToolRegistry — everything-is-a-plugin catalogue
// ---------------------------------------------------------------------------

// ToolRegistry is a thread-safe catalogue of named tools.
// Agents hold a pointer to a registry; the model can grow the registry at
// runtime via the create_tool meta-plugin.
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewToolRegistry returns an empty registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]Tool)}
}

// Register adds or replaces a tool by its Spec().Name.
// Names "final_answer" and empty strings are reserved and rejected.
func (r *ToolRegistry) Register(t Tool) error {
	if t == nil {
		return fmt.Errorf("nil tool")
	}
	name := t.Spec().Name
	if name == "" || name == finalAnswerName {
		return fmt.Errorf("tool name %q is reserved", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[name] = t
	return nil
}

// Get returns the tool registered under name, if any.
func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns a snapshot of all registered tools (order not guaranteed).
func (r *ToolRegistry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

// Names returns the sorted list of registered tool names.
func (r *ToolRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	return names
}

// Clone returns a shallow copy of the registry (same tool instances).
// Useful when giving each agent its own mutable catalogue.
func (r *ToolRegistry) Clone() *ToolRegistry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c := NewToolRegistry()
	for k, v := range r.tools {
		c.tools[k] = v
	}
	return c
}

// MustRegister is like Register but panics on error (for init-time wiring).
func (r *ToolRegistry) MustRegister(t Tool) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}

// DefaultRegistry builds a registry pre-loaded with the standard plugins:
// read, write, list_dir, shell, search, calculator, and create_tool.
// workspace is the root directory that file/shell tools are confined to.
func DefaultRegistry(workspace string) *ToolRegistry {
	r := NewToolRegistry()
	r.MustRegister(NewReadTool(workspace))
	r.MustRegister(NewWriteTool(workspace))
	r.MustRegister(NewListDirTool(workspace))
	r.MustRegister(NewShellTool(workspace))
	r.MustRegister(NewSearchTool(workspace))
	r.MustRegister(NewCalculatorTool())
	// create_tool needs a back-reference to the registry it mutates.
	r.MustRegister(NewCreateTool(r, workspace))
	return r
}
