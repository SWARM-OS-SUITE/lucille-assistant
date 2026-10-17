package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Lucille's voice: ElevenLabs first, OpenRouter's text-to-speech as a last resort.
//
// Two things used to leave her mute or generic without a word in the log:
//   - A free ElevenLabs plan refuses "library" voices over the API (HTTP 402 paid_plan_required).
//     A configured library voice was retried and refused on every single reply. Now a voice the
//     account cannot use is skipped until the next restart, and one log line says why.
//   - What was sent to the voice was the raw chat text, so emoji and markdown were read out or
//     mangled. speakable() cleans it, and caps it so one long reply cannot eat the monthly quota.

// elevenBase is a variable only so tests can point it at a local server.
var elevenBase = "https://api.elevenlabs.io"

const (
	// Lily: ElevenLabs' premade "velvety" British voice for Lucille. Premade voices work on every plan.
	defaultVoiceID = "pFZP5JQG7iQjIQuC4Bku"
	// Adam: ElevenLabs' premade deep, firm authoritative voice for Dr. I30LTZ.
	defaultI30ltzElevenVoiceID = "pNInz6obpgDQGcFmaJgB"

	elevenModel = "eleven_multilingual_v2"

	// voiceMaxChars bounds what is spoken per reply (the text on screen is never cut).
	voiceMaxChars = 500
)

// Other premade voices that work on a free plan, tried after the configured one and Lily.
// Jessica is the playful one; Sarah and Laura are the old safety nets.
var fallbackVoiceIDs = []string{
	"cgSgspJ2msm6clMCkdW9", // Jessica
	"EXAVITQu4vr4xnSDxMaL", // Sarah
	"FGY2WhTYpPnrIDTdsKH5", // Laura
}

// voiceSettingsFor returns voice tuning parameters per persona:
// - Lucille: sultry, slightly slower, warm, low stability for intimacy
// - Dr. I30LTZ: clinical, crisp, firm, authoritative pacing for diagnostic clarity
func voiceSettingsFor(persona string) map[string]any {
	if persona == "i30ltz" {
		return map[string]any{
			"stability":         0.65,
			"similarity_boost":  0.80,
			"style":             0.20,
			"use_speaker_boost": true,
			"speed":             1.0,
		}
	}
	return map[string]any{
		"stability":         0.35,
		"similarity_boost":  0.75,
		"style":             0.5,
		"use_speaker_boost": true,
		"speed":             0.92,
	}
}

func voiceSettings() map[string]any {
	return voiceSettingsFor("lucille")
}

var (
	voiceMu   sync.Mutex
	voiceDead = map[string]string{} // voice id -> why this account cannot use it
)

func voiceUsable(id string) bool {
	voiceMu.Lock()
	defer voiceMu.Unlock()
	_, dead := voiceDead[id]
	return !dead
}

func markVoiceDead(id, why string) {
	voiceMu.Lock()
	_, already := voiceDead[id]
	voiceDead[id] = why
	voiceMu.Unlock()
	if !already {
		log.Printf("tts: ElevenLabs voice %s cannot be used (%s); skipping it until restart", id, why)
	}
}

// voiceChain is the order voices are tried in, without repeats or blanks.
func voiceChain(configured string) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range append([]string{configured, defaultVoiceID}, fallbackVoiceIDs...) {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

var (
	urlRe   = regexp.MustCompile(`https?://\S+`)
	mdRe    = regexp.MustCompile("[*_`#>~|]+")
	spaceRe = regexp.MustCompile(`\s+`)
	gapRe   = regexp.MustCompile(` +([,.!?;:])`) // what a removed emoji or link leaves before punctuation
)

// speakable turns chat text into something that sounds right read aloud: no links, no markdown
// punctuation, no emoji or symbols (they get spelled out or skipped awkwardly), one run of spaces.
// Longer than max runes is cut at the last sentence end past the halfway mark, else at a word.
func speakable(text string, max int) string {
	text = urlRe.ReplaceAllString(text, "")
	text = mdRe.ReplaceAllString(text, "")
	text = strings.Map(func(r rune) rune {
		switch {
		case r == '‍' || r == '️' || r == '⃣': // joiner, emoji variation, keycap
			return -1
		case r >= 0x1F000: // emoji, pictographs
			return -1
		case r >= 0x2190 && r <= 0x2BFF: // arrows, math, dingbats, misc symbols (♥ ✨ ★ ...)
			return -1
		}
		return r
	}, text)
	text = strings.TrimSpace(gapRe.ReplaceAllString(spaceRe.ReplaceAllString(text, " "), "$1"))

	runes := []rune(text)
	if max <= 0 || len(runes) <= max {
		return text
	}
	for i := max - 1; i >= max/2; i-- {
		switch runes[i] {
		case '.', '!', '?', '…':
			return strings.TrimSpace(string(runes[:i+1]))
		}
	}
	cut := string(runes[:max])
	if i := strings.LastIndex(cut, " "); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:-") + "..."
}

// synthesizeVoice speaks text and returns base64 MP3. The error says nothing useful to a caller
// beyond "no voice"; the reasons are in the log.
//
// OpenRouter now carries ElevenLabs itself (openrouter.ai/elevenlabs), billed and authenticated
// with the same key already used for chat, so that is the normal path: no separate ElevenLabs
// account is needed. A direct ElevenLabs key, if one is configured, is a fallback with a richer
// catalog (real voice ids, not just OpenRouter's fixed named set). OpenRouter's generic
// (non-ElevenLabs) speech endpoint is the last resort if even her own voice model failed.
func (a *App) synthesizeVoice(ctx context.Context, text string) (string, error) {
	return a.synthesizeVoiceFor(ctx, text, "lucille")
}

func (a *App) synthesizeVoiceFor(ctx context.Context, text, persona string) (string, error) {
	text = speakable(text, voiceMaxChars)
	if text == "" {
		return "", fmt.Errorf("nothing to say")
	}

	targetVoice := defaultOpenRouterVoice
	genericFallbackVoice := "nova"
	if persona == "i30ltz" {
		targetVoice = envOr("LUCILLE_I30LTZ_VOICE", defaultI30ltzVoice)
		genericFallbackVoice = "echo"
	} else {
		targetVoice = a.elevenVoiceID()
	}

	if a.openRouterKey() != "" {
		voice := targetVoice
		if !validOpenRouterVoice(voice) {
			if persona == "i30ltz" {
				voice = defaultI30ltzVoice
			} else {
				voice = defaultOpenRouterVoice
			}
		}
		if audio, err := a.openRouterSpeak(ctx, openRouterElevenModel, voice, text); err == nil && len(audio) > 0 {
			return base64.StdEncoding.EncodeToString(audio), nil
		} else if err != nil {
			log.Printf("tts: openrouter/%s: %v", openRouterElevenModel, err)
		}
	}

	if a.elevenKey() != "" {
		if audio := a.elevenSpeakFor(ctx, text, persona); len(audio) > 0 {
			return base64.StdEncoding.EncodeToString(audio), nil
		}
	}

	if a.openRouterKey() != "" {
		if audio, err := a.openRouterSpeak(ctx, "openai/tts-1-hd", genericFallbackVoice, text); err == nil && len(audio) > 0 {
			return base64.StdEncoding.EncodeToString(audio), nil
		} else if err != nil {
			log.Printf("tts: openrouter/openai/tts-1-hd: %v", err)
		}
	}

	return "", fmt.Errorf("voice synthesis unavailable")
}

// openRouterAudioURL is a variable only so tests can point it at a local server.
var openRouterAudioURL = "https://openrouter.ai/api/v1/audio/speech"

// openRouterElevenVoices: the fixed set OpenRouter documents as accepted "voice" values for its
// ElevenLabs models (openrouter.ai/elevenlabs/eleven-v4-turbo) — there is no endpoint to list
// them, only this fixed set. Lily keeps the voice she already had by default.
var openRouterElevenVoices = []string{
	"george", "sarah", "adam", "alice", "bella", "bill", "brian", "callum", "charlie", "chris",
	"daniel", "eric", "harry", "jessica", "laura", "liam", "lily", "matilda", "river", "roger", "will",
}

const (
	openRouterElevenModel  = "elevenlabs/eleven-v4-turbo"
	defaultOpenRouterVoice = "lily"
	defaultI30ltzVoice     = "adam"
)

func validOpenRouterVoice(name string) bool {
	for _, v := range openRouterElevenVoices {
		if v == name {
			return true
		}
	}
	return false
}

// openRouterSpeak posts to OpenRouter's audio endpoint. model is an OpenRouter model slug
// ("elevenlabs/eleven-v4-turbo" for her real voice, "openai/tts-1-hd" as the generic last
// resort); voice is whatever that model accepts (an ElevenLabs name like "lily", or an OpenAI
// voice like "nova").
func (a *App) openRouterSpeak(ctx context.Context, model, voice, text string) ([]byte, error) {
	body, _ := json.Marshal(map[string]any{
		"model": model, "input": text, "voice": voice, "response_format": "mp3",
	})
	req, err := http.NewRequestWithContext(ctx, "POST", openRouterAudioURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.openRouterKey())
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("openrouter/%s HTTP %d: %s", model, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return io.ReadAll(resp.Body)
}

// elevenVoiceID: a voice chosen from the phone or the web page wins, then her own environment.
// It may be an OpenRouter voice name ("lily") or a direct ElevenLabs voice id, depending on
// which path picked it up; each path validates it is one of its own before using it.
func (a *App) elevenVoiceID() string {
	if v := a.aiPrefs().VoiceID; v != "" {
		return v
	}
	return a.cfg.ElevenVoiceID
}

// elevenSpeak walks the voice chain. A refusal that is about one voice (402 plan, 403, 404 gone,
// 422 bad id) marks just that voice dead and tries the next. Anything else (401 bad key or quota
// used up, 429, 5xx, network) is about the account or the service, so other voices would fail the
// same way and the walk stops.
func (a *App) elevenSpeak(ctx context.Context, text string) []byte {
	return a.elevenSpeakFor(ctx, text, "lucille")
}

func (a *App) elevenSpeakFor(ctx context.Context, text, persona string) []byte {
	targetVoice := a.elevenVoiceID()
	if persona == "i30ltz" {
		targetVoice = defaultI30ltzElevenVoiceID
	}
	for _, id := range voiceChain(targetVoice) {
		if !voiceUsable(id) {
			continue
		}
		audio, status, msg, err := elevenRequest(ctx, a.elevenKey(), id, text, persona)
		switch {
		case err == nil && len(audio) > 0:
			return audio
		case status == 402 || status == 403 || status == 404 || status == 422:
			markVoiceDead(id, fmt.Sprintf("HTTP %d: %s", status, msg))
		case err != nil:
			log.Printf("tts: ElevenLabs voice %s: %v", id, err)
			return nil
		default:
			log.Printf("tts: ElevenLabs voice %s: HTTP %d: %s", id, status, msg)
			return nil
		}
	}
	return nil
}

func elevenRequest(ctx context.Context, key, voiceID, text string, persona ...string) (audio []byte, status int, msg string, err error) {
	p := "lucille"
	if len(persona) > 0 && persona[0] != "" {
		p = persona[0]
	}
	body, _ := json.Marshal(map[string]any{
		"text":           text,
		"model_id":       elevenModel,
		"voice_settings": voiceSettingsFor(p),
	})
	req, err := http.NewRequestWithContext(ctx, "POST", elevenBase+"/v1/text-to-speech/"+voiceID, bytes.NewReader(body))
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("xi-api-key", key)
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 {
		audio, err = io.ReadAll(resp.Body)
		return audio, resp.StatusCode, "", err
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return nil, resp.StatusCode, elevenErrorMessage(raw), nil
}

// elevenErrorMessage pulls the human sentence out of ElevenLabs' {"detail":{"message":...}} (or
// {"detail":"..."}) error body, without URLs, short enough for a log line.
func elevenErrorMessage(raw []byte) string {
	var e struct {
		Detail json.RawMessage `json:"detail"`
	}
	msg := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &e) == nil && len(e.Detail) > 0 {
		var obj struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		}
		var s string
		switch {
		case json.Unmarshal(e.Detail, &obj) == nil && (obj.Message != "" || obj.Status != ""):
			msg = strings.TrimSpace(obj.Status + " " + obj.Message)
		case json.Unmarshal(e.Detail, &s) == nil && s != "":
			msg = s
		}
	}
	msg = strings.TrimSpace(urlRe.ReplaceAllString(msg, ""))
	if r := []rune(msg); len(r) > 160 {
		msg = string(r[:160]) + "..."
	}
	return msg
}

// ─── Voice picker: list and choose an ElevenLabs voice from the phone ──────────
// This app never invents a "sexy" curated shortlist; it lists exactly what ElevenLabs' own
// account returns (its name, category and labels, plus a preview_url to listen before choosing)
// and lets the person pick.

type voiceOption struct {
	ID          string `json:"voice_id"`
	Name        string `json:"name"`
	Category    string `json:"category,omitempty"`
	Description string `json:"description,omitempty"`
	Accent      string `json:"accent,omitempty"`
	Gender      string `json:"gender,omitempty"`
	Age         string `json:"age,omitempty"`
	UseCase     string `json:"use_case,omitempty"`
	PreviewURL  string `json:"preview_url,omitempty"`
	Usable      bool   `json:"usable"`
}

// handleAIVoices lists every voice on the ElevenLabs account tied to the configured key. Always
// ok:true; an empty list carries a reason (no key yet, or ElevenLabs could not be reached/refused).
func (a *App) handleAIVoices(w http.ResponseWriter, r *http.Request) {
	if !requireKeyOwner(w, r) {
		return
	}
	// synthesizeVoice tries OpenRouter's ElevenLabs model first, so this list must match what
	// she will actually use: the fixed set OpenRouter itself accepts, through the same key
	// already used for chat. No network call — OpenRouter publishes no listing endpoint, only
	// this documented set. A direct ElevenLabs key, checked below, is only the listing shown
	// when there is no OpenRouter key at all (it is still her synthesis fallback either way).
	if a.openRouterKey() != "" {
		out := make([]voiceOption, 0, len(openRouterElevenVoices))
		for _, name := range openRouterElevenVoices {
			out = append(out, voiceOption{ID: name, Name: strings.ToUpper(name[:1]) + name[1:], Usable: true})
		}
		// Her stored preference, not elevenVoiceID()'s resolved fallback: an unset preference
		// must read back as "", never the raw ElevenLabs default id, which never matches one
		// of these fixed names.
		writeJSON(w, map[string]any{"ok": true, "voices": out, "current": a.aiPrefs().VoiceID, "via": "openrouter"})
		return
	}

	key := a.elevenKey()
	if key == "" {
		writeJSON(w, map[string]any{"ok": true, "voices": []voiceOption{}, "reason": "no_key"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", elevenBase+"/v1/voices", nil)
	req.Header.Set("xi-api-key", key)
	resp, err := httpc.Do(req)
	if err != nil {
		writeJSON(w, map[string]any{"ok": true, "voices": []voiceOption{}, "reason": "unreachable"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		writeJSON(w, map[string]any{"ok": true, "voices": []voiceOption{}, "reason": "rejected"})
		return
	}
	var body struct {
		Voices []struct {
			VoiceID    string            `json:"voice_id"`
			Name       string            `json:"name"`
			Category   string            `json:"category"`
			PreviewURL string            `json:"preview_url"`
			Labels     map[string]string `json:"labels"`
		} `json:"voices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]any{"ok": true, "voices": []voiceOption{}, "reason": "unreachable"})
		return
	}
	out := make([]voiceOption, 0, len(body.Voices))
	for _, v := range body.Voices {
		out = append(out, voiceOption{
			ID: v.VoiceID, Name: v.Name, Category: v.Category, PreviewURL: v.PreviewURL,
			Description: v.Labels["description"], Accent: v.Labels["accent"],
			Gender: v.Labels["gender"], Age: v.Labels["age"], UseCase: v.Labels["use_case"],
			Usable: voiceUsable(v.VoiceID),
		})
	}
	writeJSON(w, map[string]any{"ok": true, "voices": out, "current": a.elevenVoiceID(), "via": "elevenlabs"})
}

func validVoiceID(s string) bool {
	if len(s) == 0 || len(s) > 100 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// handleAIVoiceSet saves which voice she uses, or clears it (back to the usual default and
// fallback chain) when voice_id is empty.
func (a *App) handleAIVoiceSet(w http.ResponseWriter, r *http.Request) {
	if !requireKeyOwner(w, r) {
		return
	}
	var req struct {
		VoiceID string `json:"voice_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, `Send {"voice_id": "..."} (an empty voice_id clears it).`)
		return
	}
	id := strings.TrimSpace(req.VoiceID)
	if id != "" && !validVoiceID(id) {
		writeErr(w, 400, "that does not look like an ElevenLabs voice id.")
		return
	}
	p := a.aiPrefs()
	p.VoiceID = id
	a.store.mu.Lock()
	err := a.store.writeJSON(a.prefsPath(), p)
	a.store.mu.Unlock()
	if err != nil {
		writeErr(w, 500, "Could not save that: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "prefs": p})
}
