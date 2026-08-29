package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ChatMessage is one entry in an agent's conversation memory.
type ChatMessage struct {
	Role       string     `json:"role"` // "system", "user", "assistant" or "tool"
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is the model's request to invoke one tool.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // always "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON-encoded arguments
	} `json:"function"`
}

// FunctionSpec is a provider-neutral function schema describing a tool.
type FunctionSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// Usage reports token consumption of one model call. The endpoint's reported
// numbers are preferred; when a response carries no usage, tokens are
// estimated locally with tiktoken (approximate for non-OpenAI models).
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// TotalTokens returns the sum of prompt and completion tokens.
func (u Usage) TotalTokens() int { return u.PromptTokens + u.CompletionTokens }

// Model is the protocol behind an agent: it sends the conversation memory plus
// the tool catalog and returns the assistant's next message, which may contain
// zero or more tool calls, together with the call's token usage.
// Implementations: OpenAIModel, AnthropicModel.
type Model interface {
	Chat(ctx context.Context, messages []ChatMessage, tools []FunctionSpec) (ChatMessage, Usage, error)
}

// ModelConfig selects and authenticates a model backend. Nothing has a
// provider-specific default: URL, key, protocol and model are all explicit.
type ModelConfig struct {
	Protocol  string // "openai" (any OpenAI-compatible server) or "anthropic"
	BaseURL   string // e.g. "https://api.openai.com/v1"
	APIKey    string
	ModelID   string
	MaxTokens int // anthropic only; defaults to 4096
	HTTP      *http.Client
}

// NewModel builds the Model implementation for cfg.Protocol.
func NewModel(cfg ModelConfig) (Model, error) {
	cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
	switch cfg.Protocol {
	case "openai":
		return &OpenAIModel{cfg: cfg}, nil
	case "anthropic":
		return &AnthropicModel{cfg: cfg}, nil
	default:
		return nil, fmt.Errorf("unknown protocol %q (want \"openai\" or \"anthropic\")", cfg.Protocol)
	}
}

// sharedTransport forces HTTP/1.1: some OpenAI-compatible relays behind WAF
// gateways drop Go's default HTTP/2 connections mid-request.
var sharedTransport = &http.Transport{
	ForceAttemptHTTP2: false,
	TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
}

func (cfg ModelConfig) httpClient() *http.Client {
	if cfg.HTTP != nil {
		return cfg.HTTP
	}
	return &http.Client{Timeout: 300 * time.Second, Transport: sharedTransport}
}

// ---------------------------------------------------------------------------
// OpenAI-compatible protocol (OpenAI, DeepSeek, vLLM, LiteLLM, Ollama, ...)
// ---------------------------------------------------------------------------

// OpenAIModel speaks the /chat/completions protocol.
type OpenAIModel struct {
	cfg ModelConfig
}

type openAIRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Tools    []openAITool  `json:"tools,omitempty"`
}

type openAITool struct {
	Type     string       `json:"type"` // always "function"
	Function FunctionSpec `json:"function"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type openAIResponse struct {
	Choices []struct {
		Message ChatMessage `json:"message"`
	} `json:"choices"`
	Usage *openAIUsage `json:"usage"`
}

func (m *OpenAIModel) Chat(ctx context.Context, messages []ChatMessage, tools []FunctionSpec) (ChatMessage, Usage, error) {
	schemas := make([]openAITool, len(tools))
	for i, t := range tools {
		schemas[i] = openAITool{Type: "function", Function: t}
	}
	body, err := json.Marshal(openAIRequest{Model: m.cfg.ModelID, Messages: messages, Tools: schemas})
	if err != nil {
		return ChatMessage{}, Usage{}, fmt.Errorf("marshal request: %w", err)
	}
	respBody, err := postJSON(ctx, m.cfg, "/chat/completions", body)
	if err != nil {
		return ChatMessage{}, Usage{}, err
	}

	var out openAIResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return ChatMessage{}, Usage{}, fmt.Errorf("decode response: %w", err)
	}
	if len(out.Choices) == 0 {
		return ChatMessage{}, Usage{}, errors.New("endpoint returned no choices")
	}
	msg := out.Choices[0].Message
	if out.Usage != nil && out.Usage.PromptTokens+out.Usage.CompletionTokens > 0 {
		return msg, Usage{PromptTokens: out.Usage.PromptTokens, CompletionTokens: out.Usage.CompletionTokens}, nil
	}
	prompt, completion := estimateUsage(m.cfg.ModelID, messages, msg)
	return msg, Usage{PromptTokens: prompt, CompletionTokens: completion}, nil
}

// ---------------------------------------------------------------------------
// Anthropic protocol (messages API with tool_use / tool_result blocks)
// ---------------------------------------------------------------------------

// AnthropicModel speaks Anthropic's /messages protocol.
type AnthropicModel struct {
	cfg ModelConfig
}

type anthropicBlock struct {
	Type string `json:"type"` // "text", "tool_use" or "tool_result"
	Text string `json:"text,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

type anthropicMessage struct {
	Role    string           `json:"role"` // "user" or "assistant"
	Content []anthropicBlock `json:"content"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type anthropicResponse struct {
	Content []anthropicBlock `json:"content"`
	Usage   *anthropicUsage  `json:"usage"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (m *AnthropicModel) Chat(ctx context.Context, messages []ChatMessage, tools []FunctionSpec) (ChatMessage, Usage, error) {
	req, err := m.buildRequest(messages, tools)
	if err != nil {
		return ChatMessage{}, Usage{}, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return ChatMessage{}, Usage{}, fmt.Errorf("marshal request: %w", err)
	}
	respBody, err := postJSON(ctx, m.cfg, "/messages", body)
	if err != nil {
		return ChatMessage{}, Usage{}, err
	}

	var out anthropicResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return ChatMessage{}, Usage{}, fmt.Errorf("decode response: %w", err)
	}
	if out.Error != nil {
		return ChatMessage{}, Usage{}, fmt.Errorf("anthropic error: %s", out.Error.Message)
	}

	var reply ChatMessage
	var texts []string
	for _, block := range out.Content {
		switch block.Type {
		case "text":
			texts = append(texts, block.Text)
		case "tool_use":
			input := block.Input
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			reply.ToolCalls = append(reply.ToolCalls, ToolCall{
				ID:   block.ID,
				Type: "function",
			})
			reply.ToolCalls[len(reply.ToolCalls)-1].Function.Name = block.Name
			reply.ToolCalls[len(reply.ToolCalls)-1].Function.Arguments = string(input)
		}
	}
	reply.Content = strings.Join(texts, "\n")

	var usage Usage
	if out.Usage != nil && out.Usage.InputTokens+out.Usage.OutputTokens > 0 {
		usage = Usage{PromptTokens: out.Usage.InputTokens, CompletionTokens: out.Usage.OutputTokens}
	} else {
		prompt, completion := estimateUsage(m.cfg.ModelID, messages, reply)
		usage = Usage{PromptTokens: prompt, CompletionTokens: completion}
	}
	return reply, usage, nil
}

func (m *AnthropicModel) buildRequest(messages []ChatMessage, tools []FunctionSpec) (*anthropicRequest, error) {
	req := &anthropicRequest{Model: m.cfg.ModelID, MaxTokens: m.cfg.MaxTokens}
	if req.MaxTokens <= 0 {
		req.MaxTokens = 4096
	}
	for _, t := range tools {
		req.Tools = append(req.Tools, anthropicTool{Name: t.Name, Description: t.Description, InputSchema: t.Parameters})
	}

	var systemParts []string
	var pendingToolResults []anthropicBlock // consecutive tool messages merge into one user turn
	flushToolResults := func() {
		if len(pendingToolResults) > 0 {
			req.Messages = append(req.Messages, anthropicMessage{Role: "user", Content: pendingToolResults})
			pendingToolResults = nil
		}
	}
	for _, msg := range messages {
		switch msg.Role {
		case "system":
			systemParts = append(systemParts, msg.Content)
		case "tool":
			pendingToolResults = append(pendingToolResults, anthropicBlock{
				Type: "tool_result", ToolUseID: msg.ToolCallID, Content: msg.Content,
			})
		case "assistant":
			flushToolResults()
			blocks := []anthropicBlock{}
			if msg.Content != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: msg.Content})
			}
			for _, call := range msg.ToolCalls {
				input := json.RawMessage(call.Function.Arguments)
				if len(input) == 0 {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, anthropicBlock{Type: "tool_use", ID: call.ID, Name: call.Function.Name, Input: input})
			}
			req.Messages = append(req.Messages, anthropicMessage{Role: "assistant", Content: blocks})
		default: // "user"
			flushToolResults()
			req.Messages = append(req.Messages, anthropicMessage{
				Role: "user", Content: []anthropicBlock{{Type: "text", Text: msg.Content}},
			})
		}
	}
	flushToolResults()
	req.System = strings.Join(systemParts, "\n")
	return req, nil
}

// ---------------------------------------------------------------------------

// postJSON POSTs the payload to baseURL+path and returns the raw response body.
func postJSON(ctx context.Context, cfg ModelConfig, path string, payload []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "tiny-multiagent-go/1.0")
	switch cfg.Protocol {
	case "anthropic":
		req.Header.Set("x-api-key", cfg.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		if cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		}
	}

	resp, err := cfg.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s%s: %w", cfg.BaseURL, path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("endpoint returned HTTP %d: %.2048s", resp.StatusCode, body)
	}
	return body, nil
}
