package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func resetVoiceState() {
	voiceMu.Lock()
	voiceDead = map[string]string{}
	voiceMu.Unlock()
}

// fakeEleven stands in for api.elevenlabs.io. refuse maps a voice id to the HTTP status it answers
// with (anything not listed gets audio). It records which voices were asked, in order, and the last body.
type fakeEleven struct {
	mu       sync.Mutex
	asked    []string
	lastBody map[string]any
	lastKey  string
}

func (f *fakeEleven) serve(t *testing.T, refuse map[string]int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/v1/text-to-speech/")
		f.mu.Lock()
		f.asked = append(f.asked, id)
		f.lastKey = r.Header.Get("xi-api-key")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.lastBody = body
		f.mu.Unlock()
		if code, bad := refuse[id]; bad {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"detail":{"status":"payment_required","message":"Free users cannot use library voices via the API. See https://elevenlabs.io/app/subscription"}}`))
			return
		}
		_, _ = w.Write([]byte("AUDIO-" + id))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func withEleven(t *testing.T, url string) {
	t.Helper()
	old := elevenBase
	elevenBase = url
	resetVoiceState()
	t.Cleanup(func() { elevenBase = old; resetVoiceState() })
}

// fakeOpenRouterAudio stands in for openrouter.ai/api/v1/audio/speech. status is returned for
// every request unless overridden per call by the test; it records the last request body.
type fakeOpenRouterAudio struct {
	mu      sync.Mutex
	status  int
	lastReq map[string]any
	calls   int
}

func (f *fakeOpenRouterAudio) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.lastReq = body
		f.calls++
		status := f.status
		f.mu.Unlock()
		if status == 0 || status < 300 {
			_, _ = w.Write([]byte("AUDIO-" + body["model"].(string) + "-" + body["voice"].(string)))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"nope"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func withOpenRouterAudio(t *testing.T, url string) {
	t.Helper()
	old := openRouterAudioURL
	openRouterAudioURL = url
	t.Cleanup(func() { openRouterAudioURL = old })
}

func TestSpeakableCleansForTheVoice(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Mm... there you are, babe. 🌸💕", "Mm... there you are, babe."},
		{"**Drink** some _water_, babe ✨", "Drink some water, babe"},
		{"See https://example.com/a?b=1 for more", "See for more"},
		{"  lots \n\n of   space  ", "lots of space"},
		{"# Title\n- item", "Title - item"},
		{"🌸 ✨ 💕", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := speakable(c.in, 500); got != c.want {
			t.Errorf("speakable(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSpeakableCutsAtASentence(t *testing.T) {
	long := strings.Repeat("Come closer, babe. ", 40) // 760 chars
	got := speakable(long, 100)
	if len([]rune(got)) > 100 || !strings.HasSuffix(got, "babe.") {
		t.Errorf("want a cut at a sentence end within 100 runes, got %d runes: %q", len([]rune(got)), got)
	}
	// no sentence end to cut at: falls back to a word boundary and trails off
	words := strings.Repeat("slowly ", 60)
	got = speakable(words, 50)
	if !strings.HasSuffix(got, "...") || len([]rune(got)) > 53 {
		t.Errorf("want a word-boundary cut ending in ..., got %q", got)
	}
}

func TestVoiceChainOrderAndNoRepeats(t *testing.T) {
	got := voiceChain("custom")
	if got[0] != "custom" || got[1] != defaultVoiceID {
		t.Errorf("configured voice first, then the default: %v", got)
	}
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Errorf("%s repeated in %v", id, got)
		}
		seen[id] = true
	}
	if g := voiceChain(""); g[0] != defaultVoiceID {
		t.Errorf("with nothing configured the default leads: %v", g)
	}
	if g := voiceChain(defaultVoiceID); g[0] != defaultVoiceID || len(g) != len(voiceChain("")) {
		t.Errorf("configured == default must not duplicate it: %v", g)
	}
}

// The bug this guards: a library voice on a free plan answers 402 and used to be asked again on
// every reply, then fall through to a voice she was never meant to have, with nothing in the log.
func TestLibraryVoiceIsSkippedAfterTheFirst402(t *testing.T) {
	f := &fakeEleven{}
	srv := f.serve(t, map[string]int{"library-voice": 402})
	withEleven(t, srv.URL)

	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)
	app.cfg.ElevenKey = "key-123"
	app.cfg.ElevenVoiceID = "library-voice"

	for i := 0; i < 3; i++ {
		b64, err := app.synthesizeVoice(context.Background(), "Mm... hello babe 💕")
		if err != nil {
			t.Fatalf("reply %d: %v", i, err)
		}
		raw, _ := base64.StdEncoding.DecodeString(b64)
		if string(raw) != "AUDIO-"+defaultVoiceID {
			t.Fatalf("reply %d spoken by %q, want the default voice", i, raw)
		}
	}
	libraryAsks := 0
	for _, id := range f.asked {
		if id == "library-voice" {
			libraryAsks++
		}
	}
	if libraryAsks != 1 {
		t.Errorf("library voice asked %d times, want exactly once (then skipped): %v", libraryAsks, f.asked)
	}
	if f.lastKey != "key-123" {
		t.Errorf("api key header = %q", f.lastKey)
	}
}

func TestVoiceRequestIsCleanedAndTuned(t *testing.T) {
	f := &fakeEleven{}
	withEleven(t, f.serve(t, nil).URL)
	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)
	app.cfg.ElevenKey = "k"

	if _, err := app.synthesizeVoice(context.Background(), "**Come closer**, babe 🌸"); err != nil {
		t.Fatal(err)
	}
	if got := f.lastBody["text"]; got != "Come closer, babe" {
		t.Errorf("text sent = %q, want markdown and emoji gone", got)
	}
	vs, _ := f.lastBody["voice_settings"].(map[string]any)
	if vs["speed"] != 0.92 || vs["style"] != 0.5 || vs["use_speaker_boost"] != true {
		t.Errorf("voice_settings = %v, want the tuned sultry settings", vs)
	}
	if f.lastBody["model_id"] != elevenModel {
		t.Errorf("model_id = %v", f.lastBody["model_id"])
	}
}

// A bad key or a used-up quota is about the account: asking four more voices just makes four more errors.
func TestAccountLevelFailureStopsTheWalk(t *testing.T) {
	f := &fakeEleven{}
	everyVoice := map[string]int{}
	for _, id := range voiceChain("custom") {
		everyVoice[id] = 401
	}
	withEleven(t, f.serve(t, everyVoice).URL)
	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)
	app.cfg.ElevenKey = "bad"
	app.cfg.ElevenVoiceID = "custom"

	if _, err := app.synthesizeVoice(context.Background(), "hello babe"); err == nil {
		t.Fatal("want an error when ElevenLabs refuses and there is no other provider")
	}
	if len(f.asked) != 1 {
		t.Errorf("asked %d voices after a 401, want 1: %v", len(f.asked), f.asked)
	}
	if !voiceUsable("custom") {
		t.Error("a 401 is about the key, it must not mark the voice dead")
	}
}

func TestNothingToSpeakDoesNotCallOut(t *testing.T) {
	f := &fakeEleven{}
	withEleven(t, f.serve(t, nil).URL)
	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)
	app.cfg.ElevenKey = "k"
	if _, err := app.synthesizeVoice(context.Background(), "🌸💕 ✨"); err == nil {
		t.Fatal("emoji-only text has nothing to say")
	}
	if len(f.asked) != 0 {
		t.Errorf("no request should be made for empty speech, got %v", f.asked)
	}
}

// OpenRouter now carries ElevenLabs itself, so it is the normal path: tried first, with the same
// key already used for chat, even when a direct ElevenLabs key is also configured.
func TestOpenRouterElevenIsTriedFirst(t *testing.T) {
	fe := &fakeEleven{}
	withEleven(t, fe.serve(t, nil).URL)
	fo := &fakeOpenRouterAudio{}
	withOpenRouterAudio(t, fo.serve(t).URL)

	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)
	app.cfg.OpenRouterKey = "or-key"
	app.cfg.ElevenKey = "key-123" // present, but must not be reached

	b64, err := app.synthesizeVoice(context.Background(), "Mm... hello babe 💕")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(b64)
	if string(raw) != "AUDIO-"+openRouterElevenModel+"-"+defaultOpenRouterVoice {
		t.Fatalf("spoken by %q, want OpenRouter's ElevenLabs model with the default voice", raw)
	}
	if len(fe.asked) != 0 {
		t.Errorf("the direct ElevenLabs key must not be used while OpenRouter can speak, asked %v", fe.asked)
	}
	if fo.lastReq["model"] != openRouterElevenModel || fo.lastReq["voice"] != defaultOpenRouterVoice {
		t.Errorf("request = %v", fo.lastReq)
	}
}

// A voice chosen from the phone (one of OpenRouter's accepted names) must be sent as-is.
func TestOpenRouterElevenUsesTheChosenVoice(t *testing.T) {
	fo := &fakeOpenRouterAudio{}
	withOpenRouterAudio(t, fo.serve(t).URL)
	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)
	app.cfg.OpenRouterKey = "or-key"
	_ = app.store.writeJSON(app.prefsPath(), aiPrefs{Source: "auto", CloudFirst: provOpenRouter, VoiceID: "george"})

	if _, err := app.synthesizeVoice(context.Background(), "hello babe"); err != nil {
		t.Fatal(err)
	}
	if fo.lastReq["voice"] != "george" {
		t.Errorf("voice sent = %v, want george", fo.lastReq["voice"])
	}
}

// A voice preference that is not one of OpenRouter's accepted names (e.g. a raw ElevenLabs id
// left over from the direct-account path) must fall back to the default, never be sent as-is.
func TestOpenRouterElevenRejectsAnUnknownVoiceName(t *testing.T) {
	fo := &fakeOpenRouterAudio{}
	withOpenRouterAudio(t, fo.serve(t).URL)
	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)
	app.cfg.OpenRouterKey = "or-key"
	_ = app.store.writeJSON(app.prefsPath(), aiPrefs{Source: "auto", CloudFirst: provOpenRouter, VoiceID: "pFZP5JQG7iQjIQuC4Bku"})

	if _, err := app.synthesizeVoice(context.Background(), "hello babe"); err != nil {
		t.Fatal(err)
	}
	if fo.lastReq["voice"] != defaultOpenRouterVoice {
		t.Errorf("voice sent = %v, want the default (%s)", fo.lastReq["voice"], defaultOpenRouterVoice)
	}
}

// If OpenRouter's ElevenLabs model itself fails, a direct ElevenLabs key (if configured) is the
// fallback, not silence.
func TestDirectElevenLabsIsTheFallbackWhenOpenRouterFails(t *testing.T) {
	fo := &fakeOpenRouterAudio{status: 500}
	withOpenRouterAudio(t, fo.serve(t).URL)
	fe := &fakeEleven{}
	withEleven(t, fe.serve(t, nil).URL)

	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)
	app.cfg.OpenRouterKey = "or-key"
	app.cfg.ElevenKey = "key-123"

	b64, err := app.synthesizeVoice(context.Background(), "hello babe")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(b64)
	if !strings.HasPrefix(string(raw), "AUDIO-") || fo.calls == 0 || len(fe.asked) == 0 {
		t.Fatalf("want OpenRouter tried then ElevenLabs as fallback: openrouter calls=%d eleven asked=%v spoken=%q", fo.calls, fe.asked, raw)
	}
}

func TestHandleAIVoicesListsOpenRouterFixedSetWhenNoDirectKey(t *testing.T) {
	app, h, _ := newAITestApp(t)
	app.cfg.OpenRouterKey = "or-key"

	w := aiCall(h, "GET", "/api/ai/voices", app.cfg.Token, "", false)
	var out struct {
		OK     bool          `json:"ok"`
		Voices []voiceOption `json:"voices"`
		Via    string        `json:"via"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Via != "openrouter" || len(out.Voices) != len(openRouterElevenVoices) {
		t.Fatalf("got %+v", out)
	}
}

// The bug this guards: with both a direct ElevenLabs key (e.g. one already sitting in SWARM's
// shared .env) AND an OpenRouter key present, the listing must still match what synthesizeVoice
// actually tries first — OpenRouter's fixed set — never the ElevenLabs catalog going stale or,
// worse, reporting "rejected" for a key nobody is even using for voice anymore.
func TestHandleAIVoicesPrefersOpenRouterEvenWithADirectKeyPresent(t *testing.T) {
	app, h, _ := newAITestApp(t)
	app.cfg.OpenRouterKey = "or-key"
	app.cfg.ElevenKey = "key-123" // present, must not change the answer

	w := aiCall(h, "GET", "/api/ai/voices", app.cfg.Token, "", false)
	var out struct {
		OK     bool          `json:"ok"`
		Voices []voiceOption `json:"voices"`
		Via    string        `json:"via"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Via != "openrouter" || len(out.Voices) != len(openRouterElevenVoices) {
		t.Fatalf("got %+v", out)
	}
}

func TestElevenErrorMessage(t *testing.T) {
	got := elevenErrorMessage([]byte(`{"detail":{"status":"payment_required","message":"Free users cannot use library voices. See https://elevenlabs.io/x?y=1 now"}}`))
	if !strings.HasPrefix(got, "payment_required Free users") || strings.Contains(got, "http") {
		t.Errorf("got %q", got)
	}
	if got := elevenErrorMessage([]byte(`{"detail":"bad voice"}`)); got != "bad voice" {
		t.Errorf("string detail: %q", got)
	}
	if got := elevenErrorMessage([]byte(`not json`)); got != "not json" {
		t.Errorf("non-JSON body: %q", got)
	}
}

// ── persona ────────────────────────────────────────────────────

func TestPersonaCallsYouBabeNeverDarling(t *testing.T) {
	// The prompt may mention "darling" once, as the thing she must not say.
	if !strings.Contains(lucilleSystemPrompt, `"babe"`) {
		t.Error("the system prompt must tell her to call you babe")
	}
	if strings.Count(strings.ToLower(lucilleSystemPrompt), "darling") != 1 || !strings.Contains(lucilleSystemPrompt, `Never "darling"`) {
		t.Error(`"darling" may appear in the prompt only inside the Never "darling" rule`)
	}
	if !strings.Contains(strings.ToLower(lucilleSystemPrompt), "no emoji") {
		t.Error("her words are spoken, so the prompt must rule out emoji")
	}
}

func TestOfflineRepliesAreBabeAndSpeakable(t *testing.T) {
	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)
	inputs := []string{"hey", "I feel sad", "so tired", "should I drink water", "I miss you", "something else entirely", "think about it", ""}
	for _, in := range inputs {
		r := app.fallbackLocalReply(in)
		if strings.Contains(strings.ToLower(r), "darling") {
			t.Errorf("reply to %q says darling: %q", in, r)
		}
		if !strings.Contains(r, "babe") {
			t.Errorf("reply to %q never says babe: %q", in, r)
		}
		if speakable(r, 500) != r {
			t.Errorf("reply to %q has emoji or markup that the voice would mangle: %q", in, r)
		}
	}
	// "think" and "something" contain "hi"; only the word "hi" is a greeting
	if r := app.fallbackLocalReply("think about something"); strings.Contains(r, "there you are") {
		t.Errorf("a substring match treated %q as a greeting", "think about something")
	}
	// "tired" used to be swallowed by the sad branch, so the sleep reply never fired
	if r := app.fallbackLocalReply("so tired"); !strings.Contains(r, "bed") {
		t.Errorf("tired should get the sleep reply, got %q", r)
	}
}

// ── chat model ────────────────────────────────────────────────

// The Qwen 3.6 models think before answering and the thinking counts against max_tokens: with a short
// budget they returned HTTP 200 and an EMPTY reply, which became the canned offline line with nothing in
// the log. She must ask for no reasoning, and an empty reply must be an error, not a blank answer.
func TestChatAsksForNoReasoningAndRejectsEmptyReplies(t *testing.T) {
	var got map[string]any
	reply := `{"choices":[{"message":{"content":"Mm... hello babe."},"finish_reason":"stop"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	old := openRouterChatURL
	openRouterChatURL = srv.URL
	defer func() { openRouterChatURL = old }()

	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)
	app.cfg.OpenRouterKey = "k"

	text, err := app.callOpenRouter(context.Background(), "qwen/qwen3.6-plus", ChatRequest{Message: "hi"})
	if err != nil || text != "Mm... hello babe." {
		t.Fatalf("text=%q err=%v", text, err)
	}
	if r, _ := got["reasoning"].(map[string]any); r["enabled"] != false {
		t.Errorf("reasoning = %v, want {enabled:false}", got["reasoning"])
	}
	msgs, _ := got["messages"].([]any)
	if sys, _ := msgs[0].(map[string]any); !strings.Contains(sys["content"].(string), `"babe"`) {
		t.Error("the persona prompt must be what is sent as the system message")
	}

	reply = `{"choices":[{"message":{"content":"","reasoning":"thinking..."},"finish_reason":"length"}]}`
	if text, err := app.callOpenRouter(context.Background(), "qwen/qwen3.6-plus", ChatRequest{Message: "hi"}); err == nil || !strings.Contains(err.Error(), "length") {
		t.Errorf("an empty reply must be an error that says why, got text=%q err=%v", text, err)
	}
}

func TestDrI30ltzVerbalVoiceSynthesis(t *testing.T) {
	fo := &fakeOpenRouterAudio{}
	withOpenRouterAudio(t, fo.serve(t).URL)

	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)
	app.cfg.OpenRouterKey = "k"

	b64, err := app.synthesizeVoiceFor(context.Background(), "Primary drive degraded. Image immediately.", "i30ltz")
	if err != nil {
		t.Fatalf("synthesizeVoiceFor i30ltz error: %v", err)
	}
	raw, _ := base64.StdEncoding.DecodeString(b64)
	if string(raw) != "AUDIO-"+openRouterElevenModel+"-"+defaultI30ltzVoice {
		t.Fatalf("spoken by %q, want OpenRouter's ElevenLabs model with Dr. I30LTZ voice (%s)", raw, defaultI30ltzVoice)
	}
	if fo.lastReq["voice"] != defaultI30ltzVoice {
		t.Errorf("voice sent = %v, want %s", fo.lastReq["voice"], defaultI30ltzVoice)
	}
}
