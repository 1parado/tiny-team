package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := strings.Join([]string{
		"# backend config",
		"",
		"PROTOCOL=anthropic",
		"BASE_URL=https://api.anthropic.com/v1",
		"API_KEY='sk-test 123' # inline comment",
		`MODEL="claude-sonnet-4-5"`,
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := loadDotEnv(path)
	if err != nil {
		t.Fatalf("loadDotEnv: %v", err)
	}
	want := map[string]string{
		"PROTOCOL": "anthropic",
		"BASE_URL": "https://api.anthropic.com/v1",
		"API_KEY":  "sk-test 123",
		"MODEL":    "claude-sonnet-4-5",
	}
	for key, expected := range want {
		if got[key] != expected {
			t.Errorf("%s = %q, want %q", key, got[key], expected)
		}
	}
}

func TestLoadDotEnvMalformedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("PROTOCOL=openai\nBAD LINE\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := loadDotEnv(path)
	if err == nil || !strings.Contains(err.Error(), "expected KEY=VALUE") {
		t.Fatalf("err = %v, want malformed line error", err)
	}
}

// TestEstimateUsage verifies the local tiktoken fallback used when an
// endpoint does not report usage. Skipped when the BPE dictionary cannot be
// loaded (e.g. offline machine); production runs degrade the same way.
func TestEstimateUsage(t *testing.T) {
	if tokenizerFor("cl100k_base") == nil {
		t.Skip("BPE dictionary not loadable (offline)")
	}
	prompt, completion := estimateUsage("any-model",
		[]ChatMessage{{Role: "user", Content: "hello world"}},
		ChatMessage{Content: "hi there"})
	if prompt == 0 || completion == 0 {
		t.Fatalf("prompt=%d completion=%d, want non-zero estimates", prompt, completion)
	}
}
