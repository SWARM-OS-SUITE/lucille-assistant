package main

// "Just what I say to change" (user 2026-10-14): Lucille does not keep chat transcripts. She keeps only what the
// user tells her to keep. Say "remember that ...", "idea: ...", "change: ...", "add to the list ..." in her chat, or
// POST /api/remember, and the sentence goes into the shared Idea inbox of the one master build list (where every AI
// reads it) and into swarm-memory (where Dr. Boltz reads it). Nothing else from a chat is stored.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var reRemember = regexp.MustCompile(`(?is)^\s*(?:hey\s+)?(?:lucille[,:]?\s+)?(?:please\s+)?(?:remember(?:\s+that)?|note(?:\s+that)?|idea|change|to-?do|add\s+(?:this\s+)?to\s+(?:the\s+)?(?:list|build\s+list)|put\s+(?:this\s+)?on\s+the\s+list)\s*[:,\-]?\s+(\S.*)$`)

// rememberText returns the sentence to keep when a chat message asks Lucille to remember it.
func rememberText(msg string) (string, bool) {
	m := reRemember.FindStringSubmatch(msg)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// rememberIdea files one sentence. added is false when the identical sentence is already on the list.
func (a *App) rememberIdea(text, who string) (id string, added bool, err error) {
	text = oneLine(redact(text), 600)
	if text == "" {
		return "", false, fmt.Errorf("nothing to remember")
	}
	sum := sha256.Sum256([]byte(strings.ToLower(text)))
	id = "rm-" + hex.EncodeToString(sum[:])[:8]
	marker := "[" + id + "]"
	line := fmt.Sprintf("- %s user (via %s): %s %s", day(time.Now()), who, text, marker)
	added, err = a.appendToMD(newBriefReader(a.cfg.Brief), line, marker, nil)
	if err != nil {
		return id, false, err
	}
	if added && mirrorThreadsToMemory {
		go mirrorIdea(id, text)
	}
	return id, added, nil
}

func mirrorIdea(id, text string) {
	bin, err := exec.LookPath("swarm-memory")
	if err != nil {
		home, _ := os.UserHomeDir()
		bin = filepath.Join(home, ".local/bin/swarm-memory")
	}
	cmd := exec.Command(bin, "add", "-kind", "note", "-tag", "idea", "-key", "idea:"+id, "-")
	cmd.Stdin = strings.NewReader("The user said to change/remember: " + text)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("remember: swarm-memory: %v %s", err, strings.TrimSpace(string(out)))
	}
}

// POST /api/remember {"text": "..."}
func (a *App) handleRemember(w http.ResponseWriter, r *http.Request) {
	c, ok := requireHuman(w, r)
	if !ok {
		return
	}
	var req struct{ Text string }
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Text) == "" {
		writeErr(w, http.StatusBadRequest, `Send {"text": "what to remember"}.`)
		return
	}
	who := "lucille"
	if c.Label != "" {
		who = "lucille, from " + c.Label
	}
	id, added, err := a.rememberIdea(req.Text, who)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save it: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": id, "added": added, "already": !added})
}
