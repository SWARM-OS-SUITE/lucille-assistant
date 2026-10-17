package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Project brief: Lucille reads the real project files on this PC (the read_first list
// of each briefing), pulls out what is open and what she would do next, and hands the
// phone a finished "artifact" it can render: facts, suggestions, and a command ready to
// paste. From a suggestion a person can (a) add it to the project's markdown inbox or
// (b) send it to Dr. Boltz as a coding job. If the PC cannot be reached the phone reads
// the same files from GitHub itself (each file carries its git address).
//
// Nothing here runs a command. "Ready to run" means a one-line command that is shown and
// copied, never executed from a phone: a network-reachable "run this string" endpoint is
// exactly what the PC-actions allowlist was built to avoid.

// ── config ──────────────────────────────────────────────────────

// GitRoot says which GitHub repository mirrors a directory on this PC. The phone uses
// it to read a file from git when the PC does not answer. Public repos only: no token
// is ever stored on the phone.
type GitRoot struct {
	Root   string `json:"root"`
	Repo   string `json:"repo"` // "owner/name"
	Branch string `json:"branch"`
}

type BriefConfig struct {
	// Roots are the only directories a brief may read from (symlinks are resolved first).
	Roots []string `json:"roots"`
	// MD is the markdown file suggestions are appended to, under the heading MDSection.
	MD        string `json:"md"`
	MDSection string `json:"md_section"`
	// Aliases lets one canonical path stand in for a partition mounted somewhere else on
	// this boot (C is /mnt/lucillebuild, but udisks may mount it as /mnt/lucilleassistant).
	Aliases map[string]string `json:"aliases"`
	Git     []GitRoot         `json:"git"`
	// ProjectRepo maps a briefing id to the bridge repo Dr. Boltz may work in for it.
	// A project with no entry cannot be sent to Dr. Boltz: that is a policy choice.
	ProjectRepo map[string]string `json:"project_repo"`
	// Dirs maps a briefing id to the directory its commands start in.
	Dirs map[string]string `json:"dirs"`
}

func defaultBriefConfig() BriefConfig {
	return BriefConfig{
		Roots: []string{"/mnt/Lucy", "/mnt/recoverybuild", "/mnt/lucillebuild", "/mnt/lucilleassistant",
			"/mnt/boltzbuild", "/mnt/swarmbuild"},
		MD:        "/mnt/recoverybuild/00-CURRENT/MASTER_BUILD_LIST.md",
		MDSection: "Idea inbox",
		Aliases:   map[string]string{"/mnt/lucillebuild": "/mnt/lucilleassistant"},
		Git: []GitRoot{
			{Root: "/mnt/Lucy", Repo: "cledford955-maker/i30ltz-lucille-assistant", Branch: "main"},
			{Root: "/mnt/recoverybuild", Repo: "cledford955-maker/I30LTZ-Recovery-OS", Branch: "main"},
		},
		ProjectRepo: map[string]string{"lucille-assistant": "lucille"},
		Dirs: map[string]string{
			"swarm-os":          "/mnt/Lucy",
			"recovery-os":       "/mnt/recoverybuild",
			"i30ltz-companion":  "/mnt/boltzbuild",
			"lucille-assistant": "/mnt/lucillebuild/src/LucilleAssistant",
		},
	}
}

var repoSlug = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)
var branchName = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,60}$`)

func normalizeBriefConfig(c BriefConfig) BriefConfig {
	d := defaultBriefConfig()
	var roots []string
	for _, r := range c.Roots {
		if filepath.IsAbs(r) && filepath.Clean(r) != "/" {
			roots = append(roots, filepath.Clean(r))
		}
	}
	if len(roots) == 0 {
		roots = d.Roots
	}
	c.Roots = roots
	if !filepath.IsAbs(c.MD) {
		c.MD = d.MD
	}
	if strings.TrimSpace(c.MDSection) == "" {
		c.MDSection = d.MDSection
	}
	var gits []GitRoot
	for _, g := range c.Git {
		if filepath.IsAbs(g.Root) && repoSlug.MatchString(g.Repo) && branchName.MatchString(g.Branch) {
			g.Root = filepath.Clean(g.Root)
			gits = append(gits, g)
		}
	}
	c.Git = gits
	return c
}

// ── reading project files, safely ───────────────────────────────

const (
	maxBriefFileBytes = 1 << 20 // SWARM_AI_FEATURES.md alone is ~370 KB
	maxBriefFiles     = 8
)

var (
	okExt       = map[string]bool{".md": true, ".markdown": true, ".txt": true}
	deniedName  = regexp.MustCompile(`(?i)secret|credential|passw|private|keystore|\.env|\.key|\.pem|token`)
	secretShape = regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}|AIza[0-9A-Za-z_-]{20,}|gh[pousr]_[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{12,}|xox[abprs]-[A-Za-z0-9-]{10,}|-----BEGIN [A-Z ]*PRIVATE KEY-----|(?i:(?:api[_-]?key|secret|token|passw(?:or)?d)\s*[:=]\s*\S{8,})`)
)

func redact(s string) string { return secretShape.ReplaceAllString(s, "[redacted]") }

type briefReader struct {
	cfg  BriefConfig
	real []string // symlink-resolved roots, including their aliases
}

func newBriefReader(cfg BriefConfig) *briefReader {
	r := &briefReader{cfg: cfg}
	seen := map[string]bool{}
	add := func(p string) {
		if rp, err := filepath.EvalSymlinks(p); err == nil && !seen[rp] {
			seen[rp] = true
			r.real = append(r.real, rp)
		}
	}
	for _, root := range cfg.Roots {
		add(root)
		for from, to := range cfg.Aliases {
			if root == from {
				add(to)
			}
		}
	}
	return r
}

// candidates is the path as written plus the same path under each alias, either direction.
func (c BriefConfig) candidates(p string) []string {
	out := []string{p}
	for from, to := range c.Aliases {
		for _, pair := range [][2]string{{from, to}, {to, from}} {
			if p == pair[0] || strings.HasPrefix(p, pair[0]+"/") {
				out = append(out, pair[1]+strings.TrimPrefix(p, pair[0]))
			}
		}
	}
	return out
}

// liveFor is the spelling of p that exists on this PC right now (a partition may be
// mounted at its alias on this boot), so a command shown to the user actually works.
func (c BriefConfig) liveFor(p string) string {
	for _, cand := range c.candidates(p) {
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return p
}

func (r *briefReader) inRoot(real string) bool {
	for _, root := range r.real {
		if real == root || strings.HasPrefix(real, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// BriefFile describes one file the brief tried to read.
type BriefFile struct {
	Path     string    `json:"path"` // as listed in the briefing
	Name     string    `json:"name"`
	Source   string    `json:"source"` // "pc" | "missing" | "blocked"
	Reason   string    `json:"reason,omitempty"`
	Bytes    int       `json:"bytes,omitempty"`
	Modified time.Time `json:"modified"`
	Git      *GitRef   `json:"git,omitempty"` // where the phone can read it from git
}

type GitRef struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Rel    string `json:"rel"`
}

func (r *briefReader) gitRef(p string) *GitRef {
	for _, g := range r.cfg.Git {
		if strings.HasPrefix(p, g.Root+"/") {
			return &GitRef{Repo: g.Repo, Branch: g.Branch, Rel: strings.TrimPrefix(p, g.Root+"/")}
		}
	}
	return nil
}

// read returns the file's text (nil when it could not be read) and its description.
// Only markdown/text files inside a configured root are ever opened, so a briefing edited
// from a phone cannot be used to read a key, a keystore or an .env file.
func (r *briefReader) read(p string) ([]byte, BriefFile) {
	f := BriefFile{Path: p, Name: filepath.Base(p)}
	f.Git = r.gitRef(p)
	block := func(why string) ([]byte, BriefFile) { f.Source, f.Reason = "blocked", why; return nil, f }

	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return block("not a clean absolute path")
	}
	if !okExt[strings.ToLower(filepath.Ext(p))] {
		return block("only .md and .txt files are read")
	}
	for _, part := range strings.Split(p, "/") {
		if strings.HasPrefix(part, ".") && part != "" {
			return block("hidden paths are not read")
		}
	}
	if deniedName.MatchString(filepath.Base(p)) {
		return block("looks like a secrets file")
	}
	var real string
	for _, c := range r.cfg.candidates(p) {
		if rp, err := filepath.EvalSymlinks(c); err == nil {
			real = rp
			break
		}
	}
	if real == "" {
		f.Source, f.Reason = "missing", "not found on this PC (drive not mounted?)"
		return nil, f
	}
	if !r.inRoot(real) {
		return block("outside the folders Lucille may read")
	}
	if !okExt[strings.ToLower(filepath.Ext(real))] || deniedName.MatchString(filepath.Base(real)) {
		return block("a link to something that is not a plain text file")
	}
	st, err := os.Stat(real)
	if err != nil || !st.Mode().IsRegular() {
		f.Source, f.Reason = "missing", "not a regular file"
		return nil, f
	}
	fh, err := os.Open(real)
	if err != nil {
		f.Source, f.Reason = "missing", "cannot be opened: "+err.Error()
		return nil, f
	}
	defer fh.Close()
	data, _ := io.ReadAll(io.LimitReader(fh, maxBriefFileBytes))
	n := len(data)
	f.Source, f.Bytes, f.Modified = "pc", n, st.ModTime().UTC()
	return data, f
}

// ── the tracker table ───────────────────────────────────────────

type TrackerItem struct {
	ID     string `json:"id"`
	P      string `json:"p"`
	Item   string `json:"item"`
	Status string `json:"status"`
}

type Tracker struct {
	File   string        `json:"file"`
	Open   []TrackerItem `json:"open"`
	Total  int           `json:"total"`
	Done   int           `json:"done"`
	Source string        `json:"source"` // "pc" | "none"
}

var mdBold = strings.NewReplacer("*", "", "`", "")

// parseTracker finds "## CODE: ..." in a markdown build list and reads its table.
// The phone has the same parser (ProjectBriefGit.kt) for the git fallback: keep them in step.
func parseTracker(md, code string) (items []TrackerItem, found bool) {
	in := false
	var cols map[string]int
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "## ") {
			if in {
				break
			}
			in = strings.HasPrefix(t, "## "+code+":")
			continue
		}
		if !in || !strings.HasPrefix(t, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(t, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if strings.EqualFold(cells[0], "ID") {
			cols = map[string]int{}
			for i, c := range cells {
				cols[strings.ToLower(c)] = i
			}
			found = true
			continue
		}
		if cols == nil || strings.HasPrefix(cells[0], "---") || !strings.HasPrefix(cells[0], code+"-") {
			continue
		}
		get := func(name string, def int) string {
			i, ok := cols[name]
			if !ok {
				i = def
			}
			if i < len(cells) {
				return cells[i]
			}
			return ""
		}
		items = append(items, TrackerItem{
			ID: cells[0], P: get("p", 1),
			Item:   cleanText(redact(mdBold.Replace(get("item", 2))), 220),
			Status: cleanText(redact(mdBold.Replace(get("status", 3))), 60),
		})
	}
	return items, found
}

func isOpenStatus(s string) bool {
	u := strings.ToUpper(strings.TrimSpace(s))
	if u == "" {
		return false
	}
	for _, p := range []string{"DONE", "IDEA", "DROP", "RETIRED", "WONT", "WON'T"} {
		if strings.HasPrefix(u, p) {
			return false
		}
	}
	return true
}

func summarizeTracker(file string, items []TrackerItem) Tracker {
	t := Tracker{File: file, Source: "pc", Total: len(items), Open: []TrackerItem{}}
	for _, it := range items {
		if isOpenStatus(it.Status) {
			t.Open = append(t.Open, it)
		} else if strings.HasPrefix(strings.ToUpper(it.Status), "DONE") {
			t.Done++
		}
	}
	sort.SliceStable(t.Open, func(i, j int) bool { return t.Open[i].P < t.Open[j].P })
	if len(t.Open) > 6 {
		t.Open = t.Open[:6]
	}
	return t
}

// ── suggestions ─────────────────────────────────────────────────

type BriefSuggestion struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Why        string `json:"why"`
	Body       string `json:"body"` // what Dr. Boltz would be told; the person edits it before sending
	Size       string `json:"size"`
	Source     string `json:"source"` // "model" | "briefing" | "tracker"
	Command    string `json:"command"`
	InMD       bool   `json:"in_md"`
	Sent       string `json:"sent,omitempty"`        // challenge id once sent to Dr. Boltz
	SentStatus string `json:"sent_status,omitempty"` // that challenge's current status
}

func suggestionID(project, title string) string {
	h := sha256.Sum256([]byte(project + "|" + normTitle(title)))
	return "sg-" + hex.EncodeToString(h[:])[:8]
}

var (
	numberedLine = regexp.MustCompile(`^\s*\d+[.)]\s+(.+)$`)
	sizeHours    = regexp.MustCompile(`(?i)about\s+(\d+(?:\.\d+)?)\s*h`)
)

func sizeFor(text string) string {
	l := strings.ToLower(text)
	switch {
	case strings.Contains(l, "week") || strings.Contains(l, "sprint"):
		return "sprint"
	case strings.Contains(l, "day") && !strings.Contains(l, "half a day"):
		return "weekend"
	}
	if m := sizeHours.FindStringSubmatch(text); m != nil {
		var h float64
		fmt.Sscanf(m[1], "%f", &h)
		if h > 6 {
			return "weekend"
		}
	}
	return "evening"
}

// splitSuggestion turns "Title: explanation. About 2 h." into a title and the rest.
func splitSuggestion(text string) (title, why string) {
	text = cleanText(redact(text), 700)
	if i := strings.Index(text, ":"); i > 3 && i <= 90 {
		return strings.TrimSpace(text[:i]), upperFirst(strings.TrimSpace(text[i+1:]))
	}
	if i := strings.Index(text, ". "); i > 3 && i <= 110 {
		return strings.TrimSpace(text[:i]), upperFirst(strings.TrimSpace(text[i+2:]))
	}
	r := []rune(text)
	if len(r) > 100 {
		return strings.TrimSpace(string(r[:100])) + "…", text
	}
	return text, ""
}

func upperFirst(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// briefingSuggestions reads the numbered list under "3 SUGGESTIONS" in a briefing body.
func briefingSuggestions(body string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case !in && strings.Contains(strings.ToUpper(t), "SUGGESTIONS") && !numberedLine.MatchString(t) && len(t) < 40:
			in = true
		case in && t == "":
			if len(out) > 0 {
				return out
			}
		case in:
			if m := numberedLine.FindStringSubmatch(t); m != nil {
				out = append(out, m[1])
			} else if len(out) > 0 && !strings.HasPrefix(t, "REFERENCE") && strings.ToUpper(t) != t {
				out[len(out)-1] += " " + t // a wrapped line
			} else if len(out) > 0 {
				return out
			}
		}
	}
	return out
}

// oneLine makes text safe to put inside a single-quoted shell word and a terminal:
// no newlines, no control characters.
func oneLine(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, s)
	return cleanText(s, n)
}

func (c BriefConfig) dirFor(id string) string {
	if d := c.Dirs[id]; d != "" {
		return d
	}
	return "/mnt/Lucy"
}

// suggestionPrompt is the formal prompt (formalprompt.go) for one brief suggestion. The ready-to-run
// command shows it on one line for a `claude` the person starts and watches (attended); "Approve and
// start" hands the full text to the launcher, which runs it unattended (jobs.go draftPrompt).
func (a *App) suggestionPrompt(projectID, projectTitle string, s BriefSuggestion, attended bool) formalPrompt {
	who := "Lucille (the user's assistant) is handing you a suggestion from the project brief; the user approved it."
	if attended {
		who = "Lucille (the user's assistant) is handing you a suggestion from the project brief; the user started this session and is watching."
	}
	return formalPrompt{
		Who:       who,
		Job:       oneLine(s.Title, 120) + ". " + oneLine(s.Why, 300),
		ReadFirst: []string{a.cfg.Brief.liveFor(a.cfg.Brief.MD) + " (the build list and its Idea inbox)", "the project's own notes (README, CLAUDE.md)"},
		Project:   projectTitle,
		Dir:       a.cfg.Brief.liveFor(a.cfg.Brief.dirFor(projectID)),
		Rules:     baseRules,
		DoneWhen:  "The suggestion is built and checked, or you have said what blocks it.",
		Report:    "Tell me what changed, what you checked, and what is left.",
	}
}

// The ready-to-run commands start an interactive `claude` the person watches, so the prompt is the
// formal shape on one line: one shell word, no newlines.
func (a *App) suggestionCommand(projectID, projectTitle string, s BriefSuggestion) string {
	fp := a.suggestionPrompt(projectID, projectTitle, s, true)
	return "cd " + shellQuote(fp.Dir) + " && claude " + shellQuote(fp.OneLine(1200))
}

func (a *App) catchUpCommand(projectID, projectTitle string, files []BriefFile) string {
	var names []string
	for _, f := range files {
		if f.Source == "pc" {
			names = append(names, a.cfg.Brief.liveFor(f.Path))
		}
	}
	dir := a.cfg.Brief.liveFor(a.cfg.Brief.dirFor(projectID))
	prompt := formalPrompt{
		Who:       "Lucille (the user's assistant) is asking you to catch the user up; the user started this session and is watching.",
		Job:       "Catch me up on " + projectTitle + ". Read only; change nothing.",
		ReadFirst: names,
		Project:   projectTitle,
		Dir:       dir,
		DoneWhen:  "You have read the files above.",
		Report:    "The state in five lines, then what to do first.",
	}.OneLine(1200)
	return "cd " + shellQuote(dir) + " && claude " + shellQuote(prompt)
}

type storedSuggestions struct {
	Generated time.Time         `json:"generated"`
	Items     []BriefSuggestion `json:"items"`
}

func (a *App) briefDir() string { return filepath.Join(a.cfg.DataDir, "projectbrief") }

func (a *App) loadStored(id string) (storedSuggestions, bool) {
	var s storedSuggestions
	if !briefingID.MatchString(id) {
		return s, false
	}
	b, err := os.ReadFile(filepath.Join(a.briefDir(), id+".suggestions.json"))
	if err != nil || json.Unmarshal(b, &s) != nil || len(s.Items) == 0 {
		return storedSuggestions{}, false
	}
	return s, true
}

func (a *App) saveStored(id string, s storedSuggestions) error {
	if err := os.MkdirAll(a.briefDir(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return writeFileAtomic(filepath.Join(a.briefDir(), id+".suggestions.json"), b, 0o600)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ── what Dr. Boltz was already sent ─────────────────────────────

var sentMu sync.Mutex

func (a *App) sentMap() map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(filepath.Join(a.briefDir(), "sent.json")); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (a *App) rememberSent(sid, challenge string) {
	sentMu.Lock()
	defer sentMu.Unlock()
	m := a.sentMap()
	m[sid] = challenge
	if len(m) > 500 { // forget the arbitrary oldest; this is only a convenience marker
		for k := range m {
			delete(m, k)
			if len(m) <= 400 {
				break
			}
		}
	}
	if err := os.MkdirAll(a.briefDir(), 0o700); err == nil {
		b, _ := json.Marshal(m)
		_ = writeFileAtomic(filepath.Join(a.briefDir(), "sent.json"), b, 0o600)
	}
}

func (a *App) challengeStatus(id string) string {
	if a.bridge == nil || id == "" {
		return ""
	}
	a.bridge.mu.Lock()
	defer a.bridge.mu.Unlock()
	if c := a.bridge.findLocked(id); c != nil {
		return string(c.Status)
	}
	return ""
}

// ── is this suggestion already in the inbox? ───────────────────

var inboxDated = regexp.MustCompile(`^- \d{4}-\d{2}-\d{2} `)

// inboxLines returns the dated bullet lines of the markdown, lowercased. Earlier tools
// (and people) wrote suggestions there without Lucille's [sg-...] marker, so the marker
// alone would offer to add the same idea a second time.
func inboxLines(md string) []string {
	var out []string
	for _, l := range strings.Split(md, "\n") {
		if inboxDated.MatchString(l) {
			out = append(out, strings.ToLower(l))
		}
	}
	return out
}

func sigWords(title string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(w) >= 3 {
			out = append(out, w)
		}
	}
	return out
}

// inboxHas reports whether an inbox line for this project (it names the project code)
// already carries the title: every significant word of a short title, two thirds of a
// longer one (a hand-written line paraphrases: "60-second boot self-check" was written
// as "60-second first-boot self-check screen"). A title of one short word is never
// matched this way; only the marker can say that one is there. Misses cost one duplicate
// line; a false match would wrongly say "already added", so the rule leans strict.
func inboxHas(lines []string, code, title string) bool {
	words := sigWords(title)
	n := len(words)
	need := n
	switch {
	case n == 0 || (n == 1 && len(words[0]) < 6):
		return false
	case n >= 4:
		need = (2*n + 2) / 3
	}
	codeRe, err := regexp.Compile(`\b` + regexp.QuoteMeta(strings.ToLower(code)) + `\b`)
	if code == "" || err != nil {
		return false
	}
	for _, l := range lines {
		if !codeRe.MatchString(l) {
			continue
		}
		hit := 0
		for _, w := range words {
			if strings.Contains(l, w) {
				hit++
			}
		}
		if hit >= need {
			return true
		}
	}
	return false
}

// ── the brief ───────────────────────────────────────────────────

type BriefProject struct {
	ID          string            `json:"id"`
	Code        string            `json:"code"`
	Title       string            `json:"title"`
	Summary     string            `json:"summary"`
	Updated     time.Time         `json:"briefing_updated"`
	Stale       bool              `json:"stale"` // the briefing's own status text is old
	Files       []BriefFile       `json:"files"`
	Tracker     Tracker           `json:"tracker"`
	Git         *RepoStatus       `json:"git,omitempty"`
	Repo        string            `json:"repo"`      // bridge repo for Dr. Boltz, "" if none
	CanSend     bool              `json:"can_send"`  // Dr. Boltz can take a job for this project
	SendNote    string            `json:"send_note"` // why not
	Suggestions []BriefSuggestion `json:"suggestions"`
	SugSource   string            `json:"suggestions_source"`
	SugAt       time.Time         `json:"suggestions_at"`
	Command     string            `json:"command"`
}

type BriefMD struct {
	Path     string `json:"path"`
	Section  string `json:"section"`
	Writable bool   `json:"writable"`
}

func (a *App) buildBrief(now time.Time) map[string]any {
	cfg := a.cfg.Brief
	rd := newBriefReader(cfg)
	out := map[string]any{"ok": true, "mode": "pc", "generated_at": now, "date": day(now)}

	mdText, mdFile := rd.read(cfg.MD)
	writable := false
	if mdFile.Source == "pc" {
		if f, err := os.OpenFile(cfg.MD, os.O_WRONLY, 0); err == nil {
			f.Close()
			writable = true
		}
	}
	out["md"] = BriefMD{Path: cfg.MD, Section: cfg.MDSection, Writable: writable}
	bridgeRepos := []string{}
	if a.bridge != nil {
		for _, r := range a.bridge.cfg.Repos {
			bridgeRepos = append(bridgeRepos, r.Name)
		}
	}
	out["bridge_repos"] = bridgeRepos

	list, err := a.store.Briefings()
	if err != nil {
		out["ok"] = false
		out["error"] = "Could not read the briefings: " + err.Error()
		return out
	}
	projects := make([]BriefProject, len(list))
	var wg sync.WaitGroup
	for i, b := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			projects[i] = a.buildProject(rd, b, string(mdText), now)
		}()
	}
	wg.Wait()
	out["projects"] = projects
	out["files_read"], out["files_total"] = countRead(projects)
	return out
}

func countRead(ps []BriefProject) (read, total int) {
	for _, p := range ps {
		for _, f := range p.Files {
			total++
			if f.Source == "pc" {
				read++
			}
		}
	}
	return
}

func (a *App) buildProject(rd *briefReader, b Briefing, mdText string, now time.Time) BriefProject {
	p := BriefProject{
		ID: b.ID, Code: b.Project, Title: b.Title, Summary: cleanText(redact(b.Summary), 400),
		Updated: b.Updated, Stale: now.Sub(b.Updated) > 72*time.Hour,
		Tracker: Tracker{Source: "none", Open: []TrackerItem{}}, Files: []BriefFile{},
	}
	paths := b.ReadFirst
	if len(paths) > maxBriefFiles {
		paths = paths[:maxBriefFiles]
	}
	for _, path := range paths {
		text, f := rd.read(path)
		p.Files = append(p.Files, f)
		if text != nil && p.Tracker.Source == "none" && p.Code != "" && bytes.Contains(text, []byte("## "+p.Code+":")) {
			if items, ok := parseTracker(string(text), p.Code); ok {
				p.Tracker = summarizeTracker(f.Name, items)
			}
		}
	}
	if dir := a.cfg.Brief.dirFor(b.ID); dir != "" {
		if rs := repoStatus(dir); rs.Error == "" {
			p.Git = &rs
		}
	}
	p.Command = a.catchUpCommand(b.ID, b.Title, p.Files)

	p.Repo = a.cfg.Brief.ProjectRepo[b.ID]
	switch {
	case a.bridge == nil:
		p.SendNote = "The Dr. Boltz bridge is not running."
	case p.Repo == "":
		p.SendNote = "No Dr. Boltz repo is set for this project (project_repo in config.json)."
	default:
		if _, ok := a.bridge.cfg.repo(p.Repo); ok {
			p.CanSend = true
		} else {
			p.SendNote = fmt.Sprintf("Dr. Boltz has no repo named %q (bridge.repos in config.json).", p.Repo)
		}
	}

	// Suggestions: a model's, if she has thought some up; else the briefing's own list;
	// else the top open tracker items. Never padded with invented ones.
	var sugs []BriefSuggestion
	if st, ok := a.loadStored(b.ID); ok {
		sugs, p.SugSource, p.SugAt = st.Items, "model", st.Generated
	} else if lines := briefingSuggestions(b.Body); len(lines) > 0 {
		for _, l := range lines {
			t, w := splitSuggestion(l)
			sugs = append(sugs, BriefSuggestion{Title: t, Why: w, Body: cleanText(redact(l), 700), Size: sizeFor(l), Source: "briefing"})
		}
		p.SugSource, p.SugAt = "briefing", b.Updated
	} else {
		for _, it := range p.Tracker.Open {
			if len(sugs) == 3 {
				break
			}
			sugs = append(sugs, BriefSuggestion{
				Title: fmt.Sprintf("%s: %s", it.ID, cleanText(it.Item, 80)), Why: fmt.Sprintf("Open on the build list (%s, %s).", it.P, it.Status),
				Body: it.Item, Size: "evening", Source: "tracker",
			})
		}
		p.SugSource = "tracker"
	}
	sent := a.sentMap()
	inbox := inboxLines(mdText)
	for i := range sugs {
		s := &sugs[i]
		s.Title, s.Why = oneLine(redact(s.Title), 120), oneLine(redact(s.Why), 400)
		s.Body = cleanTextKeepLines(redact(s.Body), 1800)
		if s.Size == "" {
			s.Size = "evening"
		}
		s.ID = suggestionID(b.ID, s.Title)
		s.Command = a.suggestionCommand(b.ID, b.Title, *s)
		s.InMD = strings.Contains(mdText, "["+s.ID+"]") || inboxHas(inbox, p.Code, s.Title)
		if cid := sent[s.ID]; cid != "" {
			s.Sent, s.SentStatus = cid, a.challengeStatus(cid)
		}
	}
	p.Suggestions = sugs
	if p.Suggestions == nil {
		p.Suggestions = []BriefSuggestion{}
	}
	return p
}

// cleanTextKeepLines trims and caps text but keeps its line breaks (a job body is multi-line).
func cleanTextKeepLines(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || (r >= 32 && r != 127) {
			return r
		}
		return -1
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func (a *App) handleProjectBrief(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.buildBrief(time.Now()))
}

// ── ask Lucille for fresh suggestions (explicit tap, one project) ─

const briefThinkSystem = `You are Lucille, the planner who keeps the user's hobby projects moving. Given one project's status, its open build-list items and its recent commits, propose exactly 3 concrete next improvements.
Each must be small enough for one evening, checkable (a test, a build, a command with a known result), and not busywork. Do not repeat anything already done or listed as already suggested.
Reply with ONLY a JSON array of 3 objects {"title","why","body","size"}: title under 80 characters, why one sentence, body the acceptance criteria a coder could check, size evening, weekend or sprint. No prose, no markdown.`

var (
	thinkMu   sync.Mutex
	thinkLast = map[string]time.Time{}
)

type thinkCand struct{ Title, Why, Body, Size string }

func parseThink(text string) ([]thinkCand, error) {
	i, j := strings.Index(text, "["), strings.LastIndex(text, "]")
	if i < 0 || j <= i {
		return nil, errors.New("no JSON list in the answer")
	}
	var out []thinkCand
	if err := json.Unmarshal([]byte(text[i:j+1]), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *App) handleProjectThink(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireHuman(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	b, found, err := a.store.Briefing(id)
	if err != nil || !found {
		writeErr(w, http.StatusNotFound, "No such project.")
		return
	}
	thinkMu.Lock()
	if t, ok := thinkLast[id]; ok && time.Since(t) < 20*time.Second {
		thinkMu.Unlock()
		writeErr(w, http.StatusTooManyRequests, "She just thought about this one. Give it a few seconds.")
		return
	}
	thinkLast[id] = time.Now()
	thinkMu.Unlock()

	rd := newBriefReader(a.cfg.Brief)
	p := a.buildProject(rd, b, "", time.Now())
	var sb strings.Builder
	fmt.Fprintf(&sb, "Project: %s\nBriefing summary (written %s): %s\n", b.Title, day(b.Updated), p.Summary)
	fmt.Fprintf(&sb, "Open build-list items (%d open of %d):\n", len(p.Tracker.Open), p.Tracker.Total)
	for _, it := range p.Tracker.Open {
		fmt.Fprintf(&sb, "- %s [%s, %s] %s\n", it.ID, it.P, it.Status, it.Item)
	}
	if dir := a.cfg.Brief.dirFor(id); dir != "" {
		if lg, err := git(dir, "log", "-8", "--format=%s"); err == nil {
			fmt.Fprintf(&sb, "Recent commits: %s\n", cleanText(redact(strings.ReplaceAll(lg, "\n", " | ")), 700))
		}
	}
	var seen []string
	for _, s := range p.Suggestions {
		seen = append(seen, s.Title)
	}
	if len(seen) > 0 {
		fmt.Fprintf(&sb, "Already suggested (do not repeat): %s\n", cleanText(strings.Join(seen, " | "), 900))
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	text, err := a.ask(ctx, briefThinkSystem, sb.String(), 900)
	cands, perr := parseThink(text)
	if err != nil || perr != nil || len(cands) == 0 {
		reason := "no answer"
		if err != nil {
			reason = err.Error()
		} else if perr != nil {
			reason = "an unreadable answer"
		}
		writeErr(w, http.StatusBadGateway, "No language model answered ("+cleanText(reason, 160)+"). The suggestions from the briefing stay as they are.")
		return
	}
	items := []BriefSuggestion{}
	for _, c := range cands {
		if len(items) == 3 || strings.TrimSpace(c.Title) == "" {
			continue
		}
		size := strings.ToLower(strings.TrimSpace(c.Size))
		if size != "evening" && size != "weekend" && size != "sprint" {
			size = "evening"
		}
		items = append(items, BriefSuggestion{Title: oneLine(c.Title, 120), Why: oneLine(c.Why, 400),
			Body: cleanTextKeepLines(c.Body, 1800), Size: size, Source: "model"})
	}
	if len(items) == 0 {
		writeErr(w, http.StatusBadGateway, "The model answered, but with nothing usable.")
		return
	}
	if err := a.saveStored(id, storedSuggestions{Generated: time.Now().UTC(), Items: items}); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not save her suggestions: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "count": len(items)})
}

// ── add a suggestion to the project markdown ────────────────────

var mdMu sync.Mutex

// insertInbox puts line at the end of the section whose heading starts with section
// (before the next heading or rule), or appends a new section at the end of the file.
func insertInbox(doc, section, line string) string {
	lines := strings.Split(doc, "\n")
	head := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(l)), "## "+strings.ToLower(section)) {
			head = i
			break
		}
	}
	if head < 0 {
		sep := "\n"
		if !strings.HasSuffix(doc, "\n") {
			sep = "\n\n"
		}
		return doc + sep + "## " + section + " (append only; date and who)\n" + line + "\n"
	}
	end := len(lines)
	for j := head + 1; j < len(lines); j++ {
		t := strings.TrimSpace(lines[j])
		if strings.HasPrefix(t, "## ") || t == "---" {
			end = j
			break
		}
	}
	at := end
	for at > head+1 && strings.TrimSpace(lines[at-1]) == "" {
		at--
	}
	out := append([]string{}, lines[:at]...)
	out = append(out, line)
	out = append(out, lines[at:]...)
	return strings.Join(out, "\n")
}

var errMDChanged = errors.New("the file kept changing while Lucille wrote to it; try again")

// appendToMD adds one line to the project markdown unless its marker is already there.
// The write is a same-directory temp file renamed over the original, and it is dropped
// and retried if the file changed in between (other AIs and the auto-sync job edit it).
func (a *App) appendToMD(rd *briefReader, line, marker string, dup func(doc string) bool) (added bool, err error) {
	mdMu.Lock()
	defer mdMu.Unlock()
	path := a.cfg.Brief.MD
	if _, f := rd.read(path); f.Source != "pc" {
		return false, fmt.Errorf("the project file is not available on this PC (%s)", f.Reason)
	}
	for attempt := 0; attempt < 4; attempt++ {
		before, err := os.Stat(path)
		if err != nil {
			return false, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return false, err
		}
		if bytes.Contains(data, []byte(marker)) || (dup != nil && dup(string(data))) {
			return false, nil
		}
		next := insertInbox(string(data), a.cfg.Brief.MDSection, line)
		tmp, err := os.CreateTemp(filepath.Dir(path), ".lucille-md-*")
		if err != nil {
			return false, err
		}
		_, werr := tmp.WriteString(next)
		if werr == nil {
			werr = tmp.Chmod(before.Mode().Perm())
		}
		if werr == nil {
			werr = tmp.Sync()
		}
		if cerr := tmp.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			os.Remove(tmp.Name())
			return false, werr
		}
		after, err := os.Stat(path)
		if err != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
			os.Remove(tmp.Name())
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if err := os.Rename(tmp.Name(), path); err != nil {
			os.Remove(tmp.Name())
			return false, err
		}
		return true, nil
	}
	return false, errMDChanged
}

func (a *App) handleProjectMD(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireHuman(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	b, found, err := a.store.Briefing(id)
	if err != nil || !found {
		writeErr(w, http.StatusNotFound, "No such project.")
		return
	}
	var req struct{ Title, Why string }
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Title) == "" {
		writeErr(w, http.StatusBadRequest, `Send {"title": "...", "why": "..."}.`)
		return
	}
	title, why := oneLine(redact(req.Title), 120), oneLine(redact(req.Why), 300)
	sid := suggestionID(id, title)
	marker := "[" + sid + "]"
	code := b.Project
	if code == "" {
		code = strings.ToUpper(id)
	}
	text := title
	if why != "" {
		text += ". " + why
	}
	line := fmt.Sprintf("- %s lucille: %s suggestion from the Lucille brief (unvetted): %s %s", day(time.Now()), code, text, marker)
	added, err := a.appendToMD(newBriefReader(a.cfg.Brief), line, marker, func(doc string) bool {
		return inboxHas(inboxLines(doc), b.Project, title)
	})
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errMDChanged) {
			code = http.StatusConflict
		}
		writeErr(w, code, "Could not add it: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "added": added, "already": !added, "id": sid, "file": a.cfg.Brief.MD})
}

// ── send a suggestion to Dr. Boltz ──────────────────────────────

func (a *App) handleProjectBoltz(w http.ResponseWriter, r *http.Request) {
	if !a.bridgeReady(w) {
		return
	}
	if _, ok := requireHuman(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	if _, found, err := a.store.Briefing(id); err != nil || !found {
		writeErr(w, http.StatusNotFound, "No such project.")
		return
	}
	repo := a.cfg.Brief.ProjectRepo[id]
	if repo == "" {
		writeErr(w, http.StatusConflict, "No Dr. Boltz repo is set for this project (project_repo in config.json), so it cannot be sent.")
		return
	}
	// The text must come from the person's own review on the phone: nothing is looked up
	// server-side, so text read from a file never reaches the coder without being seen.
	var req struct{ Title, Body, Size, SID string }
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Body) == "" {
		writeErr(w, http.StatusBadRequest, `Send {"title": "...", "body": "what done means ...", "size": "evening"}.`)
		return
	}
	ch, err := a.bridge.addChallenge(redact(req.Title), redact(req.Body), repo, "human", req.Size)
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, errTooMany) {
			code = http.StatusConflict
		}
		writeErr(w, code, err.Error())
		return
	}
	if sid := strings.TrimSpace(req.SID); strings.HasPrefix(sid, "sg-") && len(sid) == 11 {
		a.rememberSent(sid, ch.ID)
	}
	writeJSON(w, map[string]any{"ok": true, "challenge": ch})
}
