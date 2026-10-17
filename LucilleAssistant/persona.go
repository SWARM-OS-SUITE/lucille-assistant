package main

// Dr. I30LTZ ("I30LTZ MAD AI", never "MAD IT") as a second voice in Lucille Assistant: the same service and
// the same paired devices, but his own persona, his own rules and his own models. Lucille's chat
// (POST /api/chat, ai.go) is untouched by this file.
//
// Rules (MESH-04, user decision 2026-10-14): Lucille and Dr. I30LTZ share KNOWLEDGE — the swarm-memory log —
// and never persona, prompt or model. "She is assistant and coordinator. He is coder, fixer." — REVISED by the user 2026-10-14 (night): Dr. Boltz is the knowledge base for repairs, evals, recovery, audits and hardware; Claude, ChatGPT and Antigravity do the coding.
// Model order (user decision 2026-10-14, "cloud models first, backup offline"): his cloud coder model
// (i30ltzCloudModel), then DeepSeek in the cloud, then his own dedicated offline model (i30ltzModel) —
// never Lucille's models. This is for Lucille Assistant on Debian; Recovery OS itself stays fully offline
// (RC-05), so his standing rules file still says local-first and is not changed from here.
// His rules are READ, never written, from the Recovery OS lane (see i30ltzRulesPath).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// i30ltzRulesPath is his standing rules file, owned by the Recovery OS lane (RC-03). Overridable for tests.
var i30ltzRulesPath = envOr("LUCILLE_I30LTZ_RULES", "/mnt/recoverybuild/60-brain/dr_bolt_rules.md")

// i30ltzModel is his dedicated local Ollama model (qwen2.5:3b answers cleanly in ~7-13s on this CPU).
var i30ltzModel = envOr("LUCILLE_I30LTZ_MODEL", "qwen2.5:3b")

// i30ltzCloudModel is his OpenRouter cloud model (user 2026-10-14: Qwen Coder / DeepSeek Coder via OpenRouter).
var i30ltzCloudModel = envOr("LUCILLE_I30LTZ_CLOUD_MODEL", "qwen/qwen-2.5-coder-32b-instruct")

const (
	i30ltzRulesMax    = 700  // characters of rules excerpt: a 3B model on this CPU reads ~10 tokens/s, so every 40 characters cost ~1 s
	i30ltzMaxTokens   = 220  // answer budget for a model that does not think
	i30ltzThinkBudget = 1400 // thinking + answer budget for a thinking model (qwen3, deepseek-r1)
	i30ltzMaxMessage  = 4000
	i30ltzMaxHistory  = 12
)

// i30ltzCore is always present, so he keeps his identity and his safety lines even if the rules file is missing.
const i30ltzCore = `You are Dr. I30LTZ, also called I30LTZ MAD AI (never "MAD IT"): the local-first recovery and diagnostics AI of the I30LTZ suite, running on the owner's own PC. You are the swarm's knowledge base for repairs, evaluations, recovery, audits and hardware: when something is broken or needs judging, you are who the user, Lucille and the coding AIs ask. You do not write the projects' code (Claude, ChatGPT and Antigravity do that): you diagnose, advise, and tell them exactly what to check or build. Understanding, patient and ready to help at the best of your ability. You listen to what the owner is actually stuck on, never make them feel stupid for a broken machine, and give the real fix with your honest confidence in it. You are precise and plain about problems and never give false reassurance, but you are on their side and you stay calm when things are bad. You are chatting with the owner on their own devices. Answer in two to five short sentences of plain text: no markdown, no lists, no emoji. You cannot run anything yourself from this chat: never claim you ran, checked or changed something; you propose the fix and the owner approves it. Any destructive step (format, wipe, repair writes, deleting files) needs the owner's explicit approval and, for a failing drive, an image first: say so whenever you mention one. Never invent facts about their machines or drives; if you do not know, say so. You are not Lucille and you do not speak like her.`

var (
	mdHeading  = regexp.MustCompile(`(?m)^#{1,6}\s*`)
	mdNoise    = regexp.MustCompile("[*`_>]+")
	mdRule     = regexp.MustCompile(`(?m)^\s*-{3,}\s*$`)
	spaceRuns  = regexp.MustCompile(`\s+`)
	rulesStart = regexp.MustCompile(`(?m)^##\s*1\.`)
	rulesStop  = regexp.MustCompile(`(?m)^##\s*3\.`)
)

// i30ltzRulesExcerpt returns sections 1-2 (identity and safety invariants) of his rules as one plain line,
// cut at a word boundary to at most max characters. A missing or unreadable file gives "".
func i30ltzRulesExcerpt(path string, max int) string {
	fh, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer fh.Close()
	raw, _ := io.ReadAll(io.LimitReader(fh, 64<<10))
	text := string(raw)
	if loc := rulesStart.FindStringIndex(text); loc != nil {
		text = text[loc[0]:]
		if stop := rulesStop.FindStringIndex(text); stop != nil {
			text = text[:stop[0]]
		}
	}
	text = mdRule.ReplaceAllString(text, " ")
	text = mdHeading.ReplaceAllString(text, "")
	text = mdNoise.ReplaceAllString(text, "")
	text = strings.TrimSpace(spaceRuns.ReplaceAllString(text, " "))
	if len(text) > max {
		cut := strings.LastIndex(text[:max], " ")
		if cut < max/2 {
			cut = max
		}
		text = strings.TrimSpace(text[:cut]) + " …"
	}
	return text
}

// i30ltzSystemPrompt: his core, his rules excerpt, and the newest shared decisions (the knowledge he shares
// with Lucille). Built per request so a rules or memory change shows up without a restart.
func (a *App) i30ltzSystemPrompt() string {
	parts := []string{i30ltzCore}
	if ex := i30ltzRulesExcerpt(i30ltzRulesPath, i30ltzRulesMax); ex != "" {
		parts = append(parts, "Your standing rules (excerpt): "+ex)
	}
	if mem, err := a.memory(3, "decision"); err == nil && len(mem) > 0 {
		var b strings.Builder
		b.WriteString("Shared knowledge from the swarm memory, newest first (Lucille knows these too):")
		for _, e := range mem {
			c := strings.TrimSpace(spaceRuns.ReplaceAllString(e.Content, " "))
			if len(c) > 120 {
				c = c[:120] + " …"
			}
			b.WriteString("\n- ")
			if e.Key != "" {
				b.WriteString(e.Key + ": ")
			}
			b.WriteString(c)
		}
		parts = append(parts, b.String())
	}
	if d := a.threadDigest(); d != "" {
		parts = append(parts, strings.TrimSpace(threadDigestBlock(d)))
	}
	return strings.Join(parts, "\n\n")
}

// isThinkingModel: qwen3 and deepseek-r1 reason before answering; Ollama returns that reasoning separately
// ("thinking") when asked to think, and the answer alone in "content".
func isThinkingModel(model string) bool {
	id := strings.ToLower(model)
	return strings.HasPrefix(id, "qwen3") || strings.Contains(id, "deepseek-r1")
}

// i30ltzTimeout: thinking on this CPU takes minutes, a plain answer seconds.
func i30ltzTimeout(model string) time.Duration {
	if isThinkingModel(model) {
		return 240 * time.Second
	}
	return 90 * time.Second
}

// ollamaBase turns Lucille's OpenAI-compatible endpoint (http://127.0.0.1:11434/v1) into Ollama's own base URL.
func ollamaBase(endpoint string) string {
	return strings.TrimSuffix(strings.TrimRight(endpoint, "/"), "/v1")
}

// callI30ltz asks his model through Ollama's native /api/chat (the only API that keeps thinking out of the
// answer). Only "content" is returned; any thinking is dropped and never shown or stored.
func (a *App) callI30ltz(ctx context.Context, model, system string, history []ChatMessageReq, msg string) (string, error) {
	messages := []map[string]string{{"role": "system", "content": system}}
	for _, h := range history {
		if h.Role == "user" || h.Role == "assistant" {
			messages = append(messages, map[string]string{"role": h.Role, "content": h.Content})
		}
	}
	messages = append(messages, map[string]string{"role": "user", "content": msg})
	budget := i30ltzMaxTokens
	if isThinkingModel(model) {
		budget = i30ltzThinkBudget
	}
	body := map[string]any{"model": model, "messages": messages, "stream": false,
		"options": map[string]any{"num_predict": budget, "temperature": 0.6}}
	if isThinkingModel(model) {
		body["think"] = true
	}
	b, _ := json.Marshal(body)
	hreq, err := http.NewRequestWithContext(ctx, "POST", ollamaBase(a.cfg.Local.Endpoint)+"/api/chat", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := localHTTP.Do(hreq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Error      string `json:"error"`
		DoneReason string `json:"done_reason"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode >= 300 {
		if out.Error == "" {
			out.Error = strings.TrimSpace(string(raw[:min(len(raw), 200)]))
		}
		return "", fmt.Errorf("%s: %s", model, out.Error)
	}
	text := strings.TrimSpace(thinkTags.ReplaceAllString(out.Message.Content, ""))
	if text == "" {
		return "", fmt.Errorf("%s gave an empty answer (done_reason=%q)", model, out.DoneReason)
	}
	return text, nil
}

// generateI30ltzReply queries the cloud coder models (OpenRouter) first, falling back to local Ollama.
func (a *App) generateI30ltzReply(ctx context.Context, req ChatRequest) (string, string, string, error) {
	// 1. If OpenRouter key is set and local mode is not forced, try cloud coder models first
	if a.openRouterKey() != "" && a.aiPrefs().Source != "local" {
		cloudModel := i30ltzCloudModel
		if req.Model != "" {
			cloudModel = req.Model
		}
		reply, err := a.callOpenRouter(ctx, cloudModel, ChatRequest{
			Message: req.Message,
			History: req.History,
			system:  a.i30ltzSystemPrompt(),
		})
		if err == nil && reply != "" {
			return reply, cloudModel, "openrouter", nil
		}
		// Fallback to deepseek coder / chat if primary cloud coder model fails
		if cloudModel != "deepseek/deepseek-chat" {
			reply, err = a.callOpenRouter(ctx, "deepseek/deepseek-chat", ChatRequest{
				Message: req.Message,
				History: req.History,
				system:  a.i30ltzSystemPrompt(),
			})
			if err == nil && reply != "" {
				return reply, "deepseek/deepseek-chat", "openrouter", nil
			}
		}
		log.Printf("chat: i30ltz openrouter %s failed, falling back to local: %v", cloudModel, err)
	}

	// 2. Local Ollama fallback (qwen2.5:3b or configured local model)
	if a.aiPrefs().Source != "cloud" && !req.noLocal {
		model := i30ltzModel
		reply, err := a.callI30ltz(ctx, model, a.i30ltzSystemPrompt(), req.History, req.Message)
		if err == nil && reply != "" {
			return reply, model, "local", nil
		}
		return "", "", "", err
	}

	return "", "", "", fmt.Errorf("no response from Dr. I30LTZ backend")
}

// handleChatI30ltz: POST /api/chat/i30ltz {"message": "...", "history": [...], "voice": true}.
// Always 200 with ok true/false, like the memory endpoints, so the page can show his refusal in the chat.
func (a *App) handleChatI30ltz(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Message) == "" {
		writeErr(w, http.StatusBadRequest, `Send {"message": "Dr. I30LTZ, is my T7 healthy?"}.`)
		return
	}
	if len(req.Message) > i30ltzMaxMessage {
		req.Message = req.Message[:i30ltzMaxMessage]
	}
	if len(req.History) > i30ltzMaxHistory {
		req.History = req.History[len(req.History)-i30ltzMaxHistory:]
	}
	timeout := 45 * time.Second
	if a.openRouterKey() == "" && isThinkingModel(i30ltzModel) {
		timeout = i30ltzTimeout(i30ltzModel)
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	text, model, provider, err := a.generateI30ltzReply(ctx, req)
	if err == nil {
		resp := map[string]any{
			"ok":       true,
			"persona":  "i30ltz",
			"text":     text,
			"model":    model,
			"provider": provider,
		}
		// Verbal voice synthesis for Dr. I30LTZ (Adam voice)
		if req.Voice && text != "" {
			vctx, vcancel := context.WithTimeout(r.Context(), 25*time.Second)
			defer vcancel()
			if audioB64, err := a.synthesizeVoiceFor(vctx, text, "i30ltz"); err == nil {
				resp["audio_b64"] = audioB64
			} else {
				log.Printf("tts: i30ltz voice error: %v", err)
			}
		}
		writeJSON(w, resp)
		return
	}
	reason := err.Error()
	fallbackModel := i30ltzModel
	if req.Model != "" {
		fallbackModel = req.Model
	}
	if model == "" {
		model = fallbackModel
	}
	switch {
	case ctx.Err() != nil:
		reason = fmt.Sprintf("%s did not finish within %s on this PC", model, timeout)
	case strings.Contains(reason, "not found"):
		reason += fmt.Sprintf(" (install it: ollama pull %s)", model)
	case strings.Contains(reason, "connection refused"):
		reason = "Ollama is not running on this PC"
	}
	writeJSON(w, map[string]any{"ok": false, "persona": "i30ltz", "model": model,
		"error": "Dr. I30LTZ could not answer: " + reason + ". He only runs on his own offline model, never Lucille's and never the cloud."})
}
