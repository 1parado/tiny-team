package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Tool is the smallest unit of capability an agent can use.
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
type Agent struct {
	Name          string
	Description   string
	Model         Model
	Tools         []Tool
	ManagedAgents []*Agent
	MaxSteps      int
	Verbose       bool
	// Tracer optionally records every step of the run for the live web UI.
	Tracer *Tracer

	memory    []ChatMessage
	lastUsage TokenUsage
}

// Spec makes a managed agent look like a tool to its manager, mirroring how
// smolagents pins managed agents to a single "task" string input (agents.py
// _setup_managed_agents).
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
	a.memory = []ChatMessage{
		{Role: "system", Content: a.systemPrompt()},
		{Role: "user", Content: task},
	}
	a.trace(TraceEvent{Type: "run_start", Agent: a.Name, Text: task})

	for step := 1; step <= a.MaxSteps; step++ {
		a.logf("step %d", step)
		a.trace(TraceEvent{Type: "step", Agent: a.Name, Step: step})
		reply, usage, err := a.chatWithRetries(ctx, a.memory, a.functionSpecs())
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

// maxModelRetries bounds retries of transient model-call failures such as
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
// "Error: ..." so the model can self-correct, like smolagents logging
// AgentError into memory (agents.py _run_stream).
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
	if sub, ok := tool.(*Agent); ok {
		// Delegated: the sub-agent runs its own full loop; count its usage.
		a.trace(TraceEvent{
			Type: "delegate_start", Agent: a.Name, ToolName: sub.Name, Args: call.Function.Arguments,
		})
		if a.Tracer != nil {
			a.Tracer.Enter() // nest the sub-agent's events one level deeper
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
	for _, t := range a.Tools {
		if t.Spec().Name == name {
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
	for _, t := range a.Tools {
		names = append(names, t.Spec().Name)
	}
	for _, ma := range a.ManagedAgents {
		names = append(names, ma.Name)
	}
	return names
}

func (a *Agent) functionSpecs() []FunctionSpec {
	specs := make([]FunctionSpec, 0, 2+len(a.Tools)+len(a.ManagedAgents))
	specs = append(specs, FinalAnswerTool{}.Spec())
	for _, t := range a.Tools {
		specs = append(specs, t.Spec())
	}
	for _, ma := range a.ManagedAgents {
		specs = append(specs, ma.Spec())
	}
	return specs
}

// systemPrompt describes the agent and its catalog of tools and team members.
func (a *Agent) systemPrompt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are %s, an agent solving tasks step by step (ReAct).\n", a.Name)
	if a.Description != "" {
		b.WriteString(a.Description + "\n")
	}
	b.WriteString("\nOn each step, think briefly, then call exactly one tool with valid JSON arguments.\nWhen the task is solved, call final_answer with the result.\n\nAvailable tools and team members:\n")
	for _, spec := range a.functionSpecs() {
		fmt.Fprintf(&b, "\n- %s: %s\n", spec.Name, spec.Description)
		fmt.Fprintf(&b, "  arguments: %s\n", mustMarshalCompact(spec.Parameters))
	}
	return b.String()
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
