package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type briefingReply struct {
	OK        bool       `json:"ok"`
	Error     string     `json:"error"`
	Briefing  Briefing   `json:"briefing"`
	Briefings []Briefing `json:"briefings"`
}

// call runs one handler the way the mux would, including the {id} path value.
func call(t *testing.T, h http.HandlerFunc, method, id, body string) (int, briefingReply) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/briefings", bytes.NewReader([]byte(body)))
	if id != "" {
		req.SetPathValue("id", id)
	}
	rr := httptest.NewRecorder()
	h(rr, req)
	var out briefingReply
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: bad json %q: %v", method, id, rr.Body.String(), err)
	}
	return rr.Code, out
}

func TestBuiltinBriefingsParse(t *testing.T) {
	seeds, err := builtinBriefings()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"swarm-os": "SW", "recovery-os": "RC", "i30ltz-companion": "CP", "lucille-assistant": "LA"}
	if len(seeds) != len(want) {
		t.Fatalf("got %d built-in briefings, want %d", len(seeds), len(want))
	}
	// A secret in a seed would ship inside the binary: guard the same patterns the
	// project rules tell every AI to scan for.
	secret := regexp.MustCompile(`sk-[A-Za-z0-9]{16}|AIza[0-9A-Za-z_-]{20}|ghp_[A-Za-z0-9]{20}|AKIA[0-9A-Z]{12}|BEGIN [A-Z ]*PRIVATE KEY`)
	for _, b := range seeds {
		if want[b.ID] != b.Project {
			t.Errorf("%s: project %q, want %q", b.ID, b.Project, want[b.ID])
		}
		if b.Title == "" || b.Summary == "" || b.Body == "" || len(b.ReadFirst) == 0 || b.Order == 0 {
			t.Errorf("%s: incomplete seed %+v", b.ID, b)
		}
		if secret.MatchString(b.Body + b.Summary) {
			t.Errorf("%s: looks like it contains a secret", b.ID)
		}
	}
}

// The built-ins are status briefings, not just reference text: each one must open with a
// status, then a scheduled list of next steps, then exactly three suggestions, then the
// stable reference.
func TestBuiltinBriefingsAreStatusBriefings(t *testing.T) {
	seeds, err := builtinBriefings()
	if err != nil {
		t.Fatal(err)
	}
	numbered := regexp.MustCompile(`(?m)^\d+\. .+$`)
	for _, b := range seeds {
		i := strings.Index(b.Body, "STATUS (")
		j := strings.Index(b.Body, "NEXT STEPS & SCHEDULE")
		k := strings.Index(b.Body, "3 SUGGESTIONS")
		l := strings.Index(b.Body, "REFERENCE")
		if i != 0 || i >= j || j >= k || k >= l {
			t.Errorf("%s: sections missing or out of order (STATUS %d, NEXT STEPS %d, SUGGESTIONS %d, REFERENCE %d)", b.ID, i, j, k, l)
			continue
		}
		steps := numbered.FindAllString(b.Body[j:k], -1)
		if len(steps) < 4 {
			t.Errorf("%s: only %d scheduled next steps, want at least 4", b.ID, len(steps))
		}
		for _, st := range steps {
			if !strings.Contains(st, "[YOU]") && !strings.Contains(st, "[AI]") {
				t.Errorf("%s: step has no [YOU]/[AI] owner: %q", b.ID, st)
			}
		}
		if got := len(numbered.FindAllString(b.Body[k:l], -1)); got != 3 {
			t.Errorf("%s: %d suggestions, want exactly 3", b.ID, got)
		}
		if !strings.Contains(b.Summary, "3 suggestions") {
			t.Errorf("%s: summary should lead with the status and mention the suggestions: %q", b.ID, b.Summary)
		}
	}
}

func TestSeedBriefings(t *testing.T) {
	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)

	n, err := app.store.SeedBriefings(time.Now())
	if err != nil || n != 4 {
		t.Fatalf("first seed: n=%d err=%v, want 4", n, err)
	}
	list, _ := app.store.Briefings()
	var ids []string
	for _, b := range list {
		ids = append(ids, b.ID)
	}
	if got := strings.Join(ids, ","); got != "swarm-os,recovery-os,i30ltz-companion,lucille-assistant" {
		t.Fatalf("order = %s", got)
	}

	if n, err := app.store.SeedBriefings(time.Now()); err != nil || n != 0 {
		t.Fatalf("second seed: n=%d err=%v, want 0", n, err)
	}

	// Edits survive a restart, and a deleted built-in stays deleted.
	if _, _, err := app.store.UpdateBriefing("swarm-os", BriefingInput{Title: "My SWARM", Body: "mine"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if ok, err := app.store.DeleteBriefing("recovery-os"); !ok || err != nil {
		t.Fatalf("delete: %v %v", ok, err)
	}
	reopened, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := reopened.SeedBriefings(time.Now()); err != nil || n != 0 {
		t.Fatalf("seed after restart: n=%d err=%v, want 0", n, err)
	}
	list, _ = reopened.Briefings()
	if len(list) != 3 {
		t.Fatalf("after restart got %d briefings, want 3", len(list))
	}
	if b, _, _ := reopened.Briefing("swarm-os"); b.Title != "My SWARM" || b.Body != "mine" {
		t.Fatalf("edit lost: %+v", b)
	}
	if _, found, _ := reopened.Briefing("recovery-os"); found {
		t.Fatal("deleted built-in came back")
	}
}

func TestSeedDoesNotOverwriteExisting(t *testing.T) {
	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)

	// The user already has their own briefing under a built-in id.
	if err := os.MkdirAll(filepath.Join(dir, "briefings"), 0o700); err != nil {
		t.Fatal(err)
	}
	mine := `{"id":"swarm-os","title":"Hand-written","body":"keep me","order":9}`
	if err := os.WriteFile(filepath.Join(dir, "briefings", "swarm-os.json"), []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := app.store.SeedBriefings(time.Now())
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v, want 3", n, err)
	}
	if b, _, _ := app.store.Briefing("swarm-os"); b.Title != "Hand-written" {
		t.Fatalf("seed overwrote a user file: %+v", b)
	}
}

func TestBriefingCRUD(t *testing.T) {
	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)

	code, r := call(t, app.handleBriefingCreate, "POST", "", `{"title":"  Boltz Recovery!  ","project":"BZ","summary":"s","body":"line1\r\nline2","read_first":[" /a ","","/b"]}`)
	if code != 200 || !r.OK || r.Briefing.ID != "boltz-recovery" {
		t.Fatalf("create: %d %+v", code, r)
	}
	if r.Briefing.Title != "Boltz Recovery!" || r.Briefing.Body != "line1\nline2" || len(r.Briefing.ReadFirst) != 2 {
		t.Fatalf("not cleaned: %+v", r.Briefing)
	}

	// Same title again must not overwrite.
	_, r2 := call(t, app.handleBriefingCreate, "POST", "", `{"title":"Boltz Recovery"}`)
	if r2.Briefing.ID != "boltz-recovery-2" || r2.Briefing.Order <= r.Briefing.Order {
		t.Fatalf("collision handling: %+v", r2.Briefing)
	}

	code, r = call(t, app.handleBriefingUpdate, "PUT", "boltz-recovery", `{"title":"Boltz","body":"new"}`)
	if code != 200 || r.Briefing.Title != "Boltz" || r.Briefing.Body != "new" || r.Briefing.ID != "boltz-recovery" {
		t.Fatalf("update: %d %+v", code, r)
	}
	if !r.Briefing.Updated.After(r.Briefing.Created) && !r.Briefing.Updated.Equal(r.Briefing.Created) {
		t.Fatalf("updated before created: %+v", r.Briefing)
	}

	code, r = call(t, app.handleBriefingGet, "GET", "boltz-recovery", "")
	if code != 200 || r.Briefing.Body != "new" {
		t.Fatalf("get: %d %+v", code, r)
	}
	code, r = call(t, app.handleBriefings, "GET", "", "")
	if code != 200 || len(r.Briefings) != 2 {
		t.Fatalf("list: %d %+v", code, r)
	}

	if code, _ = call(t, app.handleBriefingDelete, "DELETE", "boltz-recovery", ""); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code, r = call(t, app.handleBriefingGet, "GET", "boltz-recovery", ""); code != 404 || r.OK {
		t.Fatalf("get after delete: %d %+v", code, r)
	}
	if code, _ = call(t, app.handleBriefingDelete, "DELETE", "boltz-recovery", ""); code != 404 {
		t.Fatalf("second delete: %d, want 404", code)
	}
}

func TestBriefingValidation(t *testing.T) {
	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)

	bad := map[string]string{
		"no title":      `{"title":"   ","body":"x"}`,
		"long title":    `{"title":"` + strings.Repeat("a", maxTitle+1) + `"}`,
		"long summary":  `{"title":"t","summary":"` + strings.Repeat("a", maxSummary+1) + `"}`,
		"long body":     `{"title":"t","body":"` + strings.Repeat("a", maxBody+1) + `"}`,
		"too many dirs": `{"title":"t","read_first":[` + strings.TrimSuffix(strings.Repeat(`"/p",`, maxReadFirst+1), ",") + `]}`,
		"not json":      `nope`,
	}
	for name, body := range bad {
		if code, r := call(t, app.handleBriefingCreate, "POST", "", body); code != 400 || r.OK || r.Error == "" {
			t.Errorf("%s: got %d %+v, want 400 with a message", name, code, r)
		}
	}
	if list, _ := app.store.Briefings(); len(list) != 0 {
		t.Fatalf("rejected input was stored: %+v", list)
	}

	// Updating or reading something that is not there is a 404, even with bad input.
	if code, _ := call(t, app.handleBriefingUpdate, "PUT", "ghost", `{"title":""}`); code != 404 {
		t.Errorf("update ghost: %d, want 404", code)
	}
}

// Ids become file names, so anything that is not a plain slug must never reach the disk.
func TestBriefingIDsCannotEscapeTheFolder(t *testing.T) {
	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "secret.json"), []byte(`{"title":"outside"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../secret", "..%2Fsecret", "a/b", ".seeded", "UPPER", strings.Repeat("a", 64), "-lead"} {
		if code, _ := call(t, app.handleBriefingGet, "GET", id, ""); code != 404 {
			t.Errorf("get %q: %d, want 404", id, code)
		}
		if code, _ := call(t, app.handleBriefingUpdate, "PUT", id, `{"title":"x"}`); code != 404 {
			t.Errorf("put %q: %d, want 404", id, code)
		}
		if code, _ := call(t, app.handleBriefingDelete, "DELETE", id, ""); code != 404 {
			t.Errorf("delete %q: %d, want 404", id, code)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "secret.json")); err != nil {
		t.Fatalf("file outside briefings/ was touched: %v", err)
	}
}

func TestCorruptBriefingIsSkipped(t *testing.T) {
	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)
	if _, err := app.store.CreateBriefing(BriefingInput{Title: "Good"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "briefings", "broken.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := app.store.Briefings()
	if err != nil || len(list) != 1 || list[0].ID != "good" {
		t.Fatalf("list = %+v err=%v, want only the good one", list, err)
	}
}

func TestSystemPromptListsBriefings(t *testing.T) {
	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)
	// Isolate from this machine's real build list / ai-board: neither exists at these paths, so
	// criticalOpenItems/dispatchSummary correctly return nothing and the prompt stays exact.
	oldList, oldBoard := masterBuildListPath, boardPath
	masterBuildListPath = filepath.Join(dir, "no-such-build-list.md")
	boardPath = filepath.Join(dir, "no-such-ai-board.tsv")
	t.Cleanup(func() { masterBuildListPath, boardPath = oldList, oldBoard })
	app.cfg.MemoryDir = filepath.Join(dir, "no-memory") // and from the real shared memory log

	if got := app.systemPrompt(); got != lucilleSystemPrompt {
		t.Fatalf("with no briefings and no build list/board, the prompt must be unchanged, got %q", got)
	}
	if _, err := app.store.SeedBriefings(time.Now()); err != nil {
		t.Fatal(err)
	}
	p := app.systemPrompt()
	for _, title := range []string{"SWARM OS", "I30LTZ Recovery OS", "I30LTZ Companion", "Lucille Assistant"} {
		if !strings.Contains(p, "- "+title+": ") {
			t.Errorf("prompt is missing %q", title)
		}
	}
	if strings.Contains(p, "NEXT STEPS & SCHEDULE") || strings.Contains(p, "STATUS (") {
		t.Error("full briefing bodies must not go to the model, only summaries")
	}
}
