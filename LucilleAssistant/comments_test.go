package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Wednesday 2026-10-14 16:30, local time.
var commentClock = time.Date(2026, 10, 14, 16, 30, 0, 0, time.Local)

func TestReadPlan(t *testing.T) {
	cases := []struct {
		text, status, due, change string
	}{
		{"done", planDone, "", "marked done"},
		{"Finished it last night, works", planDone, "", "marked done"},
		{"not done yet", planOpen, "", ""},
		{"skip this one", planSkipped, "", "skipped"},
		{"no thanks", planSkipped, "", "skipped"},
		{"later", planLater, "", "moved to later (no date)"},
		{"not now", planLater, "", "moved to later (no date)"},
		{"tomorrow", planOpen, "2026-10-15T09:00", "scheduled"},
		{"tomorrow 3pm", planOpen, "2026-10-15T15:00", "scheduled"},
		{"friday at 2", planOpen, "2026-10-16T14:00", "scheduled"},
		{"Fri 10:30", planOpen, "2026-10-16T10:30", "scheduled"},
		{"tonight", planOpen, "2026-10-14T20:00", "scheduled"},
		{"later today", planOpen, "2026-10-14T17:00", "scheduled"},
		{"9am", planOpen, "2026-10-15T09:00", "scheduled"}, // 9 AM has passed today: tomorrow
		{"6pm", planOpen, "2026-10-14T18:00", "scheduled"},
		{"in 2 days", planOpen, "2026-10-16T09:00", "scheduled"},
		{"in 3 hours", planOpen, "2026-10-14T19:30", "scheduled"},
		{"next week", planOpen, "2026-10-19T09:00", "scheduled"},
		{"monday morning", planOpen, "2026-10-19T09:00", "scheduled"},
		{"wednesday", planOpen, "2026-10-14T17:00", "scheduled"}, // today is Wednesday
		{"looks useful, needs the boot test first", planOpen, "", ""},
	}
	for _, c := range cases {
		st, due, ch := readPlan(c.text, commentClock, PlanItem{Status: planOpen})
		if st != c.status || due != c.due || !strings.HasPrefix(ch, c.change) {
			t.Errorf("%q -> (%s, %s, %q); want (%s, %s, %q…)", c.text, st, due, ch, c.status, c.due, c.change)
		}
	}
}

func TestReadPlanKeepsTimeAndReopens(t *testing.T) {
	cur := PlanItem{Status: planOpen, Due: "2026-10-15T15:00"}
	if _, due, _ := readPlan("friday", commentClock, cur); due != "2026-10-16T15:00" {
		t.Fatalf("moving the day should keep 3 PM, got %s", due)
	}
	if st, due, ch := readPlan("undo", commentClock, PlanItem{Status: planDone}); st != planOpen || due != "" || ch == "" {
		t.Fatalf("undo: %s %s %q", st, due, ch)
	}
	if st, _, _ := readPlan("good point", commentClock, PlanItem{Status: planLater}); st != planLater {
		t.Fatalf("a plain note must not change the status, got %s", st)
	}
}

func commentApp(t *testing.T) (*App, func(method, path, token, body string, remote bool) map[string]any) {
	t.Helper()
	app, h, agentTok := newAITestApp(t)
	old := commentNow
	commentNow = func() time.Time { return commentClock }
	t.Cleanup(func() { commentNow = old })
	call := func(method, path, token, body string, remote bool) map[string]any {
		w := aiCall(h, method, path, token, body, remote)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if out == nil {
			out = map[string]any{}
		}
		out["_code"] = float64(w.Code)
		return out
	}
	_ = agentTok
	return app, call
}

func TestCommentEndpointsDriveTheSchedule(t *testing.T) {
	app, call := commentApp(t)
	out := call("POST", "/api/comments/sg-a092d3a1", "", `{"text":"friday 3pm","project":"SW","title":"Boot-health chip"}`, false)
	if out["ok"] != true || !strings.Contains(out["change"].(string), "Fri Oct 16, 3:00 PM") {
		t.Fatalf("first comment: %v", out)
	}
	call("POST", "/api/comments/sg-61c8f7c4", "", `{"text":"tomorrow","project":"SW","title":"Unplugged-drive canary"}`, false)
	call("POST", "/api/comments/sg-d98178c3", "", `{"text":"skip, ISOs are retired","project":"SW","title":"ISO release gate"}`, false)
	call("POST", "/api/comments/sg-86b4a9eb", "", `{"text":"later","project":"LA","title":"Autonomous Daily Study"}`, false)

	got := call("GET", "/api/comments", "", "", false)
	sched := got["schedule"].([]any)
	var order []string
	for _, s := range sched {
		order = append(order, s.(map[string]any)["id"].(string))
	}
	want := []string{"sg-61c8f7c4", "sg-a092d3a1", "sg-86b4a9eb"} // tomorrow, friday, later; skipped is gone
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("schedule %v, want %v", order, want)
	}
	items := got["items"].(map[string]any)
	sk := items["sg-d98178c3"].(map[string]any)
	if sk["status"] != planSkipped || len(sk["comments"].([]any)) != 1 {
		t.Fatalf("skipped item: %v", sk)
	}
	// stored privately
	st, err := os.Stat(filepath.Join(app.cfg.DataDir, "comments.json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("comments.json mode: %v %v", st, err)
	}
}

func TestCommentGuards(t *testing.T) {
	app, call := commentApp(t)
	_, agentTok, _ := app.store.pairDeviceRole("worker2", roleAgent)
	if out := call("POST", "/api/comments/sg-a092d3a1", agentTok, `{"text":"done"}`, false); out["_code"] != float64(403) {
		t.Fatalf("Dr. Boltz's agent must not comment: %v", out)
	}
	if out := call("POST", "/api/comments/../../etc", "", `{"text":"done"}`, false); out["_code"] == float64(200) {
		t.Fatalf("bad id accepted: %v", out)
	}
	if out := call("POST", "/api/comments/sg-NOTHEX00", "", `{"text":"done"}`, false); out["_code"] != float64(400) {
		t.Fatalf("bad id: %v", out)
	}
	if out := call("POST", "/api/comments/sg-a092d3a1", "", `{"text":"   "}`, false); out["_code"] != float64(400) {
		t.Fatalf("empty text: %v", out)
	}
	if out := call("POST", "/api/comments/sg-a092d3a1", "", `{"text":"done"}`, true); out["_code"] != float64(401) {
		t.Fatalf("remote without token: %v", out)
	}
	long := strings.Repeat("x", 3000)
	out := call("POST", "/api/comments/sg-a092d3a1", "", `{"text":"`+long+`"}`, false)
	cs := out["item"].(map[string]any)["comments"].([]any)
	if n := len(cs[0].(map[string]any)["text"].(string)); n != commentMaxText {
		t.Fatalf("text not capped: %d", n)
	}
}

func TestCommentsSurviveADamagedFileHonestly(t *testing.T) {
	app, call := commentApp(t)
	_ = os.MkdirAll(app.cfg.DataDir, 0o700)
	_ = os.WriteFile(filepath.Join(app.cfg.DataDir, "comments.json"), []byte("{not json"), 0o600)
	if out := call("GET", "/api/comments", "", "", false); out["ok"] != false || !strings.Contains(out["error"].(string), "damaged") {
		t.Fatalf("want an honest error, got %v", out)
	}
}
