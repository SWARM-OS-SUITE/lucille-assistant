package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Claude (Anthropic) as a chat provider, over plain HTTP like every other provider here.
// This project is stdlib-only on purpose (env.sh sets GOPROXY=off), so the Anthropic Go SDK is
// not used; the Messages API is a single POST.
//
// Defaults are chosen for a spoken companion on a small budget: Claude Haiku 5.5 at low
// effort. ANTHROPIC_MODEL picks another model, ANTHROPIC_EFFORT another effort level
// (low|medium|high|xhigh|max, or "none" to send no effort at all). No sampling parameters are
// sent: current models reject non-default ones. Thinking is left at the model's default; at low
// effort that costs little, and max_tokens leaves room for it (an empty reply is an error that
// says why, the same lesson as the Qwen models).

var anthropicMessagesURL = "https://api.anthropic.com/v1/messages"

const (
	anthropicVersion      = "2023-06-01"
	defaultAnthropicModel = "claude-haiku-5-5"
)

func anthropicModel() string { return envOr("ANTHROPIC_MODEL", defaultAnthropicModel) }

func (a *App) callClaude(ctx context.Context, req ChatRequest) (string, string, error) {
	key := a.anthropicKey()
	model := anthropicModel()

	var msgs []map[string]string
	for _, h := range req.History {
		if (h.Role != "user" && h.Role != "assistant") || strings.TrimSpace(h.Content) == "" {
			continue
		}
		if len(msgs) == 0 && h.Role != "user" {
			continue // a conversation must open with the user
		}
		msgs = append(msgs, map[string]string{"role": h.Role, "content": h.Content})
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": req.Message})

	body := map[string]any{
		"model": model,
		// Thinking tokens count against max_tokens, so leave room beyond a few spoken sentences.
		"max_tokens": max(req.tokens(1000), 1024),
		"system":     req.systemOr(a),
		"messages":   msgs,
	}
	if eff := envOr("ANTHROPIC_EFFORT", "low"); eff != "none" {
		body["output_config"] = map[string]any{"effort": eff}
	}
	b, _ := json.Marshal(body)
	hreq, err := http.NewRequestWithContext(ctx, "POST", anthropicMessagesURL, bytes.NewReader(b))
	if err != nil {
		return "", model, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("x-api-key", key)
	hreq.Header.Set("anthropic-version", anthropicVersion)

	resp, err := httpc.Do(hreq)
	if err != nil {
		return "", model, fmt.Errorf("claude: %s", scrubKey(err.Error(), key))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		eb, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", model, fmt.Errorf("claude %d: %s", resp.StatusCode, scrubKey(string(eb), key))
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", model, err
	}
	var text strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	reply := strings.TrimSpace(text.String())
	if reply == "" {
		return "", model, fmt.Errorf("claude: empty reply (stop_reason=%q)", out.StopReason)
	}
	return reply, model, nil
}
