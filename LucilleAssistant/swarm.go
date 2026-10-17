package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Lucille directs the swarm. The AIs are monthly-subscription sessions (Claude Code, Antigravity,
// Codex), not API calls she can make, so they PULL: at the start of a session, or when they run out
// of work, an AI asks her what to do next. Nothing here needs an API key or a model: the choice is
// the one shared build list + the shared ai-board + which AI sessions are really running, so it
// works with the cloud off. Assignments are logged so two AIs asking at once never get the same row.
//
//	GET  /api/swarm/status            who is where (declared claims AND real processes), what is free
//	GET  /api/swarm/next?ai=NAME      what NAME should work on next (peek, records nothing)
//	POST /api/swarm/next {"ai":NAME}  the same, and records the top pick as NAME's assignment

var assignmentsPath = func() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "share", "lucille-assistant", "swarm_assignments.tsv")
}()

const (
	assignmentTTL = 4 * time.Hour
	boardScript   = "/mnt/recoverybuild/70-scripts/ai-board.sh"
)

// swarmLanes: which projects each named AI works in. An empty list (or an unknown name) means any
// project. A lane is a rule, not a preference: work outside it is listed separately as off_lane so
// the user decides, instead of an AI wandering into another AI's repo.
var swarmLanes = map[string][]string{
	"dev-claude":        {"LA", "INF", "MESH"},
	"swarm-claude":      {"SW"},
	"claude-recovery":   {"RC", "CP", "INF"},
	"gemini-recovery":   {"RC", "CP"},
	"dev-antigravity":   {},
	"codex":             {"SW"},
	"codex-coordinator": {},
}

var projectRoots = []struct{ prefix, project string }{
	{"/mnt/lucilleassistant", "LA"},
	{"/mnt/boltzbuild", "CP"},
	{"/mnt/recoverybuild", "RC"},
	{"/mnt/swarmbuild", "SW"},
	{"/mnt/Lucy", "SW"},
}

func projectOfPath(p string) string {
	for _, r := range projectRoots {
		if p == r.prefix || strings.HasPrefix(p, r.prefix+"/") {
			return r.project
		}
	}
	return ""
}

type buildRow struct {
	ID       string `json:"id"`
	Priority string `json:"priority"`
	Item     string `json:"item"`
	Status   string `json:"status"`
	Project  string `json:"project"`
	Sector   string `json:"sector"`
}

func (r buildRow) open() bool {
	s := strings.ToUpper(strings.TrimSpace(r.Status))
	return !strings.HasPrefix(s, "DONE") && !strings.HasPrefix(s, "IDEA")
}

// needsUser: rows an AI cannot do alone (a human action, sudo, a decision) are not handed out.
func (r buildRow) needsUser() bool {
	s := strings.ToLower(r.Status)
	return strings.HasPrefix(s, "blocked") || strings.Contains(s, "(user)") || strings.Contains(s, "needs sudo")
}

var (
	idTokenRe  = regexp.MustCompile(`^[A-Z]+-\d+$`)
	anyIDRe    = regexp.MustCompile(`\b([A-Z]{2,5}-\d+)\b`)
	sectorHead = regexp.MustCompile(`(?m)^## Sectors`)
)

// loadBuildRows returns every build-list row with its project and sector filled in from the
// "Sectors" table. Rows no sector lists get an empty Sector (claimable by their own ID).
func loadBuildRows() []buildRow {
	b, err := os.ReadFile(masterBuildListPath)
	if err != nil {
		return nil
	}
	text := string(b)
	sectorOf := map[string]string{}
	if loc := sectorHead.FindStringIndex(text); loc != nil {
		rest := text[loc[1]:]
		if end := strings.Index(rest, "\n## "); end >= 0 {
			rest = rest[:end]
		}
		for _, line := range strings.Split(rest, "\n") {
			cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
			if len(cells) != 3 {
				continue
			}
			proj, name := strings.TrimSpace(cells[0]), strings.TrimSpace(cells[1])
			if len(proj) != 2 || strings.ToUpper(proj) != proj || name == "" || name == "Sector" {
				continue
			}
			for _, tok := range strings.Split(cells[2], ",") {
				tok = strings.TrimSpace(tok)
				if _, err := strconv.Atoi(tok); err == nil {
					tok = fmt.Sprintf("%s-%s", proj, tok)
				}
				if idTokenRe.MatchString(tok) {
					sectorOf[tok] = name
				}
			}
		}
	}
	var rows []buildRow
	for _, line := range strings.Split(text, "\n") {
		m := buildListRowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		proj, _, _ := strings.Cut(m[1], "-")
		rows = append(rows, buildRow{ID: m[1], Priority: m[2], Item: m[3], Status: m[4], Project: proj, Sector: sectorOf[m[1]]})
	}
	return rows
}

// ── real sessions ───────────────────────────────────────────────────────────

type liveSession struct {
	Tool    string    `json:"tool"`
	PID     int       `json:"pid"`
	Cwd     string    `json:"cwd"`
	Project string    `json:"project,omitempty"`
	Since   time.Time `json:"since"`
}

var aiTools = map[string]bool{"claude": true, "agy": true, "codex": true, "gemini": true}

var liveSessionsFn = realLiveSessions

func liveSessions() []liveSession {
	return liveSessionsFn()
}

// realLiveSessions finds the AI CLIs actually running on this machine (not what the board claims):
// the tool's own process and its working directory.
func realLiveSessions() []liveSession {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []liveSession
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		comm, err := os.ReadFile("/proc/" + e.Name() + "/comm")
		if err != nil || !aiTools[strings.TrimSpace(string(comm))] {
			continue
		}
		cwd, err := os.Readlink("/proc/" + e.Name() + "/cwd")
		if err != nil {
			continue
		}
		s := liveSession{Tool: strings.TrimSpace(string(comm)), PID: pid, Cwd: cwd, Project: projectOfPath(cwd)}
		if fi, err := os.Stat("/proc/" + e.Name()); err == nil {
			s.Since = fi.ModTime()
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// ── claims ──────────────────────────────────────────────────────────────────

type claimScope struct {
	projects map[string]string // whole project -> who holds it
	sectors  map[string]string // "SW|Security & Keys" -> who
	ids      map[string]string // build-list ID mentioned in a claim -> who
}

func scopeOf(claims []boardClaim) claimScope {
	sc := claimScope{map[string]string{}, map[string]string{}, map[string]string{}}
	for _, c := range claims {
		if c.Stale {
			continue // an abandoned claim must not block anyone
		}
		if proj, sector, ok := strings.Cut(c.Area, ": "); ok && len(proj) == 2 && strings.ToUpper(proj) == proj {
			sc.sectors[proj+"|"+strings.TrimSpace(sector)] = c.AI
		} else if p := projectOfPath(strings.Fields(c.Area + " ")[0]); p != "" {
			sc.projects[p] = c.AI
		}
		for _, m := range anyIDRe.FindAllString(c.Area+" "+c.Task, -1) {
			sc.ids[m] = c.AI
		}
	}
	return sc
}

// heldByOther says who blocks this row for `ai`, or "".
func (sc claimScope) heldByOther(r buildRow, ai string) string {
	if w := sc.projects[r.Project]; w != "" && w != ai {
		return w
	}
	if r.Sector != "" {
		if w := sc.sectors[r.Project+"|"+r.Sector]; w != "" && w != ai {
			return w
		}
	}
	if w := sc.ids[r.ID]; w != "" && w != ai {
		return w
	}
	return ""
}

// ── assignments ─────────────────────────────────────────────────────────────

type assignment struct {
	At     time.Time `json:"at"`
	AI     string    `json:"ai"`
	ID     string    `json:"id"`
	Sector string    `json:"sector,omitempty"`
}

func activeAssignments() []assignment {
	f, err := os.Open(assignmentsPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []assignment
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Split(sc.Text(), "\t")
		if len(p) < 3 {
			continue
		}
		t, err := time.Parse(time.RFC3339, p[0])
		if err != nil || time.Since(t) > assignmentTTL {
			continue
		}
		a := assignment{At: t, AI: p[1], ID: p[2]}
		if len(p) > 3 {
			a.Sector = p[3]
		}
		out = append(out, a)
	}
	return out
}

func recordAssignment(ai string, r buildRow) error {
	if err := os.MkdirAll(filepath.Dir(assignmentsPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(assignmentsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\t%s\t%s\t%s\n", time.Now().Format(time.RFC3339), ai, r.ID, r.Sector)
	return err
}

// ── choosing work ───────────────────────────────────────────────────────────

type suggestion struct {
	buildRow
	ClaimArea string `json:"claim_area"`
	ClaimCmd  string `json:"claim_cmd"`
	Why       string `json:"why"`
}

func inLane(ai, project string) (laned bool, ok bool) {
	lane, known := swarmLanes[ai]
	if !known || len(lane) == 0 {
		return false, true
	}
	for _, p := range lane {
		if p == project {
			return true, true
		}
	}
	return true, false
}

func claimAreaFor(r buildRow) string {
	if r.Sector != "" {
		return r.Project + ": " + r.Sector
	}
	return r.Project + ": " + r.ID
}

// unclaimedBusyProjects: projects where an AI session is really running but nobody has claimed
// anything. Someone is working there, so an AI whose lane is "anything" is not sent in on top of them.
func unclaimedBusyProjects(claims []boardClaim, sessions []liveSession) map[string]bool {
	covered := map[string]bool{}
	for _, c := range claims {
		if c.Stale {
			continue
		}
		proj, _, ok := strings.Cut(c.Area, ": ")
		if !ok || len(proj) != 2 {
			proj = projectOfPath(strings.Fields(c.Area + " ")[0])
		}
		covered[proj] = true
	}
	busy := map[string]bool{}
	for _, s := range sessions {
		if s.Project != "" && !covered[s.Project] {
			busy[s.Project] = true
		}
	}
	return busy
}

func pickWork(ai string, rows []buildRow, claims []boardClaim, assigned []assignment, busy map[string]bool, limit int) (inLaneList, offLane []suggestion) {
	inLaneList, offLane = []suggestion{}, []suggestion{}
	sc := scopeOf(claims)
	takenID, takenSector := map[string]string{}, map[string]string{}
	for _, a := range assigned {
		if a.AI != ai {
			takenID[a.ID] = a.AI
			if a.Sector != "" {
				takenSector[a.Sector] = a.AI
			}
		}
	}
	var cand []buildRow
	for _, r := range rows {
		if !r.open() || r.needsUser() || sc.heldByOther(r, ai) != "" || takenID[r.ID] != "" {
			continue
		}
		if laned, _ := inLane(ai, r.Project); busy[r.Project] && !laned {
			continue
		}
		if r.Sector != "" && takenSector[r.Project+"|"+r.Sector] != "" {
			continue
		}
		cand = append(cand, r)
	}
	mine := func(r buildRow) bool { return strings.Contains(strings.ToLower(r.Status), strings.ToLower(ai)) }
	sort.SliceStable(cand, func(i, j int) bool {
		if mine(cand[i]) != mine(cand[j]) {
			return mine(cand[i])
		}
		return cand[i].Priority < cand[j].Priority
	})
	for _, r := range cand {
		s := suggestion{buildRow: r, ClaimArea: claimAreaFor(r)}
		s.ClaimCmd = fmt.Sprintf("%s claim %s %q %q", boardScript, ai, s.ClaimArea, r.ID)
		if len([]rune(s.Item)) > 220 {
			s.Item = string([]rune(s.Item)[:220]) + "..."
		}
		laned, ok := inLane(ai, r.Project)
		switch {
		case !ok:
			s.Why = "outside your lane (" + strings.Join(swarmLanes[ai], "/") + "); ask the user before touching it"
			if len(offLane) < limit {
				offLane = append(offLane, s)
			}
		case mine(r):
			s.Why = "already marked as yours in the build list"
			if len(inLaneList) < limit {
				inLaneList = append(inLaneList, s)
			}
		default:
			s.Why = r.Priority + ", free sector"
			if laned {
				s.Why += ", in your lane"
			}
			if len(inLaneList) < limit {
				inLaneList = append(inLaneList, s)
			}
		}
	}
	return
}

func cleanAIName(s string) string {
	s = strings.TrimSpace(s)
	if len(s) == 0 || len(s) > 40 {
		return ""
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return ""
		}
	}
	return s
}

func (a *App) handleSwarmNext(w http.ResponseWriter, r *http.Request) {
	var ai string
	record := r.Method == http.MethodPost
	if record {
		var body struct {
			AI string `json:"ai"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
		ai = cleanAIName(body.AI)
	} else {
		ai = cleanAIName(r.URL.Query().Get("ai"))
	}
	if ai == "" {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"ok": false, "error": "say who you are: ?ai=<your-name> (letters, digits, - and _)"})
		return
	}
	claims, _ := parseActiveClaims(boardPath)
	rows := loadBuildRows()
	if len(rows) == 0 {
		writeJSON(w, map[string]any{"ok": false, "error": "the build list is not readable here: " + masterBuildListPath})
		return
	}
	pick, off := pickWork(ai, rows, claims, activeAssignments(), unclaimedBusyProjects(claims, liveSessions()), 5)
	out := map[string]any{"ok": true, "ai": ai, "next": pick, "off_lane": off}
	if q := a.queuedFor(ai); len(q) > 0 {
		out["queued_prompts"] = q
		out["queued_note"] = "The user approved these prompts for you via Lucille. They come before anything in next. Claim each with POST /api/jobs/{id}/claim {\"ai\": \"" + ai + "\"} and report with POST /api/jobs/{id}/done."
	}
	var own []string
	for _, c := range claims {
		if c.AI == ai && !c.Stale {
			own = append(own, c.Area)
		}
	}
	if len(own) > 0 {
		out["you_already_hold"] = own
		out["note"] = "you still hold open claims; finish or release them with ai-board.sh done before taking more"
	}
	if len(pick) == 0 {
		out["note"] = "nothing free in your lane right now. Check off_lane, or ask the user."
	} else if record {
		if err := recordAssignment(ai, pick[0].buildRow); err != nil {
			out["warning"] = "could not record the assignment: " + err.Error()
		} else {
			out["assigned"] = pick[0]
			out["instructions"] = "Claim it with claim_cmd, mark the row WIP in MASTER_BUILD_LIST.md, and run ai-board.sh done when you stop."
		}
	}
	writeJSON(w, out)
}

type sectorFree struct {
	Project string `json:"project"`
	Sector  string `json:"sector"`
	Open    int    `json:"open"`
	Top     string `json:"top_priority"`
}

func (a *App) handleSwarmStatus(w http.ResponseWriter, r *http.Request) {
	claims, _ := parseActiveClaims(boardPath)
	rows := loadBuildRows()
	sessions := liveSessions()
	sc := scopeOf(claims)

	live := map[string]int{}
	for _, s := range sessions {
		if s.Project != "" {
			live[s.Project]++
		}
	}
	var flags []string
	covered := map[string]bool{}
	for _, c := range claims {
		proj, _, ok := strings.Cut(c.Area, ": ")
		if !ok || len(proj) != 2 {
			proj = projectOfPath(strings.Fields(c.Area + " ")[0])
		}
		if proj != "" && !c.Stale {
			covered[proj] = true
		}
		switch {
		case c.Stale:
			flags = append(flags, fmt.Sprintf("%s's claim on %q is %s old: probably gone, ignore it or ask them to close it", c.AI, c.Area, time.Since(c.ClaimedAt).Round(time.Hour)))
		case proj != "" && live[proj] == 0 && time.Since(c.ClaimedAt) > 30*time.Minute:
			flags = append(flags, fmt.Sprintf("%s claims %q but no AI session is running in %s: check they are still there", c.AI, c.Area, proj))
		}
	}
	for proj, n := range live {
		if !covered[proj] {
			flags = append(flags, fmt.Sprintf("%d AI session(s) running in %s with no claim on the board: unclaimed work", n, proj))
		}
	}
	sort.Strings(flags)

	type key struct{ p, s string }
	agg := map[key]*sectorFree{}
	for _, r := range rows {
		if !r.open() || r.needsUser() || r.Sector == "" || sc.heldByOther(r, "") != "" {
			continue
		}
		k := key{r.Project, r.Sector}
		f := agg[k]
		if f == nil {
			f = &sectorFree{Project: r.Project, Sector: r.Sector, Top: r.Priority}
			agg[k] = f
		}
		f.Open++
		if r.Priority < f.Top {
			f.Top = r.Priority
		}
	}
	free := make([]sectorFree, 0, len(agg))
	for _, f := range agg {
		free = append(free, *f)
	}
	sort.Slice(free, func(i, j int) bool {
		if free[i].Top != free[j].Top {
			return free[i].Top < free[j].Top
		}
		return free[i].Project+free[i].Sector < free[j].Project+free[j].Sector
	})

	writeJSON(w, map[string]any{
		"ok": true, "generated_at": time.Now(),
		"claims": claims, "live_sessions": sessions, "assignments": activeAssignments(),
		"flags": flags, "free_sectors": free,
	})
}

// swarmBrief is the few lines of orchestration state Lucille's chat prompt carries, so "who should
// do what next" and "is everyone where they say" get real answers when she is asked.
func swarmBrief() []string {
	claims, _ := parseActiveClaims(boardPath)
	rows := loadBuildRows()
	if len(rows) == 0 {
		return nil
	}
	var out []string
	sessions := liveSessions()
	busy := unclaimedBusyProjects(claims, sessions)
	per := map[string]int{}
	for _, s := range sessions {
		if s.Project != "" {
			per[s.Project]++
		}
	}
	if len(per) > 0 {
		var parts []string
		for _, p := range []string{"SW", "RC", "CP", "LA"} {
			if per[p] > 0 {
				parts = append(parts, fmt.Sprintf("%d in %s", per[p], p))
			}
		}
		out = append(out, "AI sessions really running right now: "+strings.Join(parts, ", "))
	}
	names := make([]string, 0, len(swarmLanes))
	for n := range swarmLanes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		pick, _ := pickWork(n, rows, claims, activeAssignments(), busy, 1)
		if len(pick) == 1 {
			item := pick[0].Item
			if r := []rune(item); len(r) > 90 {
				item = string(r[:90]) + "..."
			}
			out = append(out, fmt.Sprintf("next for %s: %s (%s) %s", n, pick[0].ID, pick[0].Priority, item))
		}
	}
	return out
}
