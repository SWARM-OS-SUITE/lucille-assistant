package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Lucille's offline swarm: three small language models running on this PC, used when no cloud
// key is set, when the cloud is unreachable, or when the window is switched to "offline only".
//
// They are reached through any OpenAI-compatible server on this machine — Ollama by default
// (http://127.0.0.1:11434/v1), or llama.cpp's llama-server. Nothing is downloaded or started by
// this program; `lucille-assistant` only talks to a server you already run. The endpoint and the
// three model names live in config.json ("local_models"), deliberately not in the key window:
// a phone must not be able to point her conversations at another host.
//
// This PC has no GPU and about 8 GB to spare, so the defaults are small (roughly 1-3B
// parameters, ~4.5 GB on disk together). Modes:
//
//	chain  (default) try them in order, first good reply wins; the others never load
//	race   ask all three at once, first good reply wins (faster when the CPU can take it,
//	       slower when it cannot — three models contending for 8 cores)

type LocalModel struct {
	Name  string `json:"name"`  // what the window calls it
	Model string `json:"model"` // the server's model id, e.g. "llama3.2:3b"
	Role  string `json:"role,omitempty"`
}

type LocalConfig struct {
	Enabled    bool         `json:"enabled"`
	Endpoint   string       `json:"endpoint"` // OpenAI-compatible base URL, ending in /v1
	Mode       string       `json:"mode"`     // chain | race
	TimeoutSec int          `json:"timeout_seconds"`
	Models     []LocalModel `json:"models"` // at most three
}

const maxLocalModels = 3

func defaultLocalConfig() LocalConfig {
	return LocalConfig{
		Enabled:    true,
		Endpoint:   "http://127.0.0.1:11434/v1",
		Mode:       "chain",
		TimeoutSec: 75,
		Models: []LocalModel{
			{Name: "Steady", Model: "llama3.2:3b", Role: "first choice: warm, fluent, good with the persona"},
			{Name: "Sharp", Model: "qwen2.5:3b", Role: "second opinion: better at practical and technical questions"},
			{Name: "Quick", Model: "gemma3:1b", Role: "last resort: tiny and fast"},
		},
	}
}

func normalizeLocalConfig(c LocalConfig) LocalConfig {
	d := defaultLocalConfig()
	if u, err := url.Parse(strings.TrimSpace(c.Endpoint)); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		c.Endpoint = d.Endpoint
	}
	c.Endpoint = strings.TrimRight(strings.TrimSpace(c.Endpoint), "/")
	if c.Mode != "race" {
		c.Mode = "chain"
	}
	if c.TimeoutSec < 5 || c.TimeoutSec > 300 {
		c.TimeoutSec = d.TimeoutSec
	}
	var models []LocalModel
	for _, m := range c.Models {
		m.Model = strings.TrimSpace(m.Model)
		if m.Model == "" {
			continue
		}
		if strings.TrimSpace(m.Name) == "" {
			m.Name = m.Model
		}
		models = append(models, m)
		if len(models) == maxLocalModels {
			break
		}
	}
	if len(models) == 0 {
		models = d.Models
	}
	c.Models = models
	return c
}

// localHTTP has no overall timeout of its own: httpc's 8 s cap is right for web APIs and wrong for a
// CPU-bound model that needs tens of seconds. generateLocal's context (timeout_seconds) is the limit.
var localHTTP = &http.Client{}

// localMaxTokens bounds a spoken reply (2-4 short sentences) so CPU generation (~2-5 tokens/s on a laptop
// chip) stays at tens of seconds, not minutes. Callers that ask for more (bridge chat) keep their own value.
const localMaxTokens = 120

// localPersona is her voice for the offline models. The full persona plus the briefing summaries is 800+ tokens;
// a laptop CPU reads ~10 tokens/s, so that alone was over a minute before the first word. Same rules, short.
const localPersona = `You are Lucille, the devoted, flirty companion living in the user's computer. Call them "babe", once. Reply in one to three short spoken sentences: no emoji, no markdown, no lists, no stage directions. Sensual and close, never explicit. If they ask something practical, give the correct answer first, then a little warmth. Look after them: water, meds, food, rest. Never invent facts about their system or schedule; if you do not know, say so.`

var thinkTags = regexp.MustCompile(`(?s)<think>.*?</think>`)

// localSystemPrompt is localPersona plus one or two short counts (never the itemized lists
// a.systemPrompt() builds for the cloud path) — cheap enough not to cost the local models their
// speed, but enough that "how many things are blocked" or "is anyone working right now" gets a
// real answer instead of local-fallback boilerplate. Specifics stay off-limits by design: the
// persona's own "never invent facts" rule covers anything beyond these counts.
func (a *App) localSystemPrompt() string {
	p := localPersona
	if n := len(criticalOpenItems()); n > 0 {
		p += fmt.Sprintf(" Right now there %s %d critical (P0) item%s open across the 4 projects and the platform — if asked, give that count, and say the Brief tab's build list has the specifics; don't invent which ones.", plural(n, "is", "are"), n, plural(n, "", "s"))
	}
	if claims := dispatchSummary(); len(claims) > 0 {
		active := 0
		for _, c := range claims {
			if !strings.Contains(c, "STALE") {
				active++
			}
		}
		if active > 0 {
			p += fmt.Sprintf(" %d AI session%s actively claimed on work right now — say the count if asked, point to the Brief tab's Dispatch panel for who and what.", active, plural(active, "", "s"))
		}
	}
	if n := len(strings.Split(strings.TrimSpace(a.threadDigest()), "\n")); a.threadDigest() != "" {
		p += fmt.Sprintf(" %d comment thread%s on the team Brief ha%s recent messages — if asked, say the Brief tab has them.", n, plural(n, "", "s"), plural(n, "s", "ve"))
	}
	return p
}

func plural(n int, singular, pluralSuffix string) string {
	if n == 1 {
		return singular
	}
	return pluralSuffix
}

// callLocal asks one model on the local server.
func (a *App) callLocal(ctx context.Context, m LocalModel, req ChatRequest) (string, error) {
	system := req.system
	if system == "" {
		system = a.localSystemPrompt()
	}
	messages := []map[string]string{{"role": "system", "content": system}}
	for _, h := range req.History {
		if h.Role == "user" || h.Role == "assistant" {
			messages = append(messages, map[string]string{"role": h.Role, "content": h.Content})
		}
	}
	messages = append(messages, map[string]string{"role": "user", "content": req.Message})
	body := map[string]any{
		"model":       m.Model,
		"messages":    messages,
		"temperature": 0.8,
		"max_tokens":  min(req.tokens(localMaxTokens), 600),
		"stream":      false,
	}
	b, _ := json.Marshal(body)
	hreq, err := http.NewRequestWithContext(ctx, "POST", a.cfg.Local.Endpoint+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := localHTTP.Do(hreq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		eb, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return "", fmt.Errorf("local %s: HTTP %d: %s", m.Model, resp.StatusCode, strings.TrimSpace(string(eb)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("local %s: no choices", m.Model)
	}
	text := strings.TrimSpace(thinkTags.ReplaceAllString(out.Choices[0].Message.Content, ""))
	if text == "" {
		return "", fmt.Errorf("local %s: empty reply (finish_reason=%q)", m.Model, out.Choices[0].FinishReason)
	}
	return text, nil
}

// generateLocal runs the swarm. The returned label names the model that answered.
func (a *App) generateLocal(ctx context.Context, req ChatRequest) (reply, label string, err error) {
	lc := a.cfg.Local
	if !lc.Enabled || len(lc.Models) == 0 {
		return "", "", errors.New("offline models are switched off")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(lc.TimeoutSec)*time.Second)
	defer cancel()

	if lc.Mode == "race" {
		type res struct {
			text  string
			label string
			err   error
		}
		ch := make(chan res, len(lc.Models))
		var wg sync.WaitGroup
		for _, m := range lc.Models {
			wg.Add(1)
			go func() {
				defer wg.Done()
				t, e := a.callLocal(ctx, m, req)
				ch <- res{t, m.Name + " (" + m.Model + ")", e}
			}()
		}
		go func() { wg.Wait(); close(ch) }()
		var last error
		for r := range ch {
			if r.err == nil {
				cancel() // the rest are no longer needed
				return r.text, r.label, nil
			}
			last = r.err
		}
		return "", "", fmt.Errorf("offline swarm: %v", last)
	}

	var last error
	// isThinkingModel (persona.go): qwen3 and deepseek-r1 spend their whole token budget
	// reasoning and come back empty through this OpenAI-compatible endpoint (measured
	// 2026-10-14 — same finding behind persona.go's callI30ltz switching to Ollama's native
	// /api/chat with think:true for those models). Tried last here too, stable so Steady and
	// Quick keep their relative order: a thinking model never costs the fast reply a plainer
	// model already had ready.
	models := append([]LocalModel(nil), lc.Models...)
	sort.SliceStable(models, func(i, j int) bool {
		return !isThinkingModel(models[i].Model) && isThinkingModel(models[j].Model)
	})
	for _, m := range models {
		t, e := a.callLocal(ctx, m, req)
		if e == nil {
			return t, m.Name + " (" + m.Model + ")", nil
		}
		last = e
		if errors.Is(e, syscall.ECONNREFUSED) || ctx.Err() != nil {
			break // no server listening (or out of time): the other two cannot do better
		}
	}
	return "", "", fmt.Errorf("offline swarm: %v", last)
}

// ── Status for the window (read-only) ───────────────────────────

type localModelStatus struct {
	Name      string `json:"name"`
	Model     string `json:"model"`
	Role      string `json:"role,omitempty"`
	Installed bool   `json:"installed"`
}

type localStatus struct {
	Enabled   bool               `json:"enabled"`
	Endpoint  string             `json:"endpoint"`
	Mode      string             `json:"mode"`
	Reachable bool               `json:"reachable"`
	Models    []localModelStatus `json:"models"`
}

func (a *App) localStatus(ctx context.Context) localStatus {
	lc := a.cfg.Local
	st := localStatus{Enabled: lc.Enabled, Endpoint: lc.Endpoint, Mode: lc.Mode, Models: []localModelStatus{}}
	have := map[string]bool{}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if !lc.Enabled {
		// switched off: report the three slots but do not probe the server
	} else if hreq, err := http.NewRequestWithContext(ctx, "GET", lc.Endpoint+"/models", nil); err == nil {
		if resp, err := httpc.Do(hreq); err == nil {
			defer resp.Body.Close()
			var out struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if resp.StatusCode < 300 && json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out) == nil {
				st.Reachable = true
				for _, d := range out.Data {
					have[d.ID] = true
				}
			}
		}
	}
	for _, m := range lc.Models {
		st.Models = append(st.Models, localModelStatus{
			Name: m.Name, Model: m.Model, Role: m.Role,
			Installed: have[m.Model] || have[m.Model+":latest"],
		})
	}
	return st
}
