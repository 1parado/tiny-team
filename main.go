package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
)

// tiny-team — a minimal multi-agent framework in Go.
//
// Agents call agents like tools; everything is a plugin.
// Built-in plugins: read, write, list_dir, shell, search, calculator, create_tool.
//
// Usage:
//
//	cp .env.example .env   # fill API_KEY, MODEL, ...
//	go run . -task "write hello.txt and read it back"
//	go run . -web :8765 -workspace ./ws -task "..."
//
// Workspace for file/shell tools defaults to "./workspace" and can be
// overridden with -workspace.
func main() {
	envFile := flag.String("env", ".env", "path to env file")
	task := flag.String("task", "", "task for the agent")
	workspace := flag.String("workspace", "./workspace", "sandbox root for file/shell plugins")
	webAddr := flag.String("web", "", "if set (e.g. :8765), serve live trace UI")
	flag.Parse()

	values, err := loadDotEnv(*envFile)
	if err != nil {
		log.Fatalf("load env %s: %v (copy .env.example and fill API_KEY / MODEL)", *envFile, err)
	}
	model, err := modelFromFile(values)
	if err != nil {
		log.Fatal(err)
	}

	// Ensure workspace exists.
	if err := os.MkdirAll(*workspace, 0o755); err != nil {
		log.Fatalf("workspace: %v", err)
	}

	// Everything-is-a-plugin: default registry with read/write/shell/search/...
	registry := DefaultRegistry(*workspace)

	agent := &Agent{
		Name:        "assistant",
		Description: "A capable assistant with file, shell, search plugins and the ability to author new tools.",
		Model:       model,
		Registry:    registry,
		Workspace:   *workspace,
		MaxSteps:    15,
		Verbose:     true,
	}

	tracer := NewTracer()
	agent.Tracer = tracer

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

	userTask := strings.TrimSpace(*task)
	if userTask == "" {
		userTask = "List the files in the workspace, then create a small hello.txt saying hello and read it back."
		fmt.Println("No -task given; using sample task.")
	}
	fmt.Println("Task:", userTask)

	res, err := agent.Run(context.Background(), userTask)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("\n=== Final answer ===\n" + res.Answer)

	usage := res.Usage
	fmt.Printf("\n=== Token usage ===\nmodel calls: %d | prompt: %d | completion: %d | total: %d\n",
		usage.Calls, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens())

	if *webAddr != "" {
		tracer.SetDone()
		fmt.Println("\n(轨迹页面保持可用，按 Ctrl+C 退出)")
		select {}
	}
}

// modelFromFile builds the model backend from the parsed env file.
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
