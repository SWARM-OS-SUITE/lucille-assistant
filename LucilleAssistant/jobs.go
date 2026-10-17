package main

// Lucille prompts the coders (user 2026-10-14: "I want Lucille to directly talk to Claude or other and prompt them;
// Dr. Boltz is the knowledge base; Claude, ChatGPT and Antigravity do the coding").
//
// From any statement's thread the user presses "Prompt Claude / Antigravity / ChatGPT". Lucille drafts a complete,
// self-contained prompt (the statement, the thread, where the code lives, how to claim work and report back); the user
// can edit it and approves by sending. The job then waits in that AI's mailbox:
//   claude / antigravity   a running session picks it up through /api/swarm/next (queued_prompts) and answers with
//                          POST /api/jobs/{id}/done; the answer appears in the thread.
//   chatgpt                has no way to call this PC, so its prompt is copied from the page and pasted by the user.
// Nothing here starts a process or edits a file: a person approves every prompt, and the AIs act in their own windows
// under their own permissions. Jobs live in append-only jobs.jsonl (last line per id wins).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Job struct {
	ID      string `json:"id"`
	To      string `json:"to"` // claude | antigravity | chatgpt | an exact AI name
	Anchor  string `json:"anchor"`
	Quote   string `json:"quote,omitempty"`
	Prompt  string `json:"prompt"`
	Status  string `json:"status"` // queued | claimed | done | manual | cancelled
	At      string `json:"at"`
	By      string `json:"by,omitempty"` // who the user is, when created
	Claimed string `json:"claimed_by,omitempty"`
	Result  string `json:"result,omitempty"`
}

var (
	jobsMu    sync.Mutex
	jobToPat  = regexp.MustCompile(`^[A-Za-z0-9_-]{2,40}$`)
	jobIDPat  = regexp.MustCompile(`^j[0-9a-z]{8,40}$`)
	projDirOf = map[string]string{"SW": "swarm-os", "RC": "recovery-os", "CP": "i30ltz-companion", "LA": "lucille-assistant"}
)

func (a *App) jobsPath() string { return filepath.Join(a.cfg.DataDir, "jobs.jsonl") }

func (a *App) readJobs() []Job {
	b, err := os.ReadFile(a.jobsPath())
	if err != nil {
		return nil
	}
	last := map[string]Job{}
	var order []string
	for _, ln := range strings.Split(string(b), "\n") {
		var j Job
		if strings.TrimSpace(ln) == "" || json.Unmarshal([]byte(ln), &j) != nil || j.ID == "" {
			continue
		}
		if _, seen := last[j.ID]; !seen {
			order = append(order, j.ID)
		}
		last[j.ID] = j
	}
	out := make([]Job, 0, len(order))
	for _, id := range order {
		out = append(out, last[id])
	}
	sort.SliceStable(out, func(i, k int) bool { return out[i].At > out[k].At })
	return out
}

func (a *App) writeJob(j Job) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	jobsMu.Lock()
	defer jobsMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(a.jobsPath()), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(a.jobsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	if err == nil {
		a.initThreadSeq()
		threadSeq.Add(1) // wake the live streams: a job changed
	}
	return err
}

// aiMatches: does a session called ai belong in the mailbox "to"?
func aiMatches(to, ai string) bool {
	to, ai = strings.ToLower(to), strings.ToLower(ai)
	switch to {
	case "claude":
		return strings.Contains(ai, "claude")
	case "antigravity":
		return strings.Contains(ai, "antigravity") || strings.Contains(ai, "agy") || strings.Contains(ai, "gemini")
	}
	return to == ai
}

func (a *App) queuedFor(ai string) []Job {
	var out []Job
	for _, j := range a.readJobs() {
		if j.Status == "queued" && aiMatches(j.To, ai) {
			out = append(out, j)
		}
	}
	return out
}

func (a *App) projectDirFor(anchor string) (code, dir string) {
	id := ""
	switch {
	case strings.HasPrefix(anchor, "proj:"):
		id = strings.TrimPrefix(anchor, "proj:")
	case strings.HasPrefix(anchor, "briefing:"):
		id = strings.TrimPrefix(anchor, "briefing:")
	case strings.HasPrefix(anchor, "doc:"):
		id = strings.TrimPrefix(anchor, "doc:")
	case strings.HasPrefix(anchor, "item:"):
		want := strings.TrimPrefix(anchor, "item:")
		for _, r := range loadBuildRows() {
			if r.ID == want {
				code = r.Project
				id = projDirOf[r.Project]
			}
		}
	case strings.HasPrefix(anchor, "sg-"):
		// A brief suggestion: it runs in its own project's directory ("Approve and start", user 2026-10-15).
		if p, _, ok := a.suggestionByAnchor(anchor); ok {
			code, id = p.Code, p.ID
		}
	}
	if id == "" {
		return code, ""
	}
	if code == "" {
		for c, proj := range projDirOf {
			if proj == id {
				code = c
				break
			}
		}
	}
	// liveFor: the spelling that exists on this boot (C is mounted at /mnt/lucilleassistant or
	// /mnt/lucillebuild), or the launcher's chdir fails.
	return code, a.cfg.Brief.liveFor(a.cfg.Brief.dirFor(id))
}

// suggestionByAnchor finds a brief suggestion by its anchor ("sg-…") and the project it belongs to.
func (a *App) suggestionByAnchor(anchor string) (BriefProject, BriefSuggestion, bool) {
	if a.store == nil || !strings.HasPrefix(anchor, "sg-") {
		return BriefProject{}, BriefSuggestion{}, false
	}
	projects, _ := a.buildBrief(time.Now())["projects"].([]BriefProject)
	for _, p := range projects {
		for _, s := range p.Suggestions {
			if s.ID == anchor {
				return p, s, true
			}
		}
	}
	return BriefProject{}, BriefSuggestion{}, false
}

func (a *App) draftPrompt(anchor, to string) string {
	quote := clip(a.statementText(anchor), threadMaxQuote)
	var said []string
	for _, m := range a.readThreads() {
		if m.Anchor != anchor || m.Err {
			continue
		}
		if quote == "" && m.Quote != "" {
			quote = m.Quote
		}
		said = append(said, fmt.Sprintf("%s: %s", m.By, clip(m.Text, 500)))
	}
	report := `When you stop, report back so it appears in the thread: curl -s -X POST http://127.0.0.1:8403/api/jobs/{{JOB_ID}}/done -H 'Content-Type: application/json' -d '{"ai": "<your-name>", "summary": "what you did, what is left, anything the user must decide"}'`
	if p, sg, ok := a.suggestionByAnchor(anchor); ok {
		fp := a.suggestionPrompt(p.ID, p.Title, sg, false)
		fp.Said = said
		fp.Rules = append(append([]string{}, baseRules...),
			`For repair, audit, recovery or hardware questions, ask Dr. Boltz instead of guessing: POST http://127.0.0.1:8403/api/chat/i30ltz with {"message": "..."}.`)
		fp.Report = report
		return fp.Text()
	}
	code, dir := a.projectDirFor(anchor)
	return formalPrompt{
		Who:     "You are being handed a job by Lucille, the coordinator of the user's AI swarm, on the user's behalf. You are the coder; Dr. Boltz is the swarm's knowledge base for repairs, evals, recovery, audits and hardware.",
		Job:     fmt.Sprintf("From the team Brief, %s:\n%s", anchor, quote),
		Said:    said,
		Project: code,
		Dir:     dir,
		Rules: append(append([]string{}, baseRules...),
			`For repair, audit, recovery or hardware questions, ask Dr. Boltz instead of guessing: POST http://127.0.0.1:8403/api/chat/i30ltz with {"message": "..."}.`,
			"Mark finished rows DONE in place in the master build list."),
		DoneWhen: "The job above works and is checked, or you are blocked on something only the user can decide.",
		Report:   report,
	}.Text()
}

// POST /api/jobs/draft {"anchor": "...", "to": "claude"}
func (a *App) handleJobDraft(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireHuman(w, r); !ok {
		return
	}
	var req struct{ Anchor, To string }
	if err := readJSON(r, &req); err != nil || !anchorPattern.MatchString(req.Anchor) || !jobToPat.MatchString(req.To) {
		writeErr(w, http.StatusBadRequest, `Send {"anchor": "item:MESH-02", "to": "claude"}.`)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "to": req.To, "prompt": a.draftPrompt(req.Anchor, req.To)})
}

// POST /api/jobs {"anchor": "...", "to": "claude", "prompt": "..."}: the user's approval of a prompt.
func (a *App) handleJobCreate(w http.ResponseWriter, r *http.Request) {
	c, ok := requireHuman(w, r)
	if !ok {
		return
	}
	var req struct {
		Anchor, To, Prompt string
		Launch             bool // "Approve and start it now": Lucille starts the coder herself (launcher.go)
	}
	if err := readJSON(r, &req); err != nil || !anchorPattern.MatchString(req.Anchor) || !jobToPat.MatchString(req.To) || strings.TrimSpace(req.Prompt) == "" {
		writeErr(w, http.StatusBadRequest, `Send {"anchor": "item:MESH-02", "to": "claude", "prompt": "..."}.`)
		return
	}
	if len(req.Prompt) > 12000 {
		writeErr(w, http.StatusBadRequest, "That prompt is too long (12000 characters at most).")
		return
	}
	id := "j" + strings.TrimPrefix(newThreadID(), "t")
	j := Job{ID: id, To: strings.ToLower(req.To), Anchor: req.Anchor, Quote: clip(a.statementText(req.Anchor), threadMaxQuote),
		Prompt: strings.ReplaceAll(redact(req.Prompt), "{{JOB_ID}}", id), Status: "queued", At: commentNow().Format(time.RFC3339Nano), By: c.Name()}
	say := "Queued for " + j.To + ". A running " + j.To + " session picks it up when it asks me for work."
	if j.To == "chatgpt" {
		j.Status = "manual"
		say = "Prompt ready for ChatGPT. Copy it from here and paste it into ChatGPT; it cannot call this PC."
	}
	launch := req.Launch && launchTool(j.To) != ""
	if launch {
		if !launchEnabled() {
			writeErr(w, http.StatusConflict, "Starting coders myself is switched off (launch.disabled). Send it to the mailbox instead.")
			return
		}
		if _, dir := a.projectDirFor(j.Anchor); dir == "" {
			writeErr(w, http.StatusConflict, "I can't tell which project that belongs to, so I can't start it myself. Send it to the mailbox instead.")
			return
		}
		say = "Approved. I'm starting " + j.To + " myself, one job at a time."
	}
	if err := a.writeJob(j); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_, _ = a.appendThread(ThreadMsg{Anchor: j.Anchor, By: "lucille", Text: say, Quote: j.Quote, Kind: "job"})
	if launch {
		a.enqueueLaunch(j.ID)
	}
	writeJSON(w, map[string]any{"ok": true, "job": j})
}

// GET /api/jobs
func (a *App) handleJobs(w http.ResponseWriter, r *http.Request) {
	jobs := a.readJobs()
	if an := r.URL.Query().Get("anchor"); an != "" {
		var f []Job
		for _, j := range jobs {
			if j.Anchor == an {
				f = append(f, j)
			}
		}
		jobs = f
	}
	writeJSON(w, map[string]any{"ok": true, "jobs": jobs})
}

func (a *App) updateJob(id string, f func(*Job) error) (Job, int, error) {
	for _, j := range a.readJobs() {
		if j.ID == id {
			if err := f(&j); err != nil {
				return j, http.StatusConflict, err
			}
			if err := a.writeJob(j); err != nil {
				return j, http.StatusInternalServerError, err
			}
			return j, 0, nil
		}
	}
	return Job{}, http.StatusNotFound, fmt.Errorf("no such job")
}

// POST /api/jobs/{id}/claim {"ai": "dev-claude"}
func (a *App) handleJobClaim(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct{ AI string }
	if err := readJSON(r, &req); err != nil || cleanAIName(req.AI) == "" || !jobIDPat.MatchString(id) {
		writeErr(w, http.StatusBadRequest, `Send {"ai": "your-name"}.`)
		return
	}
	j, code, err := a.updateJob(id, func(j *Job) error {
		if j.Status != "queued" || !aiMatches(j.To, req.AI) {
			return fmt.Errorf("that job is not waiting for %s (status %s, addressed to %s)", req.AI, j.Status, j.To)
		}
		j.Status, j.Claimed = "claimed", req.AI
		return nil
	})
	if err != nil {
		writeErr(w, code, err.Error())
		return
	}
	_, _ = a.appendThread(ThreadMsg{Anchor: j.Anchor, By: req.AI, Kind: "coder", Text: "Picked this up.", Quote: j.Quote})
	writeJSON(w, map[string]any{"ok": true, "job": j})
}

// POST /api/jobs/{id}/done {"ai": "dev-claude", "summary": "..."}
func (a *App) handleJobDone(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct{ AI, Summary string }
	ai := ""
	if err := readJSON(r, &req); err == nil {
		ai = cleanAIName(req.AI)
	}
	if ai == "" || !jobIDPat.MatchString(id) || strings.TrimSpace(req.Summary) == "" || ai == "lucille" || ai == "boltz" || ai == "you" {
		writeErr(w, http.StatusBadRequest, `Send {"ai": "your-name", "summary": "what you did"}.`)
		return
	}
	j, code, err := a.updateJob(id, func(j *Job) error {
		if j.Status == "done" || j.Status == "cancelled" {
			return fmt.Errorf("that job is already %s", j.Status)
		}
		j.Status, j.Result = "done", clip(redact(req.Summary), threadMaxText)
		if j.Claimed == "" {
			j.Claimed = ai
		}
		return nil
	})
	if err != nil {
		writeErr(w, code, err.Error())
		return
	}
	_, _ = a.appendThread(ThreadMsg{Anchor: j.Anchor, By: ai, Kind: "coder", Text: j.Result, Quote: j.Quote})
	writeJSON(w, map[string]any{"ok": true, "job": j})
}

// POST /api/jobs/{id}/cancel: the user takes a queued prompt back.
func (a *App) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireHuman(w, r); !ok {
		return
	}
	j, code, err := a.updateJob(r.PathValue("id"), func(j *Job) error {
		if j.Status != "queued" && j.Status != "manual" && j.Status != "running" {
			return fmt.Errorf("only a waiting or running job can be taken back (this one is %s)", j.Status)
		}
		if j.Status == "running" {
			stopRunning(j.ID) // kills the launched coder's whole process group
		}
		j.Status = "cancelled"
		return nil
	})
	if err != nil {
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "job": j})
}
