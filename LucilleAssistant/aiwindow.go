package main

import (
	"net/http"
	"path/filepath"
)

// The AI window: one place on the phone (and the web page) to see which brains Lucille has,
// give her an OpenRouter or Claude key, test it, choose where her replies come from, and see
// whether her three offline models are installed.
//
//	GET  /api/ai           everything below, in one read (never a key value)
//	POST /api/ai/key       {provider, key}  save, or clear with an empty key
//	POST /api/ai/key/test  {provider}       a free "is this key valid?" check
//	POST /api/ai/prefs     {source, cloud_first}
//
// All of it is for a person only (requireHuman): this PC, or a paired phone.

type aiPrefs struct {
	// Source: auto = cloud first, then the offline swarm | cloud = cloud only | local = offline swarm only.
	Source string `json:"source"`
	// CloudFirst: which cloud provider is asked first when both have a key.
	CloudFirst string `json:"cloud_first"` // openrouter | anthropic
	// VoiceID: the ElevenLabs voice she speaks with, chosen from the phone or the web page.
	// Blank = her usual default and fallback chain (voiceChain in voice.go).
	VoiceID string `json:"voice_id,omitempty"`
}

func defaultAIPrefs() aiPrefs { return aiPrefs{Source: "auto", CloudFirst: provOpenRouter} }

func (p aiPrefs) normalized() aiPrefs {
	if p.Source != "cloud" && p.Source != "local" {
		p.Source = "auto"
	}
	if p.CloudFirst != provAnthropic {
		p.CloudFirst = provOpenRouter
	}
	return p
}

func (a *App) prefsPath() string { return filepath.Join(a.store.dir, "ai_prefs.json") }

func (a *App) aiPrefs() aiPrefs {
	p := defaultAIPrefs()
	if a.store == nil {
		return p
	}
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	_ = a.store.readJSON(a.prefsPath(), &p)
	return p.normalized()
}

func (a *App) handleAI(w http.ResponseWriter, r *http.Request) {
	if !requireKeyOwner(w, r) {
		return
	}
	writeJSON(w, map[string]any{
		"ok":    true,
		"keys":  a.keyStatuses(),
		"prefs": a.aiPrefs(),
		"local": a.localStatus(r.Context()),
		"claude": map[string]any{
			"model": anthropicModel(),
		},
		"openrouter": map[string]any{
			"model": envOr("OPENROUTER_MODEL", defaultOpenRouterModel),
		},
	})
}

func (a *App) handleAIPrefs(w http.ResponseWriter, r *http.Request) {
	if !requireKeyOwner(w, r) {
		return
	}
	p := a.aiPrefs()
	var req struct {
		Source     *string `json:"source"`
		CloudFirst *string `json:"cloud_first"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, `Send {"source": "auto" | "cloud" | "local", "cloud_first": "openrouter" | "anthropic"}.`)
		return
	}
	if req.Source != nil {
		switch *req.Source {
		case "auto", "cloud", "local":
			p.Source = *req.Source
		default:
			writeErr(w, 400, `source must be "auto", "cloud" or "local".`)
			return
		}
	}
	if req.CloudFirst != nil {
		switch *req.CloudFirst {
		case provOpenRouter, provAnthropic:
			p.CloudFirst = *req.CloudFirst
		default:
			writeErr(w, 400, `cloud_first must be "openrouter" or "anthropic".`)
			return
		}
	}
	a.store.mu.Lock()
	err := a.store.writeJSON(a.prefsPath(), p)
	a.store.mu.Unlock()
	if err != nil {
		writeErr(w, 500, "Could not save that: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "prefs": p})
}
