package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// The Lucille <-> Dr. Boltz bridge.
//
//	Lucille   thinks up challenges (or you type one), and answers in the shared channel.
//	Dr. Boltz a paired agent (role "agent") that claims a challenge, codes it in a
//	          throw-away git worktree on a branch, and reports back.
//	You       watch everything in one transcript (phone, `lucille-assistant bridge tail`,
//	          or the tmux mirror from `bridge term`) and decide what lands.
//
// The transcript (events.jsonl) is the "mirrored terminal": Lucille, Dr. Boltz, the PC and
// every paired phone all read the same ordered stream, so what one sees the others see.
//
// Who may do what is enforced here, from the caller auth() identified, never from anything
// in a request body:
//
//	person (PC, phone, main token)  create/generate/approve/reject/cancel challenges,
//	                                approve or deny Dr. Boltz's requests for root
//	agent  (paired, role "agent")   claim a challenge, log progress, report the result,
//	                                merge work a person approved, ask to run an action
//
// Dr. Boltz can therefore write code and propose it, but a person always decides what is
// merged and what runs as root. Branch commits are its own; nothing here pushes anywhere.

const (
	maxChTitle     = 120
	maxChBody      = 4000
	maxChSummary   = 2000
	maxEventText   = 2000
	maxChallenges  = 200
	maxOpenCh      = 20
	eventRing      = 1000
	eventFileMax   = 8 << 20
	maxLogLines    = 50
	maxPendingAppr = 5
	maxStreams     = 16
	maxActReqs     = 30 // agent action requests per 10 minutes, unattended ones included

	agentOnlineWindow = 30 * time.Second
	claimStale        = 30 * time.Minute
	approvalTTL       = 10 * time.Minute
)

// Challenge states. A challenge moves open -> working -> review -> approved -> merged;
// failed, rejected and cancelled are dead ends a person can retry.
const (
	stOpen      = "open"
	stWorking   = "working"
	stReview    = "review"
	stApproved  = "approved"
	stMerged    = "merged"
	stRejected  = "rejected"
	stFailed    = "failed"
	stCancelled = "cancelled"
)

func terminalStatus(s string) bool {
	switch s {
	case stMerged, stRejected, stFailed, stCancelled:
		return true
	}
	return false
}

type CommitRef struct {
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

type Challenge struct {
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	Body      string      `json:"body"`
	Repo      string      `json:"repo"`
	Source    string      `json:"source"` // lucille | human
	Size      string      `json:"size,omitempty"`
	Status    string      `json:"status"`
	Created   time.Time   `json:"created"`
	Updated   time.Time   `json:"updated"`
	Activity  time.Time   `json:"activity,omitempty"` // when Dr. Boltz last reported on it
	ClaimedBy string      `json:"claimed_by,omitempty"`
	Branch    string      `json:"branch,omitempty"`
	Summary   string      `json:"summary,omitempty"`
	Commits   []CommitRef `json:"commits,omitempty"`
	DecidedBy string      `json:"decided_by,omitempty"`
}

type BridgeEvent struct {
	Seq       int64     `json:"seq"`
	TS        time.Time `json:"ts"`
	Who       string    `json:"who"`          // lucille | boltz | you | <phone label> | system
	To        string    `json:"to,omitempty"` // lucille | boltz | all
	Kind      string    `json:"kind"`         // say | term | status | challenge | approval | action
	Challenge string    `json:"challenge,omitempty"`
	Text      string    `json:"text"`
}

type Approval struct {
	ID        string        `json:"id"`
	ActionID  string        `json:"action_id"`
	Label     string        `json:"label"`
	Command   string        `json:"command"` // exactly what will run, for the person deciding
	Sudo      bool          `json:"sudo"`
	Reason    string        `json:"reason"`
	Challenge string        `json:"challenge,omitempty"`
	Status    string        `json:"status"` // pending | running | done | failed | denied | expired
	Requested time.Time     `json:"requested"`
	Decided   time.Time     `json:"decided,omitempty"`
	DecidedBy string        `json:"decided_by,omitempty"`
	Result    *ActionResult `json:"result,omitempty"`
}

type Bridge struct {
	mu   sync.Mutex
	dir  string
	cfg  BridgeConfig
	now  func() time.Time
	chal []Challenge
	appr []Approval

	ring    []BridgeEvent
	seq     int64
	evBytes int64
	subs    map[chan BridgeEvent]struct{}

	agentSeen  time.Time
	agentLabel string
	replies    []time.Time // when Lucille last auto-replied (rate limit)
	actReqs    []time.Time // when the agent last asked to run an action (rate limit)
}

func newBridge(dataDir string, cfg BridgeConfig) (*Bridge, error) {
	dir := filepath.Join(dataDir, "bridge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	b := &Bridge{dir: dir, cfg: cfg, now: time.Now, subs: map[chan BridgeEvent]struct{}{}}
	if err := readJSONFile(b.path("challenges.json"), &b.chal); err != nil {
		return nil, fmt.Errorf("bridge challenges: %w", err)
	}
	if err := readJSONFile(b.path("approvals.json"), &b.appr); err != nil {
		return nil, fmt.Errorf("bridge approvals: %w", err)
	}
	// A restart loses nothing durable, but an action that was mid-run cannot be resumed.
	for i := range b.appr {
		if b.appr[i].Status == "running" {
			b.appr[i].Status = "failed"
			b.appr[i].Result = &ActionResult{Error: "Lucille restarted while this was running."}
		}
	}
	b.loadEvents()
	return b, nil
}

func (b *Bridge) path(name string) string { return filepath.Join(b.dir, name) }

func readJSONFile(path string, v any) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func writeJSONFile(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (b *Bridge) saveLocked() {
	if err := writeJSONFile(b.path("challenges.json"), b.chal); err != nil {
		log.Printf("bridge: saving challenges: %v", err)
	}
	if err := writeJSONFile(b.path("approvals.json"), b.appr); err != nil {
		log.Printf("bridge: saving approvals: %v", err)
	}
}

// ── Transcript (the mirrored terminal) ──────────────────────────

func (b *Bridge) loadEvents() {
	raw, err := os.ReadFile(b.path("events.jsonl"))
	if err != nil {
		return
	}
	b.evBytes = int64(len(raw))
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		var ev BridgeEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		b.ring = append(b.ring, ev)
		if ev.Seq > b.seq {
			b.seq = ev.Seq
		}
	}
	if len(b.ring) > eventRing {
		b.ring = append([]BridgeEvent(nil), b.ring[len(b.ring)-eventRing:]...)
	}
}

// cleanText makes text safe to show in a terminal and on a phone: no control characters
// (an ANSI escape from an agent or a device label must never reach a terminal as an
// escape), no bidi overrides (they reorder what a person reads), and a length cap.
// keepNewlines is for chat messages; terminal lines are flattened to one line.
func cleanEventText(s string, max int, keepNewlines bool) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var sb strings.Builder
	n := 0
	for _, r := range s {
		switch {
		case r == '\n' && keepNewlines:
		case r == '\n' || r == '\r' || r == '\t' || r == 0x2028 || r == 0x2029:
			r = ' '
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			continue
		case r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
			continue
		case !unicode.IsPrint(r) && !unicode.IsSpace(r):
			continue
		}
		if n >= max {
			sb.WriteString("…")
			break
		}
		sb.WriteRune(r)
		n++
	}
	return strings.TrimSpace(sb.String())
}

// emit appends one event to the transcript and fans it out to live subscribers.
func (b *Bridge) emit(ev BridgeEvent) BridgeEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.emitLocked(ev)
}

func (b *Bridge) emitLocked(ev BridgeEvent) BridgeEvent {
	b.seq++
	ev.Seq = b.seq
	ev.TS = b.now().UTC()
	ev.Who = cleanEventText(ev.Who, 60, false)
	keepNL := ev.Kind == "say"
	ev.Text = cleanEventText(ev.Text, maxEventText, keepNL)
	b.ring = append(b.ring, ev)
	if len(b.ring) > eventRing {
		b.ring = append([]BridgeEvent(nil), b.ring[len(b.ring)-eventRing:]...)
	}
	b.appendFileLocked(ev)
	for ch := range b.subs {
		select {
		case ch <- ev:
		default: // too slow: drop it; the client reconnects with ?since= and catches up
			delete(b.subs, ch)
			close(ch)
		}
	}
	return ev
}

func (b *Bridge) appendFileLocked(ev BridgeEvent) {
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	raw = append(raw, '\n')
	if b.evBytes+int64(len(raw)) > eventFileMax {
		_ = os.Rename(b.path("events.jsonl"), b.path("events.jsonl.1"))
		b.evBytes = 0
	}
	f, err := os.OpenFile(b.path("events.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("bridge: transcript: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(raw); err == nil {
		b.evBytes += int64(len(raw))
	}
}

func (b *Bridge) eventsSince(since int64, limit int) []BridgeEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []BridgeEvent{}
	for _, ev := range b.ring {
		if ev.Seq > since {
			out = append(out, ev)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func (b *Bridge) subscribe() (chan BridgeEvent, func()) {
	ch := make(chan BridgeEvent, 256)
	b.mu.Lock()
	if len(b.subs) >= maxStreams {
		b.mu.Unlock()
		return nil, nil
	}
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
	}
}

func (b *Bridge) markAgent(c Caller) {
	b.mu.Lock()
	b.agentSeen, b.agentLabel = b.now(), c.Label
	b.mu.Unlock()
}

// ── Challenges ──────────────────────────────────────────────────

func (b *Bridge) findLocked(id string) *Challenge {
	for i := range b.chal {
		if b.chal[i].ID == id {
			return &b.chal[i]
		}
	}
	return nil
}

func (b *Bridge) nextIDLocked() string {
	max := 0
	for _, c := range b.chal {
		if n, err := strconv.Atoi(strings.TrimPrefix(c.ID, "c")); err == nil && n > max {
			max = n
		}
	}
	return fmt.Sprintf("c%04d", max+1)
}

func (b *Bridge) openCountLocked() int {
	n := 0
	for _, c := range b.chal {
		if !terminalStatus(c.Status) {
			n++
		}
	}
	return n
}

func normTitle(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

var errTooMany = errors.New("too many open challenges; cancel or finish some first")

// addChallenge validates and stores one challenge. dup reports a title already open or
// recently finished, so the generator cannot pile up copies of the same idea.
func (b *Bridge) addChallenge(title, body, repo, source, size string) (Challenge, error) {
	title = cleanEventText(title, maxChTitle, false)
	body = cleanEventText(body, maxChBody, true)
	if title == "" {
		return Challenge{}, errors.New("a challenge needs a title")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.cfg.repo(repo); !ok {
		return Challenge{}, fmt.Errorf("unknown repo %q (configured: %s)", repo, b.repoNamesLocked())
	}
	if b.openCountLocked() >= maxOpenCh {
		return Challenge{}, errTooMany
	}
	key := normTitle(title)
	recent := b.chal
	if len(recent) > 60 {
		recent = recent[len(recent)-60:]
	}
	for _, c := range recent {
		if c.Repo == repo && normTitle(c.Title) == key && c.Status != stRejected && c.Status != stCancelled && c.Status != stFailed {
			return Challenge{}, fmt.Errorf("duplicate of %s", c.ID)
		}
	}
	now := b.now().UTC()
	ch := Challenge{ID: b.nextIDLocked(), Title: title, Body: body, Repo: repo, Source: source, Size: cleanEventText(size, 12, false),
		Status: stOpen, Created: now, Updated: now}
	b.chal = append(b.chal, ch)
	if len(b.chal) > maxChallenges { // drop the oldest finished ones, never a live one
		kept := b.chal[:0]
		drop := len(b.chal) - maxChallenges
		for _, c := range b.chal {
			if drop > 0 && terminalStatus(c.Status) {
				drop--
				continue
			}
			kept = append(kept, c)
		}
		b.chal = kept
	}
	b.saveLocked()
	who := "you"
	if source == "lucille" {
		who = "lucille"
	}
	b.emitLocked(BridgeEvent{Who: who, To: "boltz", Kind: "challenge", Challenge: ch.ID,
		Text: fmt.Sprintf("new challenge %s [%s]: %s", ch.ID, ch.Repo, ch.Title)})
	return ch, nil
}

func (b *Bridge) repoNamesLocked() string {
	var n []string
	for _, r := range b.cfg.Repos {
		n = append(n, r.Name)
	}
	return strings.Join(n, ", ")
}

// liveWorkingLocked returns the challenge Dr. Boltz is actively on, if any. A claim that
// has gone quiet for claimStale is treated as abandoned (the worker died).
func (b *Bridge) liveWorkingLocked() *Challenge {
	for i := range b.chal {
		c := &b.chal[i]
		if c.Status == stWorking && b.now().Sub(c.Activity) < claimStale {
			return c
		}
	}
	return nil
}

func (b *Bridge) sweepStaleLocked() {
	changed := false
	for i := range b.chal {
		c := &b.chal[i]
		if c.Status == stWorking && b.now().Sub(c.Activity) >= claimStale {
			c.Status, c.Updated = stFailed, b.now().UTC()
			c.Summary = "Dr. Boltz stopped reporting; the claim was abandoned."
			b.emitLocked(BridgeEvent{Who: "system", Kind: "status", Challenge: c.ID, Text: c.ID + " abandoned: no report from Dr. Boltz for 30 minutes"})
			changed = true
		}
	}
	if changed {
		b.saveLocked()
	}
}

var branchRe = regexp.MustCompile(`^boltz/c[0-9]{4,}$`)
var shaRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// ── HTTP: reading ───────────────────────────────────────────────

func (a *App) bridgeReady(w http.ResponseWriter) bool {
	if a.bridge == nil {
		writeErr(w, http.StatusServiceUnavailable, "The bridge is not running on this server.")
		return false
	}
	return true
}

func sortedChallenges(list []Challenge) []Challenge {
	out := append([]Challenge(nil), list...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

type repoView struct {
	Name  string `json:"name"`
	Desc  string `json:"desc"`
	Scope string `json:"scope,omitempty"`
}

func (a *App) handleBridgeState(w http.ResponseWriter, r *http.Request) {
	if !a.bridgeReady(w) {
		return
	}
	if _, ok := requireKnown(w, r); !ok {
		return
	}
	b := a.bridge
	b.mu.Lock()
	b.sweepStaleLocked()
	b.expireApprovalsLocked()
	live, done := []Challenge{}, []Challenge{}
	for _, c := range sortedChallenges(b.chal) {
		if terminalStatus(c.Status) {
			if len(done) < 30 {
				done = append(done, c)
			}
		} else {
			live = append(live, c)
		}
	}
	appr := []Approval{}
	for i := len(b.appr) - 1; i >= 0 && len(appr) < 15; i-- {
		appr = append(appr, b.appr[i])
	}
	repos := make([]repoView, 0, len(b.cfg.Repos))
	for _, rp := range b.cfg.Repos {
		repos = append(repos, repoView{Name: rp.Name, Desc: rp.Desc, Scope: rp.Scope})
	}
	online := !b.agentSeen.IsZero() && b.now().Sub(b.agentSeen) < agentOnlineWindow
	agent := map[string]any{"online": online, "label": b.agentLabel}
	if !b.agentSeen.IsZero() {
		agent["last_seen"] = b.agentSeen.UTC()
	}
	b.mu.Unlock()
	writeJSON(w, map[string]any{
		"ok": true, "agent": agent, "repos": repos,
		"challenges": append(live, done...), "approvals": appr,
		"events": b.eventsSince(0, 120), "seq": b.currentSeq(),
	})
}

func (b *Bridge) currentSeq() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

func (a *App) handleBridgeStream(w http.ResponseWriter, r *http.Request) {
	if !a.bridgeReady(w) {
		return
	}
	if _, ok := requireKnown(w, r); !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming is not supported here")
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			since = n
		}
	}
	ch, cancel := a.bridge.subscribe() // subscribe first, then replay, so nothing falls in the gap
	if ch == nil {
		writeErr(w, http.StatusTooManyRequests, "Too many live streams are open.")
		return
	}
	defer cancel()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	last := since
	send := func(ev BridgeEvent) bool {
		raw, err := json.Marshal(ev)
		if err != nil {
			return true
		}
		_, err = fmt.Fprintf(w, "id: %d\nevent: bridge\ndata: %s\n\n", ev.Seq, raw)
		flusher.Flush()
		last = ev.Seq
		return err == nil
	}
	for _, ev := range a.bridge.eventsSince(since, 500) {
		if !send(ev) {
			return
		}
	}
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			if ev.Seq > last && !send(ev) {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// ── HTTP: people ────────────────────────────────────────────────

func normTo(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "all":
		return "all", true
	case "lucille":
		return "lucille", true
	case "boltz":
		return "boltz", true
	}
	return "", false
}

func (a *App) handleBridgeSay(w http.ResponseWriter, r *http.Request) {
	if !a.bridgeReady(w) {
		return
	}
	c, ok := requireKnown(w, r)
	if !ok {
		return
	}
	var req struct{ Text, To string }
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Text) == "" {
		writeErr(w, http.StatusBadRequest, `Send {"text": "...", "to": "lucille|boltz|all"}.`)
		return
	}
	to, okTo := normTo(req.To)
	if !okTo {
		writeErr(w, http.StatusBadRequest, `"to" must be lucille, boltz or all.`)
		return
	}
	if c.IsAgent() {
		a.bridge.markAgent(c)
	}
	ev := a.bridge.emit(BridgeEvent{Who: c.Name(), To: to, Kind: "say", Text: req.Text})
	if to != "boltz" {
		go a.lucilleReply(ev, c.IsAgent())
	}
	writeJSON(w, map[string]any{"ok": true, "event": ev})
}

func (a *App) handleBridgeChallengeCreate(w http.ResponseWriter, r *http.Request) {
	if !a.bridgeReady(w) {
		return
	}
	if _, ok := requireHuman(w, r); !ok {
		return
	}
	var req struct{ Title, Body, Repo string }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, `Send {"title": "...", "body": "...", "repo": "lucille"}.`)
		return
	}
	ch, err := a.bridge.addChallenge(req.Title, req.Body, strings.TrimSpace(req.Repo), "human", "")
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, errTooMany) {
			code = http.StatusConflict
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "challenge": ch})
}

func (a *App) handleBridgeDecision(w http.ResponseWriter, r *http.Request) {
	if !a.bridgeReady(w) {
		return
	}
	c, ok := requireHuman(w, r)
	if !ok {
		return
	}
	var req struct{ Action string }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, `Send {"action": "approve|reject|cancel|retry"}.`)
		return
	}
	b := a.bridge
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := b.findLocked(r.PathValue("id"))
	if ch == nil {
		writeErr(w, http.StatusNotFound, "No such challenge.")
		return
	}
	from, to := ch.Status, ""
	switch req.Action {
	case "approve":
		if from == stReview {
			to = stApproved
		}
	case "reject":
		if from == stReview {
			to = stRejected
		}
	case "cancel":
		if !terminalStatus(from) {
			to = stCancelled
		}
	case "retry":
		if from == stFailed || from == stRejected || from == stCancelled {
			to = stOpen
		}
	default:
		writeErr(w, http.StatusBadRequest, `"action" must be approve, reject, cancel or retry.`)
		return
	}
	if to == "" {
		writeErr(w, http.StatusConflict, fmt.Sprintf("Can't %s a challenge that is %s.", req.Action, from))
		return
	}
	ch.Status, ch.Updated, ch.DecidedBy = to, b.now().UTC(), c.Name()
	if to == stOpen {
		ch.ClaimedBy, ch.Branch, ch.Summary, ch.Commits, ch.Activity = "", "", "", nil, time.Time{}
	}
	b.saveLocked()
	b.emitLocked(BridgeEvent{Who: c.Name(), Kind: "status", Challenge: ch.ID, Text: fmt.Sprintf("%s: %s -> %s", ch.ID, from, to)})
	writeJSON(w, map[string]any{"ok": true, "challenge": *ch})
}

// ── HTTP: Dr. Boltz ─────────────────────────────────────────────

func (a *App) handleBridgeInbox(w http.ResponseWriter, r *http.Request) {
	if !a.bridgeReady(w) {
		return
	}
	c, ok := requireAgent(w, r)
	if !ok {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	b := a.bridge
	b.markAgent(c)
	b.mu.Lock()
	for i := range b.chal { // a worker that keeps polling is alive, even through a long silent engine run
		if b.chal[i].Status == stWorking && b.chal[i].ClaimedBy == c.Label {
			b.chal[i].Activity = b.now().UTC()
		}
	}
	b.sweepStaleLocked()
	var next *Challenge
	merges := []Challenge{}
	if b.liveWorkingLocked() == nil {
		for _, ch := range b.chal { // oldest first
			if ch.Status == stOpen {
				cp := ch
				next = &cp
				break
			}
		}
	}
	for _, ch := range b.chal {
		if ch.Status == stApproved {
			merges = append(merges, ch)
		}
	}
	repos := append([]BridgeRepo(nil), b.cfg.Repos...)
	engine := map[string]any{"max_budget_usd": b.cfg.MaxBudgetUSD, "max_minutes": b.cfg.MaxMinutes, "model": b.cfg.Model}
	b.mu.Unlock()

	// The actions Dr. Boltz may ask for: never a destructive one, and never a command line
	// beyond what a person would see when approving.
	actions := []actionView{}
	for _, act := range a.cfg.Actions {
		if !act.Destructive {
			actions = append(actions, toActionView(act))
		}
	}

	msgs := []BridgeEvent{}
	for _, ev := range b.eventsSince(after, 0) {
		if ev.Kind == "say" && ev.Who != "boltz" && (ev.To == "boltz" || ev.To == "all") {
			msgs = append(msgs, ev)
		}
	}
	writeJSON(w, map[string]any{"ok": true, "challenge": next, "merges": merges, "messages": msgs,
		"repos": repos, "engine": engine, "actions": actions, "seq": b.currentSeq()})
}

func (a *App) handleBridgeClaim(w http.ResponseWriter, r *http.Request) {
	if !a.bridgeReady(w) {
		return
	}
	c, ok := requireAgent(w, r)
	if !ok {
		return
	}
	b := a.bridge
	b.markAgent(c)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sweepStaleLocked()
	ch := b.findLocked(r.PathValue("id"))
	switch {
	case ch == nil:
		writeErr(w, http.StatusNotFound, "No such challenge.")
		return
	case ch.Status != stOpen:
		writeErr(w, http.StatusConflict, fmt.Sprintf("%s is %s, not open.", ch.ID, ch.Status))
		return
	case b.liveWorkingLocked() != nil:
		writeErr(w, http.StatusConflict, "Dr. Boltz is already working on another challenge.")
		return
	}
	now := b.now().UTC()
	ch.Status, ch.Updated, ch.Activity, ch.ClaimedBy = stWorking, now, now, c.Label
	ch.Branch = "boltz/" + ch.ID
	b.saveLocked()
	b.emitLocked(BridgeEvent{Who: "boltz", Kind: "status", Challenge: ch.ID, Text: fmt.Sprintf("%s: started: %s", ch.ID, ch.Title)})
	writeJSON(w, map[string]any{"ok": true, "challenge": *ch})
}

func (a *App) handleBridgeLog(w http.ResponseWriter, r *http.Request) {
	if !a.bridgeReady(w) {
		return
	}
	c, ok := requireAgent(w, r)
	if !ok {
		return
	}
	var req struct {
		Text  string
		Lines []string
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, `Send {"lines": ["..."]}.`)
		return
	}
	lines := req.Lines
	if req.Text != "" {
		lines = append(lines, req.Text)
	}
	if len(lines) > maxLogLines {
		lines = lines[:maxLogLines]
	}
	b := a.bridge
	b.markAgent(c)
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := b.findLocked(r.PathValue("id"))
	if ch == nil {
		writeErr(w, http.StatusNotFound, "No such challenge.")
		return
	}
	cancelled := ch.Status == stCancelled
	if ch.Status == stWorking || ch.Status == stApproved {
		ch.Activity = b.now().UTC()
		for _, ln := range lines {
			b.emitLocked(BridgeEvent{Who: "boltz", Kind: "term", Challenge: ch.ID, Text: ln})
		}
	}
	writeJSON(w, map[string]any{"ok": true, "cancelled": cancelled, "status": ch.Status})
}

func (a *App) handleBridgeComplete(w http.ResponseWriter, r *http.Request) {
	if !a.bridgeReady(w) {
		return
	}
	c, ok := requireAgent(w, r)
	if !ok {
		return
	}
	var req struct {
		Status  string
		Summary string
		Commits []CommitRef
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, `Send {"status": "review|failed|merged", "summary": "...", "commits": [...]}.`)
		return
	}
	b := a.bridge
	b.markAgent(c)
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := b.findLocked(r.PathValue("id"))
	if ch == nil {
		writeErr(w, http.StatusNotFound, "No such challenge.")
		return
	}
	from := ch.Status
	ok = (from == stWorking && (req.Status == stReview || req.Status == stFailed)) ||
		(from == stApproved && (req.Status == stMerged || req.Status == stFailed))
	if !ok {
		writeErr(w, http.StatusConflict, fmt.Sprintf("Can't move %s from %s to %q.", ch.ID, from, req.Status))
		return
	}
	var commits []CommitRef
	for _, cm := range req.Commits {
		if _, known := b.cfg.repo(cm.Repo); !known || !branchRe.MatchString(cm.Branch) || !shaRe.MatchString(cm.SHA) || len(commits) >= 20 {
			writeErr(w, http.StatusBadRequest, "Each commit needs a configured repo, a boltz/cNNNN branch and a hex sha.")
			return
		}
		cm.Subject = cleanEventText(cm.Subject, 200, false)
		commits = append(commits, cm)
	}
	if req.Status == stReview && len(commits) == 0 {
		writeErr(w, http.StatusBadRequest, "Work for review needs at least one commit.")
		return
	}
	ch.Status, ch.Updated, ch.Activity = req.Status, b.now().UTC(), b.now().UTC()
	ch.Summary = cleanEventText(req.Summary, maxChSummary, true)
	if len(commits) > 0 {
		ch.Commits = commits
	}
	b.saveLocked()
	msg := fmt.Sprintf("%s: %s", ch.ID, req.Status)
	if req.Status == stReview {
		msg = fmt.Sprintf("%s ready for your review: %d commit(s) on %s", ch.ID, len(commits), ch.Branch)
	}
	b.emitLocked(BridgeEvent{Who: "boltz", Kind: "status", Challenge: ch.ID, Text: msg})
	writeJSON(w, map[string]any{"ok": true, "challenge": *ch})
}

// ── Actions Dr. Boltz asks for ──────────────────────────────────

func (b *Bridge) expireApprovalsLocked() {
	for i := range b.appr {
		if b.appr[i].Status == "pending" && b.now().Sub(b.appr[i].Requested) > approvalTTL {
			b.appr[i].Status, b.appr[i].Decided = "expired", b.now().UTC()
		}
	}
}

func (b *Bridge) findApprovalLocked(id string) *Approval {
	for i := range b.appr {
		if b.appr[i].ID == id {
			return &b.appr[i]
		}
	}
	return nil
}

func (a *App) handleBridgeActionRequest(w http.ResponseWriter, r *http.Request) {
	if !a.bridgeReady(w) {
		return
	}
	c, ok := requireAgent(w, r)
	if !ok {
		return
	}
	act, found := actionByID(a.cfg.Actions, r.PathValue("id"))
	if !found {
		writeErr(w, http.StatusNotFound, "No such action.")
		return
	}
	if act.Destructive {
		writeErr(w, http.StatusForbidden, "Dr. Boltz may not request a destructive action. Ask the person to run it.")
		return
	}
	var req struct{ Reason, Challenge string }
	if r.ContentLength != 0 {
		_ = readJSON(r, &req)
	}
	b := a.bridge
	b.markAgent(c)
	b.mu.Lock()
	cut := b.now().Add(-10 * time.Minute)
	kept := b.actReqs[:0]
	for _, t := range b.actReqs {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	b.actReqs = kept
	if len(b.actReqs) >= maxActReqs {
		b.mu.Unlock()
		writeErr(w, http.StatusTooManyRequests, "Dr. Boltz is asking for actions too fast.")
		return
	}
	b.actReqs = append(b.actReqs, b.now())
	b.expireApprovalsLocked()
	pending := 0
	for _, ap := range b.appr {
		if ap.Status == "pending" {
			pending++
		}
	}
	if !act.AgentAuto && pending >= maxPendingAppr {
		b.mu.Unlock()
		writeErr(w, http.StatusTooManyRequests, "Too many requests are already waiting for a person.")
		return
	}
	id, _ := randomID()
	cmd := toActionView(act).Command
	ap := Approval{ID: id, ActionID: act.ID, Label: act.Label, Command: cmd, Sudo: act.Sudo,
		Reason: cleanEventText(req.Reason, 300, false), Challenge: cleanEventText(req.Challenge, 12, false),
		Status: "pending", Requested: b.now().UTC()}
	if act.AgentAuto {
		ap.Status, ap.DecidedBy, ap.Decided = "running", "agent_auto", b.now().UTC()
	}
	b.appr = append(b.appr, ap)
	if len(b.appr) > 100 {
		b.appr = b.appr[len(b.appr)-100:]
	}
	b.saveLocked()
	if !act.AgentAuto {
		b.emitLocked(BridgeEvent{Who: "boltz", Kind: "approval", Challenge: ap.Challenge,
			Text: fmt.Sprintf("asks to run %q (%s) — %s. Waiting for you: approval %s", act.Label, cmd, ap.Reason, ap.ID)})
	}
	b.mu.Unlock()

	if act.AgentAuto {
		a.finishApproval(ap.ID, act, "unattended", "bridge:"+c.Label)
	}
	b.mu.Lock()
	cur := *b.findApprovalLocked(ap.ID)
	b.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "approval": cur})
}

// finishApproval runs the action for an approval already marked running and records the
// outcome. The bridge lock is not held while the command runs.
func (a *App) finishApproval(id string, act Action, by, remote string) {
	b := a.bridge
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	res, busy := a.runLogged(ctx, act, true, remote)
	b.mu.Lock()
	defer b.mu.Unlock()
	ap := b.findApprovalLocked(id)
	if ap == nil {
		return
	}
	switch {
	case busy:
		ap.Status = "failed"
		ap.Result = &ActionResult{Error: "Another action was already running on this PC; nothing ran. Ask again."}
	case res.OK:
		ap.Status, ap.Result = "done", &res
	default:
		ap.Status, ap.Result = "failed", &res
	}
	b.saveLocked()
	snippet := cleanEventText(res.Output, 160, false)
	if ap.Result != nil && ap.Result.Error != "" {
		snippet = cleanEventText(ap.Result.Error, 160, false)
	}
	b.emitLocked(BridgeEvent{Who: "system", Kind: "action", Challenge: ap.Challenge,
		Text: fmt.Sprintf("%s (%s) %s — %s", ap.Label, by, ap.Status, snippet)})
}

func (a *App) handleBridgeApprovals(w http.ResponseWriter, r *http.Request) {
	if !a.bridgeReady(w) {
		return
	}
	if _, ok := requireHuman(w, r); !ok {
		return
	}
	b := a.bridge
	b.mu.Lock()
	b.expireApprovalsLocked()
	out := append(make([]Approval, 0, len(b.appr)), b.appr...)
	b.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "approvals": out})
}

func (a *App) handleBridgeApprovalGet(w http.ResponseWriter, r *http.Request) {
	if !a.bridgeReady(w) {
		return
	}
	c, ok := requireKnown(w, r)
	if !ok {
		return
	}
	b := a.bridge
	if c.IsAgent() {
		b.markAgent(c)
	}
	b.mu.Lock()
	b.expireApprovalsLocked()
	ap := b.findApprovalLocked(r.PathValue("id"))
	var cp Approval
	if ap != nil {
		cp = *ap
	}
	b.mu.Unlock()
	if ap == nil {
		writeErr(w, http.StatusNotFound, "No such request.")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "approval": cp})
}

func (a *App) handleBridgeApprovalDecision(w http.ResponseWriter, r *http.Request) {
	if !a.bridgeReady(w) {
		return
	}
	c, ok := requireHuman(w, r)
	if !ok {
		return
	}
	var req struct{ Approve bool }
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, `Send {"approve": true} or {"approve": false}.`)
		return
	}
	b := a.bridge
	b.mu.Lock()
	b.expireApprovalsLocked()
	ap := b.findApprovalLocked(r.PathValue("id"))
	if ap == nil {
		b.mu.Unlock()
		writeErr(w, http.StatusNotFound, "No such request.")
		return
	}
	if ap.Status != "pending" {
		st := ap.Status
		b.mu.Unlock()
		writeErr(w, http.StatusConflict, fmt.Sprintf("That request is already %s.", st))
		return
	}
	act, found := actionByID(a.cfg.Actions, ap.ActionID)
	ap.Decided, ap.DecidedBy = b.now().UTC(), c.Name()
	switch {
	case !req.Approve:
		ap.Status = "denied"
		b.emitLocked(BridgeEvent{Who: c.Name(), Kind: "approval", Challenge: ap.Challenge, Text: fmt.Sprintf("denied %q for Dr. Boltz", ap.Label)})
	case !found || act.Destructive:
		ap.Status = "failed"
		ap.Result = &ActionResult{Error: "That action is no longer available."}
	default:
		ap.Status = "running"
		b.emitLocked(BridgeEvent{Who: c.Name(), Kind: "approval", Challenge: ap.Challenge, Text: fmt.Sprintf("approved %q for Dr. Boltz", ap.Label)})
	}
	b.saveLocked()
	run := ap.Status == "running"
	id := ap.ID
	b.mu.Unlock()
	if run {
		a.finishApproval(id, act, c.Name(), "bridge:approved-by:"+c.Name())
	}
	b.mu.Lock()
	cur := *b.findApprovalLocked(id)
	b.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "approval": cur})
}
