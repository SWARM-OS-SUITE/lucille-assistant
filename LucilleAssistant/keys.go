package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The key window. Three providers can be given a key from the phone (or the web page)
// instead of by editing a file: OpenRouter and Claude (Anthropic) for chat, and ElevenLabs
// for her voice.
//
// Where a key lives: ~/.config/lucille-assistant/keys.env, mode 0600, one KEY=value line per
// provider, written atomically. A key saved here wins over the same key in her .env; clearing
// it here removes only this file's copy. The value is never sent back out — the API reports
// "set / where from / last four characters" and nothing more — and never logged.

const (
	provOpenRouter = "openrouter"
	provAnthropic  = "anthropic"
	provEleven     = "eleven"
)

type keyProvider struct {
	ID     string
	Label  string
	EnvVar string
}

var keyProviders = []keyProvider{
	{provOpenRouter, "OpenRouter", "OPENROUTER_API_KEY"},
	{provAnthropic, "Claude (Anthropic)", "ANTHROPIC_API_KEY"},
	{provEleven, "ElevenLabs (voice)", "ELEVEN_API_KEY"},
}

func providerByID(id string) (keyProvider, bool) {
	for _, p := range keyProviders {
		if p.ID == id {
			return p, true
		}
	}
	return keyProvider{}, false
}

// keysFilePath is where window-entered keys are kept.
func keysFilePath() string {
	if p := os.Getenv("LUCILLE_ASSISTANT_KEYS"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config/lucille-assistant/keys.env")
}

type KeyStore struct {
	mu   sync.RWMutex
	path string
	vals map[string]string // provider id -> key
}

func newKeyStore(path string) *KeyStore {
	k := &KeyStore{path: path, vals: map[string]string{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return k
	}
	for _, line := range strings.Split(string(b), "\n") {
		name, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		for _, p := range keyProviders {
			if p.EnvVar == strings.TrimSpace(name) && validKey(val) == nil {
				k.vals[p.ID] = val
			}
		}
	}
	return k
}

// get returns the key saved through the window ("" if none). A nil store is empty.
func (k *KeyStore) get(id string) string {
	if k == nil {
		return ""
	}
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.vals[id]
}

var errBadKey = errors.New("that does not look like an API key (no spaces, quotes or line breaks, 8-512 plain characters)")

// validKey is strict on purpose: the key is written into a KEY=value file, so a newline in a
// pasted "key" could otherwise add a line such as SWARM_AUTH_TOKEN= or a listen address.
func validKey(s string) error {
	if len(s) < 8 || len(s) > 512 {
		return errBadKey
	}
	for _, r := range s {
		if r <= ' ' || r >= 0x7f || r == '"' || r == '\'' || r == '\\' {
			return errBadKey
		}
	}
	return nil
}

// set saves a key, or removes the saved one when key is empty.
func (k *KeyStore) set(id, key string) error {
	if _, ok := providerByID(id); !ok {
		return fmt.Errorf("unknown provider %q", id)
	}
	key = strings.TrimSpace(key)
	if key != "" {
		if err := validKey(key); err != nil {
			return err
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	next := map[string]string{}
	for p, v := range k.vals {
		next[p] = v
	}
	if key == "" {
		delete(next, id)
	} else {
		next[id] = key
	}
	if err := writeKeysFile(k.path, next); err != nil {
		return err
	}
	k.vals = next
	return nil
}

func writeKeysFile(path string, vals map[string]string) error {
	var lines []string
	for _, p := range keyProviders {
		if v := vals[p.ID]; v != "" {
			lines = append(lines, p.EnvVar+"="+v)
		}
	}
	sort.Strings(lines)
	body := "# Written by the Lucille Assistant key window. Mode 0600. Never commit or share this file.\n" + strings.Join(lines, "\n")
	if len(lines) > 0 {
		body += "\n"
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".keys-*.tmp") // CreateTemp makes it 0600
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// openRouterKey / anthropicKey / elevenKey: a key from the window wins, then her own environment.
func (a *App) openRouterKey() string {
	if v := a.keys.get(provOpenRouter); v != "" {
		return v
	}
	return a.cfg.OpenRouterKey
}

func (a *App) anthropicKey() string {
	if v := a.keys.get(provAnthropic); v != "" {
		return v
	}
	return a.cfg.AnthropicKey
}

func (a *App) elevenKey() string {
	if v := a.keys.get(provEleven); v != "" {
		return v
	}
	return a.cfg.ElevenKey
}

func (a *App) keyFor(id string) string {
	switch id {
	case provOpenRouter:
		return a.openRouterKey()
	case provAnthropic:
		return a.anthropicKey()
	case provEleven:
		return a.elevenKey()
	}
	return ""
}

// keyStatus is all the window ever learns about a key.
type keyStatus struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Set      bool   `json:"set"`
	Source   string `json:"source"`          // window | env | ""
	Last4    string `json:"last4,omitempty"` // only for keys long enough that four characters reveal nothing useful
	EnvVar   string `json:"env_var"`
}

func (a *App) keyStatuses() []keyStatus {
	out := []keyStatus{}
	for _, p := range keyProviders {
		st := keyStatus{Provider: p.ID, Label: p.Label, EnvVar: p.EnvVar}
		if v := a.keys.get(p.ID); v != "" {
			st.Set, st.Source = true, "window"
			st.Last4 = last4(v)
		} else if v := a.keyFor(p.ID); v != "" {
			st.Set, st.Source = true, "env"
			st.Last4 = last4(v)
		}
		out = append(out, st)
	}
	return out
}

func last4(v string) string {
	if len(v) < 20 {
		return ""
	}
	return v[len(v)-4:]
}

// scrubKey removes a key from text that may be logged or returned.
func scrubKey(msg, key string) string {
	if key == "" {
		return msg
	}
	return strings.ReplaceAll(msg, key, "[key]")
}

// requireKeyOwner: only a person at this PC or a paired phone may see or change keys.
// Dr. Boltz's agent, a remote caller on a service with no token, and a revoked token cannot.
func requireKeyOwner(w http.ResponseWriter, r *http.Request) bool {
	_, ok := requireHuman(w, r)
	return ok
}

// URLs are variables only so tests can point them at a local server.
var (
	openRouterKeyCheckURL = "https://openrouter.ai/api/v1/key"
	anthropicModelsURL    = "https://api.anthropic.com/v1/models?limit=1"
	elevenUserURL         = "https://api.elevenlabs.io/v1/user"
)

func (a *App) handleKeySet(w http.ResponseWriter, r *http.Request) {
	if !requireKeyOwner(w, r) {
		return
	}
	var req struct {
		Provider string `json:"provider"`
		Key      string `json:"key"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, `Send {"provider": "openrouter" | "anthropic" | "eleven", "key": "..."} (an empty key clears it).`)
		return
	}
	if _, ok := providerByID(req.Provider); !ok {
		writeErr(w, 400, `provider must be "openrouter", "anthropic" or "eleven".`)
		return
	}
	if a.keys == nil {
		writeErr(w, 500, "The key store is not available.")
		return
	}
	if err := a.keys.set(req.Provider, req.Key); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "keys": a.keyStatuses()})
}

// handleKeyTest asks the provider a free question ("is this key valid?"), never a paid chat call.
func (a *App) handleKeyTest(w http.ResponseWriter, r *http.Request) {
	if !requireKeyOwner(w, r) {
		return
	}
	var req struct {
		Provider string `json:"provider"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, `Send {"provider": "openrouter" | "anthropic" | "eleven"}.`)
		return
	}
	if _, ok := providerByID(req.Provider); !ok {
		writeErr(w, 400, `provider must be "openrouter", "anthropic" or "eleven".`)
		return
	}
	key := a.keyFor(req.Provider)
	if key == "" {
		writeJSON(w, map[string]any{"ok": true, "result": "no_key"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var hreq *http.Request
	switch req.Provider {
	case provOpenRouter:
		hreq, _ = http.NewRequestWithContext(ctx, "GET", openRouterKeyCheckURL, nil)
		hreq.Header.Set("Authorization", "Bearer "+key)
	case provAnthropic:
		hreq, _ = http.NewRequestWithContext(ctx, "GET", anthropicModelsURL, nil)
		hreq.Header.Set("x-api-key", key)
		hreq.Header.Set("anthropic-version", anthropicVersion)
	case provEleven:
		hreq, _ = http.NewRequestWithContext(ctx, "GET", elevenUserURL, nil)
		hreq.Header.Set("xi-api-key", key)
	}
	resp, err := httpc.Do(hreq)
	if err != nil {
		writeJSON(w, map[string]any{"ok": true, "result": "unreachable"})
		return
	}
	defer resp.Body.Close()
	result := "unexpected"
	switch {
	case resp.StatusCode < 300:
		result = "valid"
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		result = "rejected"
	case resp.StatusCode == 429:
		result = "valid" // the provider knows the key; it is only busy
	}
	writeJSON(w, map[string]any{"ok": true, "result": result, "http": resp.StatusCode})
}
