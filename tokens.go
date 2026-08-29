package main

import (
	"fmt"
	"os"
	"sync"

	tiktoken "github.com/pkoukk/tiktoken-go"
)

// Token counting: endpoint-reported usage is preferred; when a response
// carries none, tokens are estimated locally with tiktoken-go. BPE
// dictionaries are downloaded once on first use and cached
// (see TIKTOKEN_CACHE_DIR); if they cannot be loaded, estimation degrades to
// zero instead of failing the run.

var (
	tokenizerMu       sync.Mutex
	tokenizerCache    = map[string]*tiktoken.Tiktoken{}
	tokenizerWarnOnce sync.Once
)

// tokenizerFor returns a tokenizer for the model, trying an exact match via
// EncodingForModel first and common OpenAI encodings as fallback. Returns nil
// when no dictionary can be loaded.
func tokenizerFor(modelID string) *tiktoken.Tiktoken {
	tokenizerMu.Lock()
	defer tokenizerMu.Unlock()
	if t, ok := tokenizerCache[modelID]; ok {
		return t
	}
	var t *tiktoken.Tiktoken
	for _, name := range []string{"", "cl100k_base", "o200k_base"} {
		var err error
		if name == "" {
			t, err = tiktoken.EncodingForModel(modelID)
		} else {
			t, err = tiktoken.GetEncoding(name)
		}
		if err == nil {
			break
		}
	}
	if t == nil {
		tokenizerWarnOnce.Do(func() {
			fmt.Fprintln(os.Stderr, "[tiktoken] token estimation unavailable (BPE dictionary not loadable); "+
				"usage is counted only when the endpoint reports it")
		})
	}
	tokenizerCache[modelID] = t
	return t
}

// estimateUsage approximates the prompt tokens of a conversation memory and
// the completion tokens of an assistant reply (content plus tool-call
// arguments). Counts are approximate for non-OpenAI models.
func estimateUsage(modelID string, messages []ChatMessage, reply ChatMessage) (prompt, completion int) {
	t := tokenizerFor(modelID)
	if t == nil {
		return 0, 0
	}
	count := func(s string) int {
		if s == "" {
			return 0
		}
		return len(t.EncodeOrdinary(s))
	}
	for _, msg := range messages {
		prompt += count(msg.Role) + count(msg.Content)
		for _, call := range msg.ToolCalls {
			prompt += count(call.Function.Name) + count(call.Function.Arguments)
		}
	}
	completion = count(reply.Content)
	for _, call := range reply.ToolCalls {
		completion += count(call.Function.Name) + count(call.Function.Arguments)
	}
	return prompt, completion
}
