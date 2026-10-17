package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fakeList = `# list
## Sectors
| Project | Sector | IDs |
|---|---|---|
| SW | Security & Keys | SW-04, 21, 30 |
| SW | Boot | SW-01, 02 |
| LA | UI | LA-04, 05 |
| INF, MESH | — | every row is its own sector |

## Rows
| ID | P | Item | Status | Notes |
|---|---|---|---|---|
| SW-01 | P0 | Fix boot | TODO | n |
| SW-02 | P1 | Boot docs | DONE | n |
| SW-04 | P1 | Rotate keys | TODO | n |
| SW-21 | P0 | Lock screen | WIP (swarm-claude, since 2026-10-14) | n |
| SW-30 | P2 | USB key | TODO (user) | needs a human |
| LA-04 | P1 | Theme | TODO | n |
| LA-05 | P3 | Icon | IDEA | n |
| INF-02 | P0 | fstab | TODO (needs sudo) | n |
| INF-05 | P2 | Mounts check | TODO | n |
`

func swarmFixture(t *testing.T, board string) {
	t.Helper()
	dir := t.TempDir()
	oldList, oldBoard, oldAssign, oldSessions := masterBuildListPath, boardPath, assignmentsPath, liveSessionsFn
	t.Cleanup(func() { masterBuildListPath, boardPath, assignmentsPath, liveSessionsFn = oldList, oldBoard, oldAssign, oldSessions })
	liveSessionsFn = func() []liveSession { return nil }
	masterBuildListPath = filepath.Join(dir, "list.md")
	boardPath = filepath.Join(dir, "board.tsv")
	assignmentsPath = filepath.Join(dir, "assign.tsv")
	if err := os.WriteFile(masterBuildListPath, []byte(fakeList), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(boardPath, []byte(board), 0o644); err != nil {
		t.Fatal(err)
	}
}

func boardLine(ago time.Duration, ai, area, action, note string) string {
	return time.Now().Add(-ago).Format(time.RFC3339) + "\t" + ai + "\t" + area + "\t" + action + "\t" + note + "\n"
}

func TestLoadBuildRowsFillsSectorsIncludingShortIDs(t *testing.T) {
	swarmFixture(t, "")
	by := map[string]buildRow{}
	for _, r := range loadBuildRows() {
		by[r.ID] = r
	}
	if by["SW-21"].Sector != "Security & Keys" || by["SW-01"].Sector != "Boot" || by["LA-05"].Sector != "UI" {
		t.Fatalf("sectors wrong: %+v", by)
	}
	if by["INF-05"].Sector != "" || by["INF-05"].Project != "INF" {
		t.Fatalf("INF row should be its own sector: %+v", by["INF-05"])
	}
}

func TestNextRespectsLaneAndSkipsDoneIdeaAndUserRows(t *testing.T) {
	swarmFixture(t, "")
	pick, off := pickWork("swarm-claude", loadBuildRows(), nil, nil, nil, 10)
	var ids []string
	for _, p := range pick {
		ids = append(ids, p.ID)
	}
	got := strings.Join(ids, ",")
	if got != "SW-21,SW-01,SW-04" {
		t.Fatalf("want own WIP first then by priority, no DONE/user/other-lane rows; got %s", got)
	}
	if len(off) == 0 {
		t.Fatal("LA/INF work should be listed off_lane for swarm-claude")
	}
	for _, o := range off {
		if o.Project == "SW" {
			t.Fatal("an in-lane row leaked into off_lane")
		}
	}
}

func TestClaimedSectorBlocksOthersButNotTheHolder(t *testing.T) {
	swarmFixture(t, boardLine(time.Hour, "codex", "SW: Security & Keys", "CLAIM", "SW-30"))
	claims, _ := parseActiveClaims(boardPath)
	other, _ := pickWork("dev-antigravity", loadBuildRows(), claims, nil, nil, 20)
	for _, p := range other {
		if p.Sector == "Security & Keys" {
			t.Fatalf("%s is in a sector codex holds", p.ID)
		}
	}
	mine, _ := pickWork("codex", loadBuildRows(), claims, nil, nil, 20)
	found := false
	for _, p := range mine {
		found = found || p.Sector == "Security & Keys"
	}
	if !found {
		t.Fatal("the holder should still see its own sector")
	}
}

func TestStaleClaimDoesNotBlock(t *testing.T) {
	swarmFixture(t, boardLine(10*time.Hour, "ghost", "SW: Security & Keys", "CLAIM", "old"))
	claims, _ := parseActiveClaims(boardPath)
	pick, _ := pickWork("swarm-claude", loadBuildRows(), claims, nil, nil, 20)
	ok := false
	for _, p := range pick {
		ok = ok || p.ID == "SW-04"
	}
	if !ok {
		t.Fatal("a 10-hour-old claim must not block the sector")
	}
}

func TestWholeProjectPathClaimBlocksTheProject(t *testing.T) {
	swarmFixture(t, boardLine(time.Hour, "someone", "/mnt/Lucy", "CLAIM", "boot"))
	claims, _ := parseActiveClaims(boardPath)
	pick, _ := pickWork("dev-antigravity", loadBuildRows(), claims, nil, nil, 20)
	for _, p := range pick {
		if p.Project == "SW" {
			t.Fatalf("whole-project claim ignored: %s", p.ID)
		}
	}
}

func TestPostRecordsAssignmentAndSecondAskerGetsSomethingElse(t *testing.T) {
	swarmFixture(t, "")
	a := &App{}
	post := func(ai string) map[string]any {
		req := httptest.NewRequest(http.MethodPost, "/api/swarm/next", strings.NewReader(`{"ai":"`+ai+`"}`))
		rec := httptest.NewRecorder()
		a.handleSwarmNext(rec, req)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%v: %s", err, rec.Body.String())
		}
		return out
	}
	first := post("dev-antigravity")["assigned"].(map[string]any)["id"]
	second := post("codex-coordinator")["assigned"].(map[string]any)["id"]
	if first == second {
		t.Fatalf("both AIs were handed %v", first)
	}
	if len(activeAssignments()) != 2 {
		t.Fatalf("assignments not recorded: %v", activeAssignments())
	}
}

func TestPeekRecordsNothingAndNeedsAName(t *testing.T) {
	swarmFixture(t, "")
	a := &App{}
	rec := httptest.NewRecorder()
	a.handleSwarmNext(rec, httptest.NewRequest(http.MethodGet, "/api/swarm/next", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no name should be 400, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	a.handleSwarmNext(rec, httptest.NewRequest(http.MethodGet, "/api/swarm/next?ai=dev-claude", nil))
	if rec.Code != 200 || len(activeAssignments()) != 0 {
		t.Fatalf("peek must not record (code %d)", rec.Code)
	}
}

func TestCleanAIName(t *testing.T) {
	for in, want := range map[string]string{"dev-claude": "dev-claude", " x ": "x", "": "", "a b": "", "a;rm": "", strings.Repeat("a", 41): ""} {
		if got := cleanAIName(in); got != want {
			t.Errorf("cleanAIName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestProjectOfPath(t *testing.T) {
	cases := map[string]string{
		"/mnt/Lucy/.claude/worktrees/x": "SW", "/mnt/recoverybuild": "RC", "/mnt/lucilleassistant/src": "LA",
		"/mnt/boltzbuild/src/x": "CP", "/home/i30ltz": "", "/mnt/Lucy2": "",
	}
	for in, want := range cases {
		if got := projectOfPath(in); got != want {
			t.Errorf("projectOfPath(%q)=%q want %q", in, got, want)
		}
	}
}

func TestStatusFlagsStaleClaimAndAlwaysOK(t *testing.T) {
	swarmFixture(t, boardLine(10*time.Hour, "ghost", "SW: Boot", "CLAIM", "old"))
	rec := httptest.NewRecorder()
	(&App{}).handleSwarmStatus(rec, httptest.NewRequest(http.MethodGet, "/api/swarm/status", nil))
	var out struct {
		OK    bool     `json:"ok"`
		Flags []string `json:"flags"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || !out.OK {
		t.Fatalf("bad status: %v %s", err, rec.Body.String())
	}
	if !strings.Contains(strings.Join(out.Flags, "|"), "ghost") {
		t.Fatalf("stale claim not flagged: %v", out.Flags)
	}
}

func TestUnclaimedBusyProjectIsOffLimitsToAnyLaneAIsButNotLaneOwners(t *testing.T) {
	swarmFixture(t, "")
	busy := unclaimedBusyProjects(nil, []liveSession{{Tool: "claude", Project: "SW"}, {Tool: "claude", Project: ""}})
	if !busy["SW"] || busy[""] {
		t.Fatalf("busy map wrong: %v", busy)
	}
	roaming, _ := pickWork("dev-antigravity", loadBuildRows(), nil, nil, busy, 20)
	for _, p := range roaming {
		if p.Project == "SW" {
			t.Fatalf("roaming AI sent into a busy unclaimed project: %s", p.ID)
		}
	}
	owner, _ := pickWork("swarm-claude", loadBuildRows(), nil, nil, busy, 20)
	if len(owner) == 0 || owner[0].Project != "SW" {
		t.Fatal("the lane owner must still get its own project's work")
	}
	covered := unclaimedBusyProjects([]boardClaim{{AI: "x", Area: "SW: Boot"}}, []liveSession{{Project: "SW"}})
	if covered["SW"] {
		t.Fatal("a claimed project is not unclaimed-busy")
	}
}
