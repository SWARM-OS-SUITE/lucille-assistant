package main

import (
	"embed"
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
	"time"
	"unicode/utf8"
)

// Briefings are named, editable project briefs: the "read this first" context
// the user keeps for Claude and other AIs, held inside Lucille so she can show
// it, hand it over and answer from it. Each one is a small JSON file under
// <data>/briefings/. The built-in set (seed/*.md) is compiled into the binary and
// written on first start, so the app needs nothing from any build tree.

//go:embed seed/*.md
var seedFS embed.FS

const (
	maxBriefings = 200
	maxTitle     = 120
	maxProject   = 40
	maxSummary   = 400
	maxBody      = 100_000
	maxReadFirst = 20
	maxPathLen   = 300
)

var briefingID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type Briefing struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Project   string    `json:"project"`
	Summary   string    `json:"summary"`
	Body      string    `json:"body"`
	ReadFirst []string  `json:"read_first"`
	Order     int       `json:"order"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
}

// BriefingInput is the editable part of a briefing, as sent by the UI.
type BriefingInput struct {
	Title     string   `json:"title"`
	Project   string   `json:"project"`
	Summary   string   `json:"summary"`
	Body      string   `json:"body"`
	ReadFirst []string `json:"read_first"`
}

// inputError marks a problem with what the user sent (HTTP 400, safe to show),
// as opposed to a disk failure (HTTP 500).
type inputError string

func (e inputError) Error() string { return string(e) }

func badInput(format string, args ...any) error { return inputError(fmt.Sprintf(format, args...)) }

// clean trims the input in place and returns a message the UI can show.
func (in *BriefingInput) clean() error {
	in.Title = strings.TrimSpace(in.Title)
	in.Project = strings.TrimSpace(in.Project)
	in.Summary = strings.TrimSpace(in.Summary)
	in.Body = strings.TrimSpace(strings.ReplaceAll(in.Body, "\r\n", "\n"))
	switch {
	case in.Title == "":
		return badInput("A briefing needs a title.")
	case utf8.RuneCountInString(in.Title) > maxTitle:
		return badInput("Title is too long (max %d characters).", maxTitle)
	case utf8.RuneCountInString(in.Project) > maxProject:
		return badInput("Project label is too long (max %d characters).", maxProject)
	case utf8.RuneCountInString(in.Summary) > maxSummary:
		return badInput("Summary is too long (max %d characters).", maxSummary)
	case len(in.Body) > maxBody:
		return badInput("Body is too long (max %d KB).", maxBody/1000)
	}
	paths := make([]string, 0, len(in.ReadFirst))
	for _, p := range in.ReadFirst {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) > maxReadFirst {
		return badInput("Too many read-first paths (max %d).", maxReadFirst)
	}
	for _, p := range paths {
		if utf8.RuneCountInString(p) > maxPathLen {
			return badInput("A read-first path is too long (max %d characters).", maxPathLen)
		}
	}
	in.ReadFirst = paths
	return nil
}

var errTooManyBriefings = badInput("You already have %d briefings; delete one first.", maxBriefings)

// slug turns a title into a file-safe id: lowercase letters, digits and dashes.
func slug(title string) string {
	var sb strings.Builder
	dash := true // swallow leading separators
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			sb.WriteRune(r)
			dash = false
		case !dash:
			sb.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(sb.String(), "-")
	if len(s) > 56 { // leave room for a "-NN" collision suffix inside the 63-char id limit
		s = strings.Trim(s[:56], "-")
	}
	if s == "" {
		s = "briefing"
	}
	return s
}

func (s *Store) briefingsDir() string { return filepath.Join(s.dir, "briefings") }

func (s *Store) briefingPath(id string) string {
	return filepath.Join(s.briefingsDir(), id+".json")
}

// listBriefings reads every briefing, ordered by their order field then title.
// Callers hold s.mu. A damaged file is logged and skipped so one bad edit never
// hides the rest.
func (s *Store) listBriefings() ([]Briefing, error) {
	entries, err := os.ReadDir(s.briefingsDir())
	if errors.Is(err, os.ErrNotExist) {
		return []Briefing{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Briefing{}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if e.IsDir() || !ok || !briefingID.MatchString(id) {
			continue
		}
		var b Briefing
		if err := s.readJSON(s.briefingPath(id), &b); err != nil {
			log.Printf("briefing %s: %v (skipped)", e.Name(), err)
			continue
		}
		b.ID = id // the file name is the id, whatever the contents say
		if b.ReadFirst == nil {
			b.ReadFirst = []string{}
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return out, nil
}

func (s *Store) Briefings() ([]Briefing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listBriefings()
}

// Briefing returns (briefing, found, error). A malformed id is simply not found.
func (s *Store) Briefing(id string) (Briefing, bool, error) {
	if !briefingID.MatchString(id) {
		return Briefing{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadBriefing(id)
}

func (s *Store) loadBriefing(id string) (Briefing, bool, error) {
	var b Briefing
	if _, err := os.Stat(s.briefingPath(id)); errors.Is(err, os.ErrNotExist) {
		return b, false, nil
	}
	if err := s.readJSON(s.briefingPath(id), &b); err != nil {
		return b, false, err
	}
	b.ID = id
	if b.ReadFirst == nil {
		b.ReadFirst = []string{}
	}
	return b, true, nil
}

func (s *Store) saveBriefing(b Briefing) error {
	if err := os.MkdirAll(s.briefingsDir(), 0o700); err != nil {
		return err
	}
	return s.writeJSON(s.briefingPath(b.ID), b)
}

// CreateBriefing adds a briefing at the end of the list. Its id comes from the
// title; a clash gets -2, -3, ... so nothing is ever overwritten.
func (s *Store) CreateBriefing(in BriefingInput, now time.Time) (Briefing, error) {
	if err := in.clean(); err != nil {
		return Briefing{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.listBriefings()
	if err != nil {
		return Briefing{}, err
	}
	if len(list) >= maxBriefings {
		return Briefing{}, errTooManyBriefings
	}
	taken := map[string]bool{}
	order := 0
	for _, b := range list {
		taken[b.ID] = true
		order = max(order, b.Order)
	}
	id := slug(in.Title)
	for n := 2; taken[id]; n++ {
		id = fmt.Sprintf("%s-%d", slug(in.Title), n)
	}
	b := Briefing{ID: id, Title: in.Title, Project: in.Project, Summary: in.Summary, Body: in.Body,
		ReadFirst: in.ReadFirst, Order: order + 1, Created: now, Updated: now}
	return b, s.saveBriefing(b)
}

// UpdateBriefing replaces the editable fields of an existing briefing.
func (s *Store) UpdateBriefing(id string, in BriefingInput, now time.Time) (Briefing, bool, error) {
	if !briefingID.MatchString(id) {
		return Briefing{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, found, err := s.loadBriefing(id)
	if err != nil || !found {
		return b, found, err
	}
	if err := in.clean(); err != nil {
		return b, true, err
	}
	b.Title, b.Project, b.Summary, b.Body, b.ReadFirst = in.Title, in.Project, in.Summary, in.Body, in.ReadFirst
	b.Updated = now
	return b, true, s.saveBriefing(b)
}

// DeleteBriefing removes a briefing; it reports whether one existed. A deleted
// built-in stays deleted: its id remains in the .seeded list.
func (s *Store) DeleteBriefing(id string) (bool, error) {
	if !briefingID.MatchString(id) {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.briefingPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// ── Built-in briefings ──────────────────────────────────────────

func (s *Store) seededPath() string { return filepath.Join(s.briefingsDir(), ".seeded") }

// SeedBriefings writes each built-in briefing the first time this data
// directory sees it. Ones the user has since edited or deleted are never
// touched or brought back, and a briefing that already exists under the same id
// is left alone. Returns how many were written.
func (s *Store) SeedBriefings(now time.Time) (int, error) {
	seeds, err := builtinBriefings()
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var done []string
	if err := s.readJSON(s.seededPath(), &done); err != nil {
		return 0, err
	}
	seen := map[string]bool{}
	for _, id := range done {
		seen[id] = true
	}
	written := 0
	for _, b := range seeds {
		if seen[b.ID] {
			continue
		}
		if _, exists, err := s.loadBriefing(b.ID); err != nil {
			return written, err
		} else if !exists {
			b.Created, b.Updated = now, now
			if err := s.saveBriefing(b); err != nil {
				return written, err
			}
			written++
		}
		done = append(done, b.ID)
	}
	if written == 0 && len(done) == len(seen) {
		return 0, nil
	}
	if err := os.MkdirAll(s.briefingsDir(), 0o700); err != nil {
		return written, err
	}
	return written, s.writeJSON(s.seededPath(), done)
}

func builtinBriefings() ([]Briefing, error) {
	files, err := seedFS.ReadDir("seed")
	if err != nil {
		return nil, err
	}
	var out []Briefing
	for _, f := range files {
		raw, err := seedFS.ReadFile("seed/" + f.Name())
		if err != nil {
			return nil, err
		}
		b, err := parseSeed(strings.TrimSuffix(f.Name(), ".md"), string(raw))
		if err != nil {
			return nil, fmt.Errorf("seed/%s: %w", f.Name(), err)
		}
		out = append(out, b)
	}
	return out, nil
}

// parseSeed reads a seed file: a "---" header of key: value lines, a second
// "---", then the body. read_first holds paths separated by " | ".
func parseSeed(id, text string) (Briefing, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	head, body, ok := strings.Cut(strings.TrimPrefix(text, "---\n"), "\n---\n")
	if !strings.HasPrefix(text, "---\n") || !ok {
		return Briefing{}, errors.New("missing --- header")
	}
	if !briefingID.MatchString(id) {
		return Briefing{}, fmt.Errorf("bad id %q", id)
	}
	b := Briefing{ID: id, Body: strings.TrimSpace(body), ReadFirst: []string{}}
	for _, line := range strings.Split(head, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "title":
			b.Title = v
		case "project":
			b.Project = v
		case "summary":
			b.Summary = v
		case "order":
			b.Order, _ = strconv.Atoi(v)
		case "read_first":
			for _, p := range strings.Split(v, "|") {
				if p = strings.TrimSpace(p); p != "" {
					b.ReadFirst = append(b.ReadFirst, p)
				}
			}
		}
	}
	in := BriefingInput{Title: b.Title, Project: b.Project, Summary: b.Summary, Body: b.Body, ReadFirst: b.ReadFirst}
	if err := in.clean(); err != nil {
		return Briefing{}, err
	}
	return b, nil
}

// ── HTTP ────────────────────────────────────────────────────────

func (a *App) handleBriefings(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.Briefings()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "briefings": list})
}

func (a *App) handleBriefingGet(w http.ResponseWriter, r *http.Request) {
	b, found, err := a.store.Briefing(r.PathValue("id"))
	switch {
	case err != nil:
		writeErr(w, 500, err.Error())
	case !found:
		writeErr(w, 404, "No such briefing.")
	default:
		writeJSON(w, map[string]any{"ok": true, "briefing": b})
	}
}

func (a *App) handleBriefingCreate(w http.ResponseWriter, r *http.Request) {
	var in BriefingInput
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, `Send {"title": "...", "project": "...", "summary": "...", "body": "...", "read_first": [...]}.`)
		return
	}
	b, err := a.store.CreateBriefing(in, time.Now())
	if err != nil {
		writeBriefingErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "briefing": b})
}

func (a *App) handleBriefingUpdate(w http.ResponseWriter, r *http.Request) {
	var in BriefingInput
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, `Send {"title": "...", "project": "...", "summary": "...", "body": "...", "read_first": [...]}.`)
		return
	}
	b, found, err := a.store.UpdateBriefing(r.PathValue("id"), in, time.Now())
	switch {
	case !found:
		writeErr(w, 404, "No such briefing.")
	case err != nil:
		writeBriefingErr(w, err)
	default:
		writeJSON(w, map[string]any{"ok": true, "briefing": b})
	}
}

func (a *App) handleBriefingDelete(w http.ResponseWriter, r *http.Request) {
	found, err := a.store.DeleteBriefing(r.PathValue("id"))
	switch {
	case err != nil:
		writeErr(w, 500, err.Error())
	case !found:
		writeErr(w, 404, "No such briefing.")
	default:
		writeJSON(w, map[string]any{"ok": true})
	}
}

// writeBriefingErr sends validation problems as 400 (the text is meant for the
// user) and anything else as 500.
func writeBriefingErr(w http.ResponseWriter, err error) {
	var bad inputError
	if errors.As(err, &bad) {
		writeErr(w, 400, bad.Error())
		return
	}
	writeErr(w, 500, err.Error())
}

// systemPrompt is Lucille's persona plus one line per briefing, plus live project status (critical
// open items and who's working where), so she can answer "where is Recovery OS at" or "what's
// blocking SWARM OS" or "who's on what right now" without being told again. Full texts stay in the
// Briefings tab, the build list and the ai-board; only condensed summaries go to the model.
func (a *App) systemPrompt() string {
	var sb strings.Builder
	sb.WriteString(lucilleSystemPrompt)

	if list, err := a.store.Briefings(); err == nil && len(list) > 0 {
		sb.WriteString("\n\nThe user's project briefings (summaries; the full text is in the Briefings tab):\n")
		for _, b := range list[:min(len(list), 12)] {
			if b.Summary != "" {
				fmt.Fprintf(&sb, "- %s: %s\n", b.Title, b.Summary)
			} else {
				fmt.Fprintf(&sb, "- %s\n", b.Title)
			}
		}
	}

	if items := criticalOpenItems(); len(items) > 0 {
		sb.WriteString("\nCritical (P0) open items across SWARM OS, Recovery OS, Companion and Lucille herself, from the one shared build list (ask the user to open it for the full picture, don't invent detail beyond this):\n")
		for _, it := range items[:min(len(items), 20)] {
			fmt.Fprintf(&sb, "- %s\n", it)
		}
	}

	if claims := dispatchSummary(); len(claims) > 0 {
		sb.WriteString("\nWho is actively working where right now, from the shared ai-board:\n")
		for _, c := range claims {
			fmt.Fprintf(&sb, "- %s\n", c)
		}
	}

	if lines := swarmBrief(); len(lines) > 0 {
		sb.WriteString("\nYou direct the swarm of AI sessions. They are monthly-subscription coding tools (Claude Code, Antigravity, Codex) that ask you for work; you cannot message them. They do all the coding, for quality; you never write or fix code yourself, you decide who does what and keep track of who is really where. Real state and who should do what next:\n")
		for _, l := range lines {
			fmt.Fprintf(&sb, "- %s\n", l)
		}
	}

	if mem, err := a.memory(5, "decision"); err == nil && len(mem) > 0 {
		sb.WriteString("\nShared team memory, newest decisions first (Dr. Boltz reads the same log):\n")
		for _, e := range mem {
			fmt.Fprintf(&sb, "- %s%s\n", func() string {
				if e.Key != "" {
					return e.Key + ": "
				}
				return ""
			}(), clip(e.Content, 160))
		}
	}
	sb.WriteString(a.gamesDigest())
	sb.WriteString(threadDigestBlock(a.threadDigest()))
	return sb.String()
}
