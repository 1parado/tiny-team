package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// samplePaper is a minimal research-paper-like markdown used by the demo.
const samplePaper = `# Attention Is Not All You Need: A Toy Study

## Abstract
We present a toy study of sequence models. We show that a simple averaging
baseline can rival attention on synthetic copy tasks under limited data.
Our results suggest inductive bias still matters when samples are scarce.

## Introduction
Transformer models dominate sequence modeling. However, the contribution of
attention versus other design choices remains debated. This paper isolates
the effect of attention with controlled synthetic tasks.

## Method
We compare three models: (1) mean-pool baseline, (2) single-head attention,
and (3) two-layer LSTM. Training uses 5k synthetic sequences. Evaluation is
exact-match accuracy on held-out copy and reverse tasks.

## Experiments
On the copy task, mean-pool reaches 91.2% while attention reaches 93.5%.
On reverse, attention leads by 4.1 points. With only 500 samples, mean-pool
closes the gap to within 1.0 point.

## Conclusion
Attention helps, but simpler biases can be competitive in low-data regimes.
We recommend reporting data-scale ablations alongside architecture claims.
`

// TestSelfAuthorPaperDecomposePlugin demonstrates the intended product loop:
//
//  1. Manager delegates a research-tooling task to a sub-agent (agent-as-tool).
//  2. The sub-agent authors new plugins via create_tool (everything is a plugin).
//  3. It runs those plugins on a workspace paper and reports a structured breakdown.
//
// The LLM is fully scripted so the demo is offline and CI-safe.
func TestSelfAuthorPaperDecomposePlugin(t *testing.T) {
	ws := t.TempDir()
	paperPath := "papers/toy_study.md"
	full := filepath.Join(ws, filepath.FromSlash(paperPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(samplePaper), 0o644); err != nil {
		t.Fatal(err)
	}

	// --- Sub-agent scripts: author 3 paper tools, run them, final_answer ---
	createSectionsArgs := `{"name":"paper_sections","description":"List markdown section headings (##) in a paper file.","parameters":{"type":"object","properties":{"path":{"type":"string","description":"Relative path to the paper"}},"required":["path"]},"command":"grep -E '^## ' {{path}} | sed 's/^## //'"}`
	createAbstractArgs := `{"name":"paper_abstract","description":"Extract the Abstract section body from a markdown paper.","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]},"command":"awk '/^## Abstract/{p=1;next}/^## /{if(p)exit}p' {{path}}"}`
	createClaimsArgs := `{"name":"paper_key_sentences","description":"Extract lines that look like quantitative experimental claims.","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]},"command":"grep -E '[0-9]+(\\.[0-9]+)?%|[0-9]+k ' {{path}} || true"}`

	engSrv, _ := fakeServer(t,
		openAIResp(toolCallMsg("e1", "create_tool", createSectionsArgs), 20, 8),
		openAIResp(toolCallMsg("e2", "create_tool", createAbstractArgs), 22, 8),
		openAIResp(toolCallMsg("e3", "create_tool", createClaimsArgs), 24, 8),
		openAIResp(toolCallMsg("e4", "paper_sections", `{"path":"papers/toy_study.md"}`), 26, 4),
		openAIResp(toolCallMsg("e5", "paper_abstract", `{"path":"papers/toy_study.md"}`), 28, 4),
		openAIResp(toolCallMsg("e6", "paper_key_sentences", `{"path":"papers/toy_study.md"}`), 30, 4),
		openAIResp(finalAnswerMsg(
			"Paper decomposition of papers/toy_study.md:\n"+
				"Sections: Abstract, Introduction, Method, Experiments, Conclusion\n"+
				"Abstract: toy study; averaging baseline rivals attention on synthetic copy tasks.\n"+
				"Key numbers: 91.2%, 93.5%, 4.1 points, 500 samples, 1.0 point.\n"+
				"Plugins authored: paper_sections, paper_abstract, paper_key_sentences.",
		), 40, 12),
	)

	mgrSrv, _ := fakeServer(t,
		openAIResp(toolCallMsg("m1", "paper_engineer",
			`{"task":"Author plugins to decompose the markdown paper at papers/toy_study.md into sections, abstract, and key quantitative claims. Register the tools with create_tool, run them, and report the structured breakdown."}`),
			30, 10),
		openAIResp(finalAnswerMsg(
			"Sub-agent authored paper_sections / paper_abstract / paper_key_sentences and decomposed the toy study. "+
				"Attention helps but simple biases compete in low-data regimes (91.2% vs 93.5% on copy).",
		), 50, 15),
	)

	engReg := DefaultRegistry(ws)
	engineer := &Agent{
		Name:        "paper_engineer",
		Description: "Implements research-paper tooling: authors plugins and runs them on workspace papers.",
		Model:       &OpenAIModel{cfg: ModelConfig{BaseURL: engSrv.URL, ModelID: "fake"}},
		Registry:    engReg,
		Workspace:   ws,
		MaxSteps:    12,
		Verbose:     testing.Verbose(),
	}

	manager := &Agent{
		Name:          "manager",
		Description:   "Coordinates specialists. Delegates paper-tooling work to paper_engineer.",
		Model:         &OpenAIModel{cfg: ModelConfig{BaseURL: mgrSrv.URL, ModelID: "fake"}},
		ManagedAgents: []*Agent{engineer},
		MaxSteps:      6,
		Verbose:       testing.Verbose(),
	}

	task := "We need reusable plugins that can dissect a research paper in the workspace. " +
		"Assign paper_engineer to implement and demonstrate them on papers/toy_study.md."
	res, err := manager.Run(context.Background(), task)
	if err != nil {
		t.Fatalf("manager.Run: %v", err)
	}
	if !strings.Contains(res.Answer, "paper_sections") && !strings.Contains(res.Answer, "91.2%") {
		t.Fatalf("unexpected manager answer: %q", res.Answer)
	}
	if res.Usage.Calls < 3 {
		t.Fatalf("usage.Calls=%d, want at least manager+engineer calls", res.Usage.Calls)
	}

	if engineer.Registry == nil {
		t.Fatal("engineer registry is nil after run")
	}
	tool, ok := engineer.Registry.Get("paper_sections")
	if !ok {
		t.Log("paper_sections not on engineer.Registry after isolation clone")
	} else {
		out, err := tool.Execute(context.Background(), `{"path":"papers/toy_study.md"}`)
		if err != nil {
			t.Fatalf("paper_sections: %v", err)
		}
		for _, want := range []string{"Abstract", "Introduction", "Method", "Experiments", "Conclusion"} {
			if !strings.Contains(out, want) {
				t.Fatalf("paper_sections missing %q in %q", want, out)
			}
		}
	}
}

// TestPaperPluginsShellTemplates checks the shell templates without an LLM.
func TestPaperPluginsShellTemplates(t *testing.T) {
	ws := t.TempDir()
	full := filepath.Join(ws, "papers", "toy_study.md")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(samplePaper), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := DefaultRegistry(ws)
	create, ok := reg.Get("create_tool")
	if !ok {
		t.Fatal("create_tool missing")
	}

	specs := []struct {
		args    string
		name    string
		wantSub string
	}{
		{
			args:    `{"name":"paper_sections","description":"list ## headings","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]},"command":"grep -E '^## ' {{path}} | sed 's/^## //'"}`,
			name:    "paper_sections",
			wantSub: "Experiments",
		},
		{
			args:    `{"name":"paper_abstract","description":"abstract body","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]},"command":"awk '/^## Abstract/{p=1;next}/^## /{if(p)exit}p' {{path}}"}`,
			name:    "paper_abstract",
			wantSub: "toy study",
		},
		{
			args:    `{"name":"paper_key_sentences","description":"numeric claims","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]},"command":"grep -E '[0-9]+(\\.[0-9]+)?%' {{path}} || true"}`,
			name:    "paper_key_sentences",
			wantSub: "91.2%",
		},
	}

	for _, sp := range specs {
		if _, err := create.Execute(context.Background(), sp.args); err != nil {
			t.Fatalf("create %s: %v", sp.name, err)
		}
		tool, ok := reg.Get(sp.name)
		if !ok {
			t.Fatalf("%s not registered", sp.name)
		}
		out, err := tool.Execute(context.Background(), `{"path":"papers/toy_study.md"}`)
		if err != nil {
			t.Fatalf("%s execute: %v (%s)", sp.name, err, out)
		}
		if !strings.Contains(strings.ToLower(out), strings.ToLower(sp.wantSub)) {
			t.Fatalf("%s output %q missing %q", sp.name, out, sp.wantSub)
		}
		t.Logf("%s => %s", sp.name, strings.TrimSpace(out))
	}
}
