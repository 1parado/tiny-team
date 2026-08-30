package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// managedTaskTemplate wraps a task delegated to a managed agent, mirroring
// smolagents' managed_agent.task prompt (agents.py __call__).
const managedTaskTemplate = "You are team member %q. Solve the task below and report back to your manager.\n\nTask:\n%s"

// TokenUsage aggregates token consumption over model calls.
type TokenUsage struct {
	Calls            int
	PromptTokens     int
	CompletionTokens int
}

// AddCall accumulates one model call's usage into u.
func (u *TokenUsage) AddCall(call Usage) {
	u.Calls++
	u.PromptTokens += call.PromptTokens
	u.CompletionTokens += call.CompletionTokens
}

// Add merges another aggregated usage into u.
func (u *TokenUsage) Add(o TokenUsage) {
	u.Calls += o.Calls
	u.PromptTokens += o.PromptTokens
	u.CompletionTokens += o.CompletionTokens
}

// TotalTokens returns the sum of prompt and completion tokens.
func (u TokenUsage) TotalTokens() int { return u.PromptTokens + u.CompletionTokens }

// RunResult is the outcome of one Run: the final answer plus token usage
// aggregated over every model call, including calls made by delegated
// sub-agents.
type RunResult struct {
	Answer string
	Usage  TokenUsage
}

// Agent is a ReAct agent: it loops LLM call -> tool call -> observation until
// the model calls final_answer or MaxSteps is exhausted.
//
// An Agent with Name and Description also implements Tool, so it can be
// registered as a ManagedAgent of another agent ("agent as tool"): to the
// manager's model it looks like an ordinary function taking a single "task"
// string, while running its own full loop with its own memory.
//
// Tools come from two places:
//   - Registry: a shared/plugin catalogue that can grow at runtime via create_tool
//   - ManagedAgents: other agents exposed as tools
type Agent struct {
	Name          string
	Description   string
	Model         Model
	Registry      *ToolRegistry // plugin catalogue (read/write/shell/search/...)
	Workspace     string        // root for file/shell plugins; used when isolating sub-agents
	ManagedAgents []*Agent
	MaxSteps      int
	Verbose       bool
	// Tracer optionally records every step of the run for the live web UI.
	Tracer *Tracer

	memory     []ChatMessage
	lastUsage  TokenUsage
	catalogSig string // last tool-catalogue signature used in system prompt
}

// Spec makes a managed agent look like a tool to its manager, mirroring how
// smolagents pins managed agents to a single "task" string input.
func (a *Agent) Spec() FunctionSpec {
	return FunctionSpec{
		Name:        a.Name,
		Description: a.Description,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task": map[string]any{
					"type":        "string",
					"description": "Long detailed description of the task for this team member.",
				},
			},
			"required": []string{"task"},
		},
	}
}

// Execute implements Tool: the manager delegates, the sub-agent runs its own
// complete ReAct loop and returns its final report as the observation.
func (a *Agent) Execute(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Task string `json:"task"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	// Isolate the sub-agent's plugin catalogue so create_tool registrations
	// do not leak across sibling managed agents that shared the manager registry.
	if a.Registry != nil && a.Workspace != "" {
		a.Registry = a.Registry.CloneIsolated(a.Workspace)
	}
	res, err := a.Run(ctx, fmt.Sprintf(managedTaskTemplate, a.Name, args.Task))
	if err != nil {
		return "", err
	}
	return res.Answer, nil
}

// Run solves the task and returns the final answer plus aggregated token
// usage. Memory is reset on every call, matching smolagents' default
// reset=True.
func (a *Agent) Run(ctx context.Context, task string) (RunResult, error) {
	if a.MaxSteps <= 0 {
		a.MaxSteps = 10
	}
	if a.Model == nil {
		return RunResult{}, fmt.Errorf("agent %q: Model is nil", a.Name)
	}
	a.lastUsage = TokenUsage{}
	a.catalogSig = a.catalogSignature()
	a.memory = []ChatMessage{
		{Role: "system", Content: a.systemPrompt()},
		{Role: "user", Content: task},
	}
	a.trace(TraceEvent{Type: "run_start", Agent: a.Name, Text: task})

	for step := 1; step <= a.MaxSteps; step++ {
		a.logf("step %d", step)
		a.trace(TraceEvent{Type: "step", Agent: a.Name, Step: step})

		// Rebuild tool catalogue every step so create_tool registrations appear.
		specs := a.functionSpecs()
		reply, usage, err := a.chatWithRetries(ctx, a.memory, specs)
		if err != nil {
			a.trace(TraceEvent{Type: "error", Agent: a.Name, Text: err.Error()})
			return RunResult{}, fmt.Errorf("step %d: %w", step, err)
		}
		a.lastUsage.AddCall(usage)

		// No tool call: treat the plain text reply as the final answer.
		if len(reply.ToolCalls) == 0 {
			a.trace(TraceEvent{Type: "final", Agent: a.Name, Text: reply.Content})
			return RunResult{Answer: reply.Content, Usage: a.lastUsage}, nil
		}

		a.trace(TraceEvent{
			Type: "assistant", Agent: a.Name, Step: step, Text: reply.Content,
			Usage: usage, ToolCalls: toolCallSummaries(reply.ToolCalls),
		})
		a.memory = append(a.memory, reply)

		// Refresh system prompt in memory if tools were added (create_tool).
		// We only rewrite the first system message when the catalogue grew.
		a.maybeRefreshSystemPrompt()

		var final string
		done := false
		for _, call := range reply.ToolCalls {
			out, subUsage := a.executeToolCall(ctx, call)
			a.lastUsage.Add(subUsage)
			a.logf("tool %s(%s) -> %.200s", call.Function.Name, call.Function.Arguments, out)
			if call.Function.Name == finalAnswerName {
				final, done = out, true
			}
			a.memory = append(a.memory, ChatMessage{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    out,
			})
		}
		if done {
			a.trace(TraceEvent{Type: "final", Agent: a.Name, Text: final})
			return RunResult{Answer: final, Usage: a.lastUsage}, nil
		}
	}
	a.trace(TraceEvent{
		Type: "error", Agent: a.Name,
		Text: fmt.Sprintf("max_steps (%d) reached without final_answer", a.MaxSteps),
	})
	return RunResult{}, fmt.Errorf("agent %q: max_steps (%d) reached without final_answer", a.Name, a.MaxSteps)
}

// LastUsage returns the token usage of the most recent Run, including calls
// made by delegated sub-agents.
func (a *Agent) LastUsage() TokenUsage { return a.lastUsage }

// maxModelRetries bounds retries on transient model-call failures such as
// gateway timeouts (HTTP 524) or dropped connections.
const maxModelRetries = 3

// chatWithRetries retries a failing model call with linear backoff, keeping
// the conversation memory unchanged so the retried request is identical.
func (a *Agent) chatWithRetries(ctx context.Context, messages []ChatMessage, tools []FunctionSpec) (ChatMessage, Usage, error) {
	var lastErr error
	for attempt := 1; attempt <= maxModelRetries; attempt++ {
		reply, usage, err := a.Model.Chat(ctx, messages, tools)
		if err == nil {
			return reply, usage, nil
		}
		lastErr = err
		if attempt < maxModelRetries {
			a.logf("model call failed (attempt %d/%d): %v - retrying", attempt, maxModelRetries, err)
			select {
			case <-ctx.Done():
				return ChatMessage{}, Usage{}, ctx.Err()
			case <-time.After(time.Duration(attempt) * 3 * time.Second):
			}
		}
	}
	return ChatMessage{}, Usage{}, lastErr
}

// executeToolCall runs one tool call and returns the string observation plus
// the token usage of delegated sub-agents. Execution errors become
// "Error: ..." so the model can self-correct.
func (a *Agent) executeToolCall(ctx context.Context, call ToolCall) (string, TokenUsage) {
	start := time.Now()
	tool, ok := a.toolByName(call.Function.Name)
	if !ok {
		msg := fmt.Sprintf("Error: unknown tool %q, should be one of: %s",
			call.Function.Name, strings.Join(a.toolNames(), ", "))
		a.trace(TraceEvent{
			Type: "tool_result", Agent: a.Name, ToolName: call.Function.Name,
			Args: call.Function.Arguments, Text: msg, IsErr: true, DurationMS: msSince(start),
		})
		return msg, TokenUsage{}
	}
	// Managed agent (delegation).
	if sub, ok := tool.(*Agent); ok {
		a.trace(TraceEvent{
			Type: "delegate_start", Agent: a.Name, ToolName: sub.Name, Args: call.Function.Arguments,
		})
		if a.Tracer != nil {
			a.Tracer.Enter()
		}
		out, err := sub.Execute(ctx, call.Function.Arguments)
		if a.Tracer != nil {
			a.Tracer.Exit()
		}
		a.trace(TraceEvent{
			Type: "delegate_end", Agent: a.Name, ToolName: sub.Name, Text: out,
			IsErr: err != nil, DurationMS: msSince(start),
		})
		if err != nil {
			return "Error: " + err.Error(), TokenUsage{}
		}
		return out, sub.lastUsage
	}
	out, err := tool.Execute(ctx, call.Function.Arguments)
	observation := out
	if err != nil {
		observation = "Error: " + err.Error()
	}
	a.trace(TraceEvent{
		Type: "tool_result", Agent: a.Name, ToolName: call.Function.Name,
		Args: call.Function.Arguments, Text: observation, IsErr: err != nil, DurationMS: msSince(start),
	})
	return observation, TokenUsage{}
}

// trace records an event when a Tracer is attached.
func (a *Agent) trace(e TraceEvent) {
	if a.Tracer != nil {
		a.Tracer.Record(e)
	}
}

func (a *Agent) toolByName(name string) (Tool, bool) {
	if name == finalAnswerName {
		return FinalAnswerTool{}, true
	}
	if a.Registry != nil {
		if t, ok := a.Registry.Get(name); ok {
			return t, true
		}
	}
	for _, ma := range a.ManagedAgents {
		if ma.Name == name {
			return ma, true
		}
	}
	return nil, false
}

func (a *Agent) toolNames() []string {
	names := []string{finalAnswerName}
	if a.Registry != nil {
		names = append(names, a.Registry.Names()...)
	}
	for _, ma := range a.ManagedAgents {
		names = append(names, ma.Name)
	}
	return names
}

func (a *Agent) functionSpecs() []FunctionSpec {
	specs := make([]FunctionSpec, 0, 4)
	specs = append(specs, FinalAnswerTool{}.Spec())
	if a.Registry != nil {
		for _, t := range a.Registry.List() {
			specs = append(specs, t.Spec())
		}
	}
	for _, ma := range a.ManagedAgents {
		specs = append(specs, ma.Spec())
	}
	return specs
}

// systemPrompt describes the agent and its catalogue of tools and team members.
func (a *Agent) systemPrompt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s, an agent solving tasks step by step (ReAct).\n", a.Name)
	if a.Description != "" {
		b.WriteString(a.Description + "\n")
	}
	b.WriteString("\nOn each step, think briefly, then call one or more tools with valid JSON arguments.\n")
	b.WriteString("When the task is solved, call final_answer with the result.\n")
	b.WriteString("You may author new tools at runtime with create_tool when existing plugins are not enough.\n")
	b.WriteString("\nAvailable tools and team members:\n")
	for _, spec := range a.functionSpecs() {
		fmt.Fprintf(&b, "\n- %s: %s\n", spec.Name, spec.Description)
		fmt.Fprintf(&b, "  arguments: %s\n", mustMarshalCompact(spec.Parameters))
	}
	return b.String()
}

// catalogSignature is a stable fingerprint of the current tool catalogue.
func (a *Agent) catalogSignature() string {
	names := a.toolNames()
	// toolNames already ends with managed agents; Names() is sorted.
	return strings.Join(names, ",")
}

// maybeRefreshSystemPrompt rewrites the system message only when the tool
// catalogue actually changed (e.g. after create_tool), avoiding a full rewrite
// on every step.
func (a *Agent) maybeRefreshSystemPrompt() {
	sig := a.catalogSignature()
	if sig == a.catalogSig {
		return
	}
	a.catalogSig = sig
	if len(a.memory) == 0 || a.memory[0].Role != "system" {
		return
	}
	a.memory[0].Content = a.systemPrompt()
	a.logf("tool catalogue changed; system prompt refreshed")
}

func mustMarshalCompact(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func (a *Agent) logf(format string, args ...any) {
	if a.Verbose {
		fmt.Printf("[%s] %s\n", a.Name, fmt.Sprintf(format, args...))
	}
}
