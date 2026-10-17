package main

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"swarm-os/lucille-assistant/internal/unlockproto"
)

// Phone-approved sudo: this file is only the CARRIER between the root-owned PAM helper
// (cmd/lucille-pam) and the phone app. It is deliberately not trusted for anything:
//
//   - the helper invents the request, nonce and 4-digit code, and never sends the code here,
//     only three candidate numbers, so this service cannot tell which one the PC is showing;
//   - the phone signs its answer with a fingerprint-gated hardware key, and the helper
//     verifies that signature against a root-owned public key, so a hijacked or fake
//     service can block an approval but never forge one.
//
// See internal/unlockproto for the protocol and its tests.

const (
	unlockMaxLive      = 5
	unlockMaxPerMinute = 10
	unlockMaxTTL       = 120 * time.Second
)

// var, not const, so tests can shrink the timing — same pattern as pairing.go's
// pairCodeTTL/pairLockout.
var (
	enrolCodeTTL     = 10 * time.Minute
	enrolMaxAttempts = 5
	enrolLockout     = 60 * time.Second
)

var (
	hex32      = regexp.MustCompile(`^[0-9a-f]{32}$`)
	fourDigits = regexp.MustCompile(`^[0-9]{4}$`)
)

type unlockEntry struct {
	req    unlockproto.Request
	state  string // pending | answered
	chosen string
	sig    string
}

type unlockHub struct {
	mu      sync.Mutex
	entries map[string]*unlockEntry
	recent  []time.Time // when requests arrived, for the per-minute cap

	enrolCode     string
	enrolMade     time.Time
	enrolFails    int
	enrolLockedTo time.Time
}

func newUnlockHub() *unlockHub { return &unlockHub{entries: map[string]*unlockEntry{}} }

// unlocks is process-wide state held in memory only: a request is worthless after its
// expiry or a restart, exactly like a pairing code.
var unlocks = newUnlockHub()

// reset clears all state between tests. It must not replace h.mu itself (only Go's
// mutex, not a mutex-in-a-struct swap): overwriting *h while mu is held would unlock a
// different mutex than was locked, which panics instead of merely racing.
func (h *unlockHub) reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = map[string]*unlockEntry{}
	h.recent = nil
	h.enrolCode, h.enrolFails = "", 0
	h.enrolMade, h.enrolLockedTo = time.Time{}, time.Time{}
}

// prune drops expired entries. Caller holds h.mu.
func (h *unlockHub) prune(now time.Time) {
	for id, e := range h.entries {
		if now.Unix() > e.req.Expires+30 {
			delete(h.entries, id)
		}
	}
	keep := h.recent[:0]
	for _, t := range h.recent {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	h.recent = keep
}

func validUnlockRequest(q unlockproto.Request, now time.Time) string {
	if !hex32.MatchString(q.ID) || !hex32.MatchString(q.Nonce) {
		return "id and nonce must be 32 hex characters"
	}
	if len(q.Choices) != 3 {
		return "exactly three choices are required"
	}
	seen := map[string]bool{}
	for _, c := range q.Choices {
		if !fourDigits.MatchString(c) || seen[c] {
			return "choices must be three distinct 4-digit numbers"
		}
		seen[c] = true
	}
	for _, s := range []string{q.Service, q.User, q.Host} {
		if s == "" || len(s) > 64 || strings.ContainsAny(s, "\n\r\x00") {
			return "service, user and host must be short single-line values"
		}
	}
	if q.Expires <= now.Unix() || q.Expires > now.Add(unlockMaxTTL).Unix() {
		return "expiry must be within the next two minutes"
	}
	return ""
}

// onlyPCPerson allows the helper (loopback, no token) and nobody else: not a phone, not
// Dr. Boltz's agent, not a request whose token failed.
func onlyPCPerson(w http.ResponseWriter, r *http.Request) bool {
	c := callerOf(r)
	if c.Kind != callerPC || c.BadToken {
		writeErr(w, http.StatusForbidden, "Only the PC itself can do this.")
		return false
	}
	return true
}

// onlyPhone allows a paired phone (or the main token, which already means "the person").
func onlyPhone(w http.ResponseWriter, r *http.Request) bool {
	c, ok := requireKnown(w, r)
	if !ok {
		return false
	}
	if c.Kind != callerPhone && c.Kind != callerMain {
		writeErr(w, http.StatusForbidden, "Only a paired phone can answer this.")
		return false
	}
	return true
}

// POST /api/unlock/request: from the PAM helper.
func (a *App) handleUnlockRequest(w http.ResponseWriter, r *http.Request) {
	if !onlyPCPerson(w, r) {
		return
	}
	var q unlockproto.Request
	if err := readJSON(r, &q); err != nil {
		writeErr(w, http.StatusBadRequest, "Bad request body.")
		return
	}
	now := time.Now()
	if msg := validUnlockRequest(q, now); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	unlocks.mu.Lock()
	defer unlocks.mu.Unlock()
	unlocks.prune(now)
	if len(unlocks.recent) >= unlockMaxPerMinute {
		writeErr(w, http.StatusTooManyRequests, "Too many approval requests; wait a minute.")
		return
	}
	live := 0
	for _, e := range unlocks.entries {
		if e.state == "pending" && now.Unix() <= e.req.Expires {
			live++
		}
	}
	if live >= unlockMaxLive {
		writeErr(w, http.StatusTooManyRequests, "Too many approvals are already waiting.")
		return
	}
	if _, dup := unlocks.entries[q.ID]; dup {
		writeErr(w, http.StatusConflict, "That request id is already in use.")
		return
	}
	unlocks.entries[q.ID] = &unlockEntry{req: q, state: "pending"}
	unlocks.recent = append(unlocks.recent, now)
	writeJSON(w, map[string]any{"ok": true})
}

func livePending(now time.Time) []unlockproto.Request {
	unlocks.mu.Lock()
	defer unlocks.mu.Unlock()
	unlocks.prune(now)
	out := []unlockproto.Request{}
	for _, e := range unlocks.entries {
		if e.state == "pending" && now.Unix() <= e.req.Expires {
			out = append(out, e.req)
		}
	}
	return out
}

// GET /api/unlock/pending: the phone polls this (and the stream below pushes the same).
func (a *App) handleUnlockPending(w http.ResponseWriter, r *http.Request) {
	if !onlyPhone(w, r) {
		return
	}
	writeJSON(w, map[string]any{"ok": true, "requests": livePending(time.Now())})
}

// GET /api/unlock/stream: server-sent events, one "unlock" event per new request, so the
// phone's notification shows within a second without polling.
func (a *App) handleUnlockStream(w http.ResponseWriter, r *http.Request) {
	if !onlyPhone(w, r) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming is not supported here")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	sent := map[string]bool{}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	lastPing := time.Now()
	for {
		for _, q := range livePending(time.Now()) {
			if sent[q.ID] {
				continue
			}
			sent[q.ID] = true
			raw, err := json.Marshal(q)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "id: %s\nevent: unlock\ndata: %s\n\n", q.ID, raw); err != nil {
				return
			}
			flusher.Flush()
		}
		if time.Since(lastPing) > 15*time.Second {
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
			lastPing = time.Now()
		}
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}

// POST /api/unlock/respond: the phone's signed answer. The service stores it verbatim
// (first answer wins) and judges nothing; the helper verifies the signature.
func (a *App) handleUnlockRespond(w http.ResponseWriter, r *http.Request) {
	if !onlyPhone(w, r) {
		return
	}
	var in unlockproto.Response
	if err := readJSON(r, &in); err != nil || len(in.Sig) > 512 {
		writeErr(w, http.StatusBadRequest, "Bad request body.")
		return
	}
	now := time.Now()
	unlocks.mu.Lock()
	defer unlocks.mu.Unlock()
	e, ok := unlocks.entries[in.ID]
	if !ok || now.Unix() > e.req.Expires {
		writeErr(w, http.StatusNotFound, "That request is gone or expired.")
		return
	}
	if e.state != "pending" {
		writeErr(w, http.StatusConflict, "That request was already answered.")
		return
	}
	found := false
	for _, c := range e.req.Choices {
		if c == in.Chosen {
			found = true
		}
	}
	if !found {
		writeErr(w, http.StatusBadRequest, "That number was not one of the choices.")
		return
	}
	e.state, e.chosen, e.sig = "answered", in.Chosen, in.Sig
	writeJSON(w, map[string]any{"ok": true})
}

// GET /api/unlock/result/{id}: the helper collects the answer. Loopback only.
func (a *App) handleUnlockResult(w http.ResponseWriter, r *http.Request) {
	if !onlyPCPerson(w, r) {
		return
	}
	id := r.PathValue("id")
	unlocks.mu.Lock()
	defer unlocks.mu.Unlock()
	unlocks.prune(time.Now())
	e, ok := unlocks.entries[id]
	if !ok {
		writeJSON(w, map[string]any{"state": "unknown"})
		return
	}
	if e.state == "answered" {
		writeJSON(w, map[string]any{"state": "answered", "chosen": e.chosen, "sig": e.sig})
		return
	}
	writeJSON(w, map[string]any{"state": "pending"})
}

// ── Enrolling the phone's public key ─────────────────────────────

func enrolCodeNow() (string, error) { return sixDigitCode() }

// GET /api/unlock/enrol/new: print a one-time code on the PC (loopback only, like pairing).
func (a *App) handleUnlockEnrolNew(w http.ResponseWriter, r *http.Request) {
	if !onlyPCPerson(w, r) {
		return
	}
	code, err := enrolCodeNow()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not make a code.")
		return
	}
	unlocks.mu.Lock()
	unlocks.enrolCode, unlocks.enrolMade, unlocks.enrolFails, unlocks.enrolLockedTo = code, time.Now(), 0, time.Time{}
	unlocks.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "code": code, "ttl_seconds": int(enrolCodeTTL.Seconds())})
}

// pendingApproverPath is where an enrolled key waits until the user installs it, as
// root, into /etc/lucille/approver.pub. The service never writes that root-owned file.
func (a *App) pendingApproverPath() string {
	return filepath.Join(a.store.dir, "approver.pub.pending")
}

// POST /api/unlock/enrol: the phone sends its public key plus the code from the PC.
func (a *App) handleUnlockEnrol(w http.ResponseWriter, r *http.Request) {
	if !onlyPhone(w, r) {
		return
	}
	var in struct {
		Code   string `json:"code"`
		Pubkey string `json:"pubkey"` // base64 (std) X.509 SubjectPublicKeyInfo DER, P-256
	}
	if err := readJSON(r, &in); err != nil || len(in.Pubkey) > 1024 {
		writeErr(w, http.StatusBadRequest, "Bad request body.")
		return
	}
	now := time.Now()
	unlocks.mu.Lock()
	if now.Before(unlocks.enrolLockedTo) {
		unlocks.mu.Unlock()
		writeErr(w, http.StatusTooManyRequests, "Too many wrong codes; try again shortly.")
		return
	}
	valid := unlocks.enrolCode != "" && now.Sub(unlocks.enrolMade) < enrolCodeTTL &&
		subtle.ConstantTimeCompare([]byte(unlocks.enrolCode), []byte(in.Code)) == 1
	if !valid {
		unlocks.enrolFails++
		if unlocks.enrolFails >= enrolMaxAttempts {
			unlocks.enrolLockedTo = now.Add(enrolLockout)
		}
		unlocks.mu.Unlock()
		writeErr(w, http.StatusForbidden, "Wrong or expired code.")
		return
	}
	unlocks.enrolCode = "" // single use
	unlocks.mu.Unlock()

	der, err := base64.StdEncoding.DecodeString(in.Pubkey)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "Public key is not valid base64.")
		return
	}
	pem, err := unlockproto.PublicKeyPEM(der)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "That is not a P-256 public key.")
		return
	}
	if err := os.WriteFile(a.pendingApproverPath(), pem, 0o600); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save the key.")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "next": "On the PC run: sudo install -D -m 0644 -o root -g root " +
		a.pendingApproverPath() + " /etc/lucille/approver.pub"})
}
