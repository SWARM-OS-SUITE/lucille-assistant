package main

// Phone -> PC clipboard (user 2026-10-15: "if I copy on her app it should be on my PC keyboard").
// Whatever the phone app copies is also put on this PC's clipboard, so Ctrl+V on the PC pastes it.
//
//   - One direction only. Nothing here reads the PC clipboard: it can hold passwords, and a phone
//     (or a stolen phone token) must never be able to pull them off the PC.
//   - The text is never logged or stored by this service.
//   - Key- and password-shaped parts are blanked out first (the same `redact` the briefs use),
//     because KDE's Klipper keeps a clipboard history on disk. The reply says how many were.
//   - Only a person (paired phone, main token, or this PC) may set it; Dr. Boltz's agent may not.
//   - Every tool is called with an argv list, never through a shell.
//   - Kill switch: LUCILLE_CLIPBOARD=0, or touch ~/.config/lucille-assistant/clipboard.disabled.

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// 64 KB: far more than any prompt or brief the app copies, and well under Linux's 128 KB limit for
// one argv string (Klipper's text travels as an argument to busctl).
const clipboardMax = 64 << 10

// Klipper answered a cold first call in ~5 s once (2026-10-15) and in ~0.05 s after that. The phone
// waits 15 s for a reply, so 10 s here still lets it hear the outcome.
const clipboardTimeout = 10 * time.Second

func clipboardEnabled() bool {
	if os.Getenv("LUCILLE_CLIPBOARD") == "0" {
		return false
	}
	home, _ := os.UserHomeDir()
	_, err := os.Stat(filepath.Join(home, ".config/lucille-assistant/clipboard.disabled"))
	return err != nil
}

type clipTool struct {
	name  string
	needs string // environment variable that must be set for this tool to work ("" = none)
	argv  func(text string) []string
	stdin bool // text goes on stdin instead of in argv
}

// Tried in order. Klipper first: the Debian PC runs KDE Plasma on X11, and it is the clipboard
// Plasma's own paste uses. wl-copy covers SWARM OS (labwc, Wayland); xclip/xsel any other X11 desktop.
var clipTools = []clipTool{
	{"klipper", "", func(t string) []string {
		return []string{"busctl", "--user", "call", "org.kde.klipper", "/klipper", "org.kde.klipper.klipper", "setClipboardContents", "s", t}
	}, false},
	{"wl-copy", "WAYLAND_DISPLAY", func(string) []string { return []string{"wl-copy", "--type", "text/plain;charset=utf-8"} }, true},
	{"xclip", "DISPLAY", func(string) []string { return []string{"xclip", "-selection", "clipboard"} }, true},
	{"xsel", "DISPLAY", func(string) []string { return []string{"xsel", "--clipboard", "--input"} }, true},
}

// setPCClipboard is a variable so tests never touch the real desktop clipboard.
var setPCClipboard = func(ctx context.Context, text string) (string, error) {
	tried := 0
	for _, t := range clipTools {
		if t.needs != "" && os.Getenv(t.needs) == "" {
			continue
		}
		argv := t.argv(text)
		if _, err := exec.LookPath(argv[0]); err != nil {
			continue
		}
		tried++
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		if t.stdin {
			cmd.Stdin = strings.NewReader(text)
		}
		// Stdout/stderr stay unset (/dev/null): wl-copy and xclip fork a child that keeps serving the
		// selection, and a pipe held open by that child would make Wait hang until the next copy.
		if err := cmd.Run(); err == nil {
			return t.name, nil
		}
		if ctx.Err() != nil {
			// The text was handed over; only the answer was late. Klipper applies it before replying.
			return "", errors.New("the PC's clipboard was slow to answer (it may still have taken it: try Ctrl+V)")
		}
	}
	if tried == 0 {
		return "", errors.New("no clipboard tool found on the PC (KDE Klipper, wl-copy, xclip or xsel)")
	}
	return "", errors.New("the PC's clipboard did not accept it (is a desktop session running?)")
}

// POST /api/clipboard {"text": "..."}
func (a *App) handleClipboard(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireHuman(w, r); !ok {
		return
	}
	if !clipboardEnabled() {
		writeErr(w, http.StatusForbidden, "Copying to the PC is switched off on the PC (clipboard.disabled or LUCILLE_CLIPBOARD=0).")
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Text) == "" {
		writeErr(w, http.StatusBadRequest, `Send {"text": "..."}.`)
		return
	}
	if len(req.Text) > clipboardMax {
		writeErr(w, http.StatusRequestEntityTooLarge, "That's too long for the PC clipboard (64 KB at most).")
		return
	}
	if !utf8.ValidString(req.Text) || strings.ContainsRune(req.Text, 0) {
		writeErr(w, http.StatusBadRequest, "That isn't plain text.")
		return
	}
	text := req.Text
	redacted := len(secretShape.FindAllStringIndex(text, -1))
	if redacted > 0 {
		text = redact(text)
	}
	ctx, cancel := context.WithTimeout(r.Context(), clipboardTimeout)
	defer cancel()
	via, err := setPCClipboard(ctx, text)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "via": via, "chars": utf8.RuneCountInString(text), "redacted": redacted})
}
