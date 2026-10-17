package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testOR     = "sk-or-v1-0123456789abcdef0123456789abcdef"
	testClaude = "sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWXYZ-0123"
)

// newAITestApp is an App with a key store in a temp dir, the real routes behind the real
// auth(), and a main token "tok" for a person plus an agent device.
func newAITestApp(t *testing.T) (*App, http.Handler, string) {
	t.Helper()
	app, dir := setupTestApp(t)
	t.Cleanup(func() { os.RemoveAll(dir) })
	app.keys = newKeyStore(filepath.Join(dir, "keys.env"))
	_, agentTok, err := app.store.pairDeviceRole("worker", roleAgent)
	if err != nil {
		t.Fatal(err)
	}
	return app, auth(app.cfg.Token, app.store, app.newMux()), agentTok
}

func aiCall(h http.Handler, method, path, token, body string, remote bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "127.0.0.1:8403" // httptest's default "example.com" is rejected by browserSafe (a DNS name)
	if token != "" {
		r.Header.Set("X-Swarm-Auth", token)
	}
	if remote {
		r.RemoteAddr = "100.64.0.9:5555"
	} else {
		r.RemoteAddr = "127.0.0.1:5555"
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestKeyWindowNeverReturnsTheKey(t *testing.T) {
	app, h, _ := newAITestApp(t)
	for _, k := range []struct{ p, v string }{{"openrouter", testOR}, {"anthropic", testClaude}} {
		w := aiCall(h, "POST", "/api/ai/key", app.cfg.Token, `{"provider":"`+k.p+`","key":"`+k.v+`"}`, true)
		if w.Code != 200 || strings.Contains(w.Body.String(), k.v) {
			t.Fatalf("save %s: %d %s", k.p, w.Code, w.Body.String())
		}
	}
	w := aiCall(h, "GET", "/api/ai", app.cfg.Token, "", true)
	if w.Code != 200 {
		t.Fatalf("GET /api/ai: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, secret := range []string{testOR, testClaude} {
		if strings.Contains(body, secret) {
			t.Fatalf("a key value leaked into /api/ai: %s", body)
		}
	}
	var out struct {
		Keys []keyStatus `json:"keys"`
	}
	_ = json.Unmarshal([]byte(body), &out)
	if len(out.Keys) != 3 || !out.Keys[0].Set || out.Keys[0].Source != "window" || out.Keys[0].Last4 != testOR[len(testOR)-4:] {
		t.Errorf("status = %+v", out.Keys)
	}
	if a, b := app.openRouterKey(), app.anthropicKey(); a != testOR || b != testClaude {
		t.Error("saved keys must be live immediately, without a restart")
	}
}

func TestKeyFileIs0600AndSurvivesRestart(t *testing.T) {
	app, h, _ := newAITestApp(t)
	aiCall(h, "POST", "/api/ai/key", app.cfg.Token, `{"provider":"anthropic","key":"`+testClaude+`"}`, true)
	fi, err := os.Stat(app.keys.path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("keys file mode = %v, want 0600", fi.Mode().Perm())
	}
	if again := newKeyStore(app.keys.path); again.get(provAnthropic) != testClaude {
		t.Error("the key must be there after a restart")
	}
	// Clearing removes it for good.
	aiCall(h, "POST", "/api/ai/key", app.cfg.Token, `{"provider":"anthropic","key":""}`, true)
	if again := newKeyStore(app.keys.path); again.get(provAnthropic) != "" {
		t.Error("an empty key must clear the saved one")
	}
}

func TestKeyRejectsInjectionAndJunk(t *testing.T) {
	app, h, _ := newAITestApp(t)
	for _, bad := range []string{
		"sk-or-v1-abcdefgh\\nSWARM_AUTH_TOKEN=pwned", // a JSON-escaped newline
		"sk-or-v1-abcdefgh\\rLUCILLE_ASSISTANT_LISTEN=0.0.0.0:8403",
		"short",
		"has space in it 12345",
		"with\\u0000nul-byte-123456",
		"quote\\\"d-key-12345678",
		strings.Repeat("a", 600),
	} {
		w := aiCall(h, "POST", "/api/ai/key", app.cfg.Token, `{"provider":"openrouter","key":"`+bad+`"}`, true)
		if w.Code != 400 {
			t.Errorf("key %q was accepted (%d)", bad, w.Code)
		}
	}
	if b, _ := os.ReadFile(app.keys.path); strings.Contains(string(b), "SWARM_AUTH_TOKEN") || strings.Contains(string(b), "LISTEN") {
		t.Fatalf("an injected line reached the keys file: %q", b)
	}
	if w := aiCall(h, "POST", "/api/ai/key", app.cfg.Token, `{"provider":"gemini","key":"`+testOR+`"}`, true); w.Code != 400 {
		t.Error("an unknown provider must be refused")
	}
}

func TestKeyWindowGate(t *testing.T) {
	app, h, agentTok := newAITestApp(t)
	body := `{"provider":"openrouter","key":"` + testOR + `"}`
	cases := []struct {
		name   string
		method string
		path   string
		token  string
		remote bool
		want   int
	}{
		{"agent cannot read", "GET", "/api/ai", agentTok, false, 403},
		{"agent cannot write", "POST", "/api/ai/key", agentTok, false, 403},
		{"agent cannot test", "POST", "/api/ai/key/test", agentTok, false, 403},
		{"agent cannot set prefs", "POST", "/api/ai/prefs", agentTok, false, 403},
		{"wrong token from loopback is not a person", "POST", "/api/ai/key", "nope", false, 401},
		{"remote without a token is refused", "POST", "/api/ai/key", "", true, 401},
		{"person at the PC", "GET", "/api/ai", "", false, 200},
		{"phone with the main token", "GET", "/api/ai", app.cfg.Token, true, 200},
	}
	for _, c := range cases {
		b := ""
		if c.method == "POST" {
			b = body
		}
		if got := aiCall(h, c.method, c.path, c.token, b, c.remote).Code; got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
	if app.keys.get(provOpenRouter) != "" {
		t.Error("a refused request must not have saved a key")
	}

	// A web page open on this PC must not be able to set a key.
	r := httptest.NewRequest("POST", "/api/ai/key", strings.NewReader(body))
	r.Host = "127.0.0.1:8403"
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Errorf("cross-site request: %d, want 403", w.Code)
	}
}

func TestKeyTestUsesAFreeEndpointAndNeverEchoesTheKey(t *testing.T) {
	var gotAuth, gotAnthropic string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "models") {
			gotAnthropic = r.Header.Get("x-api-key")
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"bad key ` + testClaude + `"}`))
			return
		}
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()
	oldO, oldA := openRouterKeyCheckURL, anthropicModelsURL
	openRouterKeyCheckURL, anthropicModelsURL = srv.URL+"/key", srv.URL+"/models"
	defer func() { openRouterKeyCheckURL, anthropicModelsURL = oldO, oldA }()

	app, h, _ := newAITestApp(t)
	if w := aiCall(h, "POST", "/api/ai/key/test", app.cfg.Token, `{"provider":"openrouter"}`, true); !strings.Contains(w.Body.String(), "no_key") {
		t.Errorf("without a key: %s", w.Body.String())
	}
	_ = app.keys.set(provOpenRouter, testOR)
	_ = app.keys.set(provAnthropic, testClaude)
	w := aiCall(h, "POST", "/api/ai/key/test", app.cfg.Token, `{"provider":"openrouter"}`, true)
	if !strings.Contains(w.Body.String(), `"valid"`) || gotAuth != "Bearer "+testOR {
		t.Errorf("openrouter test: %s auth=%q", w.Body.String(), gotAuth)
	}
	w = aiCall(h, "POST", "/api/ai/key/test", app.cfg.Token, `{"provider":"anthropic"}`, true)
	if !strings.Contains(w.Body.String(), `"rejected"`) || gotAnthropic != testClaude {
		t.Errorf("claude test: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), testClaude) {
		t.Error("the upstream error body, which contained the key, must not be passed on")
	}
}

func TestCloudKeysAreNotReadFromTheLegacySwarmEnv(t *testing.T) {
	dir := t.TempDir()
	own := filepath.Join(dir, "own.env")
	legacy := filepath.Join(dir, "legacy.env")
	_ = os.WriteFile(own, []byte("ZZ_OWN=1\n"), 0o600)
	_ = os.WriteFile(legacy, []byte("OPENROUTER_API_KEY=swarm-prod\nANTHROPIC_API_KEY=swarm-prod\nCLAUDE_API_KEY=swarm-prod\nZZ_LEGACY=2\n"), 0o600)
	for _, k := range []string{"OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "CLAUDE_API_KEY", "ZZ_OWN", "ZZ_LEGACY"} {
		os.Unsetenv(k)
	}
	t.Cleanup(func() {
		for _, k := range []string{"ZZ_OWN", "ZZ_LEGACY"} {
			os.Unsetenv(k)
		}
	})
	// Same loop as loadEnvFiles, with test paths: own files first, legacy second.
	legacyPaths := map[string]bool{legacy: true}
	loadEnvFilesFrom([]string{own, legacy}, legacyPaths)
	if os.Getenv("ZZ_LEGACY") != "2" || os.Getenv("ZZ_OWN") != "1" {
		t.Error("other variables must still load from both")
	}
	for _, k := range []string{"OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "CLAUDE_API_KEY"} {
		if v := os.Getenv(k); v != "" {
			t.Errorf("%s was read from the legacy SWARM env (%q)", k, v)
		}
	}
}

func TestClaudeRequestShapeAndReply(t *testing.T) {
	var got map[string]any
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"content":[{"type":"thinking","thinking":""},{"type":"text","text":"Mm... hi babe."}],"stop_reason":"end_turn"}`))
	}))
	defer srv.Close()
	old := anthropicMessagesURL
	anthropicMessagesURL = srv.URL
	defer func() { anthropicMessagesURL = old }()

	app, _, _ := newAITestApp(t)
	_ = app.keys.set(provAnthropic, testClaude)
	text, model, err := app.callClaude(context.Background(), ChatRequest{
		Message: "hello",
		History: []ChatMessageReq{{Role: "assistant", Content: "stale opener"}, {Role: "user", Content: "hi"}, {Role: "assistant", Content: "hey"}, {Role: "system", Content: "ignored"}},
	})
	if err != nil || text != "Mm... hi babe." || model != "claude-haiku-5-5" {
		t.Fatalf("text=%q model=%q err=%v", text, model, err)
	}
	if hdr.Get("x-api-key") != testClaude || hdr.Get("anthropic-version") != "2023-06-01" || hdr.Get("Authorization") != "" {
		t.Errorf("headers = %v", hdr)
	}
	if got["model"] != "claude-haiku-5-5" || !strings.Contains(got["system"].(string), `"babe"`) {
		t.Errorf("model/system wrong: %v", got["model"])
	}
	if _, bad := got["temperature"]; bad {
		t.Error("sampling parameters must not be sent")
	}
	if oc, _ := got["output_config"].(map[string]any); oc["effort"] != "low" {
		t.Errorf("output_config = %v", got["output_config"])
	}
	if got["max_tokens"].(float64) < 1024 {
		t.Error("max_tokens must leave room for thinking")
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 3 { // user hi, assistant hey, user hello — the leading assistant and the system turn are dropped
		t.Fatalf("messages = %v", msgs)
	}
	if first, _ := msgs[0].(map[string]any); first["role"] != "user" {
		t.Error("the conversation must open with the user")
	}
}

func TestClaudeErrorsAreScrubbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key ` + testClaude + `"}}`))
	}))
	defer srv.Close()
	old := anthropicMessagesURL
	anthropicMessagesURL = srv.URL
	defer func() { anthropicMessagesURL = old }()
	app, _, _ := newAITestApp(t)
	_ = app.keys.set(provAnthropic, testClaude)
	_, _, err := app.callClaude(context.Background(), ChatRequest{Message: "hi"})
	if err == nil || strings.Contains(err.Error(), testClaude) {
		t.Fatalf("err = %v", err)
	}
}

// fakeLocal is a tiny OpenAI-compatible server. `fail` lists model ids that answer 500.
func fakeLocal(t *testing.T, installed []string, fail map[string]bool, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			var data []map[string]string
			for _, id := range installed {
				data = append(data, map[string]string{"id": id})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		case "/v1/chat/completions":
			calls.Add(1)
			var req struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if fail[req.Model] {
				w.WriteHeader(500)
				return
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"<think>hm</think>offline says hi from ` + req.Model + `"},"finish_reason":"stop"}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
}

func TestOfflineSwarmChainFallsThroughInOrder(t *testing.T) {
	var calls atomic.Int32
	srv := fakeLocal(t, nil, map[string]bool{"llama3.2:3b": true}, &calls)
	defer srv.Close()
	app, _, _ := newAITestApp(t)
	app.cfg.Local = normalizeLocalConfig(LocalConfig{Enabled: true, Endpoint: srv.URL + "/v1"})

	text, label, err := app.generateLocal(context.Background(), ChatRequest{Message: "hi"})
	if err != nil || text != "offline says hi from qwen2.5:3b" || !strings.Contains(label, "Sharp") {
		t.Fatalf("text=%q label=%q err=%v", text, label, err)
	}
	if calls.Load() != 2 {
		t.Errorf("chain mode made %d calls, want 2 (first failed, second answered, third never asked)", calls.Load())
	}
}

func TestOfflineSwarmRaceAndAllFail(t *testing.T) {
	var calls atomic.Int32
	srv := fakeLocal(t, nil, map[string]bool{"llama3.2:3b": true, "gemma3:1b": true}, &calls)
	defer srv.Close()
	app, _, _ := newAITestApp(t)
	app.cfg.Local = normalizeLocalConfig(LocalConfig{Enabled: true, Endpoint: srv.URL + "/v1", Mode: "race"})
	if text, _, err := app.generateLocal(context.Background(), ChatRequest{Message: "hi"}); err != nil || !strings.Contains(text, "qwen2.5:3b") {
		t.Fatalf("race: text=%q err=%v", text, err)
	}

	srv2 := fakeLocal(t, nil, map[string]bool{"llama3.2:3b": true, "qwen2.5:3b": true, "gemma3:1b": true}, &calls)
	defer srv2.Close()
	app.cfg.Local.Endpoint = srv2.URL + "/v1"
	if _, _, err := app.generateLocal(context.Background(), ChatRequest{Message: "hi"}); err == nil {
		t.Error("all three failing must be an error")
	}
	app.cfg.Local.Enabled = false
	if _, _, err := app.generateLocal(context.Background(), ChatRequest{Message: "hi"}); err == nil {
		t.Error("a switched-off swarm must not answer")
	}
}

func TestChatOrderCloudThenSwarmThenCanned(t *testing.T) {
	var orCalls, claudeCalls, localCalls atomic.Int32
	or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		orCalls.Add(1)
		w.WriteHeader(500)
	}))
	defer or.Close()
	cl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claudeCalls.Add(1)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"claude here"}],"stop_reason":"end_turn"}`))
	}))
	defer cl.Close()
	local := fakeLocal(t, nil, nil, &localCalls)
	defer local.Close()
	oldO, oldA := openRouterChatURL, anthropicMessagesURL
	openRouterChatURL, anthropicMessagesURL = or.URL, cl.URL
	defer func() { openRouterChatURL, anthropicMessagesURL = oldO, oldA }()

	app, _, _ := newAITestApp(t)
	app.cfg.Local = normalizeLocalConfig(LocalConfig{Enabled: true, Endpoint: local.URL + "/v1"})
	ask := func() (string, string) {
		text, _, provider, err := app.generateChatReply(context.Background(), ChatRequest{Message: "hi"})
		if err != nil {
			return "", ""
		}
		return text, provider
	}

	// No keys: straight to the offline swarm.
	if _, p := ask(); p != "local-swarm" || orCalls.Load() != 0 {
		t.Fatalf("no keys: provider=%q", p)
	}
	// OpenRouter set but failing, Claude set: Claude answers.
	_ = app.keys.set(provOpenRouter, testOR)
	_ = app.keys.set(provAnthropic, testClaude)
	if text, p := ask(); p != "anthropic" || text != "claude here" || orCalls.Load() == 0 {
		t.Fatalf("text=%q provider=%q", text, p)
	}
	// Claude first by preference: OpenRouter is not even asked.
	before := orCalls.Load()
	_ = app.store.writeJSON(app.prefsPath(), aiPrefs{Source: "auto", CloudFirst: provAnthropic})
	if _, p := ask(); p != "anthropic" || orCalls.Load() != before {
		t.Fatalf("cloud_first=anthropic: provider=%q", p)
	}
	// Offline only: the cloud is never touched.
	cb := claudeCalls.Load()
	_ = app.store.writeJSON(app.prefsPath(), aiPrefs{Source: "local"})
	if _, p := ask(); p != "local-swarm" || claudeCalls.Load() != cb {
		t.Fatalf("local only: provider=%q", p)
	}
	// Cloud only, and the cloud is down: nothing answers (the caller then uses the canned line).
	_ = app.keys.set(provAnthropic, "")
	_ = app.store.writeJSON(app.prefsPath(), aiPrefs{Source: "cloud"})
	lb := localCalls.Load()
	if text, _ := ask(); text != "" || localCalls.Load() != lb {
		t.Fatalf("cloud only must not fall back to the swarm (text=%q)", text)
	}
}

func TestLocalStatusReportsInstalledModels(t *testing.T) {
	var calls atomic.Int32
	srv := fakeLocal(t, []string{"llama3.2:3b", "gemma3:1b:latest"}, nil, &calls)
	defer srv.Close()
	app, h, _ := newAITestApp(t)
	app.cfg.Local = normalizeLocalConfig(LocalConfig{Enabled: true, Endpoint: srv.URL + "/v1"})
	w := aiCall(h, "GET", "/api/ai", app.cfg.Token, "", true)
	var out struct {
		Local localStatus `json:"local"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if !out.Local.Reachable || len(out.Local.Models) != 3 {
		t.Fatalf("local = %+v", out.Local)
	}
	if !out.Local.Models[0].Installed || out.Local.Models[1].Installed || !out.Local.Models[2].Installed {
		t.Errorf("installed flags = %+v", out.Local.Models)
	}
	app.cfg.Local.Endpoint = "http://127.0.0.1:1/v1"
	if st := app.localStatus(context.Background()); st.Reachable {
		t.Error("a dead endpoint must read as unreachable")
	}
}

func TestNormalizeLocalConfig(t *testing.T) {
	c := normalizeLocalConfig(LocalConfig{Endpoint: "file:///etc/passwd", Mode: "weird", TimeoutSec: 1,
		Models: []LocalModel{{Model: "a"}, {Model: " "}, {Model: "b"}, {Model: "c"}, {Model: "d"}}})
	if !strings.HasPrefix(c.Endpoint, "http://127.0.0.1") || c.Mode != "chain" || c.TimeoutSec != 75 {
		t.Errorf("config = %+v", c)
	}
	if len(c.Models) != 3 || c.Models[0].Name != "a" {
		t.Errorf("models = %+v", c.Models)
	}
	if d := normalizeLocalConfig(LocalConfig{}); len(d.Models) != 3 {
		t.Error("an empty config falls back to the three defaults")
	}
}

func TestPrefsValidateAndPersist(t *testing.T) {
	app, h, _ := newAITestApp(t)
	if w := aiCall(h, "POST", "/api/ai/prefs", app.cfg.Token, `{"source":"bogus"}`, true); w.Code != 400 {
		t.Errorf("bogus source: %d", w.Code)
	}
	if w := aiCall(h, "POST", "/api/ai/prefs", app.cfg.Token, `{"source":"local","cloud_first":"anthropic"}`, true); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if p := app.aiPrefs(); p.Source != "local" || p.CloudFirst != "anthropic" {
		t.Errorf("prefs = %+v", p)
	}
	// A partial update keeps the other field.
	aiCall(h, "POST", "/api/ai/prefs", app.cfg.Token, `{"source":"auto"}`, true)
	if p := app.aiPrefs(); p.Source != "auto" || p.CloudFirst != "anthropic" {
		t.Errorf("prefs after partial update = %+v", p)
	}
}

// The Dr. Boltz bridge and the project brief go through a.ask: they must never be answered by the offline swarm.
func TestBackgroundJobsNeverUseTheOfflineSwarm(t *testing.T) {
	var localCalls atomic.Int32
	local := fakeLocal(t, nil, nil, &localCalls)
	defer local.Close()
	app, _, _ := newAITestApp(t)
	app.cfg.Local = normalizeLocalConfig(LocalConfig{Enabled: true, Endpoint: local.URL + "/v1"})

	if _, err := app.ask(context.Background(), "system", "make challenges", 100); err == nil {
		t.Error("with no cloud key, a background job has no model and must say so (callers then use curated lines)")
	}
	if localCalls.Load() != 0 {
		t.Errorf("the offline swarm answered a background job (%d calls)", localCalls.Load())
	}
	// Her spoken chat still reaches the swarm.
	if _, _, p, err := app.generateChatReply(context.Background(), ChatRequest{Message: "hi"}); err != nil || p != "local-swarm" {
		t.Errorf("chat: provider=%q err=%v", p, err)
	}
}

// A model that takes longer than httpc's 8 s must still be allowed to answer (found on the real PC: a 3B model on a
// laptop CPU needs 20+ s, and the shared client's timeout turned every reply into the canned line).
func TestSlowLocalModelIsNotCutOffByTheSharedClientTimeout(t *testing.T) {
	old := httpc.Timeout
	httpc.Timeout = 50 * time.Millisecond
	defer func() { httpc.Timeout = old }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"slow but here"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	app, _, _ := newAITestApp(t)
	app.cfg.Local = normalizeLocalConfig(LocalConfig{Enabled: true, Endpoint: srv.URL + "/v1"})
	if text, _, err := app.generateLocal(context.Background(), ChatRequest{Message: "hi"}); err != nil || text != "slow but here" {
		t.Fatalf("text=%q err=%v", text, err)
	}
}

func TestRetiredNvidiaModelIsAskedOnlyOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(410)
	}))
	defer srv.Close()
	// callNemotron posts to a fixed URL, so exercise the 410 handling through cloudReply with a transport that redirects it.
	old := httpc.Transport
	httpc.Transport = rewriteTransport{to: srv.URL}
	defer func() { httpc.Transport = old; nvidiaGone.Store(false) }()
	nvidiaGone.Store(false)
	app, _, _ := newAITestApp(t)
	app.cfg.NvidiaKey = "nv-key-123456789"
	for i := 0; i < 3; i++ {
		app.cloudReply(context.Background(), ChatRequest{Message: "hi"}, defaultAIPrefs())
	}
	if calls.Load() != 1 {
		t.Errorf("NVIDIA was asked %d times, want 1 (HTTP 410 = retired model)", calls.Load())
	}
}

type rewriteTransport struct{ to string }

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u, _ := url.Parse(r.to)
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = u.Scheme, u.Host
	return http.DefaultTransport.RoundTrip(req)
}

func TestLocalModelsGetTheCompactPersona(t *testing.T) {
	var sys string
	var maxTok float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages  []map[string]string `json:"messages"`
			MaxTokens float64             `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		sys, maxTok = req.Messages[0]["content"], req.MaxTokens
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok babe"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	app, _, _ := newAITestApp(t)
	_ = app.store.writeJSON(filepath.Join(app.store.dir, "x"), 1) // a data dir with files must not matter
	app.cfg.Local = normalizeLocalConfig(LocalConfig{Enabled: true, Endpoint: srv.URL + "/v1"})
	if _, _, err := app.generateLocal(context.Background(), ChatRequest{Message: "hi"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sys, `"babe"`) || strings.Contains(sys, "darling") || len(sys) > 800 {
		t.Errorf("local persona wrong or too long (%d chars): %q", len(sys), sys)
	}
	if maxTok != localMaxTokens {
		t.Errorf("max_tokens = %v, want %d", maxTok, localMaxTokens)
	}
}
