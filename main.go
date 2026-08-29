// Command tiny-multiagent-go is a miniature multi-agent demo inspired by
// Hugging Face smolagents: a manager agent delegates tasks to specialist
// agents, which are exposed to the manager as ordinary tools ("agent as
// tool"), while each runs its own ReAct loop with its own memory.
//
// Configuration is explicit — there are no provider defaults. Copy
// .env.example to ".env" and fill in:
//
//	PROTOCOL   "openai" (any OpenAI-compatible server) or "anthropic"
//	           (default: "openai")
//	BASE_URL   API base URL, e.g. "https://api.openai.com/v1",
//	           "https://api.anthropic.com/v1", or your own server
//	           (default: the official URL for the chosen protocol)
//	API_KEY    API key (sent as Bearer token for openai, x-api-key for
//	           anthropic)
//	MODEL      model name, e.g. "gpt-4o-mini", "claude-sonnet-4-5"
//	           (required)
//
// The file path defaults to ".env" in the working directory and can be
// overridden: go run . -env path/to/config.env
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
)

func main() {
	envPath := flag.String("env", ".env", "path to the env config file")
	webAddr := flag.String("web", ":8765", "address for the live trace web UI (empty disables it)")
	flag.Parse()

	values, err := loadDotEnv(*envPath)
	if err != nil {
		log.Fatalf("load env file: %v (copy .env.example to .env and fill in your values)", err)
	}
	model, err := modelFromFile(values)
	if err != nil {
		log.Fatal(err)
	}

	// Two specialists; each is a full agent that only the manager can call.
	researcher := &Agent{
		Name:        "researcher",
		Description: "Finds factual information using web search.",
		Model:       model,
		Tools:       []Tool{webSearchTool{}},
		MaxSteps:    5,
		Verbose:     true,
	}
	mathematician := &Agent{
		Name:        "mathematician",
		Description: "Performs precise arithmetic calculations.",
		Model:       model,
		Tools:       []Tool{calculatorTool{}},
		MaxSteps:    5,
		Verbose:     true,
	}

	// The manager has no tools of its own — only team members.
	manager := &Agent{
		Name:          "manager",
		Description:   "Coordinates team members to answer the user's question.",
		Model:         model,
		ManagedAgents: []*Agent{researcher, mathematician},
		MaxSteps:      10,
		Verbose:       true,
	}

	// All agents share one tracer so the web UI shows delegations nested.
	tracer := NewTracer()
	for _, ag := range []*Agent{manager, researcher, mathematician} {
		ag.Tracer = tracer
	}
	if *webAddr != "" {
		ln, err := net.Listen("tcp", *webAddr)
		if err != nil {
			log.Fatalf("web UI: %v", err)
		}
		mux := http.NewServeMux()
		RegisterTraceRoutes(mux, tracer)
		go func() { _ = http.Serve(ln, mux) }()
		fmt.Printf("live trace web UI: http://localhost:%d\n", ln.Addr().(*net.TCPAddr).Port)
	}

	task := "The Eiffel Tower was built between 28 January 1887 and 31 March 1889. " +
		"How many days did its construction last in total? Use your team members for facts and math."
	res, err := manager.Run(context.Background(), task)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("\n=== Final answer ===\n" + res.Answer)

	usage := res.Usage
	fmt.Printf("\n=== Token usage ===\nmodel calls: %d | prompt: %d | completion: %d | total: %d\n",
		usage.Calls, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens())
	for _, member := range []*Agent{researcher, mathematician} {
		memberUsage := member.LastUsage()
		fmt.Printf("  %-14s calls: %d | total: %d\n", member.Name, memberUsage.Calls, memberUsage.TotalTokens())
	}

	if *webAddr != "" {
		tracer.SetDone()
		fmt.Println("\n(轨迹页面保持可用，按 Ctrl+C 退出)")
		select {}
	}
}

// modelFromFile builds the model backend from the parsed env file; see the
// package comment for the keys and their meaning.
func modelFromFile(cfg map[string]string) (Model, error) {
	modelID := cfg["MODEL"]
	if modelID == "" {
		return nil, fmt.Errorf("MODEL is required in the env file (e.g. gpt-4o-mini, claude-sonnet-4-5); " +
			"also set PROTOCOL and BASE_URL if you are not using OpenAI defaults")
	}

	protocol := cfg["PROTOCOL"]
	if protocol == "" {
		protocol = "openai"
	}
	baseURL := cfg["BASE_URL"]
	if baseURL == "" {
		switch protocol {
		case "anthropic":
			baseURL = "https://api.anthropic.com/v1"
		case "openai":
			baseURL = "https://api.openai.com/v1"
		default:
			return nil, fmt.Errorf("unknown PROTOCOL %q (want \"openai\" or \"anthropic\")", protocol)
		}
	}
	return NewModel(ModelConfig{
		Protocol: protocol,
		BaseURL:  baseURL,
		APIKey:   cfg["API_KEY"],
		ModelID:  modelID,
	})
}

// calculatorTool is a real, deterministic tool.
type calculatorTool struct{}

func (calculatorTool) Spec() FunctionSpec {
	return FunctionSpec{
		Name:        "calculator",
		Description: "Compute arithmetic on two numbers.",
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

// webSearchTool is a stub standing in for a real search backend; it always
// returns the same canned fact so the demo works without any API key.
type webSearchTool struct{}

func (webSearchTool) Spec() FunctionSpec {
	return FunctionSpec{
		Name:        "web_search",
		Description: "Search the web for factual information.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "The search query."},
			},
			"required": []string{"query"},
		},
	}
}

func (webSearchTool) Execute(_ context.Context, argsJSON string) (string, error) {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", err
	}
	return "STUB RESULT — no real search backend is configured. Canned fact: " +
		"The Eiffel Tower was built from 28 January 1887 to 31 March 1889.", nil
}
