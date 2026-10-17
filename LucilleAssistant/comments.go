package main

// Comments on Lucille's project-brief suggestions, and the schedule they drive (user request 2026-10-14:
// "a comment box beside her suggestions, and a schedule that updates as needed from what I write").
//
// Each suggestion keeps its stable id from projectbrief.go (sg-xxxxxxxx). A comment is stored as written and
// is also read for plain scheduling words — "done", "skip", "later", "tomorrow", "friday 3pm", "in 2 days",
// "undo" — which move the suggestion on the schedule. That reading is deterministic (no model call per
// comment), so the same words always do the same thing, and the reply says exactly what changed.
//
// Stored at <data_dir>/comments.json (0600). People only: Dr. Boltz's agent token cannot comment.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	planOpen    = "open"
	planDone    = "done"
	planSkipped = "skipped"
	planLater   = "later"

	commentMaxText  = 1000
	commentMaxTitle = 200
	commentsPerItem = 50
	dueLayout       = "2006-01-02T15:04"
)

var commentNow = time.Now // tests pin the clock

var (
	commentsMu  sync.Mutex
	sgIDPattern = regexp.MustCompile(`^sg-[0-9a-f]{8}$`)
)

type SuggestionComment struct {
	At   string `json:"at"`
	By   string `json:"by"`
	Text string `json:"text"`
}

type PlanItem struct {
	ID       string              `json:"id"`
	Project  string              `json:"project"`
	Title    string              `json:"title"`
	Status   string              `json:"status"`        // open | done | skipped | later
	Due      string              `json:"due,omitempty"` // local time, 2006-01-02T15:04
	Updated  string              `json:"updated"`
	Comments []SuggestionComment `json:"comments"`
}

type commentsFile struct {
	Items map[string]*PlanItem `json:"items"`
}

func (a *App) commentsPath() string { return filepath.Join(a.cfg.DataDir, "comments.json") }

func (a *App) readComments() (*commentsFile, error) {
	cf := &commentsFile{Items: map[string]*PlanItem{}}
	b, err := os.ReadFile(a.commentsPath())
	if os.IsNotExist(err) {
		return cf, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, cf); err != nil {
		return nil, fmt.Errorf("comments.json is damaged: %v", err)
	}
	if cf.Items == nil {
		cf.Items = map[string]*PlanItem{}
	}
	return cf, nil
}

func (a *App) writeComments(cf *commentsFile) error {
	if err := os.MkdirAll(a.cfg.DataDir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.commentsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.commentsPath()) // atomic: a reader never sees half a file
}

// ── reading scheduling words ─────────────────────────────────────

var (
	reDone    = regexp.MustCompile(`\b(done|did it|finished|completed|fixed|shipped)\b`)
	reNotDone = regexp.MustCompile(`\b(not|isn'?t|wasn'?t|never) (yet |quite |really )?(done|finished|completed|fixed)\b`)
	reSkip    = regexp.MustCompile(`\b(skip|skipped|drop it|not needed|no thanks|don'?t want|cancel)\b`)
	reLater   = regexp.MustCompile(`\b(later|not now|someday|backlog|park it)\b`)
	reReopen  = regexp.MustCompile(`\b(undo|reopen|back on)\b`)
	reInDays  = regexp.MustCompile(`\bin (\d{1,2}) days?\b`)
	reInHours = regexp.MustCompile(`\bin (\d{1,2}) hours?\b`)
	reAmPm    = regexp.MustCompile(`\b(\d{1,2})(?::([0-5]\d))?\s*(am|pm)\b`)
	reClock   = regexp.MustCompile(`\b([01]?\d|2[0-3]):([0-5]\d)\b`)
	reAt      = regexp.MustCompile(`\bat (\d{1,2})\b`)
	weekdays  = map[string]time.Weekday{"sunday": time.Sunday, "sun": time.Sunday, "monday": time.Monday, "mon": time.Monday,
		"tuesday": time.Tuesday, "tue": time.Tuesday, "tues": time.Tuesday, "wednesday": time.Wednesday, "wed": time.Wednesday,
		"thursday": time.Thursday, "thu": time.Thursday, "thurs": time.Thursday, "friday": time.Friday, "fri": time.Friday,
		"saturday": time.Saturday, "sat": time.Saturday}
	reWeekday = regexp.MustCompile(`\b(sunday|sun|monday|mon|tuesday|tues|tue|wednesday|wed|thursday|thurs|thu|friday|fri|saturday|sat)\b`)
)

func min23(h int) int {
	if h > 23 {
		return 23
	}
	return h
}

func atDay(t time.Time, hour, min int) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), hour, min, 0, 0, t.Location())
}

// readPlan works out what a comment asks for. It returns the new status and due ("" = no date) and a short
// plain-words description of the change ("" when the comment is only a note).
func readPlan(text string, now time.Time, cur PlanItem) (status, due, change string) {
	status, due = cur.Status, cur.Due
	if status == "" {
		status = planOpen
	}
	l := " " + strings.ToLower(text) + " "

	switch {
	case reDone.MatchString(l) && !reNotDone.MatchString(l):
		return planDone, "", "marked done"
	case reSkip.MatchString(l):
		return planSkipped, "", "skipped"
	}

	// a date and/or a time
	var day time.Time
	hasDay, hasTime := false, false
	hour, min := 9, 0
	switch {
	case strings.Contains(l, "tomorrow"):
		day, hasDay = now.AddDate(0, 0, 1), true
	case strings.Contains(l, "tonight"):
		day, hasDay, hour, hasTime = now, true, 20, true
	case strings.Contains(l, "today"):
		day, hasDay = now, true
	case strings.Contains(l, "next week"):
		d := now.AddDate(0, 0, 1)
		for d.Weekday() != time.Monday {
			d = d.AddDate(0, 0, 1)
		}
		day, hasDay = d, true
	}
	if m := reInDays.FindStringSubmatch(l); m != nil {
		n, _ := strconv.Atoi(m[1])
		day, hasDay = now.AddDate(0, 0, n), true
	}
	if m := reWeekday.FindStringSubmatch(l); m != nil && !hasDay {
		want := weekdays[m[1]]
		d := now
		for d.Weekday() != want {
			d = d.AddDate(0, 0, 1)
		}
		day, hasDay = d, true
	}
	if m := reAmPm.FindStringSubmatch(l); m != nil {
		h, _ := strconv.Atoi(m[1])
		mm, _ := strconv.Atoi(m[2])
		if h >= 1 && h <= 12 {
			h %= 12
			if m[3] == "pm" {
				h += 12
			}
			hour, min, hasTime = h, mm, true
		}
	} else if m := reClock.FindStringSubmatch(l); m != nil {
		hour, _ = strconv.Atoi(m[1])
		min, _ = strconv.Atoi(m[2])
		hasTime = true
	} else if m := reAt.FindStringSubmatch(l); m != nil {
		h, _ := strconv.Atoi(m[1])
		if h >= 1 && h <= 12 {
			if h < 8 { // "at 3" means the afternoon; "at 9" the morning
				h += 12
			}
			hour, hasTime = h, true
		}
	} else if strings.Contains(l, "noon") {
		hour, hasTime = 12, true
	} else if strings.Contains(l, "morning") {
		hour, hasTime = 9, true
	} else if strings.Contains(l, "afternoon") {
		hour, hasTime = 14, true
	} else if strings.Contains(l, "evening") {
		hour, hasTime = 19, true
	}
	if m := reInHours.FindStringSubmatch(l); m != nil {
		n, _ := strconv.Atoi(m[1])
		t := now.Add(time.Duration(n) * time.Hour)
		day, hasDay, hour, min, hasTime = t, true, t.Hour(), t.Minute(), true
	}

	if hasDay || hasTime {
		if !hasDay {
			day = now
			if !atDay(now, hour, min).After(now) {
				day = now.AddDate(0, 0, 1) // that time has passed today
			}
		}
		if !hasTime && cur.Due != "" {
			if old, err := time.ParseInLocation(dueLayout, cur.Due, now.Location()); err == nil {
				hour, min = old.Hour(), old.Minute() // keep the time they already had
			}
		} else if !hasTime && day.YearDay() == now.YearDay() && day.Year() == now.Year() {
			next := now.Add(time.Hour).Truncate(time.Hour) // "today" with no time: the next full hour
			hour, min = min23(next.Hour()), 0
			if next.Day() != now.Day() {
				hour = 23
			}
		}
		when := atDay(day, hour, min)
		return planOpen, when.Format(dueLayout), "scheduled for " + when.Format("Mon Jan 2, 3:04 PM")
	}

	switch {
	case reLater.MatchString(l):
		return planLater, "", "moved to later (no date)"
	case reReopen.MatchString(l):
		return planOpen, due, "back on the list"
	}
	return status, due, ""
}

// schedule: dated open items by time, then undated open ones, then "later". Done and skipped drop off.
func scheduleOf(cf *commentsFile) []*PlanItem {
	var dated, undated, later []*PlanItem
	for _, it := range cf.Items {
		switch {
		case it.Status == planOpen && it.Due != "":
			dated = append(dated, it)
		case it.Status == planOpen:
			undated = append(undated, it)
		case it.Status == planLater:
			later = append(later, it)
		}
	}
	sort.Slice(dated, func(i, j int) bool { return dated[i].Due < dated[j].Due })
	byTitle := func(s []*PlanItem) {
		sort.Slice(s, func(i, j int) bool { return s[i].Project+s[i].Title < s[j].Project+s[j].Title })
	}
	byTitle(undated)
	byTitle(later)
	return append(append(dated, undated...), later...)
}

// ── HTTP ─────────────────────────────────────────────────────────

// GET /api/comments: every suggestion that has comments, plus the schedule.
func (a *App) handleComments(w http.ResponseWriter, r *http.Request) {
	commentsMu.Lock()
	cf, err := a.readComments()
	commentsMu.Unlock()
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "items": cf.Items, "schedule": scheduleOf(cf)})
}

// POST /api/comments/{id} {"text": "...", "project": "SW", "title": "..."}
func (a *App) handleCommentAdd(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	if c.IsAgent() || c.BadToken {
		writeErr(w, http.StatusForbidden, "Only a person can comment on Lucille's suggestions.")
		return
	}
	id := r.PathValue("id")
	if !sgIDPattern.MatchString(id) {
		writeErr(w, http.StatusBadRequest, "Unknown suggestion id (expected sg- and 8 hex digits).")
		return
	}
	var req struct{ Text, Project, Title string }
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Text) == "" {
		writeErr(w, http.StatusBadRequest, `Send {"text": "tomorrow 3pm", "project": "SW", "title": "..."}.`)
		return
	}
	text := strings.TrimSpace(req.Text)
	by := c.Label
	if by == "" {
		by = "PC"
	}
	it, change, schedule, err := a.applyPlanComment(id, by, text, req.Project, req.Title)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "item": it, "change": change, "schedule": schedule})
}

// applyPlanComment stores one comment on a suggestion and applies any scheduling words in it. Shared by
// POST /api/comments/{id} and the team Brief's threads (threads.go), so both read the same words the same way.
func (a *App) applyPlanComment(id, by, text, project, title string) (*PlanItem, string, []*PlanItem, error) {
	if len(text) > commentMaxText {
		text = text[:commentMaxText]
	}
	now := commentNow()

	commentsMu.Lock()
	defer commentsMu.Unlock()
	cf, err := a.readComments()
	if err != nil {
		return nil, "", nil, err
	}
	it := cf.Items[id]
	if it == nil {
		it = &PlanItem{ID: id, Status: planOpen}
		cf.Items[id] = it
	}
	if t := strings.TrimSpace(title); t != "" {
		if len(t) > commentMaxTitle {
			t = t[:commentMaxTitle]
		}
		it.Title = t
	}
	if p := strings.TrimSpace(project); p != "" && len(p) <= 8 {
		it.Project = p
	}
	status, due, change := readPlan(text, now, *it)
	it.Status, it.Due = status, due
	it.Updated = now.Format(time.RFC3339)
	it.Comments = append(it.Comments, SuggestionComment{At: now.Format(time.RFC3339), By: by, Text: text})
	if len(it.Comments) > commentsPerItem {
		it.Comments = it.Comments[len(it.Comments)-commentsPerItem:]
	}
	if err := a.writeComments(cf); err != nil {
		return nil, "", nil, err
	}
	if change == "" {
		change = "noted"
	}
	return it, change, scheduleOf(cf), nil
}
