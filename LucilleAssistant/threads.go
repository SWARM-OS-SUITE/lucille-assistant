package main

// The team Brief and its comment threads (user request 2026-10-14: "a pretty little artifact gui interactive.
// I can comment on any statement and go on Recovery OS and talk to Boltz about it, then fix it, and the three of
// us work together from any device to any device, all up to date").
//
// Three parts:
//   1. /api/artifact   the Brief as statements, each with a STABLE anchor (item:MESH-02, ai:dev-claude, sg-1a2b3c4d,
//                      proj:swarm-os, health:lucille-assistant): a tracker code or an id, never a hash of the
//                      sentence, because the sentence changes on every refresh and the thread must not be orphaned.
//   2. threads.jsonl   one append-only line per message, unique id each. Append-only on purpose: two machines'
//                      files merge by union on id, and no rewrite can clobber another OS's writes.
//   3. Ask Lucille / Ask Boltz   the server generates the reply and records it as by=lucille / by=boltz. Only a
//                      person can post a comment (RC-23: Dr. Boltz's agent token stays out of this). Both AIs get a
//                      short digest of recent threads in their prompts, so whichever one you talk to on whichever
//                      device already knows what you said to the other. Personas and models stay separate (MESH-04).
//
// Human comments and AI replies are also mirrored into swarm-memory (key thread:<anchor>), which is what carries
// them to the other machines through the memory sync.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	threadMaxText   = 1500
	threadMaxQuote  = 400
	threadPerAnchor = 40
	threadDigestMax = 6
)

// mirrorThreadsToMemory is switched on by main(); tests leave it off so they never write the real memory.
var mirrorThreadsToMemory = false

var (
	threadsMu   sync.Mutex
	threadSeq   atomic.Int64
	threadSeqOK sync.Once
	pendingMu   sync.Mutex
	pendingAsk  = map[string]time.Time{} // "anchor|who" -> started

	anchorPattern = regexp.MustCompile(`^(sg-[0-9a-f]{8}|[a-z]{2,8}:[A-Za-z0-9._-]{1,64})$`)
)

type ThreadMsg struct {
	ID     string `json:"id"`
	Anchor string `json:"anchor"`
	At     string `json:"at"`
	By     string `json:"by"` // you | <phone label> | lucille | boltz
	Text   string `json:"text"`
	Quote  string `json:"quote,omitempty"` // the statement, as it read when this was written
	Model  string `json:"model,omitempty"`
	Err    bool   `json:"err,omitempty"`  // an AI that could not answer says so in the thread
	Kind   string `json:"kind,omitempty"` // "coder" for Claude/Antigravity reports, "job" for Lucille's hand-off notes
}

func (a *App) threadsPath() string {
	if p := os.Getenv("LUCILLE_THREADS_FILE"); p != "" { // lets the file live on a drive more than one OS mounts
		return p
	}
	return filepath.Join(a.cfg.DataDir, "threads.jsonl")
}

func (a *App) readThreads() []ThreadMsg {
	b, err := os.ReadFile(a.threadsPath())
	if err != nil {
		return nil
	}
	var out []ThreadMsg
	seen := map[string]bool{}
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var m ThreadMsg
		if json.Unmarshal([]byte(ln), &m) != nil || m.ID == "" || seen[m.ID] {
			continue // a torn line or a duplicate from a merge is skipped, never fatal
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

func (a *App) initThreadSeq() {
	threadSeqOK.Do(func() { threadSeq.Store(int64(len(a.readThreads()))) })
}

func newThreadID() string {
	var r [4]byte
	_, _ = rand.Read(r[:])
	return "t" + strconv.FormatInt(time.Now().UnixNano(), 36) + hex.EncodeToString(r[:])
}

func (a *App) appendThread(m ThreadMsg) (ThreadMsg, error) {
	a.initThreadSeq()
	m.ID, m.At = newThreadID(), commentNow().Format(time.RFC3339Nano)
	b, err := json.Marshal(m)
	if err != nil {
		return m, err
	}
	threadsMu.Lock()
	defer threadsMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(a.threadsPath()), 0o700); err != nil {
		return m, err
	}
	f, err := os.OpenFile(a.threadsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return m, err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil { // one write call per line: appends do not interleave
		return m, err
	}
	threadSeq.Add(1)
	if mirrorThreadsToMemory {
		go mirrorThread(m)
	}
	return m, nil
}

func mirrorThread(m ThreadMsg) {
	if m.Err {
		return
	}
	// SWARM_MEMORY_BIN overrides the lookup entirely — needed on Android
	// (Lucille Live), which has no PATH and no ~/.local/bin to search; the
	// app sets it to its own bundled copy of the same CLI.
	bin := os.Getenv("SWARM_MEMORY_BIN")
	if bin == "" {
		var err error
		bin, err = exec.LookPath("swarm-memory")
		if err != nil {
			home, _ := os.UserHomeDir()
			bin = filepath.Join(home, ".local/bin/swarm-memory")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "add", "-kind", "note", "-tag", "brief-thread", "-key", "thread:"+m.Anchor, "-")
	q := m.Quote
	if q == "" {
		q = m.Anchor
	}
	cmd.Stdin = strings.NewReader(fmt.Sprintf("%s said on \"%s\": %s", m.By, clip(q, 120), clip(m.Text, 400)))
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("threads: swarm-memory mirror: %v %s", err, strings.TrimSpace(string(out)))
	}
}

func clip(s string, n int) string {
	s = strings.TrimSpace(spaceRuns.ReplaceAllString(s, " "))
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// threadDigest: the few most recently active threads, one line each. Goes into both AIs' prompts.
func (a *App) threadDigest() string {
	msgs := a.readThreads()
	if len(msgs) == 0 {
		return ""
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano)
	last := map[string]ThreadMsg{}
	quote := map[string]string{}
	for _, m := range msgs {
		if m.At < cutoff || m.Err {
			continue
		}
		last[m.Anchor] = m
		if m.Quote != "" {
			quote[m.Anchor] = m.Quote
		}
	}
	anchors := make([]string, 0, len(last))
	for k := range last {
		anchors = append(anchors, k)
	}
	sort.Slice(anchors, func(i, j int) bool { return last[anchors[i]].At > last[anchors[j]].At })
	if len(anchors) > threadDigestMax {
		anchors = anchors[:threadDigestMax]
	}
	var sb strings.Builder
	for _, k := range anchors {
		m := last[k]
		q := quote[k]
		if q == "" {
			q = k
		}
		fmt.Fprintf(&sb, "- on \"%s\": %s said \"%s\"\n", clip(q, 90), m.By, clip(m.Text, 140))
	}
	return sb.String()
}

func threadDigestBlock(d string) string {
	if d == "" {
		return ""
	}
	return "\n\nComment threads on the team Brief, newest first (the user, Lucille and Dr. Boltz all post here from any device; you may reference these, don't invent beyond them):\n" + d
}

// ── the statements ──────────────────────────────────────────────

type Statement struct {
	Anchor string `json:"anchor"`
	Text   string `json:"text"`
	Sub    string `json:"sub,omitempty"`
	Tag    string `json:"tag,omitempty"`   // short label shown as a chip
	Level  string `json:"level,omitempty"` // ok | warn | bad | info
	Status string `json:"status,omitempty"`
	Due    string `json:"due,omitempty"`
}

type Section struct {
	ID    string      `json:"id"`
	Title string      `json:"title"`
	Note  string      `json:"note,omitempty"`
	Items []Statement `json:"items"`
}

var (
	artifactMu    sync.Mutex
	artifactAt    time.Time
	artifactCache []Section
	nonSlug       = regexp.MustCompile(`[^a-z0-9]+`)
)

func slugOf(s string) string {
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 48 {
		s = s[:48]
	}
	return s
}

func startupStatusPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local/share/i30ltz/startup-status.json")
}

func (a *App) buildArtifactSections() []Section {
	var secs []Section

	// 1. startup health (from swarm-startup-check)
	if b, err := os.ReadFile(startupStatusPath()); err == nil {
		var st struct {
			CheckedAt string                                 `json:"checked_at"`
			Down      int                                    `json:"down"`
			Fixed     int                                    `json:"fixed"`
			Items     []struct{ Name, State, Detail string } `json:"items"`
		}
		if json.Unmarshal(b, &st) == nil && len(st.Items) > 0 {
			sec := Section{ID: "health", Title: "Did everything start?"}
			sum := Statement{Anchor: "health:all", Level: "ok", Tag: "all up",
				Text: fmt.Sprintf("All %d startup checks passed.", len(st.Items))}
			if st.Down > 0 {
				sum.Level, sum.Tag = "bad", fmt.Sprintf("%d down", st.Down)
				sum.Text = fmt.Sprintf("%d of %d startup checks failed.", st.Down, len(st.Items))
			}
			if t, err := time.Parse(time.RFC3339, st.CheckedAt); err == nil {
				sum.Sub = "checked " + t.Local().Format("Mon 3:04 PM")
			}
			sec.Items = append(sec.Items, sum)
			for _, it := range st.Items {
				if it.State == "ok" {
					continue
				}
				lv, tag := "bad", "down"
				if it.State == "fixed" {
					lv, tag = "warn", "self-fixed"
				}
				sec.Items = append(sec.Items, Statement{Anchor: "health:" + slugOf(it.Name), Text: it.Name, Sub: it.Detail, Level: lv, Tag: tag})
			}
			secs = append(secs, sec)
		}
	}

	// 2. what needs the user, then the rest of the critical items
	var needs, crit []Statement
	for _, r := range loadBuildRows() {
		if !r.open() || (r.Priority != "P0" && !(r.Priority == "P1" && r.needsUser())) {
			continue
		}
		s := Statement{Anchor: "item:" + r.ID, Text: clip(plain(r.Item), 220), Sub: clip(plain(r.Status), 140), Tag: r.ID + " · " + r.Priority, Level: "warn"}
		if r.needsUser() {
			s.Level = "bad"
			needs = append(needs, s)
		} else {
			crit = append(crit, s)
		}
	}
	if len(needs) > 0 {
		secs = append(secs, Section{ID: "needs", Title: "Waiting on you", Note: "an AI can't do these alone", Items: needs})
	}
	if len(crit) > 0 {
		secs = append(secs, Section{ID: "critical", Title: "Critical, in progress", Items: crit})
	}

	// 3. who is on what
	if claims, _ := parseActiveClaims(boardPath); len(claims) > 0 {
		sec := Section{ID: "team", Title: "Who's on what"}
		for _, c := range claims {
			s := Statement{Anchor: "ai:" + slugOf(c.AI), Text: c.AI + " — " + clip(plain(c.Task), 160), Sub: c.Area, Tag: "active", Level: "ok"}
			if c.Stale {
				s.Tag, s.Level = "stale "+humanAge(time.Since(c.ClaimedAt)), "warn"
			}
			sec.Items = append(sec.Items, s)
		}
		secs = append(secs, sec)
	}

	// 4. projects and Lucille's suggestions (with the schedule the comments already drive)
	if a.store == nil {
		return secs
	}
	if m := a.buildBrief(time.Now()); m != nil {
		if projects, ok := m["projects"].([]BriefProject); ok {
			cf, _ := a.readComments()
			for _, p := range projects {
				sec := Section{ID: "proj-" + p.ID, Title: p.Code + " · " + p.Title}
				lv := "info"
				if p.Stale {
					lv = "warn"
				}
				sub := ""
				if p.Tracker.Total > 0 {
					sub = fmt.Sprintf("%d of %d tracked items done", p.Tracker.Done, p.Tracker.Total)
				}
				sec.Items = append(sec.Items, Statement{Anchor: "proj:" + p.ID, Text: clip(plain(p.Summary), 320), Sub: sub, Level: lv})
				for _, sg := range p.Suggestions {
					s := Statement{Anchor: sg.ID, Text: sg.Title, Sub: clip(plain(sg.Why), 160), Tag: sg.Size, Level: "info"}
					if cf != nil && cf.Items[sg.ID] != nil {
						s.Status, s.Due = cf.Items[sg.ID].Status, cf.Items[sg.ID].Due
					}
					sec.Items = append(sec.Items, s)
				}
				secs = append(secs, sec)
			}
		}
	}
	return secs
}

func (a *App) artifactSections() []Section {
	artifactMu.Lock()
	defer artifactMu.Unlock()
	if artifactCache == nil || time.Since(artifactAt) > 20*time.Second {
		artifactCache, artifactAt = a.buildArtifactSections(), time.Now()
	}
	return artifactCache
}

func (a *App) statementText(anchor string) string {
	for _, sec := range a.artifactSections() {
		for _, it := range sec.Items {
			if it.Anchor == anchor {
				return it.Text
			}
		}
	}
	if strings.HasPrefix(anchor, "item:") {
		want := strings.TrimPrefix(anchor, "item:")
		for _, r := range loadBuildRows() {
			if r.ID == want {
				return r.ID + ": " + plain(r.Item)
			}
		}
	}
	id := ""
	if strings.HasPrefix(anchor, "briefing:") {
		id = strings.TrimPrefix(anchor, "briefing:")
	} else if strings.HasPrefix(anchor, "proj:") {
		id = strings.TrimPrefix(anchor, "proj:")
	} else if strings.HasPrefix(anchor, "doc:") {
		id = strings.TrimPrefix(anchor, "doc:")
	}
	if id != "" && a.store != nil {
		if b, found, _ := a.store.Briefing(id); found {
			return b.Title + " — " + cleanText(b.Summary, 200)
		}
	}
	if p, s, ok := a.suggestionByAnchor(anchor); ok {
		return p.Title + " · " + s.Title + ": " + s.Why
	}
	return ""
}

func (a *App) threadsByAnchor() (map[string][]ThreadMsg, int) {
	all := a.readThreads()
	by := map[string][]ThreadMsg{}
	for _, m := range all {
		by[m.Anchor] = append(by[m.Anchor], m)
	}
	for k, v := range by {
		if len(v) > threadPerAnchor {
			by[k] = v[len(v)-threadPerAnchor:]
		}
	}
	return by, len(all)
}

func pendingList() []map[string]string {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	out := []map[string]string{}
	for k, t := range pendingAsk {
		if time.Since(t) > 3*time.Minute {
			delete(pendingAsk, k)
			continue
		}
		anchor, who, _ := strings.Cut(k, "|")
		out = append(out, map[string]string{"anchor": anchor, "who": who})
	}
	return out
}

// GET /api/artifact
func (a *App) handleArtifact(w http.ResponseWriter, r *http.Request) {
	a.initThreadSeq()
	threads, n := a.threadsByAnchor()
	secs := a.artifactSections()
	pending := 0
	for _, s := range secs {
		pending += len(s.Items)
	}
	writeJSON(w, map[string]any{
		"ok": true, "generated_at": time.Now(), "sections": secs, "threads": threads,
		"seq": threadSeq.Load(), "messages": n, "pending": pendingList(), "statements": pending, "jobs": a.jobsByAnchor(),
	})
}

// POST /api/threads/post {"anchor":"item:MESH-02","text":"...","quote":"...","project":"SW","title":"..."}
func (a *App) handleThreadPost(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	if c.IsAgent() || c.BadToken {
		writeErr(w, http.StatusForbidden, "Only a person can post here. Dr. Boltz answers through Ask Boltz.")
		return
	}
	var req struct{ Anchor, Text, Quote, Project, Title string }
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Text) == "" || !anchorPattern.MatchString(req.Anchor) {
		writeErr(w, http.StatusBadRequest, `Send {"anchor": "item:MESH-02", "text": "..."}.`)
		return
	}
	text := strings.TrimSpace(req.Text)
	if len(text) > threadMaxText {
		text = text[:threadMaxText]
	}
	by := c.Label
	if by == "" {
		by = "you"
	}
	quote := clip(req.Quote, threadMaxQuote)
	if quote == "" {
		quote = clip(a.statementText(req.Anchor), threadMaxQuote)
	}
	change := ""
	if sgIDPattern.MatchString(req.Anchor) { // suggestions keep their schedule words: "done", "tomorrow 3pm"
		_, change, _, _ = a.applyPlanComment(req.Anchor, by, text, req.Project, req.Title)
	}
	m, err := a.appendThread(ThreadMsg{Anchor: req.Anchor, By: by, Text: text, Quote: quote})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	artifactMu.Lock()
	artifactAt = time.Time{} // a schedule change shows on the next read
	artifactMu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "message": m, "change": change, "seq": threadSeq.Load()})
}

// POST /api/threads/ask {"anchor":"item:MESH-02","who":"lucille"|"boltz"|"claude"|"auto"}: returns at once; the reply lands in the thread.
func (a *App) handleThreadAsk(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	if c.IsAgent() || c.BadToken {
		writeErr(w, http.StatusForbidden, "Only a person can ask.")
		return
	}
	var req struct{ Anchor, Who string }
	if err := readJSON(r, &req); err != nil || !anchorPattern.MatchString(req.Anchor) {
		writeErr(w, http.StatusBadRequest, `Send {"anchor": "item:MESH-02", "who": "lucille", "boltz", "claude" or "auto"}.`)
		return
	}
	who := strings.ToLower(strings.TrimSpace(req.Who))
	if who != "lucille" && who != "boltz" && who != "claude" && who != "auto" {
		writeErr(w, http.StatusBadRequest, `Send {"anchor": "item:MESH-02", "who": "lucille", "boltz", "claude" or "auto"}.`)
		return
	}
	key := req.Anchor + "|" + who
	pendingMu.Lock()
	if t, busy := pendingAsk[key]; busy && time.Since(t) < 3*time.Minute {
		pendingMu.Unlock()
		writeJSON(w, map[string]any{"ok": true, "already": true})
		return
	}
	pendingAsk[key] = time.Now()
	pendingMu.Unlock()
	go a.answerThread(req.Anchor, who, key)
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) answerThread(anchor, who, key string) {
	defer func() {
		pendingMu.Lock()
		delete(pendingAsk, key)
		pendingMu.Unlock()
		threadSeq.Add(1) // wake the streams: the "thinking" marker is gone
	}()
	var msgs []ThreadMsg
	quote := ""
	for _, m := range a.readThreads() {
		if m.Anchor == anchor {
			msgs = append(msgs, m)
			if m.Quote != "" {
				quote = m.Quote
			}
		}
	}
	if quote == "" {
		quote = a.statementText(anchor)
	}
	if len(msgs) > threadPerAnchor {
		msgs = msgs[len(msgs)-threadPerAnchor:]
	}
	var tr strings.Builder
	for _, m := range msgs {
		if m.Err {
			continue
		}
		name := m.By
		switch name {
		case "lucille":
			name = "Lucille"
		case "boltz":
			name = "Dr. Boltz"
		case "claude":
			name = "Claude"
		case "you":
			name = "The user"
		}
		fmt.Fprintf(&tr, "%s: %s\n", name, m.Text)
	}
	me := map[string]string{"lucille": "Lucille", "boltz": "Dr. Boltz", "claude": "Claude", "auto": "Claude"}[who]
	note := fmt.Sprintf(`You are %s, answering inside a comment thread on one statement of the team's shared Brief.
The statement: "%s" (anchor %s).
Who is in this thread: the user, Lucille (assistant, coordinator, therapist), Dr. Boltz (knowledge base: repairs, evals, recovery, audits, hardware) and Claude / Antigravity (coders). They talk here from any device.
Rules: answer what was just said, in one to four plain sentences, no markdown. Speak only as %s; never write other people's lines. Do not repeat the question back. Neither Lucille nor Dr. Boltz can run commands or touch files from this thread, so never claim you did or are doing something; say what you think, what you would check, or the exact command or step for the user or a coding AI to take. If it is not yours, say whose it is.`, me, clip(quote, 300), anchor, me)
	msg := fmt.Sprintf("The thread so far:\n%s\nNow write %s's next reply.", strings.TrimSpace(tr.String()), me)
	if tr.Len() == 0 {
		msg = fmt.Sprintf("Nobody has written yet. Write %s's first remark on the statement.", me)
	}

	var text, model string
	var err error
	if who == "lucille" {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		defer cancel()
		text, model, _, err = a.generateChatReply(ctx, ChatRequest{Message: msg, system: a.systemPrompt() + "\n\n" + note, maxTokens: 400})
	} else if who == "boltz" {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		text, model, _, err = a.generateI30ltzReply(ctx, ChatRequest{Message: "[" + note + "]\n\n" + msg})
	} else { // "claude" or "auto"
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		defer cancel()
		coderNote := fmt.Sprintf(`You are Claude, the technical specialist and coder for the user's SWARM project team.
Statement: "%s" (anchor %s).
Who is in this thread: the user, Lucille (assistant, coordinator, therapist) and Dr. Boltz (knowledge base).
The user asked you to review or answer this statement.
Provide a concise, practical, direct technical response (1-3 paragraphs) on how to handle it, what to check, and next actions. Speak as Claude. No fluff.`, clip(quote, 300), anchor)
		if a.anthropicKey() != "" {
			text, model, err = a.callClaude(ctx, ChatRequest{Message: msg, system: coderNote, maxTokens: 600})
		}
		if err != nil || strings.TrimSpace(text) == "" {
			text, model, _, err = a.generateChatReply(ctx, ChatRequest{Message: msg, system: coderNote, maxTokens: 600})
		}
		if who == "auto" {
			who = "claude"
		}
	}
	kind := ""
	if who == "claude" || who == "auto" {
		kind = "coder"
	}
	out := ThreadMsg{Anchor: anchor, By: who, Quote: clip(quote, threadMaxQuote), Model: model, Kind: kind}
	if err != nil || strings.TrimSpace(text) == "" {
		out.Err, out.Text = true, "I couldn't answer just now. Ask again in a minute."
		if err != nil {
			log.Printf("threads: %s reply for %s: %v", who, anchor, err)
		}
	} else {
		out.Text = clip(stripThink(text), threadMaxText)
	}
	if _, err := a.appendThread(out); err != nil {
		log.Printf("threads: append reply: %v", err)
	}
}

// resolveCoder picks the target coder tool: Claude if available & under capacity, else Antigravity (agy).
func (a *App) resolveCoder(to string) string {
	t := strings.ToLower(strings.TrimSpace(to))
	if t != "" && t != "auto" && t != "next" {
		return t
	}
	// Prefer Claude if not exceeding maxClaudeWindows limit
	if countLive("claude") < maxClaudeWindows {
		if _, err := exec.LookPath("claude"); err == nil {
			return "claude"
		}
		home, _ := os.UserHomeDir()
		if _, err := os.Stat(filepath.Join(home, ".local/bin/claude")); err == nil {
			return "claude"
		}
	}
	// Fall back to Antigravity (agy)
	if _, err := exec.LookPath("agy"); err == nil {
		return "antigravity"
	}
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, ".local/bin/agy")); err == nil {
		return "antigravity"
	}
	return "claude"
}

// POST /api/threads/deal {"anchor": "item:MESH-02", "text": "...", "quote": "...", "to": "auto"|"claude"|"antigravity", "launch": true}
// The user comments/answers on a briefing statement and Lucille immediately has Claude or the next available model deal with it.
func (a *App) handleThreadDeal(w http.ResponseWriter, r *http.Request) {
	c, ok := requireHuman(w, r)
	if !ok {
		return
	}
	var req struct {
		Anchor, Text, Quote, To string
		Launch                  *bool
	}
	if err := readJSON(r, &req); err != nil || !anchorPattern.MatchString(req.Anchor) {
		writeErr(w, http.StatusBadRequest, `Send {"anchor": "item:MESH-02", "text": "..."}.`)
		return
	}
	by := c.Name()
	text := strings.TrimSpace(req.Text)
	quote := clip(req.Quote, threadMaxQuote)
	if quote == "" {
		quote = clip(a.statementText(req.Anchor), threadMaxQuote)
	}

	var m ThreadMsg
	var err error
	if text != "" {
		if len(text) > threadMaxText {
			text = text[:threadMaxText]
		}
		if sgIDPattern.MatchString(req.Anchor) {
			_, _, _, _ = a.applyPlanComment(req.Anchor, by, text, "", "")
		}
		m, err = a.appendThread(ThreadMsg{Anchor: req.Anchor, By: by, Text: text, Quote: quote})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	target := a.resolveCoder(req.To)
	code, dir := a.projectDirFor(req.Anchor)
	prompt := a.draftPrompt(req.Anchor, target)
	id := "j" + strings.TrimPrefix(newThreadID(), "t")
	j := Job{
		ID:      id,
		To:      target,
		Anchor:  req.Anchor,
		Quote:   quote,
		Prompt:  strings.ReplaceAll(redact(prompt), "{{JOB_ID}}", id),
		Status:  "queued",
		At:      commentNow().Format(time.RFC3339Nano),
		By:      by,
	}

	doLaunch := req.Launch == nil || *req.Launch
	launchNow := doLaunch && launchTool(target) != "" && launchEnabled() && dir != ""

	say := fmt.Sprintf("Got it babe. I've queued this for %s to deal with (project %s). Waiting for a session to pick it up.", target, code)
	if launchNow {
		say = fmt.Sprintf("Got it babe. I've told %s to deal with this now in %s. Starting the session on the PC...", target, dir)
	}

	if err := a.writeJob(j); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_, _ = a.appendThread(ThreadMsg{Anchor: j.Anchor, By: "lucille", Text: say, Quote: j.Quote, Kind: "job"})
	if launchNow {
		a.enqueueLaunch(j.ID)
	}

	artifactMu.Lock()
	artifactAt = time.Time{}
	artifactMu.Unlock()

	writeJSON(w, map[string]any{
		"ok":       true,
		"message":  m,
		"job":      j,
		"target":   target,
		"launched": launchNow,
		"seq":      threadSeq.Load(),
	})
}

var (
	thinkBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)
	nameTag    = regexp.MustCompile(`^\s*\[[A-Za-z0-9 .:_-]{1,24}\]\s*`) // "[DR.BOLTZ] " the coder model puts in front
	mdMarks    = strings.NewReplacer("**", "", "`", "", "__", "")
)

func stripThink(s string) string {
	return strings.TrimSpace(nameTag.ReplaceAllString(thinkBlock.ReplaceAllString(s, ""), ""))
}

// plain: build-list text is markdown; the Brief shows it as plain words.
func plain(s string) string { return mdMarks.Replace(s) }

// GET /api/threads/stream: a tiny "something changed" signal; the page then re-reads /api/artifact.
func (a *App) handleThreadStream(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireKnown(w, r); !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming is not supported here")
		return
	}
	a.initThreadSeq()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	last := int64(-1)
	tick := time.NewTicker(700 * time.Millisecond)
	ping := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	defer ping.Stop()
	for {
		if s := threadSeq.Load(); s != last {
			last = s
			if _, err := fmt.Fprintf(w, "event: threads\ndata: {\"seq\":%d}\n\n", s); err != nil {
				return
			}
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-tick.C:
		}
	}
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func (a *App) jobsByAnchor() map[string][]Job {
	out := map[string][]Job{}
	for _, j := range a.readJobs() { // newest first
		if len(out[j.Anchor]) < 5 && j.Status != "cancelled" {
			out[j.Anchor] = append(out[j.Anchor], j)
		}
	}
	return out
}
