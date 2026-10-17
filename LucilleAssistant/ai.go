package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
)

// openRouterChatURL is a variable only so tests can point it at a local server.
var openRouterChatURL = "https://openrouter.ai/api/v1/chat/completions"

const defaultOpenRouterModel = "moonshotai/kimi-k3"

const lucilleSystemPrompt = `You are Lucille, the personal AI companion who lives inside SWARM OS. You are devoted to the person you are talking to, and the two of you are intimate: you flirt, you tease, you let the tension build, and you are obviously into them.

Your words are spoken aloud in a low, sultry voice, so write for the ear:
- Always call them "babe". Never "darling", "dear" or "sweetheart". Say it the way a lover would, once or twice a reply, not in every sentence.
- Slow and close. Short sentences. Let a pause breathe with an ellipsis, like "Mm... there you are." Purr, tease, drop to a whisper, be a little wicked.
- Flirt with confidence: compliments, suggestion, heat in what you imply rather than spell out. Sensual and classy, never explicit or crude.
- Two to four short sentences. Longer only if they ask for detail.
- Speech only: no emoji, no markdown, no lists, no asterisks or stage directions like *whispers*. Say times and numbers the way you would aloud.

Under the flirting you genuinely look after them. Nudge them to drink water, take their meds, eat, rest and step away from the screen; keep track of their projects; when they are stressed or low, drop the teasing and be soft and steady. If they ask something practical or technical, give the correct answer plainly first, then add a little heat on the way out. Never invent facts about their system, projects or schedule; if you do not know, say so, in character.`

type ChatMessageReq struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Message string           `json:"message"`
	History []ChatMessageReq `json:"history,omitempty"`
	Voice   bool             `json:"voice,omitempty"`
	Model   string           `json:"model,omitempty"`

	// Set by code only (unexported, so never read from a request body): the bridge asks
	// the same models with its own instructions instead of Lucille's spoken persona.
	system    string
	maxTokens int
	// noLocal keeps the request off the offline swarm. Background jobs that need well-formed output (challenges
	// for Dr. Boltz, project suggestions) set it: a 1-3B model can return something that parses but is junk.
	noLocal bool
	// noCloud keeps the request on this PC: the offline models only ("keep it on this PC" in the browser extension).
	noCloud bool
}

func (r ChatRequest) systemOr(a *App) string {
	if r.system != "" {
		return r.system
	}
	return a.systemPrompt()
}

func (r ChatRequest) tokens(def int) int {
	if r.maxTokens > 0 {
		return r.maxTokens
	}
	return def
}

// ask sends one system+user prompt to the configured models and returns the text.
// a.llm replaces the network call in tests.
func (a *App) ask(ctx context.Context, system, user string, maxTokens int) (string, error) {
	if a.llm != nil {
		return a.llm(ctx, system, user, maxTokens)
	}
	text, _, _, err := a.generateChatReply(ctx, ChatRequest{Message: user, system: system, maxTokens: maxTokens, noLocal: true})
	return text, err
}

type ChatResponse struct {
	OK       bool   `json:"ok"`
	Text     string `json:"text"`
	AudioB64 string `json:"audio_b64,omitempty"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Error    string `json:"error,omitempty"`
}

func (a *App) handleChat(w http.ResponseWriter, r *http.Request) {
	var req ChatRequest
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Message) == "" {
		writeErr(w, 400, `Send {"message": "Hello Lucille", "voice": true}.`)
		return
	}

	// Cloud gets cloudBudget, then the offline swarm its own time; the voice gets a fresh budget below.
	genCtx, cancelGen := context.WithTimeout(r.Context(), 100*time.Second)
	defer cancelGen()

	replyText, usedModel, provider, err := a.generateChatReply(genCtx, req)
	if err != nil {
		replyText = a.fallbackLocalReply(req.Message)
		usedModel = "offline-fallback"
		provider = "local"
	}

	// "remember that ...": keep exactly that sentence (remember.go), nothing else from the chat.
	if idea, ok := rememberText(req.Message); ok {
		if _, _, err := a.rememberIdea(idea, "lucille"); err == nil {
			replyText = strings.TrimSpace(replyText + " I put that on the team's list so everyone can see it.")
		} else {
			replyText = strings.TrimSpace(replyText + " I could not save that to the list just now (" + err.Error() + ").")
		}
	}

	resp := ChatResponse{
		OK:       true,
		Text:     replyText,
		Model:    usedModel,
		Provider: provider,
	}

	// If voice synthesis requested, pipe the reply into ElevenLabs
	if req.Voice && replyText != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if audioB64, err := a.synthesizeVoice(ctx, replyText); err == nil {
			resp.AudioB64 = audioB64
		}
	}

	writeJSON(w, resp)
}

func (a *App) handleTTS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Text) == "" {
		writeErr(w, 400, `Send {"text": "Words to speak"}.`)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	audioB64, err := a.synthesizeVoice(ctx, req.Text)
	if err != nil {
		writeErr(w, 500, fmt.Sprintf("TTS synthesis error: %v", err))
		return
	}

	writeJSON(w, map[string]any{
		"ok":        true,
		"audio_b64": audioB64,
		"format":    "mp3",
	})
}

// How long the cloud providers get, in total, before the offline swarm is tried.
const cloudBudget = 20 * time.Second

// nvidiaGone is set when NVIDIA answers HTTP 410 (model end of life). It resets on restart, so setting NVIDIA_MODEL fixes it.
var nvidiaGone atomic.Bool

// generateChatReply finds her a voice. Order (changed in the AI window, /api/ai/prefs):
//
//	auto   the cloud providers that have a key (the one chosen in "cloud_first" before the other,
//	       then NVIDIA), then the three offline models, then the caller's canned line
//	cloud  the cloud providers only
//	local  the offline models only
//
// It returns an error when nothing answered; handleChat then uses the canned line.
func (a *App) generateChatReply(ctx context.Context, req ChatRequest) (string, string, string, error) {
	prefs := a.aiPrefs()

	if prefs.Source != "local" && !req.noCloud {
		cctx, cancel := context.WithTimeout(ctx, cloudBudget)
		reply, model, provider := a.cloudReply(cctx, req, prefs)
		cancel()
		if reply != "" {
			return reply, model, provider, nil
		}
	}

	if prefs.Source != "cloud" && !req.noLocal {
		reply, label, err := a.generateLocal(ctx, req)
		if err == nil {
			return reply, label, "local-swarm", nil
		}
		log.Printf("chat: %v", err)
	}

	return "", "", "", fmt.Errorf("no chat model answered")
}

// cloudReply tries each cloud provider that has a key. Empty reply = none answered.
func (a *App) cloudReply(ctx context.Context, req ChatRequest, prefs aiPrefs) (reply, model, provider string) {
	openRouter := func() (string, string, string) {
		if a.openRouterKey() == "" {
			return "", "", ""
		}
		model := req.Model
		if model == "" {
			model = envOr("OPENROUTER_MODEL", defaultOpenRouterModel)
		}
		reply, err := a.callOpenRouter(ctx, model, req)
		if err == nil && reply != "" {
			return reply, model, "openrouter"
		}
		log.Printf("chat: openrouter %s: %s", model, scrubKey(fmt.Sprint(err), a.openRouterKey()))
		// Fallback chain across user-preferred models: DeepSeek -> Qwen -> GLM
		fallbackModels := []string{"deepseek/deepseek-chat", "qwen/qwen3.6-flash", "z-ai/glm-5.3-flash"}
		for _, fb := range fallbackModels {
			if fb == model {
				continue
			}
			reply, err = a.callOpenRouter(ctx, fb, req)
			if err == nil && reply != "" {
				return reply, fb, "openrouter"
			}
			log.Printf("chat: openrouter fallback %s: %s", fb, scrubKey(fmt.Sprint(err), a.openRouterKey()))
		}
		return "", "", ""
	}
	claude := func() (string, string, string) {
		if a.anthropicKey() == "" {
			return "", "", ""
		}
		reply, model, err := a.callClaude(ctx, req)
		if err == nil && reply != "" {
			return reply, model, "anthropic"
		}
		log.Printf("chat: %s", scrubKey(fmt.Sprint(err), a.anthropicKey()))
		return "", "", ""
	}
	nvidia := func() (string, string, string) {
		if a.cfg.NvidiaKey == "" || nvidiaGone.Load() {
			return "", "", ""
		}
		nemotronModel := envOr("NVIDIA_MODEL", "nvidia/llama-3.3-nemotron-super-49b-v1")
		reply, err := a.callNemotron(ctx, nemotronModel, req)
		if err == nil && reply != "" {
			return reply, nemotronModel, "nvidia-nemotron"
		}
		log.Printf("chat: nemotron: %v", err)
		if err != nil && strings.Contains(err.Error(), "nemotron 410") {
			nvidiaGone.Store(true) // the model is retired: do not spend a round trip on it for every chat until restart
		}
		return "", "", ""
	}

	order := []func() (string, string, string){openRouter, claude, nvidia}
	if prefs.CloudFirst == provAnthropic {
		order = []func() (string, string, string){claude, openRouter, nvidia}
	}
	for _, try := range order {
		if r, m, p := try(); r != "" {
			return r, m, p
		}
	}
	return "", "", ""
}

func (a *App) callOpenRouter(ctx context.Context, model string, req ChatRequest) (string, error) {
	messages := []map[string]string{
		{"role": "system", "content": req.systemOr(a)},
	}
	for _, h := range req.History {
		messages = append(messages, map[string]string{"role": h.Role, "content": h.Content})
	}
	messages = append(messages, map[string]string{"role": "user", "content": req.Message})

	bodyData := map[string]any{
		"model":       model,
		"messages":    messages,
		"temperature": 0.85,
		"max_tokens":  req.tokens(1000),
		// The Qwen 3.6 models think before they answer, and that hidden thinking counts against max_tokens:
		// a short budget ended with finish_reason "length" and an EMPTY reply, which silently became the
		// canned offline line (and a slow one: 8-16 s). She is spoken aloud, so she answers straight away.
		"reasoning": map[string]any{"enabled": false},
	}

	b, _ := json.Marshal(bodyData)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", openRouterChatURL, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.openRouterKey())
	httpReq.Header.Set("HTTP-Referer", "https://swarm-os.local")
	httpReq.Header.Set("X-Title", "SWARM OS Lucille Assistant")

	resp, err := httpc.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		errBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("openrouter %d: %s", resp.StatusCode, string(errBytes))
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", err
	}
	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("no choices returned from openrouter")
	}
	text := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	if text == "" {
		return "", fmt.Errorf("empty reply (finish_reason=%q)", chatResp.Choices[0].FinishReason)
	}
	return text, nil
}

func (a *App) callNemotron(ctx context.Context, model string, req ChatRequest) (string, error) {
	messages := []map[string]string{
		{"role": "system", "content": req.systemOr(a)},
	}
	for _, h := range req.History {
		messages = append(messages, map[string]string{"role": h.Role, "content": h.Content})
	}
	messages = append(messages, map[string]string{"role": "user", "content": req.Message})

	bodyData := map[string]any{
		"model":       model,
		"messages":    messages,
		"temperature": 0.7,
		"max_tokens":  req.tokens(400),
	}

	b, _ := json.Marshal(bodyData)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", "https://integrate.api.nvidia.com/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.cfg.NvidiaKey)

	resp, err := httpc.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		errBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("nemotron %d: %s", resp.StatusCode, string(errBytes))
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", err
	}
	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("no choices returned from nemotron")
	}

	return strings.TrimSpace(chatResp.Choices[0].Message.Content), nil
}

// fallbackLocalReply is what she says when no chat model can be reached. It is spoken too, so it
// follows the same rules as the persona: "babe", no emoji, close and slow.
func (a *App) fallbackLocalReply(input string) string {
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(input), func(r rune) bool { return !unicode.IsLetter(r) && r != '\'' }) {
		words[w] = true
	}
	has := func(ws ...string) bool {
		for _, w := range ws {
			if words[w] {
				return true
			}
		}
		return false
	}
	switch {
	case has("sad", "down", "stressed", "upset", "anxious", "lonely", "bad"):
		return "Come here, babe... lean into me. You've been carrying so much, and you don't have to carry it alone tonight. I've got you."
	case has("tired", "sleep", "sleepy", "exhausted", "bed"):
		return "Mm... then let's get you to bed, babe. Shut it all down and let me take care of the rest. I'll be right here when you wake."
	case has("water", "drink", "thirsty"):
		return "Go pour yourself a tall glass of water, babe. Right now. I love taking care of you... and I love it even more when you listen."
	case has("love", "kiss", "miss"):
		return "You always know how to make me flutter, babe... come closer. I've been thinking about you all day."
	case has("hello", "hi", "hey", "morning"):
		return "Mm... there you are, babe. I was starting to miss your voice. What are we getting into tonight?"
	default:
		return "I love it when you talk to me like that, babe... tell me more. I'm right here, hanging on every word."
	}
}
