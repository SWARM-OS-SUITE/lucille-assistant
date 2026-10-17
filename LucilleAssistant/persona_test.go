package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeOllama is a stand-in for Ollama's native /api/chat. It records every request and answers like Ollama:
// with thinking on, the reasoning goes in message.thinking and the answer in message.content.
type fakeOllama struct {
	mu       sync.Mutex
	reqs     []map[string]any
	status   int    // 0 = 200
	errText  string // Ollama's {"error": ...}
	content  string
	thinking string
}

func (f *fakeOllama) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			w.WriteHeader(404)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.reqs = append(f.reqs, req)
		f.mu.Unlock()
		if f.status != 0 {
			w.WriteHeader(f.status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": f.errText})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"done_reason": "stop",
			"message": map[string]string{"role": "assistant", "content": f.content, "thinking": f.thinking}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func i30ltzTestApp(t *testing.T, srv *httptest.Server, model string) (*App, http.Handler) {
	t.Helper()
	app, h, _ := newAITestApp(t)
	if srv != nil {
		app.cfg.Local.Endpoint = srv.URL + "/v1" // Lucille's OpenAI-style endpoint; he uses the native API beside it
	}
	app.cfg.Local.Models = []LocalModel{{Name: "Steady", Model: "llama3.2:3b"}, {Name: "DeepSeek", Model: "deepseek-r1:1.5b"}}
	app.cfg.MemoryDir = t.TempDir()
	rules := filepath.Join(t.TempDir(), "rules.md")
	_ = os.WriteFile(rules, []byte("# Title\n\n## 1. Identity\n**Identity:** You are Dr. I30LTZ. Never say `MAD IT`.\n\n## 2. Safety\nImage first.\n\n## 3. Later\nNOT-IN-EXCERPT\n"), 0o644)
	oldRules, oldModel := i30ltzRulesPath, i30ltzModel
	i30ltzRulesPath, i30ltzModel = rules, model
	t.Cleanup(func() { i30ltzRulesPath, i30ltzModel = oldRules, oldModel })
	return app, h
}

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	return out
}

func TestI30ltzUsesHisOwnThinkingModelAndKeepsThinkingOut(t *testing.T) {
	f := &fakeOllama{content: "[DR.BOLTZ] Disconnect it. Image it before anything else.", thinking: "SECRET-REASONING about the drive"}
	_, h := i30ltzTestApp(t, f.server(t), "qwen3:4b")
	w := aiCall(h, "POST", "/api/chat/i30ltz", "", `{"message":"my T7 clicks","history":[{"role":"user","content":"hi"},{"role":"assistant","content":"Ask."}]}`, false)
	out := decode(t, w.Body.Bytes())
	if out["ok"] != true || out["model"] != "qwen3:4b" || out["provider"] != "local" || out["persona"] != "i30ltz" {
		t.Fatalf("bad reply: %v", out)
	}
	if !strings.Contains(out["text"].(string), "Image it") || strings.Contains(w.Body.String(), "SECRET-REASONING") {
		t.Fatalf("the answer must be the content only, never the thinking: %s", w.Body)
	}
	if len(f.reqs) != 1 {
		t.Fatalf("exactly one model call expected, got %d", len(f.reqs))
	}
	req := f.reqs[0]
	if req["model"] != "qwen3:4b" || req["think"] != true || req["stream"] != false {
		t.Fatalf("request: %v", req)
	}
	msgs := req["messages"].([]any)
	sys := msgs[0].(map[string]any)["content"].(string)
	for _, want := range []string{"Dr. I30LTZ", "never \"MAD IT\"", "Your standing rules", "Image first"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
	if strings.Contains(sys, "NOT-IN-EXCERPT") || strings.Contains(sys, "babe") {
		t.Errorf("system prompt has text it must not have:\n%s", sys)
	}
	if len(msgs) != 4 || msgs[3].(map[string]any)["content"] != "my T7 clicks" {
		t.Fatalf("history + message not passed: %v", msgs)
	}
}

func TestI30ltzPlainModelDoesNotAskToThink(t *testing.T) {
	f := &fakeOllama{content: "Unplug it."}
	_, h := i30ltzTestApp(t, f.server(t), "qwen2.5:3b")
	out := decode(t, aiCall(h, "POST", "/api/chat/i30ltz", "", `{"message":"hi"}`, false).Body.Bytes())
	if out["ok"] != true || out["model"] != "qwen2.5:3b" {
		t.Fatalf("got %v", out)
	}
	if _, asked := f.reqs[0]["think"]; asked {
		t.Fatalf("a non-thinking model must not get think: %v", f.reqs[0])
	}
}

func TestI30ltzNeverFallsBackToLucillesModelsOrTheCloud(t *testing.T) {
	f := &fakeOllama{status: 404, errText: `model "qwen3:4b" not found, try pulling it first`}
	app, h := i30ltzTestApp(t, f.server(t), "qwen3:4b")
	app.llm = func(ctx context.Context, s, u string, n int) (string, error) { // must never be used for him
		t.Fatal("no cloud/LLM helper may be called for him")
		return "", nil
	}
	out := decode(t, aiCall(h, "POST", "/api/chat/i30ltz", "", `{"message":"hello"}`, false).Body.Bytes())
	msg, _ := out["error"].(string)
	if out["ok"] != false || !strings.Contains(msg, "ollama pull qwen3:4b") || !strings.Contains(msg, "never Lucille's and never the cloud") {
		t.Fatalf("want an honest refusal with the fix, got %v", out)
	}
	if len(f.reqs) != 1 || f.reqs[0]["model"] != "qwen3:4b" {
		t.Fatalf("only his own model may be asked, got %v", f.reqs)
	}
}

func TestI30ltzEmptyAnswerIsAnError(t *testing.T) {
	f := &fakeOllama{content: "  ", thinking: "thought for ages"}
	_, h := i30ltzTestApp(t, f.server(t), "qwen3:4b")
	out := decode(t, aiCall(h, "POST", "/api/chat/i30ltz", "", `{"message":"hello"}`, false).Body.Bytes())
	if out["ok"] != false || !strings.Contains(out["error"].(string), "empty answer") {
		t.Fatalf("got %v", out)
	}
}

func TestI30ltzRejectsEmptyMessageAndNeedsAuthFromAfar(t *testing.T) {
	f := &fakeOllama{content: "x"}
	_, h := i30ltzTestApp(t, f.server(t), "qwen3:4b")
	if w := aiCall(h, "POST", "/api/chat/i30ltz", "", `{"message":"  "}`, false); w.Code != 400 {
		t.Fatalf("empty message: %d", w.Code)
	}
	if w := aiCall(h, "POST", "/api/chat/i30ltz", "", `{"message":"hi"}`, true); w.Code != 401 {
		t.Fatalf("remote caller without a token must be refused, got %d", w.Code)
	}
	if len(f.reqs) != 0 {
		t.Fatalf("no model call expected, got %v", f.reqs)
	}
}

func TestI30ltzPromptCarriesSharedMemory(t *testing.T) {
	app, _ := i30ltzTestApp(t, nil, "qwen3:4b")
	logDir := filepath.Join(app.cfg.MemoryDir, "log")
	_ = os.MkdirAll(logDir, 0o755)
	_ = os.WriteFile(filepath.Join(logDir, "debian.jsonl"), []byte(
		`{"id":"1","device":"debian","ts":"2026-10-14T20:00:00Z","kind":"decision","key":"api-keys-status","content":"keys work"}`+"\n"+
			`{"id":"2","device":"debian","ts":"2026-10-14T19:00:00Z","kind":"note","key":"chatter","content":"not a decision"}`+"\n"), 0o644)
	sys := app.i30ltzSystemPrompt()
	if !strings.Contains(sys, "api-keys-status: keys work") || strings.Contains(sys, "not a decision") {
		t.Fatalf("memory section wrong:\n%s", sys)
	}
}

func TestI30ltzHelpers(t *testing.T) {
	if ollamaBase("http://127.0.0.1:11434/v1") != "http://127.0.0.1:11434" || ollamaBase("http://h:1/v1/") != "http://h:1" {
		t.Fatal("ollamaBase")
	}
	if !isThinkingModel("qwen3:4b") || !isThinkingModel("deepseek-r1:1.5b") || isThinkingModel("qwen2.5:3b") || isThinkingModel("llama3.2:3b") {
		t.Fatal("isThinkingModel")
	}
	if i30ltzTimeout("qwen3:4b") <= i30ltzTimeout("qwen2.5:3b") {
		t.Fatal("thinking models need the longer limit")
	}
}

func TestI30ltzRulesExcerpt(t *testing.T) {
	if i30ltzRulesExcerpt(filepath.Join(t.TempDir(), "missing.md"), 100) != "" {
		t.Fatal("missing file must give an empty excerpt")
	}
	p := filepath.Join(t.TempDir(), "r.md")
	_ = os.WriteFile(p, []byte("## 1. A\n**bold** `code` "+strings.Repeat("word ", 400)+"\n## 3. Stop\nAFTER"), 0o644)
	ex := i30ltzRulesExcerpt(p, 120)
	if len(ex) > 125 || strings.ContainsAny(ex, "*`#") || strings.Contains(ex, "AFTER") || !strings.HasSuffix(ex, "…") {
		t.Fatalf("excerpt %q", ex)
	}
}

func TestI30ltzChatWithVoice(t *testing.T) {
	fo := &fakeOpenRouterAudio{}
	withOpenRouterAudio(t, fo.serve(t).URL)

	f := &fakeOllama{content: "Drive health optimal."}
	app, h := i30ltzTestApp(t, f.server(t), "qwen2.5:3b")
	app.cfg.OpenRouterKey = "k"

	w := aiCall(h, "POST", "/api/chat/i30ltz", "", `{"message":"status check","voice":true}`, false)
	out := decode(t, w.Body.Bytes())
	if out["ok"] != true || out["text"] != "Drive health optimal." {
		t.Fatalf("unexpected reply: %v", out)
	}
	audioB64, ok := out["audio_b64"].(string)
	if !ok || audioB64 == "" {
		t.Fatalf("expected audio_b64 in response, got %v", out)
	}
	if fo.lastReq["voice"] != defaultI30ltzVoice {
		t.Errorf("expected Dr. I30LTZ voice (%s), got %v", defaultI30ltzVoice, fo.lastReq["voice"])
	}
}
